package relay

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// TestMemoryStorePublishAndQuery covers the tiny RelayStore used as the local
// side of negentropy reconciliation.
func TestMemoryStorePublishAndQuery(t *testing.T) {
	store := &memoryStore{}
	events := []nostr.Event{
		{ID: "a", Kind: 32267, CreatedAt: 10},
		{ID: "b", Kind: 32267, CreatedAt: 20},
	}
	for i := range events {
		if err := store.Publish(context.Background(), events[i]); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	all, err := store.QuerySync(context.Background(), nostr.Filter{})
	if err != nil || len(all) != 2 {
		t.Fatalf("QuerySync returned %d events, err=%v; want 2", len(all), err)
	}
	matching, err := store.QuerySync(context.Background(), nostr.Filter{Kinds: []int{32267}})
	if err != nil || len(matching) != 2 {
		t.Fatalf("kind filter matched %d events, err=%v; want 2", len(matching), err)
	}
	none, _ := store.QuerySync(context.Background(), nostr.Filter{Kinds: []int{1}})
	if len(none) != 0 {
		t.Fatalf("kind filter matched %d events, want 0", len(none))
	}
}

// TestMemoryStoreDeduplicatesByID guards against the live-observed OOM kill:
// concurrent relays that carry the same globally-known event must not each
// add their own copy - memory has to stay proportional to the distinct
// event set, not (relay count * event count).
func TestMemoryStoreDeduplicatesByID(t *testing.T) {
	store := &memoryStore{}
	for i := 0; i < 50; i++ {
		// 50 "relays" all republishing the same 3 distinct event IDs.
		for _, id := range []string{"a", "b", "c"} {
			if err := store.Publish(context.Background(), nostr.Event{ID: id, Kind: 32267, CreatedAt: 1}); err != nil {
				t.Fatalf("Publish: %v", err)
			}
		}
	}
	got := store.snapshot()
	if len(got) != 3 {
		t.Fatalf("snapshot has %d events after 50x republishing 3 ids, want 3 (deduplicated)", len(got))
	}
}

// TestFetchAllUsesNegentropyAndSorts verifies the pinned DOWN reconciliation
// populates the store and the result is returned oldest-first.
func TestFetchAllUsesNegentropyAndSorts(t *testing.T) {
	original := nostrNegentropySync
	defer func() { nostrNegentropySync = original }()
	nostrNegentropySync = func(ctx context.Context, store nostr.RelayStore, url string, filter nostr.Filter) error {
		for _, ev := range []nostr.Event{
			{ID: "new", Kind: 32267, CreatedAt: 20},
			{ID: "old", Kind: 32267, CreatedAt: 10},
		} {
			e := ev
			if err := store.Publish(ctx, e); err != nil {
				return err
			}
		}
		return nil
	}

	c := &Client{urls: []string{"ws://relay"}}
	got := c.FetchAll(context.Background(), nostr.Filter{Kinds: []int{32267}})
	if len(got) != 2 || got[0].ID != "old" || got[1].ID != "new" {
		t.Fatalf("FetchAll returned %v, want [old new] sorted by created_at", got)
	}
}

// TestFetchAllFallsBackWhenNegentropyUnsupported verifies a relay without
// NIP-77 still yields the legacy (bounded) query result rather than nothing.
func TestFetchAllFallsBackWhenNegentropyUnsupported(t *testing.T) {
	original := nostrNegentropySync
	defer func() { nostrNegentropySync = original }()
	nostrNegentropySync = func(ctx context.Context, store nostr.RelayStore, url string, filter nostr.Filter) error {
		return errors.New("relay does not support NIP-77")
	}

	c := &Client{urls: []string{"ws://relay"}}
	// No live relay: the fallback query yields nothing, but it must not panic
	// and must be the code path taken (lastErr present, store empty).
	got := c.FetchAll(context.Background(), nostr.Filter{Kinds: []int{32267}})
	if len(got) != 0 {
		t.Fatalf("FetchAll fallback returned %d events, want 0 from an unreachable relay", len(got))
	}
}

// TestFetchAllSurvivesAStuckRelay reproduces the live-observed hang: a relay
// whose negentropy sync never returns and ignores the context it was given
// (go-nostr's nip77.NegentropySync has been seen blocking on a channel
// receive indefinitely against a relay that never completes the handshake).
// A second, working relay must still contribute its events - one stuck relay
// must not wedge fetchAll (and by extension catalog.Bootstrap) forever.
func TestFetchAllSurvivesAStuckRelay(t *testing.T) {
	// Deliberately not restoring nostrNegentropySync via defer: the whole
	// point of this test is a goroutine that blocks forever and leaks past
	// the test's own return, and every other test in this file sets its own
	// nostrNegentropySync before calling fetchAll/FetchAll, so leaving this
	// one in place is harmless - restoring it here would race the leaked
	// goroutine's read of the var against this test's write to it.
	originalTimeout := negentropyTimeout
	defer func() { negentropyTimeout = originalTimeout }()
	negentropyTimeout = 20 * time.Millisecond
	nostrNegentropySync = func(ctx context.Context, store nostr.RelayStore, url string, filter nostr.Filter) error {
		if url == "ws://stuck" {
			<-make(chan struct{}) // never returns, and ignores ctx - the observed bug
		}
		return store.Publish(ctx, nostr.Event{ID: "ok", Kind: 32267, CreatedAt: 1})
	}

	c := &Client{urls: []string{"ws://stuck", "ws://working"}}
	done := make(chan []*nostr.Event, 1)
	go func() { done <- c.FetchAll(context.Background(), nostr.Filter{Kinds: []int{32267}}) }()

	select {
	case got := <-done:
		if len(got) != 1 || got[0].ID != "ok" {
			t.Fatalf("FetchAll returned %v, want the working relay's event despite the stuck one", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FetchAll did not return within 2s - a stuck relay blocked the whole batch")
	}
}

// TestFetchAllRunsRelaysConcurrently guards against regressing back to a
// serial relay loop. A NIP-65/66-discovered relay set can reach into the
// hundreds; live-observed against real discovered relays, a serial pass at
// ~30s/relay took well over half an hour. Here 40 relays each take 50ms -
// serially that is 2s, so a wall-clock budget well under that (400ms) only
// passes if fetchAll actually overlaps the calls.
func TestFetchAllRunsRelaysConcurrently(t *testing.T) {
	original := nostrNegentropySync
	defer func() { nostrNegentropySync = original }()
	const relayCount = 40
	const perRelay = 50 * time.Millisecond
	nostrNegentropySync = func(ctx context.Context, store nostr.RelayStore, url string, filter nostr.Filter) error {
		time.Sleep(perRelay)
		return store.Publish(ctx, nostr.Event{ID: url, Kind: 32267, CreatedAt: 1})
	}

	urls := make([]string, relayCount)
	for i := range urls {
		urls[i] = fmt.Sprintf("ws://relay-%d", i)
	}
	c := &Client{urls: urls}

	start := time.Now()
	got := c.FetchAll(context.Background(), nostr.Filter{Kinds: []int{32267}})
	elapsed := time.Since(start)

	if len(got) != relayCount {
		t.Fatalf("FetchAll returned %d events, want %d (one per relay)", len(got), relayCount)
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("FetchAll took %v for %d relays at %v each - relays are not running concurrently", elapsed, relayCount, perRelay)
	}
}
