# nexus notes: gvisor-tap-vsock fork

Wired in by root `go.mod`:
`replace github.com/containers/gvisor-tap-vsock => ./third_party/gvisor-tap-vsock`.
Full lineage and license: `PROVENANCE.md`.

## Base

Upstream `github.com/containers/gvisor-tap-vsock` `v0.8.9`. Exact upstream
commit hash: unknown (history here starts at the vendoring commit `cc21432`).

## Patches on top of upstream

Both came from the clawkwork `egress-filters` branch (hashes are that
repo's, not in this history):

- `d84c3e5f` (clawkwork tag `v0.8.9-1`): forwarder and dns egress
  address-filter and DNS observer hooks. Why: egress policy enforcement.
  The verbatim diff is not preserved in this repo.
- `32ffb082`: `virtualnetwork.WithDialer` outbound dial hook (touches
  `pkg/services/forwarder/tcp.go`, `pkg/virtualnetwork/options.go`,
  `pkg/virtualnetwork/services.go`). Why: let nexus route guest TCP through
  its own dialer instead of `net.Dial`. Diff: `withdialer.patch`.

The tree is pruned (`vendor/`, `tools/` removed). The `go.mod` requires are
kept in step with the root `go.mod`.

## Rebase on a newer upstream

1. Fetch the upstream tag; copy it over this dir, keeping `NEXUS.md`,
   `PROVENANCE.md`, `withdialer.patch`.
2. Re-apply the egress-filter hooks (forwarder, dns) by hand; no patch file
   exists for them.
3. `git apply withdialer.patch` (resolve conflicts by hand).
4. Remove `vendor/` and `tools/`; align `go.mod` versions with root; `go mod tidy`.
5. Run `make build` and the egress tests through `make`.
