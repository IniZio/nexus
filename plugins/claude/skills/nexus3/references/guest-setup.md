# Guest setup a dev sandbox needs

Three things are not done for you. Each produces a confusing failure rather than a clear one.

## 1. Install the MITM CA for non-Node clients

Allowlisted hosts are **TLS-intercepted** by the per-sandbox MITM proxy. The `claude-code` profile exports `NODE_EXTRA_CA_CERTS`, which only Node reads — so `claude` works while every other TLS client fails certificate validation. The CA is seeded to disk but never installed into the system trust store:

```sh
update-ca-certificates
```

Run this once per boot before any `go`, `curl`, `pip`, or `cargo` call. Allowlisting a host is necessary but **not sufficient** without it.

## 2. Clear git's ownership check on a virtiofs mount

A `--mount`ed repo is owned by a host uid the guest does not recognise, so every git command fails with `detected dubious ownership`:

```sh
git config --global --add safe.directory /work
```

## 3. Put the Go toolchain on PATH

Go is installed at `/usr/local/go/bin` but is absent from the login-shell PATH:

```sh
export PATH=$PATH:/usr/local/go/bin
```

---

## Trap: `go test` exits 0 in-guest without running anything

Five packages detect that they are running inside a nexus3 guest — via `/proc/1/comm` == `nexus3-agent` — and call `os.Exit(0)` **before** `m.Run()`, so a nested test run cannot pollute the operator's real state directory:

- `internal/cli`
- `internal/core/service`
- `internal/core/recovery`
- `internal/core/perimeter/netstack`
- `internal/mcp`

The consequence is a green exit code that asserts nothing:

```sh
go test -count=1 ./internal/cli/...   # ok ... 0.015s — ZERO test bodies ran
```

**Two tells.** Each package prints a line to **stderr** before exiting:

```
cli: skipping tests — running inside nexus3 guest VM (host-side package)
```

and the timing stays a suspiciously-fast `0.0Xs` regardless of which `-run` filter is applied. `go test` prints `ok` either way, so the stderr line is the reliable signal — check for it before believing an in-guest pass.

To get a real signal in-guest, give the test binary its own PID namespace so `/proc/1/comm` no longer reads `nexus3-agent`:

```sh
unshare --pid --mount-proc --fork -- env TMPDIR=/tmp go test -count=1 ./internal/cli/...
```

Use this only for pure-filesystem packages. Packages that spawn real hypervisor or supervisor machinery (`internal/core/driver/cloudhypervisor`, `internal/core/service`) are skipped deliberately — forcing them to run in-guest surfaces unrelated environment failures.

**When verification must be trustworthy, run it on the host**, where `TestMain` does not skip. An in-guest green is not evidence unless it was produced one of these two ways.
