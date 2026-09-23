// Package protocol contains the wire-level types for the catalogue event schema.
package protocol

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

const (
	// AppDeclarationKind is the interoperable software-application record.
	// Exact YunoHost release hashes remain extension tags.
	AppDeclarationKind       int = 32267
	LegacyAppDeclarationKind int = 30078
	// NpackReleaseKind is npack's own signed package-release event (see
	// forks/npack in the nostrhost repo). It carries a different tag shape
	// than AppDeclarationKind (no manifest-hash equivalent - see
	// ParseFromNpackRelease) but the same trust/curation/attestation
	// machinery below applies to it once parsed into an AppDeclaration.
	NpackReleaseKind int = 9900
)

// ProfileKind and NoteKind are the standard NIP-01 kinds used to make a
// publisher key behave like an ordinary Nostr account: a kind-0 profile
// (name/about/picture) so the key isn't just an opaque hex string, and
// kind-1 text notes announcing releases so updates show up in a normal
// client feed instead of only as an unrendered replaceable event. Neither
// is provisional - both are long-established NIP-01 kinds - unlike
// AppDeclarationKind and verification.AttestationKind.
const (
	ProfileKind int = 0
	NoteKind    int = 1
)

// Event aliases the SDK's event type so all event serialization, IDs, and
// signature operations use the maintained Nostr implementation.
type Event = nostr.Event

// AppDeclaration is the validated, platform-specific data extracted from a
// parameterised replaceable Nostr app declaration.
type AppDeclaration struct {
	AppID         string
	Publisher     string
	Repository    string
	PackagePath   string
	Version       string
	Commit        string
	ManifestHash  string
	ContentHash   string
	Category      string
	Name          string
	Description   string
	Architectures []string
}

var (
	appIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	// Hex64Pattern matches a 64-character lowercase hexadecimal string, such
	// as an event ID or public key. It is exported so other packages
	// (verification, publisher) validate hex64 strings identically instead
	// of maintaining their own copies of the same pattern.
	Hex64Pattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hex128Pattern = regexp.MustCompile(`^[0-9a-f]{128}$`)
	// CommitPattern matches a 40-64 character lowercase hexadecimal git
	// commit reference. Exported for the same reason as Hex64Pattern.
	CommitPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
)

// ParseAppDeclaration validates the event envelope and extracts the app
// declaration. Signature verification is deliberately kept separate because
// it requires the selected Nostr crypto implementation.
func ParseAppDeclaration(event Event) (AppDeclaration, error) {
	if event.Kind != AppDeclarationKind && event.Kind != LegacyAppDeclarationKind {
		return AppDeclaration{}, fmt.Errorf("unexpected event kind %d", event.Kind)
	}
	if event.CreatedAt <= 0 {
		return AppDeclaration{}, fmt.Errorf("created_at must be positive")
	}
	if !Hex64Pattern.MatchString(event.PubKey) {
		return AppDeclaration{}, fmt.Errorf("pubkey must be 64 lowercase hexadecimal characters")
	}
	if !Hex64Pattern.MatchString(event.ID) || !hex128Pattern.MatchString(event.Sig) {
		return AppDeclaration{}, fmt.Errorf("id must be 64 and sig must be 128 lowercase hexadecimal characters")
	}

	tags, err := parseTags(event.Tags)
	if err != nil {
		return AppDeclaration{}, err
	}
	appID := tags["d"][0]
	if !appIDPattern.MatchString(appID) {
		return AppDeclaration{}, fmt.Errorf("invalid app ID %q", appID)
	}
	platform := firstTag(tags, "platform")
	if platform == "" {
		platform = firstTag(tags, "platforms")
	}
	if platform != "yunohost" && platform != "linux" {
		return AppDeclaration{}, fmt.Errorf("platform must identify yunohost or linux")
	}
	repository := firstTag(tags, "repository")
	if repository == "" {
		repository = firstTag(tags, "repo")
	}
	if err := validateRepository(repository); err != nil {
		return AppDeclaration{}, err
	}
	packagePath := firstTag(tags, "package")
	if packagePath != "" {
		if err := validatePackagePath(packagePath); err != nil {
			return AppDeclaration{}, err
		}
	}
	if !CommitPattern.MatchString(tags["commit"][0]) {
		return AppDeclaration{}, fmt.Errorf("commit must be 40-64 lowercase hexadecimal characters")
	}
	for _, name := range []string{"manifest", "content"} {
		if err := ValidateHash(name, tags[name][0]); err != nil {
			return AppDeclaration{}, err
		}
	}
	if err := validateContent(event.Content); err != nil {
		return AppDeclaration{}, err
	}

	declaration := AppDeclaration{
		AppID:        appID,
		Publisher:    event.PubKey,
		Repository:   repository,
		PackagePath:  packagePath,
		Version:      tags["version"][0],
		Commit:       tags["commit"][0],
		ManifestHash: tags["manifest"][0],
		ContentHash:  tags["content"][0],
		Category:     firstTag(tags, "category"),
		Name:         firstTag(tags, "name"),
		Description:  firstTag(tags, "description"),
	}
	var metadata struct {
		Name          string   `json:"name"`
		Description   string   `json:"description"`
		Architectures []string `json:"architectures"`
	}
	if err := json.Unmarshal([]byte(event.Content), &metadata); err == nil {
		if declaration.Name == "" {
			declaration.Name = metadata.Name
		}
		if declaration.Description == "" {
			declaration.Description = metadata.Description
		}
		declaration.Architectures = metadata.Architectures
	}
	return declaration, nil
}

