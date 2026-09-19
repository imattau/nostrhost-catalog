// Command nostrhost-catalog manages the local trusted catalogue projection.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/imattau/nostrhost-catalog/internal/catalog"
	"github.com/imattau/nostrhost-catalog/internal/protocol"
	"github.com/imattau/nostrhost-catalog/internal/relay"
	"github.com/imattau/nostrhost-catalog/internal/repository"
	"github.com/imattau/nostrhost-catalog/internal/trust"
	"github.com/imattau/nostrhost-catalog/internal/verification"
	"github.com/nbd-wtf/go-nostr"
)

// reverifyTimeout bounds the on-demand git clone reverify performs - an
// admin clicking the button waits synchronously for the response, so this
// needs to be short enough not to look hung against a slow or unreachable
// repository host.
const reverifyTimeout = 20 * time.Second

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "nostrhost-catalog:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: nostrhost-catalog [--state PATH] [--publishers KEYS] [--relay URLS] ingest|list|get APP_ID|publish|sync|trust|reverify|rebuild|verify")
	}
	flags := flag.NewFlagSet("nostrhost-catalog", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	statePath := flags.String("state", "/var/lib/nostrhost/catalogue.json", "projection state path")
	publisherList := flags.String("publishers", "", "comma-separated trusted publisher hex keys or npubs")
	relayList := flags.String("relay", "", "comma-separated relay ws:// or wss:// URLs (sync only)")
	appID := flags.String("app-id", "", "app ID (reverify only)")
	logoDir := flags.String("logo-dir", "/usr/share/yunohost/applogos", "directory for extracted app logos (empty disables)")
	attestationPolicy := flags.String("attestation-policy", "off", "attestation policy mode (trust only): off|prefer|require")
	minAttestations := flags.Int("min-attestations", 0, "minimum acceptable attestations to count a revision verified (trust only, 0 = default of 1)")
	requiredChecks := flags.String("required-checks", "", "comma-separated required CI check names (trust only)")
	trustedVerifiers := flags.String("trusted-verifiers", "", "comma-separated trusted attestation verifier keys (trust only; prefer/require refuse to start without at least one - empty means trust nobody)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *publisherList == "" {
		return errors.New("--publishers is required")
	}
	keys := splitNonEmpty(*publisherList)
	store, err := openStore(*statePath, keys)
	if err != nil {
		return err
	}
	switch flags.Arg(0) {
	case "ingest":
		if flags.NArg() > 1 {
			return errors.New("ingest reads one event JSON object from stdin")
		}
		return ingest(store, *statePath, os.Stdin)
	case "list":
		if flags.NArg() != 1 {
			return errors.New("list takes no arguments")
		}
		return writeJSON(os.Stdout, store.Snapshot())
	case "get":
		if flags.NArg() != 2 {
			return errors.New("get requires an app ID")
		}
		entry, ok := store.Resolve(flags.Arg(1))
		if !ok {
			return fmt.Errorf("app %q was not found", flags.Arg(1))
		}
		return writeJSON(os.Stdout, entry)
	case "publish":
		return publish(*relayList, os.Stdin)
	case "sync":
		return syncCatalog(*statePath, keys, splitNonEmpty(*relayList), *logoDir)
	case "rebuild":
		// WP5: catalogue.json is disposable — refetch the whole history from
		// the relay and overwrite it with a fresh projection.
		return rebuildCatalog(*statePath, keys, splitNonEmpty(*relayList), *logoDir, false)
	case "verify":
		// WP5: rebuild into a throwaway store and report drift, no write.
		return rebuildCatalog(*statePath, keys, splitNonEmpty(*relayList), *logoDir, true)
	case "trust":
		if flags.NArg() > 1 {
			return errors.New("trust takes no arguments")
		}
		return runTrust(store, *attestationPolicy, *minAttestations, splitNonEmpty(*requiredChecks), splitNonEmpty(*trustedVerifiers))
	case "reverify":
		if flags.NArg() > 1 {
			return errors.New("reverify takes no arguments")
		}
		return runReverify(store, *appID)
	default:
		return fmt.Errorf("unknown command %q", flags.Arg(0))
	}
}

