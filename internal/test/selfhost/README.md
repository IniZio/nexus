# internal/test/selfhost

Self-hosting base ext4 image harness (Run 5, slice S1).

## What it does

`BuildSelfHostBaseImage` produces the ext4 rootfs that a nexus3 workspace boots
from so that nexus3 can be developed entirely in-workspace:

| Layer | Contents |
|-------|----------|
| OS | Debian bookworm-slim (glibc ≥ 2.28) |
| Toolchain | Upstream Go 1.26.5 at `/usr/local/go` |
| Runtime deps | git, ca-certificates (no gcc — `CGO_ENABLED=0` throughout) |
| Agent | `nexus3-agent` static binary at `/sbin/nexus3-agent` (VM init) |
| Module cache | nexus3's Go deps pre-seeded at `/usr/local/gopath/pkg/mod` |

The seeded module cache means an in-workspace `go build ./...` resolves all
dependencies without network access. Prototype 28 measured: cold build 32 s,
incremental 11 s, per-package test 2 s.

## Go version rationale

Debian bookworm ships Go 1.19, which is too old (nexus3 `go.mod` requires
`go 1.25.0`). The Containerfile fetches upstream **Go 1.26.5** from
`dl.google.com`, the latest stable release as of 2026-08-07
(SHA-256 `5c2c3b16…` verified against `go.dev/dl/?mode=json`).

## Module cache seeding

During `docker build`, a dedicated `mod-seeder` stage runs:

```
GOTOOLCHAIN=local go mod download all
```

with only `go.mod` + `go.sum` in the working directory. `GOTOOLCHAIN=local`
prevents a redundant toolchain re-download (go1.26.5 already satisfies
`go 1.25.0`). The resulting `~1.2 GB` cache directory is then COPYed into the
final stage as a Docker layer, so it is baked into the image.

**Caveat:** the seeded cache enables offline builds for all modules listed in
`go.mod` + `go.sum`. If `go.sum` changes (new dep added), rebuild the image.

## Build mechanism

```
docker build (multi-stage Containerfile)
  │
  ├─ go-fetcher:  apt install curl, curl+verify Go tarball, unpack to /usr/local/go
  ├─ mod-seeder:  go mod download all → /usr/local/gopath/pkg/mod
  └─ final:       apt install git+ca-certs, COPY Go, COPY modcache, COPY agent
  │
  └─→ docker create <img> /bin/true
      docker export <containerID>
      extractTar → rootfs/
      mke2fs -t ext4 -d rootfs/ -U 00000… -E hash_seed=00000… (5 GiB, sparse)
      SHA-256 hash → image.Cache.Put
```

The mke2fs flags (`-U`, `-E hash_seed`, `SOURCE_DATE_EPOCH=0`) are the same
deterministic parameters used by `internal/core/builder/ext4.go`. The ext4
digest will still vary run-to-run (apt/Docker layer graph are not reproducible)
but each produced artifact is content-addressed by its actual SHA-256.

## Prerequisites

| Tool | Purpose |
|------|---------|
| `docker` | Multi-stage image build + container export |
| `mke2fs` (e2fsprogs) | Assemble the rootfs tree into a raw ext4 |

If either is absent, `BuildSelfHostBaseImage` returns a sentinel error
(`ErrDockerUnavailable` or `builder.ErrMke2fsUnavailable`). Integration tests
treat this as `t.Skip`, not `t.Fatal`.

## Running the integration test

```bash
# Requires docker + mke2fs on PATH. First run takes ~10–20 minutes (Go + apt downloads).
# Subsequent runs are faster — Docker layer caching amortises the downloads.
go test -tags integration -run TestBuildSelfHostBaseImage \
    ./internal/test/selfhost/ -v -timeout 30m
```

The unit-level skip test (`TestBuildSelfHostBaseImageSkips`) runs without the
tag and has no external dependencies.

## Non-reproducibility

The ext4 digest varies across runs because Docker layers, apt package timestamps,
and `go mod download` metadata are not byte-deterministic. The fixed mke2fs
parameters (UUID, hash seed, epoch) make *logically identical* trees produce the
same bytes, but the tree itself changes between Docker builds.