// ParseFromNpackRelease adapts a signed npack kind-9900 release event (see
// forks/npack/npack-cli/src/main.rs's sign_release_event) into the same
// AppDeclaration shape ParseAppDeclaration produces, so the rest of the
// catalogue (trust, curation, attestation, Store) operates on npack releases
// identically to kind-32267 declarations. It does not reuse parseTags, which
// enforces kind-32267-specific required tags ("d", "manifest", "content",
// "platform") that npack releases don't carry.
//
// ManifestHash is left empty: npack's signed event has no field covering
// nostrhost's own embedded native-manifest hash (that hash is computed
// locally when staging, entirely outside what npack itself signs). Callers
// matching on ManifestHash (see Store.AttestationsFor) must treat an empty
// value as "not checked", not "must equal empty".
//
// Repository is a NIP-34 kind:30617 address ("30617:<pubkey>:<identifier>"),
// not the HTTPS URL kind-32267 declarations use - npack's own release
// signing rejects any other repo format (validate_repo_reference in
// sign_release_event), so a publisher needs an existing NIP-34
// git-repository announcement to make a release attestable at all.
func ParseFromNpackRelease(event Event) (AppDeclaration, error) {
	if event.Kind != NpackReleaseKind {
		return AppDeclaration{}, fmt.Errorf("unexpected event kind %d", event.Kind)
	}
	if event.CreatedAt <= 0 {
		return AppDeclaration{}, fmt.Errorf("created_at must be positive")
	}
	if !Hex64Pattern.MatchString(event.PubKey) {
		return AppDeclaration{}, fmt.Errorf("pubkey must be 64 lowercase hexadecimal characters")
	}
	if !Hex64Pattern.MatchString(event.ID) || !hex128Pattern.MatchString(event.Sig) {
		return AppDeclaration{}, fmt.Errorf("id must be 64 and sig must be 128 lowercase hexadecimal characters")
	}

	tags := make(map[string]string, len(event.Tags))
	for _, tag := range event.Tags {
		if len(tag) < 2 || tag[0] == "" || tag[1] == "" {
			continue
		}
		if _, exists := tags[tag[0]]; !exists {
			tags[tag[0]] = tag[1]
		}
	}

	appID := tags["name"]
	if !appIDPattern.MatchString(appID) {
		return AppDeclaration{}, fmt.Errorf("invalid app ID %q", appID)
	}
	version := tags["version"]
	if version == "" {
		return AppDeclaration{}, fmt.Errorf("release declares no version")
	}
	repository := tags["repo"]
	if err := validateNip34RepoReference(repository); err != nil {
		return AppDeclaration{}, fmt.Errorf("npack release has no attestable repo/commit provenance: %w", err)
	}
	commit := tags["commit"]
	if !CommitPattern.MatchString(commit) {
		return AppDeclaration{}, fmt.Errorf("npack release has no attestable repo/commit provenance: commit must be 40-64 lowercase hexadecimal characters")
	}
	contentHash := "sha256:" + tags["x"]
	if err := ValidateHash("content", contentHash); err != nil {
		return AppDeclaration{}, err
	}

	return AppDeclaration{
		AppID:       appID,
		Publisher:   event.PubKey,
		Repository:  repository,
		Version:     version,
		Commit:      commit,
		ContentHash: contentHash,
		Name:        appID,
	}, nil
}

