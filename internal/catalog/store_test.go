package catalog

import (
	"context"
	"path/filepath"
	"testing"
	"time"

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

func npackReleaseEvent(t *testing.T, privateKey string) nostr.Event {
	t.Helper()
	publicKey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	event := nostr.Event{
		CreatedAt: nostr.Timestamp(1),
		Kind:      protocol.NpackReleaseKind,
		Tags: nostr.Tags{
			{"d", "hello_nostr/1.0.0/x86_64"},
			{"name", "hello_nostr"},
			{"version", "1.0.0"},
			{"x", publisher.HashBytes([]byte("content"))[len("sha256:"):]},
			{"repo", "30617:" + publicKey + ":hello_nostr_ynh"},
			{"commit", "cccccccccccccccccccccccccccccccccccccccc"},
		},
		PubKey: publicKey,
	}
	if err := event.Sign(privateKey); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestStoreAttestationPolicyMatchesNpackReleaseWithNoManifestHash(t *testing.T) {
	// npack releases have no manifest-hash equivalent (ParseFromNpackRelease
	// leaves it empty); AttestationsFor must still match on
	// (app_id, repo, commit, content_hash) alone.
	event := npackReleaseEvent(t, privateKey)
	declarationData, err := protocol.ParseFromNpackRelease(event)
	if err != nil {
		t.Fatal(err)
	}
	if declarationData.ManifestHash != "" {
		t.Fatalf("expected empty ManifestHash for an npack-sourced declaration, got %q", declarationData.ManifestHash)
	}
	attestationEvent, err := verification.Build(declarationData.AppID, declarationData.Repository, declarationData.Commit, publisher.HashBytes([]byte("some-manifest")), declarationData.ContentHash, "test-ci", "run-1", map[string]string{"package_check": "pass"}, "pass", privateKey)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := trust.NewExplicitPublishers([]string{event.PubKey})
	if err != nil {
		t.Fatal(err)
	}
	store := New(policy)
	if _, err := store.Apply(event); err != nil {
		t.Fatal(err)
	}
	if changed, err := store.ApplyAttestation(attestationEvent); err != nil || !changed {
		t.Fatalf("ApplyAttestation() = (%v, %v)", changed, err)
	}
	attestationPolicy := trust.AttestationPolicy{Mode: trust.AttestationRequire, RequiredChecks: []string{"package_check"}}
	resolved, decision, ok := store.ResolveInstallable("hello_nostr", attestationPolicy)
	if !ok || !decision.Verified || resolved.Version != "1.0.0" {
		t.Fatalf("ResolveInstallable() = (%+v, %+v, %v)", resolved, decision, ok)
	}
}

func TestStoreRecordsAndPersistsLogoResult(t *testing.T) {
	event := declaration(t, privateKey, "1.0.0~ynh1")
	policy, err := trust.NewExplicitPublishers([]string{event.PubKey})
	if err != nil {
		t.Fatal(err)
	}
	store := New(policy)
	if _, err := store.Apply(event); err != nil {
		t.Fatal(err)
	}
	hash := publisher.HashBytes([]byte("logo-bytes"))
	store.SetLogoResult(event.PubKey, "hello_nostr", hash)

	entries := store.Snapshot()
	if len(entries) != 1 || entries[0].LogoHash != hash || !entries[0].LogoChecked {
		t.Fatalf("Snapshot() = %+v", entries)
	}

	path := filepath.Join(t.TempDir(), "catalogue.json")
	if err := store.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path, policy)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := loaded.Snapshot()
	if len(reloaded) != 1 || reloaded[0].LogoHash != hash || !reloaded[0].LogoChecked {
		t.Fatalf("loaded Snapshot() = %+v", reloaded)
	}
}

func TestStoreResetLogoWhenDeclarationChanges(t *testing.T) {
	first := declaration(t, privateKey, "1.0.0~ynh1")
	policy, err := trust.NewExplicitPublishers([]string{first.PubKey})
	if err != nil {
		t.Fatal(err)
	}
	store := New(policy)
	if _, err := store.Apply(first); err != nil {
		t.Fatal(err)
	}
	store.SetLogoResult(first.PubKey, "hello_nostr", publisher.HashBytes([]byte("logo-bytes")))

	// Declarations are timestamped to the second; wait so the upgrade is
	// strictly newer than the declaration currently projected.
	time.Sleep(1100 * time.Millisecond)
	second := declaration(t, privateKey, "1.1.0~ynh1")
	if changed, err := store.Apply(second); err != nil || !changed {
		t.Fatalf("Apply(upgrade) = (%v, %v)", changed, err)
	}
	entries := store.Snapshot()
	if len(entries) != 1 || entries[0].LogoHash != "" || entries[0].LogoChecked {
		t.Fatalf("changed declaration kept a stale logo: %+v", entries)
	}
}

func TestBackfillLogosDisabledAndSkipsChecked(t *testing.T) {
	event := declaration(t, privateKey, "1.0.0~ynh1")
	policy, err := trust.NewExplicitPublishers([]string{event.PubKey})
	if err != nil {
		t.Fatal(err)
	}
	store := New(policy)
	if _, err := store.Apply(event); err != nil {
		t.Fatal(err)
	}
	if updated := backfillLogos(context.Background(), store, ""); updated != 0 {
		t.Fatalf("backfillLogos(disabled) = %d, want 0", updated)
	}
	store.SetLogoResult(event.PubKey, "hello_nostr", "")
	if updated := backfillLogos(context.Background(), store, t.TempDir()); updated != 0 {
		t.Fatalf("backfillLogos(checked) = %d, want 0", updated)
	}
}
