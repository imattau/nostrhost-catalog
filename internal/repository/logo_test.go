package repository

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/imattau/nostrhost-catalog/internal/protocol"
)

// minimalPng carries a valid PNG signature so validLogo accepts it.
var minimalPng = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x01, 0x02, 0x03}

func initLogoRepository(t *testing.T, files map[string][]byte) (string, string) {
	t.Helper()
	directory := t.TempDir()
	for name, data := range files {
		target := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.invalid"}, {"config", "user.name", "Test"}, {"add", "."}, {"commit", "-m", "test"}} {
		if _, err := gitCommand(directory, args...); err != nil {
			t.Fatal(err)
		}
	}
	commit, err := gitOutput(directory, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return directory, commit
}

func TestFetchLogoStoresHashedPng(t *testing.T) {
	repositoryDirectory, commit := initLogoRepository(t, map[string][]byte{"logo.png": minimalPng})
	destination := t.TempDir()
	declaration := protocol.AppDeclaration{AppID: "demo", Repository: repositoryDirectory, Commit: commit}

	hash, err := FetchLogo(context.Background(), declaration, destination)
	if err != nil {
		t.Fatal(err)
	}
	if hash != LogoHash(minimalPng) {
		t.Fatalf("hash = %q, want %q", hash, LogoHash(minimalPng))
	}
	target := filepath.Join(destination, hash+".png")
	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != string(minimalPng) {
		t.Fatalf("written logo = %x, want %x", written, minimalPng)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("logo mode = %v, want 0644", info.Mode().Perm())
	}
}

func TestFetchLogoMissingReturnsEmpty(t *testing.T) {
	repositoryDirectory, commit := initLogoRepository(t, map[string][]byte{"manifest.toml": []byte("id = \"demo\"\n")})
	declaration := protocol.AppDeclaration{AppID: "demo", Repository: repositoryDirectory, Commit: commit}

	hash, err := FetchLogo(context.Background(), declaration, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if hash != "" {
		t.Fatalf("hash = %q, want empty", hash)
	}
}

func TestFetchLogoHonorsPackagePath(t *testing.T) {
	repositoryDirectory, commit := initLogoRepository(t, map[string][]byte{"packages/demo/logo.png": minimalPng})
	declaration := protocol.AppDeclaration{
		AppID:       "demo",
		Repository:  repositoryDirectory,
		Commit:      commit,
		PackagePath: "packages/demo/package.toml",
	}

	hash, err := FetchLogo(context.Background(), declaration, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if hash != LogoHash(minimalPng) {
		t.Fatalf("hash = %q, want %q", hash, LogoHash(minimalPng))
	}
}

func TestFetchLogoRejectsNonPng(t *testing.T) {
	repositoryDirectory, commit := initLogoRepository(t, map[string][]byte{"logo.png": []byte("not a png")})
	declaration := protocol.AppDeclaration{AppID: "demo", Repository: repositoryDirectory, Commit: commit}

	hash, err := FetchLogo(context.Background(), declaration, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if hash != "" {
		t.Fatalf("hash = %q, want empty", hash)
	}
}
