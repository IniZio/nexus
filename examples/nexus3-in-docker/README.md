# Example: nexus3 host daemon in a Docker container

Demonstrates running the nexus3 **host daemon** inside a Docker container so
that it can boot microVMs — the same deployment shape microsandbox uses for its
`msbserver` image.

**Proven end-to-end (Aug 2026, docker 29.5.3):**

1. `nexus3 doctor` — all 5 capability checks pass with `--device /dev/kvm`;
   the `kvm` check FAILs without it.
2. `nexus3 create --file` — pulled `moby/buildkit`, assembled the builder ext4,
   booted the builder microVM, ran buildkit, and produced the image.
3. `nexus3 exec` in the guest returned `/marker` and `uname -sr`:

   ```
   built
   Linux 6.12.76
   ```

## What it takes (the microsandbox-shaped tradeoff)

The container is privileged toward the host kernel; isolation lives in the VM.

| Requirement | Why |
|---|---|
| `--device /dev/kvm` | hypervisor accelerator — hard requirement |
| `--device /dev/net/tun` | TAP devices for the in-process gvproxy/netns perimeter |
| `--cap-add NET_ADMIN` | create netns + TAP + program netfilter |
| `--cap-add SYS_ADMIN` | unshare(CLONE_NEWNET) inside the egress supervisor |
| glibc base image | `nexus3` is glibc-dynamic (CH + virtiofsd are static-pie) |
| kernel via `NEXUS3_KERNEL_PATH` | `images/kernel/vmlinux-x86_64` baked in |
| gvproxy | **none** — it is in-process (vendored), no separate binary |

`--privileged` is NOT required. The two `--cap-add` flags above suffice.

## Four portability gotchas

### 1. State ownership (disk locking)

Mount a fresh container-owned Docker volume at `/root/.local/state/nexus3`.
If you bind-mount the host's `~/.local/state/nexus3`, disk images are owned by
the host uid; cloud-hypervisor opens them `O_RDWR` from inside its user-namespace
wrapper and gets `Permission denied (os error 13) — The VM could not boot`.

### 2. Worktree TMPDIR device guard

`create --file` refuses when `TMPDIR` is on a different device than the source
tree, and loops infinitely if `TMPDIR` is **inside** the source tree.  Put
source and `TMPDIR` as **siblings on the same volume** (e.g. `/work/src` and
`/work/tmp`).

### 3. Socket path consistency (XDG_RUNTIME_DIR required)

nexus3 has two socket-directory formulas that diverge when `$TMPDIR` is set:

- CLI (`orcaSocketDir`): uses `$TMPDIR`
- CHDriver fallback (`defaultSocketDir`): hardcodes `/tmp`

`XDG_RUNTIME_DIR` takes precedence in **both** codepaths.  Set it to a
writable path on the work volume (e.g. `-e XDG_RUNTIME_DIR=/work/rt`).
Without this, `create` puts sockets in `/work/rt/nexus3/` but `exec` looks
in `/tmp/nexus3-0/` → every exec fails with "no such file or directory".

### 4. /proc/sys/net is read-only in unprivileged containers

Docker's `--cap-add SYS_ADMIN` is insufficient to remount `/proc/sys/net`
as writable (the kernel blocks proc remount in user namespaces inside Docker).
nexus3 writes `disable_ipv6` and `forwarding` sysctls before bringing up TAP
interfaces; these writes are **best-effort** (non-fatal on EROFS) — the Linux
defaults (forwarding=0, IPv6 link-local in an isolated netns) are safe.  A
warning is printed to stderr but the VM boots normally.

## Binary provenance

| Binary | Source |
|---|---|
| `nexus3` | Built from source in the Go builder stage (`go build ./cmd/nexus3`) |
| `cloud-hypervisor` | Downloaded from GitHub releases, **v52.0**, SHA-256 verified |
| `virtiofsd` | Staged from host binary (v1.13.3) — GitLab virtio-fs/virtiofsd publishes only source archives; a Rust build stage is deferred (see TODO in Dockerfile) |
| guest kernel + agent | Copied from `images/kernel/` in the repo |

