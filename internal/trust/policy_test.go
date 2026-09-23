package trust

import (
	"strings"
	"testing"

	"github.com/imattau/nostrhost-catalog/internal/protocol"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

func signedEvent(t *testing.T) nostr.Event {
	t.Helper()
	privateKey := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	publicKey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	event := nostr.Event{
		CreatedAt: nostr.Timestamp(1),
		Kind:      30078,
		Tags: nostr.Tags{
			{"d", "hello_nostr"},
			{"platform", "yunohost"},
			{"repo", "https://github.com/example/hello_nostr_ynh"},
			{"version", "1.0.0~ynh1"},
			{"commit", "cccccccccccccccccccccccccccccccccccccccc"},
			{"manifest", "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},
			{"content", "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},
		},
		Content: `{"name":"Hello Nostr"}`,
		PubKey:  publicKey,
	}
	if err := event.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestExplicitPublishersAcceptsNpub(t *testing.T) {
	event := signedEvent(t)
	npub, err := nip19.EncodePublicKey(event.PubKey)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := NewExplicitPublishers([]string{npub})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.Validate(event); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func signedNpackReleaseEvent(t *testing.T) nostr.Event {
	t.Helper()
	privateKey := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	publicKey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	event := nostr.Event{
		CreatedAt: nostr.Timestamp(1),
		Kind:      protocol.NpackReleaseKind,
		Tags: nostr.Tags{
			{"d", "opencode-web_nh/1.18.30/x86_64"},
			{"name", "opencode-web_nh"},
			{"version", "1.18.30"},
			{"x", strings.Repeat("3", 64)},
			{"repo", "30617:" + strings.Repeat("2", 64) + ":opencode-web_nh"},
			{"commit", strings.Repeat("5", 40)},
		},
		PubKey: publicKey,
	}
	if err := event.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestExplicitPublishersValidateRoutesNpackReleaseKind(t *testing.T) {
	event := signedNpackReleaseEvent(t)
	policy, err := NewExplicitPublishers([]string{event.PubKey})
	if err != nil {
		t.Fatal(err)
	}
	declaration, err := policy.Validate(event)
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if declaration.AppID != "opencode-web_nh" || declaration.Commit != strings.Repeat("5", 40) {
		t.Fatalf("Validate() did not route a kind-%d event through ParseFromNpackRelease: %+v", protocol.NpackReleaseKind, declaration)
	}
}

func TestExplicitPublishersRejectsUnknownPublisher(t *testing.T) {
	event := signedEvent(t)
	otherKey, err := nostr.GetPublicKey("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	policy, err := NewExplicitPublishers([]string{otherKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := policy.Validate(event); err == nil {
		t.Fatal("Validate() accepted an untrusted publisher")
	}
}
