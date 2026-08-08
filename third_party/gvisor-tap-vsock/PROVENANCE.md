# Provenance: github.com/containers/gvisor-tap-vsock

## Upstream

- Module: `github.com/containers/gvisor-tap-vsock`
- Upstream repo: <https://github.com/containers/gvisor-tap-vsock>
- Base version: `v0.8.9`

## Fork lineage (clawkwork)

The tree here was extracted from the `egress-filters` branch of the
clawkwork internal fork. Two commits sit on top of upstream `v0.8.9`:

| Commit | Description |
|--------|-------------|
| `d84c3e5f` | forwarder, dns: add egress address-filter and DNS observer hooks (clawkwork tag **v0.8.9-1**) |
| `32ffb082` | virtualnetwork: add WithDialer outbound dial hook |

The second commit (`32ffb082`) is the **WithDialer patch**: 21 insertions /
3 deletions across three files:

- `pkg/services/forwarder/tcp.go` — accepts an optional `dialer` func argument; falls back to `net.Dial` when nil
- `pkg/virtualnetwork/options.go` — adds `dialer` field to `options`; exports `WithDialer` functional option
- `pkg/virtualnetwork/services.go` — threads `opts.dialer` through to `forwarder.TCP`

The verbatim diff is preserved in `withdialer.patch` alongside this file.

## Tree modifications

The extracted tree is pruned:
- `vendor/` removed (transitive deps resolve through nexus3's module graph)
- `tools/` removed (build tooling not needed at compile time)

All other files, including `LICENSE`, are retained intact.

## License

Apache License, Version 2.0. See `LICENSE`.

## go.mod replace directive (nexus3)

```
replace github.com/containers/gvisor-tap-vsock => ./third_party/gvisor-tap-vsock
```
