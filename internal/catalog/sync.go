package catalog

import (
	"context"
	"fmt"
	"log"
	"time"

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
		if err != nil {
			log.Printf("catalogue: ignored bootstrap declaration: %v", err)
			ignored++
			continue
		}
		if !changed {
			ignored++
			continue
		}
		accepted++
	}
	return accepted, ignored
}

func BootstrapAttestations(ctx context.Context, client *relay.Client, store *Store) (accepted, ignored int) {
	for _, event := range client.FetchAttestations(ctx) {
		if event == nil {
			ignored++
			continue
		}
		changed, err := store.ApplyAttestation(*event)
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
	accepted, ignored := Bootstrap(ctx, client, store)
	log.Printf("catalogue: bootstrap accepted=%d ignored=%d", accepted, ignored)
	attestationsAccepted, attestationsIgnored := BootstrapAttestations(ctx, client, store)
	log.Printf("catalogue: attestation bootstrap accepted=%d ignored=%d", attestationsAccepted, attestationsIgnored)
resubscribe:
	for {
		events := client.SubscribeAppDeclarations(ctx)
		attestationEvents := client.SubscribeAttestations(ctx)
		select {
		case <-ctx.Done():
			return store.Save(statePath)
		case relayEvent, ok := <-events:
			if !ok {
				if err := store.Save(statePath); err != nil {
					return err
				}
				if ctx.Err() != nil {
					return nil
				}
				if err := waitToResubscribe(ctx); err != nil {
					return err
				}
				continue resubscribe
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
		case relayEvent, ok := <-attestationEvents:
			if !ok {
				if err := store.Save(statePath); err != nil {
					return err
				}
				if ctx.Err() != nil {
					return nil
				}
				if err := waitToResubscribe(ctx); err != nil {
					return err
				}
				continue resubscribe
			}
			if relayEvent.Event == nil {
				continue
			}
			changed, err := store.ApplyAttestation(*relayEvent.Event)
			if err != nil {
				log.Printf("catalogue: ignored attestation: %v", err)
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

func waitToResubscribe(ctx context.Context) error {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil
	case <-timer.C:
		return nil
	}
}
