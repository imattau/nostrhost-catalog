package relay

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip77"
)

// memoryStore is a minimal in-memory nostr.RelayStore. It exists only to give
// NIP-77 reconciliation a "local" side to compare against: an *empty* store
// makes negentropy report every relay event as missing-to-us, so a DOWN sync
// fetches the relay's complete matching set. This is the WP5 replacement for
// an unbounded REQ, which the relay backend silently truncates to its default
// page (badger MaxLimit/4) and which therefore drops older events.
type memoryStore struct {
	mu     sync.Mutex
	events []*nostr.Event
}

func (s *memoryStore) Publish(_ context.Context, event nostr.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, &event)
	return nil
}

func (s *memoryStore) QueryEvents(_ context.Context, _ nostr.Filter) (chan *nostr.Event, error) {
	ch := make(chan *nostr.Event)
	go func() {
		defer close(ch)
		s.mu.Lock()
		snapshot := append([]*nostr.Event(nil), s.events...)
		s.mu.Unlock()
		for _, event := range snapshot {
			ch <- event
		}
	}()
	return ch, nil
}

func (s *memoryStore) QuerySync(_ context.Context, filter nostr.Filter) ([]*nostr.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*nostr.Event, 0, len(s.events))
	for _, event := range s.events {
		if filter.Matches(event) {
			out = append(out, event)
		}
	}
	return out, nil
}

func (s *memoryStore) snapshot() []*nostr.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*nostr.Event(nil), s.events...)
}

// FetchAll reconciles the complete set of relay events matching filter using
// NIP-77 negentropy (DOWN), instead of a single unbounded REQ.
//
// It reconciles against an empty in-memory store, so every relay event is
// "missing locally" and is pulled in. Falls back to a plain paged query when
// the relay does not support NIP-77 (the SDK surfaces that as an error), so a
// relay without negentropy still yields a (possibly truncated) result rather
// than nothing.
func (c *Client) FetchAll(ctx context.Context, filter nostr.Filter) []*nostr.Event {
	return c.fetchAll(ctx, c.urls, filter)
}

// fetchAll reconciles against an explicit URL list (declarations may add
// NIP-65-discovered publisher relays on top of the configured set).
func (c *Client) fetchAll(ctx context.Context, urls []string, filter nostr.Filter) []*nostr.Event {
	store := &memoryStore{}
	var lastErr error
	for _, url := range urls {
		if err := runWithDeadline(ctx, negentropyTimeout, func(syncCtx context.Context) error {
			return nostrNegentropySync(syncCtx, store, url, filter)
		}); err != nil {
			lastErr = err
			log.Printf("catalogue relay %s: negentropy sync: %v", url, err)
		}
	}
	if len(store.snapshot()) == 0 && lastErr != nil {
		// Negentropy unavailable or failed on every relay: fall back to the
		// legacy query so the caller still gets a (bounded) result.
		return c.query(ctx, urls, filter)
	}
	events := store.snapshot()
	slicesSortByCreatedAt(events)
	return events
}

// nostrNegentropySync is a package-level indirection so tests can substitute
// the reconciliation without a live relay.
var nostrNegentropySync = func(ctx context.Context, store nostr.RelayStore, url string, filter nostr.Filter) error {
	return nip77.NegentropySync(ctx, store, url, filter, nip77.Down)
}

// runWithDeadline bounds fn to timeout even when fn itself does not honour
// context cancellation. This matters here: go-nostr's nip77.NegentropySync
// has been observed to block indefinitely on a channel receive against a
// relay that doesn't complete the negentropy handshake (seen live against
// this project's own local relay, which fully hung Bootstrap() for hours -
// no per-relay context.WithTimeout ever fired because the library's inner
// wait loop doesn't select on ctx.Done()). Running fn in its own goroutine
// and racing it against a real timer means one unresponsive relay can no
// longer block every later relay in fetchAll's loop; the abandoned goroutine
// leaks until (if ever) the relay connection itself unblocks it, which is a
// bounded, per-relay-per-call cost worth paying to keep the sync loop live.
func runWithDeadline(ctx context.Context, timeout time.Duration, fn func(context.Context) error) error {
	syncCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- fn(syncCtx)
	}()
	select {
	case err := <-done:
		return err
	case <-syncCtx.Done():
		return syncCtx.Err()
	}
}

func slicesSortByCreatedAt(events []*nostr.Event) {
	sort.Slice(events, func(i, j int) bool {
		if events[i].CreatedAt != events[j].CreatedAt {
			return events[i].CreatedAt < events[j].CreatedAt
		}
		return events[i].ID < events[j].ID
	})
}

// negentropyTimeout bounds one relay's reconciliation so an unresponsive
// relay cannot stall the others.
var negentropyTimeout = 30 * time.Second

