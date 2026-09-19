// Package relay adapts the Go Nostr SDK to the catalogue's small relay
// interface.
package relay

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sort"
	"time"

	"github.com/imattau/nostrhost-catalog/internal/protocol"
	"github.com/imattau/nostrhost-catalog/internal/verification"
	"github.com/nbd-wtf/go-nostr"
)

// Client publishes and subscribes through a configured set of relays.
type Client struct {
	pool *nostr.SimplePool
	urls []string
}

const relayListKind = 10002

const relayDiscoveryKind = 30166

// New validates relay URLs and creates a multi-relay client. The SDK owns
// connection management, reconnects, deduplication, and relay backoff.
func New(ctx context.Context, relayURLs []string) (*Client, error) {
	if len(relayURLs) == 0 {
		return nil, fmt.Errorf("at least one relay is required")
	}
	urls := make([]string, 0, len(relayURLs))
	seen := make(map[string]struct{}, len(relayURLs))
	for _, raw := range relayURLs {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "wss" && u.Scheme != "ws") || u.Host == "" {
			return nil, fmt.Errorf("invalid relay URL %q: expected ws:// or wss://", raw)
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		urls = append(urls, raw)
	}
	return &Client{
		pool: nostr.NewSimplePool(ctx, nostr.WithPenaltyBox()),
		urls: urls,
	}, nil
}

// PublishResult records one relay's result for a published event.
type PublishResult struct {
	Relay string
	Error error
}

// FetchReplaceable retrieves the latest parameterised-replaceable event of
// kind authored by publisher with the given "d" tag identifier, using the
// SDK's replaceable-event handling. Works for any addressable kind this
// package deals with (app declarations, d=app_id; attestations,
// d=app_id:commit; ...).
func (c *Client) FetchReplaceable(ctx context.Context, kind int, publisher, identifier string) (*nostr.Event, error) {
	events := c.query(ctx, c.urls, nostr.Filter{Kinds: []int{kind}, Authors: []string{publisher}, Tags: nostr.TagMap{"d": {identifier}}})
	var event *nostr.Event
	for _, candidate := range events {
		if event == nil || candidate.CreatedAt > event.CreatedAt {
			event = candidate
		}
	}
	if event == nil {
		return nil, fmt.Errorf("event not found")
	}
	return event, nil
}

// FetchAppDeclarations fetches the latest declaration per publisher/app pair.
func (c *Client) FetchAppDeclarations(ctx context.Context, publishers []string) []*nostr.Event {
	latest := make(map[nostr.ReplaceableKey]*nostr.Event)
	queryURLs := appendUniqueRelayURLs(c.urls, c.discoverRelayURLs(ctx, publishers))
	// Fetch declaration kinds separately. Some local relays mishandle a
	// multi-kind filter, and relay-side filtering is not a trust boundary in
	// any case because Store.Apply still enforces the publisher allow-list.
	for _, kind := range []int{protocol.AppDeclarationKind, protocol.LegacyAppDeclarationKind} {
		// WP5: reconcile the complete set with NIP-77 rather than a single
		// (truncated) unbounded REQ, so an older declaration survives a
		// catalogue larger than the relay's default page.
		for _, event := range c.fetchAll(ctx, queryURLs, nostr.Filter{Kinds: []int{kind}}) {
			key := nostr.ReplaceableKey{PubKey: event.PubKey, D: event.Tags.GetD()}
			if current, ok := latest[key]; !ok || event.CreatedAt > current.CreatedAt {
				latest[key] = event
			}
		}
	}
	events := make([]*nostr.Event, 0)
	for _, event := range latest {
		events = append(events, event)
	}
	return events
}

// FetchAttestations fetches CI attestation events for replay into the local
// projection. Trust and declaration matching are enforced by Store.
func (c *Client) FetchAttestations(ctx context.Context) []*nostr.Event {
	// WP5: NIP-77 reconciliation so older attestations are not truncated.
	return c.fetchAll(ctx, c.urls, nostr.Filter{Kinds: []int{verification.AttestationKind}})
}

// discoverRelayURLs reads NIP-65 publisher relay lists and NIP-66 relay
// discovery events from the configured bootstrap relays. Discovery is
// deliberately best-effort: discovered URLs are only additional read
// candidates, never a trust or installation decision.
func (c *Client) discoverRelayURLs(ctx context.Context, publishers []string) []string {
	urls := make([]string, 0)
	if len(publishers) > 0 {
		events := c.query(ctx, c.urls, nostr.Filter{
			Kinds:   []int{relayListKind},
			Authors: publishers,
		})
		urls = appendUniqueRelayURLs(urls, relayURLsFromNIP65(events))
	}
	return appendUniqueRelayURLs(urls, relayURLsFromNIP66(c.query(ctx, c.urls, nostr.Filter{
		Kinds: []int{relayDiscoveryKind},
	})))
}