Pinned versions:
- cloud-hypervisor: **v52.0** (`cloud-hypervisor-static`)
  SHA-256: `829af01ff075bb96c4f183905134c453a88d68cbabdc6b87df21098842581ee9`
- virtiofsd: **1.13.3** (host binary, SHA-256: `7ef50584b2dce226f994ac99aa86fbf603d4d61cd6e8caaffd0b005ce4796024`)

## Image stats

- Base: `debian:bookworm-slim`
- Total size: **82 MiB** (compressed layers)
- Tagged: `nexus3-host:latest`

## Build

```bash
# From nexus3 repo root — stages virtiofsd from host then builds the image:
bash examples/nexus3-in-docker/build.sh
```

`build.sh` copies the host `virtiofsd` binary into `examples/nexus3-in-docker/stage/`
(git-ignored) before invoking `docker build`.  The binary is sourced from
`$VIRTIOFSD_BIN` or `$(which virtiofsd)`.

## Run recipe

```bash
# Doctor — with /dev/kvm: all 5 checks OK:
docker run --rm --device /dev/kvm --device /dev/net/tun \
    --cap-add NET_ADMIN --cap-add SYS_ADMIN \
    nexus3-host:latest doctor

# Doctor — without /dev/kvm: kvm check FAILS:
docker run --rm --device /dev/net/tun \
    --cap-add NET_ADMIN --cap-add SYS_ADMIN \
    nexus3-host:latest doctor

# Full boot (fresh container-owned state so nexus3 owns its disks):
docker volume create n3state
docker volume create n3work
docker run --rm \
    --device /dev/kvm --device /dev/net/tun \
    --cap-add NET_ADMIN --cap-add SYS_ADMIN \
    -e TMPDIR=/work/tmp \
    -e XDG_RUNTIME_DIR=/work/rt \
    -v n3state:/root/.local/state/nexus3 \
    -v n3work:/work \
    --entrypoint sh nexus3-host:latest -c '
        mkdir -p /work/src/.nexus /work/tmp /work/rt
        printf "FROM alpine:3.20\nRUN echo built > /marker\n" > /work/src/.nexus/Containerfile
        nexus3 create ephemeral/demo --file /work/src
        nexus3 start ephemeral/demo
        nexus3 exec ephemeral/demo -- sh -c "cat /marker && uname -sr"
    '
# Expected output:
#   built
#   Linux 6.12.76
```

## Minimal docker run invocation

```
docker run --rm \
  --device /dev/kvm \
  --device /dev/net/tun \
  --cap-add NET_ADMIN \
  --cap-add SYS_ADMIN \
  nexus3-host:latest [command]
```

For full sandbox boot, also add:
```
  -e TMPDIR=/work/tmp \
  -e XDG_RUNTIME_DIR=/work/rt \
  -v n3state:/root/.local/state/nexus3 \
  -v n3work:/work \
```

## Testing

Two-tier verification covers the example in CI and on KVM hosts:

### Tier 1 — CI build smoke (no KVM, no virtiofsd required)

`go test ./...` in CI (ubuntu-latest, no KVM) skips the boot test but a
separate `example-image-smoke` job in `.github/workflows/ci.yml` builds only
the CI-feasible Dockerfile stages:

```bash
# Proves nexus3 compiles correctly inside the image build:
docker build -f examples/nexus3-in-docker/Dockerfile --target go-builder -t n3-smoke-go .

# Proves the cloud-hypervisor download URL + SHA-256 pin still resolve:
docker build -f examples/nexus3-in-docker/Dockerfile --target downloader -t n3-smoke-dl .
```

The `virtiofsd-stage` and `runtime` stages are skipped in CI because they
require a host-staged `virtiofsd` binary absent on ubuntu-latest.

### Tier 2 — KVM-gated full boot test (dev/KVM machine only)

```bash
# Runs the full image build + microVM boot + exec + assertion:
TMPDIR=/tmp go test -tags integration \
    -run TestExampleNexus3InDocker_BootsMicroVM \
    ./internal/test/selfhost/ -v -timeout 30m
```

Automatically skips when `/dev/kvm`, `docker`, or `virtiofsd` are absent —
safe to include in the regular test suite.
