package relay

import (
	"context"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestNewRejectsInvalidRelay(t *testing.T) {
	if _, err := New(context.Background(), []string{"https://relay.example"}); err == nil {
		t.Fatal("New() accepted an HTTPS URL as a relay")
	}
}

func TestRelayURLsFromNIP65UsesNewestReadList(t *testing.T) {
	old := &nostr.Event{PubKey: "publisher", Kind: relayListKind, CreatedAt: nostr.Timestamp(10), Tags: nostr.Tags{{"r", "wss://old.example"}}}
	newest := &nostr.Event{PubKey: "publisher", Kind: relayListKind, CreatedAt: nostr.Timestamp(20), Tags: nostr.Tags{
		{"r", "wss://read.example", "read"},
		{"r", "wss://write.example", "write"},
		{"r", "not-a-relay"},
	}}
	got := relayURLsFromNIP65([]*nostr.Event{old, newest})
	if len(got) != 1 || got[0] != "wss://read.example" {
		t.Fatalf("got relay URLs %v, want only the newest read relay", got)
	}
}

func TestAppendUniqueRelayURLsFiltersAndDeduplicates(t *testing.T) {
	got := appendUniqueRelayURLs([]string{"wss://one.example"}, []string{
		"wss://one.example", "https://invalid.example", "ws://two.example",
	})
	want := []string{"wss://one.example", "ws://two.example"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got relay URLs %v, want %v", got, want)
	}
}

func TestRelayURLsFromNIP66UsesRelayDiscoveryDTag(t *testing.T) {
	events := []*nostr.Event{
		{Kind: relayDiscoveryKind, Tags: nostr.Tags{{"d", "wss://discovered.example"}, {"r", "wss://ignored.example"}}},
		{Kind: relayListKind, Tags: nostr.Tags{{"d", "wss://wrong-kind.example"}}},
	}
	got := relayURLsFromNIP66(events)
	if len(got) != 1 || got[0] != "wss://discovered.example" {
		t.Fatalf("got relay URLs %v, want the NIP-66 d-tag relay", got)
	}
}

func TestNewDeduplicatesRelays(t *testing.T) {
	client, err := New(context.Background(), []string{
		"wss://relay.example",
		"wss://relay.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.urls) != 1 {
		t.Fatalf("got %d relay URLs, want 1", len(client.urls))
	}
}

func TestNewRequiresRelay(t *testing.T) {
	if _, err := New(context.Background(), nil); err == nil {
		t.Fatal("New() accepted an empty relay list")
	}
}
