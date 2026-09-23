// Package trust contains local administrator trust policies.
package trust

import (
	"fmt"
	"sort"

	"github.com/imattau/nostrhost-catalog/internal/protocol"
	"github.com/nbd-wtf/go-nostr"
)

// ExplicitPublishers accepts declarations only from configured publisher
// public keys. Keys may be supplied as lowercase hex keys or npub values.
type ExplicitPublishers struct {
	trusted map[string]struct{}
}

// NewExplicitPublishers builds the MVP allow-list policy.
func NewExplicitPublishers(keys []string) (ExplicitPublishers, error) {
	trusted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		publicKey, err := protocol.NormalizePublicKey(key)
		if err != nil {
			return ExplicitPublishers{}, fmt.Errorf("invalid publisher public key: %w", err)
		}
		trusted[publicKey] = struct{}{}
	}
	return ExplicitPublishers{trusted: trusted}, nil
}

// Publishers returns the normalized hexadecimal publisher keys in stable order.
func (p ExplicitPublishers) Publishers() []string {
	keys := make([]string, 0, len(p.trusted))
	for key := range p.trusted {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Allows reports whether a normalized publisher key is in the local trust
// policy. It is used when loading the derived catalogue projection.
func (p ExplicitPublishers) Allows(publicKey string) bool {
	_, ok := p.trusted[publicKey]
	return ok
}

// Validate verifies the event cryptographically, validates its declaration,
// and applies the local publisher allow-list.
func (p ExplicitPublishers) Validate(event nostr.Event) (protocol.AppDeclaration, error) {
	if _, ok := p.trusted[event.PubKey]; !ok {
		return protocol.AppDeclaration{}, fmt.Errorf("publisher %s is not trusted", event.PubKey)
	}
	if err := protocol.VerifyID(event); err != nil {
		return protocol.AppDeclaration{}, err
	}
	if err := protocol.VerifySignature(event); err != nil {
		return protocol.AppDeclaration{}, err
	}
	if event.Kind == protocol.NpackReleaseKind {
		return protocol.ParseFromNpackRelease(event)
	}
	return protocol.ParseAppDeclaration(event)
}
