package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/imattau/nostrhost-catalog/internal/protocol"
	"github.com/imattau/nostrhost-catalog/internal/relay"
)

// canonicalEntry is a declaration with the cosmetic logo fields excluded, so a
// rebuild never reports drift merely because a logo fetch did or did not run.
type canonicalEntry struct {
	Declaration protocol.AppDeclaration `json:"declaration"`
	EventID     string                  `json:"event_id"`
	CreatedAt   int64                   `json:"created_at"`
}

// Canonical returns a stable, logo-independent digest of the projection. Two
// stores built from the same relay history produce the same digest, so
// `--verify` can compare the on-disk projection to a fresh rebuild.
func (s *Store) Canonical() (string, error) {
	entries := make([]canonicalEntry, 0)
	for _, entry := range s.Snapshot() {
		entries = append(entries, canonicalEntry{
			Declaration: entry.Declaration,
			EventID:     entry.EventID,
			CreatedAt:   int64(entry.CreatedAt),
		})
	}
	state := struct {
		Entries      []canonicalEntry   `json:"entries"`
		Attestations []AttestationEntry `json:"attestations"`
	}{Entries: entries, Attestations: attestationSnapshot(s.attestations)}
	payload, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("encode canonical catalogue: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// Rebuild fetches the full declaration + attestation history from the relays
// into a fresh store, ignoring any on-disk projection. This is the WP5
// "catalogue.json is disposable" path: the relay events are authoritative and
// the file is only a derived read model. Returns how many declarations and
// attestations were applied.
func Rebuild(ctx context.Context, client *relay.Client, store *Store) (declarations int, attestations int, err error) {
	if client == nil || store == nil {
		return 0, 0, fmt.Errorf("rebuild requires a relay client and store")
	}
	declarations, _ = Bootstrap(ctx, client, store)
	attestations, _ = BootstrapAttestations(ctx, client, store)
	return declarations, attestations, nil
}
