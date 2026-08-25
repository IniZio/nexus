# Agent Image Rebuild Rule

## When to rebuild

Rebuild `nexus3-agent-base` whenever **any file under these paths changes**:

| Path | Why |
|------|-----|
| `cmd/nexus3-agent/**` | Agent binary source — PID-1 inside every sandbox |
| `internal/core/agent/**` | Agent library (mounts, builder role, disk telemetry) |

If you are unsure whether a change touches agent code, run:

```sh
git diff --name-only HEAD | grep -E '^(cmd/nexus3-agent|internal/core/agent)/'
```

If that prints anything, rebuild.

## How to rebuild

### Step 1: rebuild the agent binary

```sh
images/kernel/rebuild-base.sh
```

This produces `images/kernel/nexus3-agent` (CGO_ENABLED=0, linux/amd64) stamped
with a build tag `YYYYMMDD-<git-short-sha>`.

### Step 2: rebuild the full image

```sh
images/kernel/rebuild-base.sh --image
```

Requires: `docker` and `mke2fs` in PATH.  Takes 15–30 minutes on a cold cache
(downloads Go toolchain + Node.js + Claude Code).  Subsequent runs hit the
Docker layer cache and finish in ~2 minutes.

The rebuilt image is registered in the production image cache
(`~/.local/state/nexus3/images/`) with `Ref = nexus3-agent-base`.

### For dev/test: rootfs shortcut

If you only need to test an agent change (e.g. workspace automount), you can
skip the full image rebuild and use `--rootfs` instead:

```sh
# Build agent binary only
images/kernel/rebuild-base.sh

# Build a minimal rootfs (no Node.js/Claude) via docker
docker build \
  --build-arg NEXUS3_AGENT=images/kernel/nexus3-agent \
  -t nexus3-base-dev \
  images/base/

# Export to ext4 (requires mke2fs)
CTR=$(docker create nexus3-base-dev /bin/true)
ROOTFS=$(mktemp -d)
docker export "$CTR" | tar -C "$ROOTFS" -xf -
docker rm "$CTR"
mke2fs -t ext4 -d "$ROOTFS" /tmp/nexus3-base-dev.ext4 6g
rm -rf "$ROOTFS"

# Use with sandbox create
NEXUS3_KERNEL_PATH=images/kernel/vmlinux-x86_64 \
  go run ./cmd/nexus3 sandbox create \
  --rootfs /tmp/nexus3-base-dev.ext4 \
  --workspace /path/to/project \
  myproject/my-sandbox
```

## Staleness detection

Every agent binary built by `rebuild-base.sh` or `BuildAgentBaseImage` carries
a `build` tag embedded via `-ldflags -X main.agentBuildTag=<tag>`.

**At sandbox boot**, the agent logs to the serial console:
```
nexus3-agent: starting (pid=1 build=20260815-abc1234)
```

**To check for staleness**:

```sh
# 1. Boot a sandbox and check its serial log
NEXUS3_KERNEL_PATH=... go run ./cmd/nexus3 sandbox create --image nexus3-agent-base ...
# Read the boot log (sandbox serial output is captured to stdout/stderr during boot)

# 2. Compare the in-sandbox build tag to the current source
git rev-parse --short=7 HEAD  # e.g. abc1234
```

If the two short SHAs differ, the image is stale.  Run `rebuild-base.sh --image`.

**The silent-ignore failure mode (eliminated)**:

Before 2026-08-15 the agent did not log unrecognized cmdline args.  An Aug-11
agent binary silently ignored `--workspace-mount=` (added Aug-13), so the
workspace disks were attached but never mounted.  This required `cat /proc/cmdline`
to diagnose and `mount /dev/vdf /workspace/example-app` as a manual workaround.

As of 2026-08-15, any unrecognized arg produces a console log line:
```
nexus3-agent: WARN: unrecognized cmdline arg "--new-flag=..." — host/guest version skew? agent build=20260811-...
```

This makes staleness immediately visible in the boot output rather than
requiring `/proc/cmdline` inspection.

## What image does `sandbox create --image nexus3-agent-base` use?

The image is resolved by scanning the local image cache
(`~/.local/state/nexus3/images/sha256/`) for the first entry with
`"ref": "nexus3-agent-base"` in its `meta.json`.  The scan returns entries in
SHA-256 hex alphabetical order, not by creation date.

After a rebuild, prune stale entries so the new image is always found first:

```sh
# List images
go run ./cmd/nexus3 image ls

# The new image has a fresh created_at; stale ones are 2026-08-11.
# Pass the two stale digests to prune (keep the rest):
# (image prune keeps all digests NOT in the provided set — run sandbox ls
#  to collect referenced digests first)
```

## Enforcement (wired)

The recommendation below is **enforced** since 2026-08-15 via the Makefile
target `check-agent-fresh` (CI-AGENT-REBUILD, D-PD-27/28):

```sh
make check-agent-fresh
```

It fails when any `.go` under `cmd/nexus3-agent/` or `internal/core/agent/`
is newer than the checked binary. The 2026-08-15 egress outage (stale Aug-11
agent image assigned the guest IP to dummy0; virtio eth0 left DOWN; every
sandbox booted with dead networking) was exactly this failure mode — run the
target before any live-sandbox proof.

**Two binaries are checked:**

1. `images/kernel/nexus3-agent` — the base-image agent embedded in `nexus3-agent-base`.
   Rebuilding: `images/kernel/rebuild-base.sh [--image]`

2. The **on-PATH builder agent** (`exec.LookPath("nexus3-agent")`, typically
   `~/.local/bin/nexus3-agent`) — baked into builder VMs by `nexus3 create --file`.
   Builder-image cache key = `sha256(agentBytes)[:8]`, so a stale on-PATH agent
   silently re-bakes old code even after the CLI is rebuilt.
   Rebuilding: `make install-agent`

For the full live-e2e workflow — including the two-binary rebuild requirement
and the `herdr_live` test incantation — see `CONTRIBUTING.md §Live-e2e incantation`.

## CI recommendation (superseded by enforcement above)