// trustEntry is one declaration's trust picture: what this projection holds,
// what CI-backed attestations exist for its exact revision, and what the
// given policy decides as a result - so a declaration filtered by
// --attestation-policy=require is explained here, not silently hidden.
type trustEntry struct {
	Declaration  protocol.AppDeclaration    `json:"declaration"`
	Attestations []verification.Attestation `json:"attestations"`
	Verified     bool                       `json:"verified"`
	Accepted     bool                       `json:"accepted"`
}

func runTrust(store *catalog.Store, mode string, minAttestations int, requiredChecks, trustedVerifiers []string) error {
	parsedMode, err := trust.ParseAttestationMode(mode)
	if err != nil {
		return err
	}
	policy, err := trust.NewAttestationPolicy(parsedMode, minAttestations, requiredChecks, trustedVerifiers)
	if err != nil {
		return err
	}
	snapshot := store.Snapshot()
	entries := make([]trustEntry, 0, len(snapshot))
	for _, entry := range snapshot {
		attestations := store.AttestationsFor(entry.Declaration)
		decision := policy.Evaluate(attestations)
		entries = append(entries, trustEntry{
			Declaration:  entry.Declaration,
			Attestations: attestations,
			Verified:     decision.Verified,
			Accepted:     decision.Accepted,
		})
	}
	return writeJSON(os.Stdout, entries)
}

// runReverify independently re-checks one accepted declaration on demand: it
// re-clones the repository fresh at the declared commit and recomputes both
// hashes, rather than trusting whatever was true at ingestion time. This
// performs real outbound network I/O (a git clone) but only ever against a
// repository already accepted into this projection - appID selects which
// already-trusted revision to re-check, it cannot name an arbitrary
// repository.
func runReverify(store *catalog.Store, appID string) error {
	if appID == "" {
		return errors.New("reverify requires --app-id")
	}
	declaration, ok := store.Resolve(appID)
	if !ok {
		return fmt.Errorf("app %q was not found", appID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), reverifyTimeout)
	defer cancel()
	verified, verifyErr := repository.VerifyDeclaration(ctx, declaration)
	result := map[string]any{
		"app_id":     appID,
		"publisher":  declaration.Publisher,
		"commit":     declaration.Commit,
		"repository": declaration.Repository,
		"ok":         verifyErr == nil,
	}
	if verifyErr != nil {
		result["error"] = verifyErr.Error()
	} else {
		result["manifest"] = verified.Manifest
		result["branch"] = verified.Branch
	}
	if err := writeJSON(os.Stdout, result); err != nil {
		return err
	}
	if verifyErr != nil {
		return fmt.Errorf("reverify: %w", verifyErr)
	}
	return nil
}

type publishResult struct {
	Relay string `json:"relay"`
	Error string `json:"error,omitempty"`
}

// publish sends one already-signed event to every configured relay. Signing
// remains deliberately separate: the catalogue CLI never accepts private
// keys, so a publisher can use its own offline signing workflow and this
// command only handles transport and propagation reporting.
func publish(relayURLs string, input io.Reader) error {
	urls := splitNonEmpty(relayURLs)
	if len(urls) == 0 {
		return errors.New("publish requires --relay URL")
	}
	var event nostr.Event
	if err := json.NewDecoder(input).Decode(&event); err != nil {
		return fmt.Errorf("decode signed event: %w", err)
	}
	if !event.CheckID() {
		return errors.New("validate signed event: event ID does not match canonical serialization")
	}
	valid, err := event.CheckSignature()
	if err != nil {
		return fmt.Errorf("validate signed event signature: %w", err)
	}
	if !valid {
		return errors.New("validate signed event signature: signature is invalid")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := relay.New(ctx, urls)
	if err != nil {
		return err
	}
	results := client.Publish(ctx, event)
	output := make([]publishResult, 0, len(results))
	failed := 0
	for _, result := range results {
		item := publishResult{Relay: result.Relay}
		if result.Error != nil {
			item.Error = result.Error.Error()
			failed++
		}
		output = append(output, item)
	}
	if err := writeJSON(os.Stdout, map[string]any{
		"event_id":  event.ID,
		"published": len(results) - failed,
		"failed":    failed,
		"relays":    output,
	}); err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("event publication failed on %d of %d relays", failed, len(results))
	}
	return nil
}

