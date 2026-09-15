// Package catalog projects trusted app-declaration events into a local view.
package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/imattau/nostrhost-catalog/internal/protocol"
	"github.com/imattau/nostrhost-catalog/internal/trust"
	"github.com/imattau/nostrhost-catalog/internal/verification"
	"github.com/nbd-wtf/go-nostr"
)

// Entry is one accepted declaration and the event metadata needed to reject
// stale replaceable-event replays.
type Entry struct {
	Declaration protocol.AppDeclaration `json:"declaration"`
	EventID     string                  `json:"event_id"`
	CreatedAt   nostr.Timestamp         `json:"created_at"`
}

type AttestationEntry struct {
	Attestation verification.Attestation
	EventID     string
	CreatedAt   nostr.Timestamp
}

// Store is the fork-native catalogue projection. The relay remains the
// source of events; this store is only a derived, restartable read model.
type Store struct {
	publishers   trust.ExplicitPublishers
	entries      map[string]Entry
	attestations map[string]AttestationEntry
}

// New creates a catalogue projection with an explicit publisher allow-list.
func New(publishers trust.ExplicitPublishers) *Store {
	return &Store{publishers: publishers, entries: make(map[string]Entry), attestations: make(map[string]AttestationEntry)}
}

// ApplyAttestation validates and stores one CI attestation. It is retained
// independently of declarations so relay replay order cannot matter.
func (s *Store) ApplyAttestation(event nostr.Event) (bool, error) {
	attestation, err := verification.Parse(event)
	if err != nil {
		return false, err
	}
	key := attestation.Verifier + "\x00" + attestation.AppID + "\x00" + attestation.Commit
	if previous, ok := s.attestations[key]; ok && event.CreatedAt <= previous.CreatedAt {
		return false, nil
	}
	s.attestations[key] = AttestationEntry{Attestation: attestation, EventID: event.ID, CreatedAt: event.CreatedAt}
	return true, nil
}

// AttestationsFor returns attestations matching the declaration's complete
// repository and content identity, not merely its app ID or commit.
func (s *Store) AttestationsFor(declaration protocol.AppDeclaration) []verification.Attestation {
	result := make([]verification.Attestation, 0)
	for _, entry := range s.attestations {
		a := entry.Attestation
		if a.AppID == declaration.AppID && a.Repository == declaration.Repository && a.Commit == declaration.Commit && a.ManifestHash == declaration.ManifestHash && a.ContentHash == declaration.ContentHash {
			result = append(result, a)
		}
	}
	return result
}

// ResolveInstallable resolves the canonical declaration and applies the
// configured CI-attestation policy to its exact revision.
func (s *Store) ResolveInstallable(appID string, policy trust.AttestationPolicy) (protocol.AppDeclaration, trust.AttestationDecision, bool) {
	declaration, ok := s.Resolve(appID)
	if !ok {
		return protocol.AppDeclaration{}, trust.AttestationDecision{}, false
	}
	decision := policy.Evaluate(s.AttestationsFor(declaration))
	if !decision.Accepted {
		return protocol.AppDeclaration{}, decision, false
	}
	return declaration, decision, true
}

// Apply validates and projects one declaration. It returns true only when
// the local view changed. Older or equal events are harmless replay noise.
func (s *Store) Apply(event nostr.Event) (bool, error) {
	declaration, err := s.publishers.Validate(event)
	if err != nil {
		return false, err
	}
	key := declaration.Publisher + "\x00" + declaration.AppID
	if previous, ok := s.entries[key]; ok && event.CreatedAt <= previous.CreatedAt {
		return false, nil
	}
	s.entries[key] = Entry{Declaration: declaration, EventID: event.ID, CreatedAt: event.CreatedAt}
	return true, nil
}

// Resolve returns the newest trusted declaration for an app ID. If several
// trusted publishers declare the same app, ties are resolved by publisher key
// so all nodes expose the same deterministic result.
func (s *Store) Resolve(appID string) (protocol.AppDeclaration, bool) {
	var selected Entry
	found := false
	for _, entry := range s.entries {
		if entry.Declaration.AppID != appID {
			continue
		}
		if !found || entry.CreatedAt > selected.CreatedAt ||
			(entry.CreatedAt == selected.CreatedAt && entry.Declaration.Publisher < selected.Declaration.Publisher) {
			selected, found = entry, true
		}
	}
	if !found {
		return protocol.AppDeclaration{}, false
	}
	return selected.Declaration, true
}

// Snapshot returns all projected entries in stable app/publisher order.
func (s *Store) Snapshot() []Entry {
	entries := make([]Entry, 0, len(s.entries))
	for _, entry := range s.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Declaration.AppID != entries[j].Declaration.AppID {
			return entries[i].Declaration.AppID < entries[j].Declaration.AppID
		}
		return entries[i].Declaration.Publisher < entries[j].Declaration.Publisher
	})
	return entries
}

