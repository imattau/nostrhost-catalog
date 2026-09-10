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

	"github.com/imattau/nostrhost-catalog/internal/catalog"
	"github.com/imattau/nostrhost-catalog/internal/relay"
	"github.com/nbd-wtf/go-nostr"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "nostrhost-catalog:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: nostrhost-catalog [--state PATH] [--publishers KEYS] [--relay URLS] ingest|list|get APP_ID|sync")
	}
	flags := flag.NewFlagSet("nostrhost-catalog", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	statePath := flags.String("state", "/var/lib/nostrhost/catalogue.json", "projection state path")
	publisherList := flags.String("publishers", "", "comma-separated trusted publisher hex keys or npubs")
	relayList := flags.String("relay", "", "comma-separated relay ws:// or wss:// URLs (sync only)")
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
	case "sync":
		return syncCatalog(*statePath, keys, splitNonEmpty(*relayList))
	default:
		return fmt.Errorf("unknown command %q", flags.Arg(0))
	}
}

func syncCatalog(statePath string, publishers, relayURLs []string) error {
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
	return catalog.Run(ctx, client, store, statePath)
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
