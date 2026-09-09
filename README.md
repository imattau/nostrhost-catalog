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
| `internal/ciresult` | CI attestation result schema |

These map to the roadmap's catalogue extraction list: event schema, relay
discovery, publisher verification, attestation verification, trust filtering
and repository resolution.

## Explicitly not here (yet)

The daemon-side state and command layer — `internal/catalog` (store),
`internal/attestation`, `internal/localstate`, `internal/announce`,
`internal/reverify`, and the `cmd/nostr-catalogd` / `cmd/nostr-ynh` binaries —
stays in the reference repo until the fork's native catalogue provider
(roadmap stage 9) consumes this library.

## Development

```
go build ./...
go test ./...
```

Requires Go 1.24 (the `go.mod` toolchain directive auto-fetches it).