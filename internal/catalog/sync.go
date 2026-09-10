package catalog

import (
	"context"
	"fmt"
	"log"

	"github.com/imattau/nostrhost-catalog/internal/relay"
)

// Bootstrap fetches the latest declaration for every configured publisher
// from the relays. Invalid or untrusted events are ignored; relay state is
// untrusted input and must not prevent the provider from starting.
func Bootstrap(ctx context.Context, client *relay.Client, store *Store) (accepted, ignored int) {
	for _, event := range client.FetchAppDeclarations(ctx, store.publishers.Publishers()) {
		if event == nil {
			ignored++
			continue
		}
		changed, err := store.Apply(*event)
		if err != nil || !changed {
			ignored++
			continue
		}
		accepted++
	}
	return accepted, ignored
}

// Run subscribes to live declarations after bootstrapping the projection.
// It returns when ctx is canceled or when the relay subscription closes. A
// snapshot is saved after every accepted event and once more on shutdown.
func Run(ctx context.Context, client *relay.Client, store *Store, statePath string) error {
	if client == nil || store == nil {
		return fmt.Errorf("catalogue sync requires a relay client and store")
	}
	if statePath == "" {
		return fmt.Errorf("catalogue sync state path is empty")
	}
	Bootstrap(ctx, client, store)
	events := client.SubscribeAppDeclarations(ctx)
	for {
		select {
		case <-ctx.Done():
			return store.Save(statePath)
		case relayEvent, ok := <-events:
			if !ok {
				return store.Save(statePath)
			}
			if relayEvent.Event == nil {
				continue
			}
			changed, err := store.Apply(*relayEvent.Event)
			if err != nil {
				log.Printf("catalogue: ignored declaration: %v", err)
				continue
			}
			if changed {
				if err := store.Save(statePath); err != nil {
					return err
				}
			}
		}
	}
}
