# nostrhost-catalog

Reusable Nostr app-catalogue library for the [nostrhost](../) Nostr-native
YunoHost derivative. Extracted from the reference implementation
[`nostr-yunohost`](https://github.com/imattau/nostr-yunohost) (the Go
`nostr-catalogd` / `nostr-ynh` stack that still ships for stock YunoHost via
[`nostr_catalog_ynh`](https://github.com/imattau/nostr_catalog_ynh) — which
remains operational during this migration).

## What it provides

| package | provides |
|---|---|
| `internal/protocol` | catalogue event schema + structural parsing (NIP-01 events) |
| `internal/verification` | signed-declaration / attestation verification |
| `internal/relay` | relay client (query/bootstrap discovery) |
| `internal/publisher` | publisher event + address resolution |
| `internal/curation` | curated-lists + endorsement policy (trust filtering) |
| `internal/repository` | package repository metadata + verification |
| `internal/trust` | publisher/attestation trust policy |
| `internal/catalog` | restartable trusted-declaration projection and deterministic resolution |
| `internal/ciresult` | CI attestation result schema |

These map to the roadmap's catalogue extraction list: event schema, relay
discovery, publisher verification, attestation verification, trust filtering
and repository resolution.

The native projection can be persisted and queried with the CLI:

```text
echo '<signed kind-32267 event JSON>' | \
  nostrhost-catalog --state /var/lib/nostrhost/catalogue.json \
  --publishers <publisher-hex-or-npub> ingest
nostrhost-catalog --state /var/lib/nostrhost/catalogue.json \
  --publishers <publisher-hex-or-npub> get <app-id>
```

`trust` (dashboard) and `reverify` (on-demand independent re-check) are
read-only commands built entirely on top of the already-extracted packages
above (`internal/trust`'s attestation policy and `internal/repository`'s
`VerifyDeclaration`) - no signing capability was added to this binary, so
the "never accept private keys" property `publish` already had still holds
for the whole CLI:

```text
nostrhost-catalog --state /var/lib/nostrhost/catalogue.json \
  --publishers <publisher-hex-or-npub> \
  --attestation-policy require --min-attestations 1 trust
nostrhost-catalog --state /var/lib/nostrhost/catalogue.json \
  --publishers <publisher-hex-or-npub> --app-id <app-id> reverify
```

## Daemon deployment

`deploy/nostrhost-catalog.service` provides the restart-safe systemd wrapper
for `sync`. Install the binary as `/usr/bin/nostrhost-catalog`, copy
`deploy/catalogue.env.example` to `/etc/nostrhost/catalogue.env`, set the
trusted publisher keys, then enable the service. The state file is written
atomically with a private umask; relay discovery is best-effort and never
overrides publisher or attestation policy.

## Explicitly not here (yet)

The remaining daemon-side state and command layer — `internal/attestation`, `internal/localstate`, `internal/announce`,
`internal/reverify`, and the `cmd/nostr-catalogd` / `cmd/nostr-ynh` binaries —
stays in the reference repo until the fork's native catalogue provider
(roadmap stage 9) consumes this library. `internal/catalog` is the first
fork-facing projection seam: it accepts only cryptographically valid,
explicitly trusted declarations and is safe to rebuild from relay replay.
New declarations use kind `32267` (software application); kind `30078` is
accepted only as a legacy-read compatibility path. Release-specific
YunoHost integrity fields remain extension tags until separate kind `30063`
release-artifact events are added.

## Development

```
go build ./...
go test ./...
```

Requires Go 1.24 (the `go.mod` toolchain directive auto-fetches it).