func syncCatalog(statePath string, publishers, relayURLs []string, logoDirectory string) error {
	if len(relayURLs) == 0 {
		return errors.New("sync requires --relay URL")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	client, err := relay.New(ctx, relayURLs)
	if err != nil {
		return err
	}
	store, err := openStore(statePath, publishers)
	if err != nil {
		return err
	}
	return catalog.Run(ctx, client, store, statePath, logoDirectory)
}

// rebuildCatalog refetches the complete declaration + attestation history
// from the relay into a *fresh* store. With verifyOnly it compares the fresh
// digest to the on-disk projection and reports drift without writing; without
// it, it overwrites the on-disk projection with the rebuilt one.
func rebuildCatalog(statePath string, publishers, relayURLs []string, logoDirectory string, verifyOnly bool) error {
	if len(relayURLs) == 0 {
		return errors.New("rebuild/verify requires --relay URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	client, err := relay.New(ctx, relayURLs)
	if err != nil {
		return err
	}
	fresh, err := catalog.NewFromPublishers(publishers)
	if err != nil {
		return err
	}
	if verifyOnly {
		declarations, attestations, err := catalog.Rebuild(ctx, client, fresh, logoDirectory)
		if err != nil {
			return err
		}
		rebuiltDigest, err := fresh.Canonical()
		if err != nil {
			return err
		}
		current, loadErr := openStore(statePath, publishers)
		currentDigest := ""
		if loadErr == nil {
			currentDigest, err = current.Canonical()
			if err != nil {
				return err
			}
		} else if !os.IsNotExist(loadErr) {
			return loadErr
		}
		drift := currentDigest != rebuiltDigest
		if err := writeJSON(os.Stdout, map[string]any{
			"verified":       !drift,
			"drift":          drift,
			"current_digest": currentDigest,
			"rebuilt_digest": rebuiltDigest,
			"declarations":   declarations,
			"attestations":   attestations,
		}); err != nil {
			return err
		}
		if drift {
			return errors.New("catalogue projection differs from the relay rebuild")
		}
		return nil
	}
	declarations, attestations, err := catalog.Rebuild(ctx, client, fresh, logoDirectory)
	if err != nil {
		return err
	}
	if err := fresh.Save(statePath); err != nil {
		return err
	}
	return writeJSON(os.Stdout, map[string]any{
		"rebuilt":      true,
		"declarations": declarations,
		"attestations": attestations,
	})
}

func openStore(path string, publishers []string) (*catalog.Store, error) {
	policyStore, err := catalog.NewFromPublishers(publishers)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err == nil {
		return catalog.LoadFromPublishers(path, publishers)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect catalogue state: %w", err)
	}
	return policyStore, nil
}

func ingest(store *catalog.Store, path string, input io.Reader) error {
	var event nostr.Event
	if err := json.NewDecoder(input).Decode(&event); err != nil {
		return fmt.Errorf("decode declaration event: %w", err)
	}
	changed, err := store.Apply(event)
	if err != nil {
		return err
	}
	if err := store.Save(path); err != nil {
		return err
	}
	return writeJSON(os.Stdout, map[string]any{"changed": changed, "event_id": event.ID})
}

func writeJSON(output io.Writer, value any) error {
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func splitNonEmpty(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}
