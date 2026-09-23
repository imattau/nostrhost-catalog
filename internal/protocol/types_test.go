package protocol

import (
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func validEvent() Event {
	return Event{
		ID:        "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		PubKey:    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CreatedAt: 1,
		Kind:      AppDeclarationKind,
		Tags: nostr.Tags{
			{"d", "hello_nostr"},
			{"platform", "yunohost"},
			{"repo", "https://github.com/example/hello_nostr_ynh"},
			{"version", "1.0.0~ynh1"},
			{"commit", "cccccccccccccccccccccccccccccccccccccccc"},
			{"manifest", "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"},
			{"content", "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"},
		},
		Content: `{"name":"Hello Nostr","architectures":["amd64"]}`,
		Sig:     "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	}
}

func TestParseAppDeclaration(t *testing.T) {
	declaration, err := ParseAppDeclaration(validEvent())
	if err != nil {
		t.Fatalf("ParseAppDeclaration() error = %v", err)
	}
	if declaration.AppID != "hello_nostr" || declaration.Name != "Hello Nostr" {
		t.Fatalf("unexpected declaration: %+v", declaration)
	}
}

func TestParseAppDeclarationRejectsDuplicateRequiredTag(t *testing.T) {
	event := validEvent()
	event.Tags = append(event.Tags, []string{"version", "2.0.0~ynh1"})
	if _, err := ParseAppDeclaration(event); err == nil {
		t.Fatal("ParseAppDeclaration() accepted duplicate version tag")
	}
}

func TestParseAppDeclarationRejectsNonHTTPSRepository(t *testing.T) {
	event := validEvent()
	event.Tags[2][1] = "git@github.com:example/hello_nostr_ynh.git"
	if _, err := ParseAppDeclaration(event); err == nil {
		t.Fatal("ParseAppDeclaration() accepted non-HTTPS repository")
	}
}

func validNpackReleaseEvent() Event {
	return Event{
		ID:        strings.Repeat("1", 64),
		PubKey:    strings.Repeat("2", 64),
		CreatedAt: 1,
		Kind:      NpackReleaseKind,
		Tags: nostr.Tags{
			{"d", "opencode-web_nh/1.18.30/x86_64"},
			{"name", "opencode-web_nh"},
			{"version", "1.18.30"},
			{"os", "linux"},
			{"arch", "x86_64"},
			{"format", "npk"},
			{"x", strings.Repeat("3", 64)},
			{"artifact", strings.Repeat("4", 64)},
			{"repo", "30617:" + strings.Repeat("2", 64) + ":opencode-web_nh"},
			{"commit", strings.Repeat("5", 40)},
		},
		Content: "npack release .../opencode-web_nh 1.18.30",
		Sig:     strings.Repeat("f", 128),
	}
}

func TestParseFromNpackRelease(t *testing.T) {
	declaration, err := ParseFromNpackRelease(validNpackReleaseEvent())
	if err != nil {
		t.Fatalf("ParseFromNpackRelease() error = %v", err)
	}
	if declaration.AppID != "opencode-web_nh" || declaration.Name != "opencode-web_nh" {
		t.Fatalf("unexpected AppID/Name: %+v", declaration)
	}
	if declaration.Version != "1.18.30" || declaration.Commit != strings.Repeat("5", 40) {
		t.Fatalf("unexpected Version/Commit: %+v", declaration)
	}
	if declaration.ContentHash != "sha256:"+strings.Repeat("3", 64) {
		t.Fatalf("content hash not prefixed with sha256:: %+v", declaration)
	}
	if declaration.ManifestHash != "" {
		t.Fatalf("npack releases have no manifest-hash equivalent, want empty, got %q", declaration.ManifestHash)
	}
}

func TestParseFromNpackReleaseRejectsWrongKind(t *testing.T) {
	event := validNpackReleaseEvent()
	event.Kind = AppDeclarationKind
	if _, err := ParseFromNpackRelease(event); err == nil {
		t.Fatal("ParseFromNpackRelease() accepted a non-npack-release kind")
	}
}

func TestParseFromNpackReleaseRequiresRepoAndCommit(t *testing.T) {
	withoutRepo := validNpackReleaseEvent()
	withoutRepo.Tags = nostr.Tags{{"name", "opencode-web_nh"}, {"version", "1.18.30"}, {"x", strings.Repeat("3", 64)}, {"commit", strings.Repeat("5", 40)}}
	if _, err := ParseFromNpackRelease(withoutRepo); err == nil {
		t.Fatal("ParseFromNpackRelease() accepted a release with no repo (unattestable)")
	}

	withoutCommit := validNpackReleaseEvent()
	withoutCommit.Tags = nostr.Tags{{"name", "opencode-web_nh"}, {"version", "1.18.30"}, {"x", strings.Repeat("3", 64)}, {"repo", "30617:" + strings.Repeat("2", 64) + ":opencode-web_nh"}}
	if _, err := ParseFromNpackRelease(withoutCommit); err == nil {
		t.Fatal("ParseFromNpackRelease() accepted a release with no commit (unattestable)")
	}
}
