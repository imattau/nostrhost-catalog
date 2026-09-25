package relay

import (
	"context"
	"errors"
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
	original := nostrNegentropySync
	originalTimeout := negentropyTimeout
	defer func() {
		nostrNegentropySync = original
		negentropyTimeout = originalTimeout
	}()
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