// NewFromPublishers is a convenience constructor for configuration loaders.
func NewFromPublishers(keys []string) (*Store, error) {
	policy, err := trust.NewExplicitPublishers(keys)
	if err != nil {
		return nil, fmt.Errorf("configure catalogue publishers: %w", err)
	}
	return New(policy), nil
}

type diskEntry struct {
	Declaration protocol.AppDeclaration `json:"declaration"`
	EventID     string                  `json:"event_id"`
	CreatedAt   nostr.Timestamp         `json:"created_at"`
}

type diskAttestation struct {
	Attestation verification.Attestation `json:"attestation"`
	EventID     string                   `json:"event_id"`
	CreatedAt   nostr.Timestamp          `json:"created_at"`
}

type diskState struct {
	Entries      []diskEntry       `json:"entries"`
	Attestations []diskAttestation `json:"attestations"`
}

// Save writes the derived projection atomically. It intentionally stores no
// private keys or raw untrusted events; the relay remains the replay source.
func (s *Store) Save(path string) error {
	if path == "" {
		return fmt.Errorf("catalogue state path is empty")
	}
	entries := s.Snapshot()
	disks := diskState{Entries: entriesToDisk(entries), Attestations: attestationsToDisk(s.attestations)}
	payload, err := json.Marshal(disks)
	if err != nil {
		return fmt.Errorf("encode catalogue state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create catalogue state directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".catalogue-*.tmp")
	if err != nil {
		return fmt.Errorf("create catalogue state temporary file: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect catalogue state: %w", err)
	}
	if _, err := temporary.Write(payload); err != nil {
		temporary.Close()
		return fmt.Errorf("write catalogue state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close catalogue state: %w", err)
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("install catalogue state: %w", err)
	}
	return nil
}

// Load restores a previously saved projection. Publisher membership is
// rechecked so a changed allow-list cannot resurrect data; the relay remains
// responsible for cryptographic revalidation during the next replay.
func Load(path string, publishers trust.ExplicitPublishers) (*Store, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read catalogue state: %w", err)
	}
	var state diskState
	if err := json.Unmarshal(data, &state); err != nil {
		// Accept the original array-only projection format during upgrade.
		if legacyErr := json.Unmarshal(data, &state.Entries); legacyErr != nil {
			return nil, fmt.Errorf("decode catalogue state: %w", err)
		}
	}
	store := New(publishers)
	for _, item := range state.Entries {
		if item.Declaration.AppID == "" || item.Declaration.Publisher == "" || item.EventID == "" {
			return nil, fmt.Errorf("catalogue state contains an incomplete entry")
		}
		if !publishers.Allows(item.Declaration.Publisher) {
			return nil, fmt.Errorf("catalogue state contains an untrusted publisher %s", item.Declaration.Publisher)
		}
		key := item.Declaration.Publisher + "\x00" + item.Declaration.AppID
		store.entries[key] = Entry{Declaration: item.Declaration, EventID: item.EventID, CreatedAt: item.CreatedAt}
	}
	for _, item := range state.Attestations {
		if item.Attestation.AppID == "" || item.Attestation.Verifier == "" || item.EventID == "" {
			return nil, fmt.Errorf("catalogue state contains an incomplete attestation")
		}
		key := item.Attestation.Verifier + "\x00" + item.Attestation.AppID + "\x00" + item.Attestation.Commit
		store.attestations[key] = AttestationEntry{Attestation: item.Attestation, EventID: item.EventID, CreatedAt: item.CreatedAt}
	}
	return store, nil
}

// LoadFromPublishers combines trust-policy configuration with projection
// restore for command and daemon entry points.
func LoadFromPublishers(path string, keys []string) (*Store, error) {
	policy, err := trust.NewExplicitPublishers(keys)
	if err != nil {
		return nil, fmt.Errorf("configure catalogue publishers: %w", err)
	}
	return Load(path, policy)
}

func entriesToDisk(entries []Entry) []diskEntry {
	result := make([]diskEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, diskEntry{Declaration: entry.Declaration, EventID: entry.EventID, CreatedAt: entry.CreatedAt})
	}
	return result
}

func attestationsToDisk(entries map[string]AttestationEntry) []diskAttestation {
	result := make([]diskAttestation, 0, len(entries))
	for _, entry := range entries {
		result = append(result, diskAttestation{Attestation: entry.Attestation, EventID: entry.EventID, CreatedAt: entry.CreatedAt})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Attestation.Verifier+result[i].Attestation.AppID+result[i].Attestation.Commit < result[j].Attestation.Verifier+result[j].Attestation.AppID+result[j].Attestation.Commit
	})
	return result
}
