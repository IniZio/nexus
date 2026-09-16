# Guest setup a dev sandbox needs

Three things are not done for you. Each produces a confusing failure rather than a clear one.

## 1. Install the MITM CA for non-Node clients

Allowlisted hosts are **TLS-intercepted** by the per-sandbox MITM proxy. The `claude-code` profile exports `NODE_EXTRA_CA_CERTS`, which only Node reads — so `claude` works while every other TLS client fails certificate validation.

The CA is seeded to `/usr/local/share/ca-certificates/nexus3-mitm.crt` and activated (`update-ca-certificates` is run) by the supervisor at boot. If you rotate the cert file while the supervisor is running, re-run `update-ca-certificates` to reload it into the system store. After that, `go`, `curl`, `pip`, `cargo`, and any other TLS client that reads the system store will trust the intercepting CA.

### Docker build containers

Every `--file` (`.nexus/Containerfile`) guest image ships a runc shim at `/usr/local/sbin/runc` that precedes the distro runc on PATH. Because dockerd, containerd, and BuildKit resolve "runc" by name, every container the guest's docker creates — `docker run`, `docker compose`, and `docker build` RUN steps — gets:

- **bind-mount** `/etc/nexus3/ca/` → `/etc/nexus3/ca/` (read-only): `ca-certificates.crt`, `wgetrc`, `apt.conf`.
- **env vars** pre-set: `SSL_CERT_FILE`, `CURL_CA_BUNDLE`, `REQUESTS_CA_BUNDLE`, `PIP_CERT`, `NODE_EXTRA_CA_CERTS`, `GIT_SSL_CAINFO`, `CARGO_HTTP_CAINFO`, `NIX_SSL_CERT_FILE`, `DENO_CERT` (all `=/etc/nexus3/ca/ca-certificates.crt`); `WGETRC=/etc/nexus3/ca/wgetrc`; `APT_CONFIG=/etc/nexus3/ca/apt.conf`.

Result: `RUN wget/curl/git/pip/npm/apt https://<TLS-intercepted host>` inside a Dockerfile works with no Dockerfile change. Live-proven 2026-09-16 (docker 29.1.3, legacy builder): wget in a RUN step logged `Loaded CA certificate '/etc/nexus3/ca/ca-certificates.crt'` and got HTTP 200 from api.github.com; curl 200; git ls-remote OK.

**Caveats:**
- The shim is inert until the supervisor has seeded `/usr/local/share/ca-certificates/nexus3-mitm.crt`. It never overlays package-owned paths, so `apt-get install ca-certificates` inside a build still works.
- A user `ENV SSL_CERT_FILE=...` in the Dockerfile wins over the shim's injection under shell-run commands (theirs is later in the env).
- Tools with vendored trust stores (Java keystores, some Go TLS configs) ignore these env vars — copy the cert in the Dockerfile.
- Sandboxes created before this change do not have the shim. The image is rebuilt on the next `nexus3 sandbox create` (the shim is a build-fingerprint input). Relaunch the sandbox.

**After TLS succeeds, egress policy still applies.** A request to a secret-bound host path outside `egress.policy` paths gets HTTP 403 from the perimeter. For example: if `github.com` allows only `/oursky/hanlun-lms/**`, then `wget https://github.com/pgpartman/pg_partman/archive/...` in a Dockerfile `RUN` step gets 403 — not a certificate error. Fix it with a path entry in `.nexus/config.yaml`, not a Dockerfile change.

**Diagnostics:**

```sh
# Inside a docker build step:
RUN env | grep SSL_CERT_FILE
RUN ls /etc/nexus3/ca

# On the guest:
ls -l /usr/local/sbin/runc   # must be the shim script, not a binary
which -a runc
```

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
