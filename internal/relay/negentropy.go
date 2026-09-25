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
//
// Deduplicated by event ID, not appended to a plain slice: fetchAll syncs
// many relays concurrently (see fetchAllConcurrency), and public relays
// heavily overlap in which globally-known events they carry - a live OOM
// kill on the clean7 testbed (a 3.8GB VM) traced back to this store holding
// the same widely-relayed events once per relay that returned them, across
// tens of thousands of matching-kind events. Keying by ID keeps memory
// proportional to the distinct event set, not (relay count * event count).
type memoryStore struct {
	mu     sync.Mutex
	events map[string]*nostr.Event
}

func (s *memoryStore) Publish(_ context.Context, event nostr.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.events == nil {
		s.events = make(map[string]*nostr.Event)
	}
	s.events[event.ID] = &event
	return nil
}

func (s *memoryStore) QueryEvents(_ context.Context, _ nostr.Filter) (chan *nostr.Event, error) {
	ch := make(chan *nostr.Event)
	go func() {
		defer close(ch)
		for _, event := range s.snapshot() {
			ch <- event
		}
	}()
	return ch, nil
}

func (s *memoryStore) QuerySync(_ context.Context, filter nostr.Filter) ([]*nostr.Event, error) {
	snapshot := s.snapshot()
	out := make([]*nostr.Event, 0, len(snapshot))
	for _, event := range snapshot {
		if filter.Matches(event) {
			out = append(out, event)
		}
	}
	return out, nil
}

func (s *memoryStore) snapshot() []*nostr.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*nostr.Event, 0, len(s.events))
	for _, event := range s.events {
		out = append(out, event)
	}
	return out
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
//
// URLs are synced concurrently, bounded by fetchAllConcurrency: a serial loop
// over a NIP-65/66-discovered relay set can easily reach into the hundreds
// of URLs, and at negentropyTimeout=30s per relay a serial pass takes
// (relay count * 30s) - live-observed taking well over half an hour against
// a real discovered set, which starves the whole sync loop (Bootstrap must
// finish this call before Run() ever reaches the live subscribe/apply loop
// that would otherwise pick up new declarations). Bounding concurrency
// rather than firing every relay at once keeps the local machine from
// opening hundreds of simultaneous websocket connections.
func (c *Client) fetchAll(ctx context.Context, urls []string, filter nostr.Filter) []*nostr.Event {
	store := &memoryStore{}
	var mu sync.Mutex
	var lastErr error
	sem := make(chan struct{}, fetchAllConcurrency)
	var wg sync.WaitGroup
	for _, url := range urls {
		url := url
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			if err := runWithDeadline(ctx, negentropyTimeout, func(syncCtx context.Context) error {
				return nostrNegentropySync(syncCtx, store, url, filter)
			}); err != nil {
				mu.Lock()
				lastErr = err
				mu.Unlock()
				log.Printf("catalogue relay %s: negentropy sync: %v", url, err)
			}
		}()
	}
	wg.Wait()
	if len(store.snapshot()) == 0 && lastErr != nil {
		// Negentropy unavailable or failed on every relay: fall back to the
		// legacy query so the caller still gets a (bounded) result.
		return c.query(ctx, urls, filter)
	}
	events := store.snapshot()
	slicesSortByCreatedAt(events)
	return events
}

// fetchAllConcurrency bounds how many relays fetchAll syncs at once.
const fetchAllConcurrency = 16

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

