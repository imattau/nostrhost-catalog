package catalog

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/imattau/nostrhost-catalog/internal/protocol"
	"github.com/imattau/nostrhost-catalog/internal/relay"
	"github.com/imattau/nostrhost-catalog/internal/repository"
)

// Bootstrap fetches the latest declaration for every configured publisher
// from the relays. Invalid or untrusted events are ignored; relay state is
// untrusted input and must not prevent the provider from starting.
func Bootstrap(ctx context.Context, client *relay.Client, store *Store, logoDirectory string) (accepted, ignored int) {
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
		if declaration, err := store.publishers.Validate(*event); err == nil {
			extractLogo(ctx, store, declaration, logoDirectory)
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
func Run(ctx context.Context, client *relay.Client, store *Store, statePath, logoDirectory string) error {
	if client == nil || store == nil {
		return fmt.Errorf("catalogue sync requires a relay client and store")
	}
	if statePath == "" {
		return fmt.Errorf("catalogue sync state path is empty")
	}
	// Backfill logos before the relay crawl. Bootstrap reads a best-effort,
	// potentially very large relay set and can take minutes (or stall on an
	// unresponsive relay); app logos are derived from the already-persisted
	// projection, so they must not be gated behind it. Entries persisted
	// before logo extraction existed (or whose previous attempt did not
	// complete) are filled in once here.
	backfilled := backfillLogos(ctx, store, logoDirectory)
	if backfilled > 0 {
		log.Printf("catalogue: backfilled %d app logos", backfilled)
	}
	if err := store.Save(statePath); err != nil {
		return err
	}
	accepted, ignored := Bootstrap(ctx, client, store, logoDirectory)
	log.Printf("catalogue: bootstrap accepted=%d ignored=%d", accepted, ignored)
	attestationsAccepted, attestationsIgnored := BootstrapAttestations(ctx, client, store)
	log.Printf("catalogue: attestation bootstrap accepted=%d ignored=%d", attestationsAccepted, attestationsIgnored)
	if err := store.Save(statePath); err != nil {
		return err
	}
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
				if declaration, err := store.publishers.Validate(*relayEvent.Event); err == nil {
					extractLogo(ctx, store, declaration, logoDirectory)
				}
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

// extractLogo pulls the app's optional logo.png from its declared repository
// and records the content hash on the current projection. Failures are logged
// and swallowed: a logo is cosmetic and must never stop a sync.
func extractLogo(ctx context.Context, store *Store, declaration protocol.AppDeclaration, logoDirectory string) {
	if logoDirectory == "" {
		return
	}
	hash, err := repository.FetchLogo(ctx, declaration, logoDirectory)
	if err != nil {
		log.Printf("catalogue: logo extraction for %s failed: %v", declaration.AppID, err)
		return
	}
	store.SetLogoResult(declaration.Publisher, declaration.AppID, hash)
}

// backfillLogos extracts logos for entries that predate logo extraction or
// whose previous attempt did not complete, returning how many were updated.
func backfillLogos(ctx context.Context, store *Store, logoDirectory string) int {
	if logoDirectory == "" {
		return 0
	}
	updated := 0
	for _, entry := range store.Snapshot() {
		if entry.LogoChecked {
			continue
		}
		hash, err := repository.FetchLogo(ctx, entry.Declaration, logoDirectory)
		if err != nil {
			log.Printf("catalogue: logo backfill for %s failed: %v", entry.Declaration.AppID, err)
			continue
		}
		store.SetLogoResult(entry.Declaration.Publisher, entry.Declaration.AppID, hash)
		updated++
	}
	return updated
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
