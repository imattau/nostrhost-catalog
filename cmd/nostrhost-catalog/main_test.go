package main

import "testing"

// runAttestRelease's own logic (fetching one release + its attestations and
// evaluating the same trust.AttestationPolicy runTrust already uses) is
// exercised in internal/catalog and internal/protocol's tests against real
// signed events (Phase 2). This package has no relay-mocking seam anywhere
// (see internal/relay/client_test.go - it only tests pure URL logic, never
// a live round-trip), so these only cover the validation runAttestRelease
// does before any network I/O.

func TestRunAttestReleaseRequiresNameAndVersion(t *testing.T) {
	err := runAttestRelease("aa", "", "1.0.0", "x86_64", nil, []string{"wss://relay.example"}, "off", 0, nil, nil)
	if err == nil {
		t.Fatal("runAttestRelease() accepted an empty --name")
	}
	err = runAttestRelease("aa", "myapp", "", "x86_64", nil, []string{"wss://relay.example"}, "off", 0, nil, nil)
	if err == nil {
		t.Fatal("runAttestRelease() accepted an empty --version")
	}
}

func TestRunAttestReleaseRejectsInvalidPublisher(t *testing.T) {
	err := runAttestRelease("not-a-key", "myapp", "1.0.0", "x86_64", nil, []string{"wss://relay.example"}, "off", 0, nil, nil)
	if err == nil {
		t.Fatal("runAttestRelease() accepted an invalid --publisher")
	}
}

func TestRunAttestReleaseRequiresRelay(t *testing.T) {
	publisher := "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"
	err := runAttestRelease(publisher, "myapp", "1.0.0", "x86_64", nil, nil, "off", 0, nil, nil)
	if err == nil {
		t.Fatal("runAttestRelease() accepted an empty relay list")
	}
}

func TestRunAttestReleaseRejectsInvalidAttestationPolicy(t *testing.T) {
	publisher := "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d"
	err := runAttestRelease(publisher, "myapp", "1.0.0", "x86_64", nil, []string{"wss://relay.example"}, "bogus", 0, nil, nil)
	if err == nil {
		t.Fatal("runAttestRelease() accepted an invalid --attestation-policy")
	}
}
