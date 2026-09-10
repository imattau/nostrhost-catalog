package catalog

import (
	"path/filepath"
	"testing"

	"github.com/imattau/nostrhost-catalog/internal/protocol"
	"github.com/imattau/nostrhost-catalog/internal/publisher"
	"github.com/imattau/nostrhost-catalog/internal/trust"
	"github.com/imattau/nostrhost-catalog/internal/verification"
	"github.com/nbd-wtf/go-nostr"
)

const privateKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func declaration(t *testing.T, privateKey, version string) nostr.Event {
	t.Helper()
	event, err := publisher.BuildDeclaration(publisher.Metadata{
		AppID: "hello_nostr", Repository: "https://github.com/example/hello_nostr_ynh",
		Version: version, Commit: "cccccccccccccccccccccccccccccccccccccccc",
		ManifestHash: publisher.HashBytes([]byte("manifest-" + version)),
		ContentHash:  publisher.HashBytes([]byte("tree-" + version)), Name: "Hello Nostr",
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestStoreProjectsTrustedDeclarationsAndIgnoresReplay(t *testing.T) {
	event := declaration(t, privateKey, "1.0.0~ynh1")
	policy, err := trust.NewExplicitPublishers([]string{event.PubKey})
	if err != nil {
		t.Fatal(err)
	}
	store := New(policy)
	changed, err := store.Apply(event)
	if err != nil || !changed {
		t.Fatalf("first Apply() = (%v, %v), want changed", changed, err)
	}
	changed, err = store.Apply(event)
	if err != nil || changed {
		t.Fatalf("replay Apply() = (%v, %v), want unchanged", changed, err)
	}
	resolved, ok := store.Resolve("hello_nostr")
	if !ok || resolved.Version != "1.0.0~ynh1" {
		t.Fatalf("Resolve() = (%+v, %v)", resolved, ok)
	}
}

func TestStoreRejectsUntrustedAndStaleDeclarations(t *testing.T) {
	trusted := declaration(t, privateKey, "1.0.0~ynh1")
	otherKey := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	untrusted := declaration(t, otherKey, "9.9.9~ynh1")
	policy, err := trust.NewExplicitPublishers([]string{trusted.PubKey})
	if err != nil {
		t.Fatal(err)
	}
	store := New(policy)
	if _, err := store.Apply(untrusted); err == nil {
		t.Fatal("Apply() accepted an untrusted publisher")
	}
	if _, err := store.Apply(trusted); err != nil {
		t.Fatal(err)
	}
	stale := trusted
	stale.CreatedAt = trusted.CreatedAt - 1
	if err := stale.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.Apply(stale); err != nil || changed {
		t.Fatalf("Apply(stale) = (%v, %v), want unchanged", changed, err)
	}
}

func TestStoreSaveAndLoadRoundTrip(t *testing.T) {
	event := declaration(t, privateKey, "1.0.0~ynh1")
	policy, err := trust.NewExplicitPublishers([]string{event.PubKey})
	if err != nil {
		t.Fatal(err)
	}
	store := New(policy)
	if _, err := store.Apply(event); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state", "catalogue.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, policy)
	if err != nil {
		t.Fatal(err)
	}
	resolved, ok := loaded.Resolve("hello_nostr")
	if !ok || resolved.Version != "1.0.0~ynh1" {
		t.Fatalf("loaded Resolve() = (%+v, %v)", resolved, ok)
	}
}

func TestStoreAttestationPolicyMatchesExactRevision(t *testing.T) {
	declarationEvent := declaration(t, privateKey, "1.0.0~ynh1")
	declarationData, err := protocol.ParseAppDeclaration(declarationEvent)
	if err != nil {
		t.Fatal(err)
	}
	attestationEvent, err := verification.Build(declarationData.AppID, declarationData.Repository, declarationData.Commit, declarationData.ManifestHash, declarationData.ContentHash, "test-ci", "run-1", map[string]string{"package_check": "pass"}, "pass", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := trust.NewExplicitPublishers([]string{declarationEvent.PubKey})
	if err != nil {
		t.Fatal(err)
	}
	store := New(policy)
	if _, err := store.Apply(declarationEvent); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.ApplyAttestation(attestationEvent); err != nil || !changed {
		t.Fatalf("ApplyAttestation() = (%v, %v)", changed, err)
	}
	attestationPolicy := trust.AttestationPolicy{Mode: trust.AttestationRequire, RequiredChecks: []string{"package_check"}}
	resolved, decision, ok := store.ResolveInstallable("hello_nostr", attestationPolicy)
	if !ok || !decision.Verified || resolved.Version != "1.0.0~ynh1" {
		t.Fatalf("ResolveInstallable() = (%+v, %+v, %v)", resolved, decision, ok)
	}
}
