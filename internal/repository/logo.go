package repository

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"

	"github.com/imattau/nostrhost-catalog/internal/protocol"
)

// FetchLogo retrieves an app's optional logo.png from its declared repository
// at the pinned commit, stores it in destinationDirectory as
// "<sha256>.png", and returns that hash. The hash matches the YunoHost logo
// convention, so the portal serves the file at
// /nostrhost/sso/applogos/<hash>.png with no further mapping.
//
// A repository that ships no logo (or an unusable one) returns "" and no
// error: logos are cosmetic and must never fail a catalogue sync. A missing
// destinationDirectory only skips persistence; the hash is still returned.
func FetchLogo(ctx context.Context, declaration protocol.AppDeclaration, destinationDirectory string) (string, error) {
	if declaration.Repository == "" || declaration.Commit == "" {
		return "", nil
	}
	if err := validateRevision(declaration.Commit); err != nil {
		return "", err
	}
	logoPath := packageLogoPath(declaration.PackagePath)

	temporaryDirectory, err := os.MkdirTemp("", "nostr-ynh-logo-")
	if err != nil {
		return "", fmt.Errorf("create logo workspace: %w", err)
	}
	defer os.RemoveAll(temporaryDirectory)
	if _, err := gitCommandContext(ctx, temporaryDirectory, "clone", "--no-checkout", "--filter=blob:none", "--quiet", declaration.Repository, "."); err != nil {
		return "", fmt.Errorf("clone repository: %w", err)
	}

	// cat-file -e resolves the path against the tree without materializing the
	// whole checkout; only the logo blob itself is lazily fetched by show.
	reference := declaration.Commit + ":" + logoPath
	if _, err := gitCommandContext(ctx, temporaryDirectory, "cat-file", "-e", reference); err != nil {
		return "", nil
	}
	data, err := gitCommandContext(ctx, temporaryDirectory, "show", reference)
	if err != nil {
		return "", fmt.Errorf("read logo blob: %w", err)
	}
	data = validLogo(data)
	if len(data) == 0 {
		return "", nil
	}

	hash := LogoHash(data)
	if destinationDirectory != "" {
		if err := os.MkdirAll(destinationDirectory, 0o755); err != nil {
			return "", fmt.Errorf("create app logo directory: %w", err)
		}
		target := filepath.Join(destinationDirectory, hash+".png")
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return "", fmt.Errorf("write app logo: %w", err)
		}
		// The daemon runs with UMask=0077, so override the resulting 0600 to
		// keep the file readable by the web server.
		if err := os.Chmod(target, 0o644); err != nil {
			return "", fmt.Errorf("publish app logo: %w", err)
		}
	}
	return hash, nil
}

// packageLogoPath is the logo location inside the repository, relative to the
// package directory (package_path may point at a subdirectory or a file).
func packageLogoPath(packagePath string) string {
	return path.Join(path.Dir(filepath.ToSlash(packagePath)), "logo.png")
}