// VerifyID checks the event ID using the SDK's NIP-01 implementation.
func VerifyID(event Event) error {
	if !event.CheckID() {
		return fmt.Errorf("event ID does not match the SDK's canonical serialization")
	}
	return nil
}

// VerifySignature checks the event's Schnorr signature using the SDK.
func VerifySignature(event Event) error {
	valid, err := event.CheckSignature()
	if err != nil {
		return fmt.Errorf("check event signature: %w", err)
	}
	if !valid {
		return fmt.Errorf("event signature is invalid")
	}
	return nil
}

func parseTags(raw nostr.Tags) (map[string][]string, error) {
	values := make(map[string][]string)
	for _, tag := range raw {
		if len(tag) < 2 || tag[0] == "" || tag[1] == "" {
			return nil, fmt.Errorf("tags must contain a name and value")
		}
		values[tag[0]] = append(values[tag[0]], tag[1])
	}
	for _, name := range []string{"d", "version", "commit", "manifest", "content"} {
		if len(values[name]) != 1 {
			return nil, fmt.Errorf("required tag %q must occur exactly once", name)
		}
	}
	if len(values["platform"]) == 0 && len(values["platforms"]) == 0 {
		return nil, fmt.Errorf("required tag %q must occur at least once", "platform/platforms")
	}
	if len(values["repo"]) == 0 && len(values["repository"]) == 0 {
		return nil, fmt.Errorf("required tag %q must occur at least once", "repo/repository")
	}
	return values, nil
}

func validateRepository(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Path == "" {
		return fmt.Errorf("repo must be an HTTPS repository URL")
	}
	return nil
}

// validateNip34RepoReference checks npack's own repo format: a NIP-34
// kind:30617 address ("30617:<pubkey>:<identifier>"), not the HTTPS URL
// kind-32267 declarations use (see validateRepository). npack itself
// rejects any other shape when building a release (validate_repo_reference
// in forks/npack/npack-cli/src/main.rs), so an npack release's repo tag is
// always this format, never a bare URL - a publisher needs an existing
// NIP-34 git-repository announcement event to make a release attestable.
func validateNip34RepoReference(raw string) error {
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) != 3 || parts[0] != "30617" {
		return fmt.Errorf("repo must be a NIP-34 kind:30617 address (30617:<pubkey>:<identifier>)")
	}
	if !Hex64Pattern.MatchString(parts[1]) {
		return fmt.Errorf("repo publisher must be a 64-character lowercase hexadecimal Nostr key")
	}
	if parts[2] == "" {
		return fmt.Errorf("repo is missing its identifier")
	}
	return nil
}

func validatePackagePath(raw string) error {
	if strings.HasPrefix(raw, "/") || strings.Contains(raw, "\\") {
		return fmt.Errorf("package path must be a relative slash-separated path")
	}
	for _, part := range strings.Split(raw, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("package path contains an unsafe component")
		}
	}
	return nil
}

func validateContent(raw string) error {
	var value map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &value); err != nil || value == nil {
		return fmt.Errorf("content must be a JSON object")
	}
	return nil
}

// ValidateHash checks that raw is a "sha256:<64 lowercase hexadecimal
// characters>" digest, the format used throughout the catalogue protocol
// (declaration manifest/content hashes and attestation manifest/content
// hashes). name identifies the field in the returned error message.
func ValidateHash(name, raw string) error {
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 || parts[0] != "sha256" || !Hex64Pattern.MatchString(parts[1]) {
		return fmt.Errorf("%s must use sha256:<64 lowercase hexadecimal characters>", name)
	}
	return nil
}

// NormalizePublicKey accepts a public key supplied as either lowercase hex or
// an NIP-19 npub value and returns the normalized lowercase hex form. It is
// shared by every local trust/curation policy that accepts operator-supplied
// keys in either format.
func NormalizePublicKey(raw string) (string, error) {
	key := strings.TrimSpace(raw)
	if nostr.IsValidPublicKey(key) {
		return key, nil
	}
	prefix, value, err := nip19.Decode(key)
	if err != nil || prefix != "npub" {
		return "", fmt.Errorf("invalid public key %q", raw)
	}
	publicKey, ok := value.(string)
	if !ok || !nostr.IsValidPublicKey(publicKey) {
		return "", fmt.Errorf("invalid npub public key %q", raw)
	}
	return publicKey, nil
}

func firstTag(tags map[string][]string, name string) string {
	if len(tags[name]) == 0 {
		return ""
	}
	return tags[name][0]
}