func relayURLsFromNIP65(events []*nostr.Event) []string {
	latest := make(map[string]*nostr.Event)
	for _, event := range events {
		if event == nil || event.Kind != relayListKind {
			continue
		}
		if current, ok := latest[event.PubKey]; !ok || event.CreatedAt > current.CreatedAt {
			latest[event.PubKey] = event
		}
	}
	urls := make([]string, 0)
	for _, event := range latest {
		for _, tag := range event.Tags {
			if len(tag) < 2 || tag[0] != "r" {
				continue
			}
			if len(tag) >= 3 && tag[2] == "write" {
				continue
			}
			if isRelayURL(tag[1]) {
				urls = appendUniqueRelayURLs(urls, []string{tag[1]})
			}
		}
	}
	sort.Strings(urls)
	return urls
}

func relayURLsFromNIP66(events []*nostr.Event) []string {
	urls := make([]string, 0)
	for _, event := range events {
		if event == nil || event.Kind != relayDiscoveryKind {
			continue
		}
		for _, tag := range event.Tags {
			if len(tag) >= 2 && tag[0] == "d" && isRelayURL(tag[1]) {
				urls = appendUniqueRelayURLs(urls, []string{tag[1]})
			}
		}
	}
	sort.Strings(urls)
	return urls
}

func appendUniqueRelayURLs(base, additions []string) []string {
	seen := make(map[string]struct{}, len(base)+len(additions))
	result := make([]string, 0, len(base)+len(additions))
	for _, raw := range append(append([]string(nil), base...), additions...) {
		if !isRelayURL(raw) {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		result = append(result, raw)
	}
	return result
}

func isRelayURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "wss" || u.Scheme == "ws") && u.Host != ""
}

// query uses a short-lived direct relay connection for historical reads. The
// SDK's SimplePool fetch-many-replaceable helper does not reliably return
// events from the local control relay, while direct QuerySync is interoperable
// with both the local relay and public relays. Pool connections remain useful
// for publication and live subscriptions.
func (c *Client) query(ctx context.Context, urls []string, filter nostr.Filter) []*nostr.Event {
	all := make([]*nostr.Event, 0)
	for _, url := range urls {
		// Bound each historical query independently so one unavailable relay
		// cannot hold up the rest of the configured/discovered relay set.
		queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		relay, err := nostr.RelayConnect(context.Background(), url)
		if err != nil {
			cancel()
			log.Printf("catalogue relay %s: connect: %v", url, err)
			continue
		}
		events, err := relay.QuerySync(queryCtx, filter)
		cancel()
		if err != nil {
			log.Printf("catalogue relay %s: query: %v", url, err)
			continue
		}
		all = append(all, events...)
	}
	return all
}

// Publish sends an already signed event to every configured relay. A partial
// failure is returned as per-relay results so callers can report propagation
// without discarding successful publications.
func (c *Client) Publish(ctx context.Context, event nostr.Event) []PublishResult {
	results := make([]PublishResult, 0, len(c.urls))
	for result := range c.pool.PublishMany(ctx, c.urls, event) {
		results = append(results, PublishResult{Relay: result.RelayURL, Error: result.Error})
	}
	return results
}

// SubscribeAppDeclarations streams app declaration events from all configured
// relays until ctx is canceled. Duplicate and replaceable-event handling is
// provided by the SDK's pool.
func (c *Client) SubscribeAppDeclarations(ctx context.Context) <-chan nostr.RelayEvent {
	return c.pool.SubscribeMany(ctx, c.urls, nostr.Filter{
		Kinds: []int{protocol.AppDeclarationKind, protocol.LegacyAppDeclarationKind},
	})
}

// SubscribeEndorsements streams curator endorsement events from all relays.
func (c *Client) SubscribeEndorsements(ctx context.Context) <-chan nostr.RelayEvent {
	return c.pool.SubscribeMany(ctx, c.urls, nostr.Filter{Kinds: []int{30079}})
}

// SubscribeAttestations streams CI-backed attestation events from all
// relays, mirroring SubscribeEndorsements's live-only semantics: like
// endorsements, attestations are not backfilled by a historical fetch on
// startup, only accumulated from events seen after the subscription opens.
func (c *Client) SubscribeAttestations(ctx context.Context) <-chan nostr.RelayEvent {
	return c.pool.SubscribeMany(ctx, c.urls, nostr.Filter{Kinds: []int{verification.AttestationKind}})
}
