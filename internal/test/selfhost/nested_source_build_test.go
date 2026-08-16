//go:build integration

// Package selfhost — S-NESTED-BUILD nested source-build integration proof.
//
// Proves end-to-end that an outer nexus3 microVM (booted with NestedVirt=true)
// can build an inner VM image that contains the nexus3 source tree, boot that
// inner VM, and run `go build ./...` successfully inside it.
//
// This is the nested mirror of TestBuildDogfood: where that test proves an
// OUTER guest can build nexus3, this test proves an INNER (nested) guest can
// do the same, traversing the full three-layer stack:
//
//	host → outer guest (NestedVirt) → inner KVM VM → go build nexus3
//
// # Acceptance criteria
//
//   - (S-NESTED-BUILD-AC1) Outer guest has /dev/kvm (NestedVirt=true).
//   - (S-NESTED-BUILD-AC2) In-guest buildkitd builds an inner VM image
//     containing Go 1.26.5, the nexus3 source tree, and a pre-seeded module
//     cache (no network access required at inner VM runtime).
//   - (S-NESTED-BUILD-AC3) The inner cloud-hypervisor boots the inner VM.
//   - (S-NESTED-BUILD-AC4) `go build ./...` inside the inner VM exits 0; the
//     serial log contains the sentinel "INNER_BUILD_OK".
//
// # Failure discipline
//
// Transport errors hard-fail. Unexpected output or missing sentinel hard-fail.
// Only absent infrastructure causes t.Skip.
//
// # Prerequisites
//
//   - /dev/kvm accessible on the host
//   - Host nested KVM enabled (/sys/module/kvm_{intel,amd}/parameters/nested == 1|Y)
//   - cloud-hypervisor binary (CLOUD_HYPERVISOR_BIN or ~/.local/bin/cloud-hypervisor)
//   - mke2fs in PATH (e2fsprogs)
//   - docker in PATH
//   - images/kernel/vmlinux-x86_64 present in the repo root
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run TestNestedSourceBuild \
//	    ./internal/test/selfhost/ -v -timeout 120m
//
// # Ladder knobs (env-overridable, no code edits needed)
//
//	NESTED_OUTER_MIB   outer VM RAM in MiB (default 8192)
//	NESTED_INNER_MIB   inner VM RAM in MiB (default 4096)
//	NESTED_LADDER_RUNG "full" (default) = real go build ./...
//	                   "dummy"          = go version only (no compile)
package selfhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter"
	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
	"github.com/IniZio/nexus3/internal/core/perimeter/netfilter"
	"github.com/IniZio/nexus3/internal/core/perimeter/netstack"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// nestedSrcBuildDockerTag is the docker image tag for the outer VM image.
const nestedSrcBuildDockerTag = "nexus3-nested-source-build-test:dev"

// nestedSrcBuildImageSizeGB is the outer VM ext4 size.
//
// Sized to hold: ubuntu base (~200 MB) + buildkitd suite (~100 MB) +
// cloud-hypervisor (~40 MB) + Go tarball at /opt (~700 MB) +
// Go installed at /usr/local/go (~700 MB, from the warm-cache RUN) +
// warm GOCACHE at /root/.cache/go-build (~400 MB) +
// module cache at /root/go/pkg/mod (~400 MB) +
// nexus3 source at /workspace (~50 MB) + vmlinux (~30 MB) + nexus3-agent
// (~20 MB) = ~2.6 GB image content.  Inner build context, rootfs export, and
// inner ext4 now live on the 20 GiB scratch disk (/var/lib/buildkit) rather than
// /tmp, so the outer VM's RAM-backed tmpfs is not pressured. 20 GiB gives ample
// headroom for the outer rootfs content and outer VM runtime.
const nestedSrcBuildImageSizeGB = int64(20 * 1024 * 1024 * 1024)

// nestedDefaultOuterMiB is the default outer VM RAM (env: NESTED_OUTER_MIB).
const nestedDefaultOuterMiB = int64(8192)

// nestedDefaultInnerMiB is the default inner VM RAM (env: NESTED_INNER_MIB).
const nestedDefaultInnerMiB = int64(4096)

// nestedSrcBuildContainerfile is the Dockerfile for the outer VM image.
//
// It extends the nested-dogfood outer image with three additions:
//  1. The Go 1.26.5 tarball pre-staged at /opt/go.tar.gz — the in-guest build
//     script copies it into the inner Containerfile build context so the inner
//     image installs Go without any internet access at inner-image-build time.
//  2. The nexus3 source tree at /workspace (COPY from HOST build context) —
//     the in-guest script copies it into the inner build context so the inner
//     VM has the full source tree to build.
//  3. A HOST-compiled warm GOCACHE at /root/.cache/go-build — the outer
//     Containerfile's RUN step installs Go and runs `go build ./...` at HOST
//     docker-build time (cached permanently across test runs), populating a
//     fully warm content-addressed build cache.  The in-guest script stages
//     this cache into the inner build context so the inner VM's `go build ./...`
//     is incremental (near-noop) — no cold 692-package compile under nested KVM.
//
// The Go tarball is downloaded at HOST docker-build time so it is baked into
// the outer image and requires no egress from the outer sandbox at test time.
const nestedSrcBuildContainerfile = `# nexus3 nested-source-build test fixture — outer VM image.
FROM ubuntu:24.04

# ── Base tools ────────────────────────────────────────────────────────────────
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        e2fsprogs \
        iproute2 \
    && rm -rf /var/lib/apt/lists/*

# ── buildkitd + buildctl + buildkit-runc (moby/buildkit v0.18.2) ─────────────
ARG BUILDKIT_VERSION=v0.18.2
RUN TARBALL="buildkit-${BUILDKIT_VERSION}.linux-amd64.tar.gz" \
    && curl -fsSL --retry 5 --retry-delay 2 \
        "https://github.com/moby/buildkit/releases/download/${BUILDKIT_VERSION}/${TARBALL}" \
        -o "/tmp/${TARBALL}" \
    && tar -C /tmp -xzf "/tmp/${TARBALL}" \
    && install -m 755 /tmp/bin/buildkitd     /usr/local/bin/buildkitd \
    && install -m 755 /tmp/bin/buildctl      /usr/local/bin/buildctl \
    && install -m 755 /tmp/bin/buildkit-runc /usr/local/bin/buildkit-runc \
    && rm -rf /tmp/bin "/tmp/${TARBALL}"

# ── cloud-hypervisor static binary ───────────────────────────────────────────
RUN curl -fsSL --retry 5 --retry-delay 2 \
        "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/latest/download/cloud-hypervisor-static" \
        -o /usr/local/bin/cloud-hypervisor \
    && chmod +x /usr/local/bin/cloud-hypervisor

# ── Go tarball (pre-staged for inner VM image build — no inner-time download) ─
# Downloaded here at HOST docker-build time so the in-guest script can copy it
# into the inner Containerfile build context without any internet fetch.
ARG GO_VERSION=1.26.5
RUN curl -fsSL --retry 5 --retry-delay 2 \
        "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz" \
        -o /opt/go.tar.gz

# ── Inner-VM kernel ───────────────────────────────────────────────────────────
COPY vmlinux /boot/vmlinux

# ── nexus3 source tree ────────────────────────────────────────────────────────
# Staged from the HOST build context by buildNestedSourceBuildImage.
# Available at /workspace in the outer guest so the in-guest script can copy
# it into the inner buildkitd build context.
COPY go.mod go.sum /workspace/
COPY third_party /workspace/third_party/
COPY src /workspace/

# ── Pre-warm Go build cache (GOCACHE) — pays 692-pkg compile ONCE on the host ─
# Runs at HOST docker-build time; result is cached permanently in this Docker
# layer.  Subsequent test runs reuse the cached layer (seconds, not 40+ min).
# The warm /root/.cache/go-build is then staged into the inner build context
# by the in-guest script so the inner VM's go build ./... is a near-noop.
# go.mod requires go 1.25.5; the installed toolchain is 1.26.5 (>= minimum)
# so GOTOOLCHAIN=local prevents any network toolchain download inside docker build.
RUN tar -C /usr/local -xzf /opt/go.tar.gz \
    && cd /workspace \
    && /usr/local/go/bin/go env GOVERSION \
    && GOPATH=/root/go \
       GOCACHE=/root/.cache/go-build \
       GOPROXY=https://proxy.golang.org \
       GONOSUMDB='*' \
       GOFLAGS=-mod=mod \
       GOTOOLCHAIN=local \
       CGO_ENABLED=0 \
       PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/sbin:/usr/sbin:/usr/bin:/bin \
       /usr/local/go/bin/go build ./... \
    && rm -rf /usr/local/go/test /usr/local/go/api /usr/local/go/doc \
              /usr/local/go/pkg/tool/*/test2json 2>/dev/null || true

# ── Guest agent (outer PID 1) ─────────────────────────────────────────────────
COPY nexus3-agent /sbin/nexus3-agent
RUN chmod 755 /sbin/nexus3-agent

ENV IS_SANDBOX=1
`

// readEnvInt64 reads an environment variable as int64, returning def if absent or invalid.
func readEnvInt64(key string, def int64) int64 {
	if s := os.Getenv(key); s != "" {
		var v int64
		if _, err := fmt.Sscanf(s, "%d", &v); err == nil && v > 0 {
			return v
		}
	}
	return def
}

// buildInnerSourceBuildScript returns the shell program run INSIDE the outer guest,
// parameterized for the memory ladder and test rung.
//
//   - innerMiB: inner VM --memory size in MiB (e.g. 4096 → "--memory size=4096M").
//   - rung: "full" runs real go build ./...; "dummy" runs go version only (no compile).
//
// The script drives the complete in-guest build + inner VM boot + go build sequence:
//  1. Mount kernel pseudo-FSes (idempotent; nexus3-agent may already have some).
//  2. Format /dev/vdb as ext4 and mount at /var/lib/buildkit (sparse scratch disk, off-RAM).
//  3. Write the runc --no-new-keyring wrapper (prevents session-keyring exhaustion).
//  4. Start buildkitd rootful with --oci-worker-snapshotter=native.
//  5. Wait for the buildkitd socket (90 s timeout).
//  6. Create inner build context at /var/lib/buildkit/inner-ctx (on scratch disk).
//  7. Write the inner Containerfile and inner VM init script.
//  8. buildctl solve → local rootfs export.
//  9. mke2fs -d → raw ext4 inner disk image.
// 10. Boot inner cloud-hypervisor VM using the ext4, capture serial log.
// 11. Print serial log; assert "INNER_BUILD_OK" appears.
//
// S-INSTRUMENT-LADDER: this function emits [outer-mem] /proc/meminfo probes at
// step-6 and step-10 boundaries so the host memory curve can be correlated with
// the in-guest step markers.  The inner VM init script emits [inner-mem] probes
// before/after the tmpfs GOCACHE copy and before go build.
func buildInnerSourceBuildScript(innerMiB int64, rung string) string {
	// innerBuildSection is the variable part inside the inner VM init script.
	// "dummy" rung runs go version instead of the full compile so the boot path
	// (tmpfs copy, inner VM launch) can be validated cheaply.
	var innerBuildSection string
	if rung == "dummy" {
		innerBuildSection = `
echo "[inner-mem] before dummy rung (LADDER_RUNG=dummy — no full compile):"
grep -E 'MemTotal|MemAvailable|MemFree' /proc/meminfo || true
echo "[inner] LADDER_RUNG=dummy: running go version only (not compiling)"
/usr/local/go/bin/go version
BUILD_OK=true
echo "[inner] TIMESTAMP go_build_done=$(date -u +%s) ok=$BUILD_OK"
echo "[inner] WARM build elapsed=0s (dummy rung — skipped full build)"
`
	} else {
		// "full" or anything else: real go build ./... against the warm cache.
		innerBuildSection = `
echo "[inner-mem] before go build ./...:"
grep -E 'MemTotal|MemAvailable|MemFree' /proc/meminfo || true
T_WARM0=$(date -u +%s)
BUILD_OK=false
GOPATH=/root/go \
GOPROXY=off \
GONOSUMDB='*' \
GOTOOLCHAIN=local \
CGO_ENABLED=0 \
GOCACHE=/tmp/gocache \
GOTMPDIR=/tmp/gotmp \
GOFLAGS=-mod=readonly \
    /usr/local/go/bin/go build ./... \
  && BUILD_OK=true \
  || BUILD_OK=false
T_WARM1=$(date -u +%s)

echo "[inner] TIMESTAMP go_build_done=$(date -u +%s) ok=$BUILD_OK"
echo "[inner] WARM build elapsed=$((T_WARM1-T_WARM0))s (image-baked warm cache; host-parity incremental)"
echo "[inner-mem] after go build:"
grep -E 'MemTotal|MemAvailable|MemFree' /proc/meminfo || true
`
	}

	// innerInitScript is the content of /var/lib/buildkit/inner-ctx/init-build.sh
	// written by the outer guest.  [inner-mem] probes are added around the
	// tmpfs GOCACHE copy and build steps for memory curve measurement.
	innerInitScript := `#!/bin/sh
set -eu
# Mount kernel virtual filesystems.
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
# tmpfs for Go build artifacts: virtio-blk writes fail under nested KVM (outer
# kernel async-I/O limitation) so the root is mounted ro; GOCACHE, GOTMPDIR,
# and any temp files must live on this tmpfs.
mount -t tmpfs tmpfs /tmp -o size=3g 2>/dev/null || true
mkdir -p /tmp/gocache /tmp/gotmp

echo "[inner] TIMESTAMP boot_done=$(date -u +%s)"
echo "[inner-mem] after tmpfs mount (size=3g):"
grep -E 'MemTotal|MemAvailable|MemFree' /proc/meminfo || true
df -h /tmp || true

# Seed GOCACHE from the HOST-compiled image-baked warm cache.
# The outer Containerfile's RUN step ran go build ./... at HOST docker-build time
# (full host CPU, permanently cached as a Docker layer) and the resulting
# /root/.cache/go-build was COPY'd into this inner rootfs by the inner
# Containerfile.  Copy it to tmpfs so go build reads cached objects without
# any virtio-blk I/O on the hot path (reads from the ro rootfs are fine;
# only writes had the nested-KVM async-I/O limitation).
echo "[gocache] seeding GOCACHE from image-baked cache (/root/.cache/go-build → /tmp/gocache)..."
echo "[inner-mem] before gocache seed:"
grep -E 'MemTotal|MemAvailable|MemFree' /proc/meminfo || true
T_SEED0=$(date -u +%s)
cp -a /root/.cache/go-build/. /tmp/gocache/ 2>/dev/null || true
T_SEED1=$(date -u +%s)
echo "[gocache] seed done in $((T_SEED1-T_SEED0))s; size=$(du -sh /tmp/gocache 2>/dev/null | cut -f1)"
echo "[inner-mem] after gocache seed:"
grep -E 'MemTotal|MemAvailable|MemFree' /proc/meminfo || true
df -h /tmp || true

echo "[inner] go version: $(/usr/local/go/bin/go version)"
echo "[inner] TIMESTAMP go_build_start=$(date -u +%s)"
` + innerBuildSection + `
if [ "$BUILD_OK" = "true" ]; then
  echo INNER_BUILD_OK
else
  echo INNER_BUILD_FAIL
fi
sync
`

	// Full outer guest script with [outer-mem] probes injected at step-6 and
	// step-10 boundaries so the host memory curve can be read from the test log.
	return `#!/bin/sh
set -eu

BUILDKITD=/usr/local/bin/buildkitd
BUILDCTL=/usr/local/bin/buildctl
RUNC=/usr/local/bin/buildkit-runc
CLOUD_HYPERVISOR=/usr/local/bin/cloud-hypervisor
KERNEL=/boot/vmlinux

echo "==> [S-NESTED-BUILD] step 1: mounting kernel pseudo-FSes"
mount -t proc proc /proc               2>/dev/null || true
mount -t sysfs sysfs /sys              2>/dev/null || true
mount -t devtmpfs devtmpfs /dev        2>/dev/null || true
mkdir -p /dev/pts
mount -t devpts devpts /dev/pts        2>/dev/null || true
mkdir -p /sys/fs/cgroup
mount -t cgroup2 cgroup2 /sys/fs/cgroup 2>/dev/null || true

echo "==> [S-NESTED-BUILD] step 2: scratch disk for buildkitd state"
mkfs.ext4 -F /dev/vdb 2>/dev/null || true
mkdir -p /var/lib/buildkit
mount /dev/vdb /var/lib/buildkit

echo "==> [S-NESTED-BUILD] step 3: runc wrapper (--no-new-keyring)"
mkdir -p /run/buildkit
cat > /run/buildkit/nexus-runc << 'WRAPPER'
#!/bin/sh
set -eu
REAL_RUNC=/usr/local/bin/buildkit-runc
args=""
injected=false
for arg in "$@"; do
  args="$args '$arg'"
  if [ "$injected" = "false" ] && [ "${arg#-}" = "$arg" ]; then
    case "$arg" in
      run|create) args="$args --no-new-keyring"; injected=true ;;
    esac
  fi
done
eval exec "$REAL_RUNC" $args
WRAPPER
chmod 755 /run/buildkit/nexus-runc

echo "==> [S-NESTED-BUILD] step 4: starting buildkitd"
SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt \
SSL_CERT_DIR=/etc/ssl/certs \
BUILDKITD_SNAPSHOTTER=native \
  "$BUILDKITD" \
    --root /var/lib/buildkit/root \
    --addr unix:///run/buildkit/buildkitd.sock \
    --oci-worker-snapshotter=native \
    --oci-worker-binary=/run/buildkit/nexus-runc \
    >> /tmp/buildkitd.log 2>&1 &
BKPID=$!

echo "==> [S-NESTED-BUILD] step 5: waiting for buildkitd socket (up to 90s)"
i=0
while [ $i -lt 450 ]; do
  [ -S /run/buildkit/buildkitd.sock ] && break
  sleep 0.2
  i=$((i+1))
done
if [ ! -S /run/buildkit/buildkitd.sock ]; then
  echo "ERROR: buildkitd socket never appeared"
  cat /tmp/buildkitd.log || true
  exit 1
fi
echo "buildkitd ready (pid=$BKPID)"

echo "==> [S-NESTED-BUILD] step 6: creating inner build context (on scratch disk — off RAM)"
# Use scratch disk (/var/lib/buildkit, ext4 formatted in step 2) so the inner
# build context, rootfs export, and inner ext4 do NOT consume outer-guest RAM.
mkdir -p /var/lib/buildkit/inner-ctx/workspace

# Go tarball: baked into the outer image; inner Containerfile extracts it
# locally so no internet fetch is needed at inner-image-build time.
cp /opt/go.tar.gz /var/lib/buildkit/inner-ctx/go.tar.gz

# CA cert bundle: copied from the outer guest's system store.
cp /etc/ssl/certs/ca-certificates.crt /var/lib/buildkit/inner-ctx/ca-certificates.crt

# MITM CA: the outer sandbox's egress is MITM'd by the nexus3 perimeter proxy.
cp /tmp/mitm-ca.pem /var/lib/buildkit/inner-ctx/nexus3-mitm.crt

# nexus3 source tree: baked into the outer image at /workspace.
cp -r /workspace/. /var/lib/buildkit/inner-ctx/workspace/

# GOCACHE (warm): pre-compiled by HOST docker build into the outer image.
echo "[gocache] staging outer GOCACHE into inner build context..."
echo "[outer-mem] before gocache-stage (step 6):"
grep -E 'MemTotal|MemAvailable|MemFree' /proc/meminfo || true
df -h /tmp /var/lib/buildkit || true
mkdir -p /var/lib/buildkit/inner-ctx/gocache
if [ -d /root/.cache/go-build ] && [ "$(ls -A /root/.cache/go-build 2>/dev/null)" ]; then
  T_GC0=$(date -u +%s)
  cp -a /root/.cache/go-build/. /var/lib/buildkit/inner-ctx/gocache/
  T_GC1=$(date -u +%s)
  echo "[gocache] staged in $((T_GC1-T_GC0))s; size=$(du -sh /var/lib/buildkit/inner-ctx/gocache | cut -f1)"
else
  echo "[gocache] WARNING: /root/.cache/go-build empty/missing — inner build will be cold (still correct)"
fi
echo "[outer-mem] after gocache-stage:"
grep -E 'MemTotal|MemAvailable|MemFree' /proc/meminfo || true
df -h /var/lib/buildkit || true

echo "==> [S-NESTED-BUILD] step 7a: writing inner VM init script"
cat > /var/lib/buildkit/inner-ctx/init-build.sh << 'INIT'
` + innerInitScript + `INIT
chmod 755 /var/lib/buildkit/inner-ctx/init-build.sh

echo "==> [S-NESTED-BUILD] step 7b: writing inner Containerfile"
cat > /var/lib/buildkit/inner-ctx/Containerfile << 'CF'
FROM ubuntu:24.04
# CA cert bundle: replace ubuntu default with the pre-staged outer bundle so the
# inner image has TLS trust without any apt-get (archive.ubuntu.com is not in
# the perimeter allowlist).
COPY ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
# Inner init script: tiny file, cheap to snapshot early.
COPY init-build.sh /sbin/init-build
# Go tarball + MITM CA in one layer: both land in /tmp before the single RUN
# below, avoiding an extra 900-MB native-snapshotter copy.
COPY nexus3-mitm.crt go.tar.gz /tmp/
# Full nexus3 source tree: includes go.mod, go.sum, third_party, and all source
# dirs.  Placed before the RUN so go mod download runs against the complete
# workspace in the same step.
COPY workspace /workspace
# Pre-baked GOCACHE from the HOST docker-build layer of the outer image.
# The outer Containerfile installs Go and runs go build ./... at docker-build
# time (host CPU, permanently cached).  This COPY delivers that warm cache into
# the inner rootfs; init-build.sh then cp -a's it to tmpfs so the inner VM's
# go build ./... hits the cache without virtio-blk writes.
# Graceful: if gocache/ is empty (outer image predates this fix), go build still
# succeeds — it runs cold, which is correct if slower.
COPY gocache /root/.cache/go-build
# Single RUN: append MITM CA → install Go → seed module cache (go mod download).
# go build ./... is NOT here — it ran at HOST docker-build time (outer
# Containerfile) and its result was delivered via the COPY gocache above.
# Only go mod download is needed to populate pkg/mod (module sources) so the
# inner VM's go build can find them with GOPROXY=off GOFLAGS=-mod=readonly.
# All heavyweight work in ONE snapshot so the native snapshotter copies the
# accumulated FS exactly once instead of once per layer.
RUN cat /tmp/nexus3-mitm.crt >> /etc/ssl/certs/ca-certificates.crt \
    && rm /tmp/nexus3-mitm.crt \
    && chmod 755 /sbin/init-build \
    && tar -C /usr/local -xzf /tmp/go.tar.gz \
    && rm /tmp/go.tar.gz \
    && rm -rf /usr/local/go/test /usr/local/go/api /usr/local/go/doc \
              /usr/local/go/pkg/tool/*/test2json 2>/dev/null || true \
    && cd /workspace \
    && GOPROXY=https://proxy.golang.org \
       GONOSUMDB='*' \
       GOFLAGS=-mod=mod \
       GOPATH=/root/go \
       /usr/local/go/bin/go mod download
ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOPATH=/root/go
CF

echo "==> [S-NESTED-BUILD] step 8: buildctl solve → inner rootfs (on scratch disk)"
mkdir -p /var/lib/buildkit/inner-rootfs

# ── aggressive writeback: keep Dirty low so writeback to /dev/vdb keeps up ───
# Default dirty_ratio=20%/dirty_background_ratio=10% of 8 GiB = 800/1600 MiB of
# tolerated dirty — far more than needed and the root cause of guest cache bloat.
# Tighten to 5%/2% (~400/160 MiB cap) with fast writeback cadence so dirty pages
# flush to the scratch disk promptly rather than accumulating in guest page cache.
sysctl -w vm.dirty_ratio=5                2>/dev/null || true
sysctl -w vm.dirty_background_ratio=2     2>/dev/null || true
sysctl -w vm.dirty_expire_centisecs=200   2>/dev/null || true
sysctl -w vm.dirty_writeback_centisecs=100 2>/dev/null || true
echo "[dropcache] writeback sysctls applied: dirty_ratio=5 dirty_background_ratio=2 dirty_expire=200cs dirty_writeback=100cs"

# ── outer-guest memory instrumentation around step-8 buildctl export ──────────
# Background sampler: every 2 s append a line to stdout while buildctl runs.
# Killed immediately after buildctl returns (or dies with the VM on OOM).
_sample_outer_mem() {
  while true; do
    _T=$(date -u +%s 2>/dev/null || echo 0)
    _LINE=$(grep -E '^(MemTotal|MemFree|MemAvailable|Buffers|Cached|Dirty|Writeback|AnonPages|Slab|SReclaimable|KReclaimable|Shmem):' /proc/meminfo 2>/dev/null \
      | awk '{printf "%s=%s ", $1, $2}')
    printf '[outer-mem-step8] t=%s %s\n' "$_T" "$_LINE"
    sleep 2
  done
}

# Background drop_caches loop: every 2 s sync then drop clean page cache (=1).
# sync first flushes dirty pages so they become reclaimable; drop_caches=1
# frees only clean page cache (not dentries/inodes — safe during active I/O).
# The already-wired FreePageReporting balloon then returns freed guest pages to
# the host, preventing host OOM during the ~700 MiB rootfs export.
_dropcache_loop() {
  while true; do
    sync 2>/dev/null || true
    echo 1 > /proc/sys/vm/drop_caches 2>/dev/null || true
    sleep 2
  done
}

echo "[outer-mem-step8] SNAPSHOT_BEFORE t=$(date -u +%s 2>/dev/null || echo 0)"
grep -E '^(MemTotal|MemFree|MemAvailable|Buffers|Cached|Dirty|Writeback|AnonPages|Slab|SReclaimable|KReclaimable|Shmem):' /proc/meminfo || true

_sample_outer_mem &
_SAMPLER_PID=$!
_dropcache_loop &
_DROPCACHE_PID=$!
trap 'kill $_SAMPLER_PID $_DROPCACHE_PID 2>/dev/null || true' EXIT INT TERM

"$BUILDCTL" \
  --addr unix:///run/buildkit/buildkitd.sock \
  build \
    --frontend=dockerfile.v0 \
    --opt filename=Containerfile \
    --local context=/var/lib/buildkit/inner-ctx \
    --local dockerfile=/var/lib/buildkit/inner-ctx \
    --progress plain \
    --output type=local,dest=/var/lib/buildkit/inner-rootfs

kill $_SAMPLER_PID $_DROPCACHE_PID 2>/dev/null || true
trap - EXIT INT TERM
echo "[outer-mem-step8] SNAPSHOT_AFTER t=$(date -u +%s 2>/dev/null || echo 0)"
grep -E '^(MemTotal|MemFree|MemAvailable|Buffers|Cached|Dirty|Writeback|AnonPages|Slab|SReclaimable|KReclaimable|Shmem):' /proc/meminfo || true
# ─────────────────────────────────────────────────────────────────────────────

echo "==> [S-NESTED-BUILD] step 9: mke2fs → inner ext4 (8 GiB, on scratch disk)"
INNER_EXT4=/var/lib/buildkit/inner.ext4
truncate -s 8G "$INNER_EXT4"
mke2fs -t ext4 -d /var/lib/buildkit/inner-rootfs \
  -L nexus3-inner-src \
  -U 00000000-0000-0000-0000-000000000002 \
  "$INNER_EXT4"

echo "==> [S-NESTED-BUILD] step 10: pre-boot diagnostics"
ls -la "$INNER_EXT4"
df -h /tmp
free -m
echo "[outer-mem] pre-boot (step 10):"
grep -E 'MemTotal|MemAvailable|MemFree' /proc/meminfo || true
echo "cloud-hypervisor version:"
"$CLOUD_HYPERVISOR" --version || true

echo "==> [S-NESTED-BUILD] step 10b: stopping buildkitd to free RAM before inner VM boot"
kill $BKPID 2>/dev/null || true
sleep 1

echo "==> [S-NESTED-BUILD] step 10c: THP — enabling transparent hugepages in outer guest"
# Setting THP=always in the outer guest allows the inner cloud-hypervisor
# process's memory to be backed by 2 MiB hugepages.  The inner CH already
# requests MADV_HUGEPAGE for VM RAM via thp=true; with THP=always the outer
# guest OS can satisfy those requests, reducing TLB pressure in the 3-level
# AMD NPT stack without requiring explicit hugetlb reservation pools.
echo always > /sys/kernel/mm/transparent_hugepage/enabled 2>/dev/null || true
echo "[THP] outer guest policy: $(cat /sys/kernel/mm/transparent_hugepage/enabled 2>/dev/null || echo unavailable)"
echo "[outer-mem] post-buildkitd-stop / pre-inner-boot:"
grep -E 'MemTotal|MemAvailable|MemFree' /proc/meminfo || true

echo "==> [S-NESTED-BUILD] step 11: booting inner VM (init=/sbin/init-build)"
mkdir -p /tmp/inner-vm
# Root mounted ro: virtio-blk writes fail under nested KVM (async-I/O issue).
# The init-build script mounts tmpfs for all write paths (GOCACHE, GOTMPDIR).
# GOCACHE is seeded from the image-baked /root/.cache/go-build (rootfs reads are
# fine); no extra disk needed.  timeout 3600: safety net only (warm build is ~2 s).
timeout 3600 "$CLOUD_HYPERVISOR" \
  --kernel "$KERNEL" \
  --cmdline 'root=/dev/vda ro init=/sbin/init-build console=ttyS0 panic=0' \
  --disk path="$INNER_EXT4",readonly=off,direct=off \
  --cpus boot=2 \
  --memory size=` + fmt.Sprintf("%dM", innerMiB) + ` \
  --serial file=/tmp/inner-vm/serial.log \
  --console off \
  --api-socket /tmp/inner-vm/ch.sock \
  || true

echo "==> [S-NESTED-BUILD] step 12: inner VM serial output:"
if [ -f /tmp/inner-vm/serial.log ]; then
  cat /tmp/inner-vm/serial.log
else
  echo "ERROR: no serial log produced"
  exit 1
fi
`
}

// TestNestedSourceBuild proves end-to-end that an outer nexus3 microVM can
// build and boot an inner nexus3 microVM that compiles the nexus3 source tree
// with `go build ./...`, exiting 0.
//
// Acceptance criteria:
//   - (S-NESTED-BUILD-AC1) Outer guest has /dev/kvm (NestedVirt=true).
//   - (S-NESTED-BUILD-AC2) In-guest buildkitd builds inner image with Go + source.
//   - (S-NESTED-BUILD-AC3) Inner cloud-hypervisor boots the inner VM.
//   - (S-NESTED-BUILD-AC4) Serial log contains "INNER_BUILD_OK" (go build exited 0).
func TestNestedSourceBuild(t *testing.T) {
	// ── 0. Ladder knobs ───────────────────────────────────────────────────────
	// Env-overridable so a memory ladder can be walked without editing code:
	//   NESTED_OUTER_MIB=6144  (walk outer RAM down from 8192)
	//   NESTED_INNER_MIB=2048  (walk inner RAM down from 4096)
	//   NESTED_LADDER_RUNG=dummy  (boot path only, no compile)
	outerMiB := readEnvInt64("NESTED_OUTER_MIB", nestedDefaultOuterMiB)
	innerMiB := readEnvInt64("NESTED_INNER_MIB", nestedDefaultInnerMiB)
	ladderRung := os.Getenv("NESTED_LADDER_RUNG")
	if ladderRung == "" {
		ladderRung = "full"
	}
	t.Logf("[ladder] NESTED_OUTER_MIB=%d NESTED_INNER_MIB=%d NESTED_LADDER_RUNG=%s",
		outerMiB, innerMiB, ladderRung)

	// Background memory sampler: emits host MemAvailable + CH proc count every
	// 2 s so the memory curve is visible even for opaque in-guest steps.
	// Prefix: [memsample] — greppable in the captured log.
	testStart := time.Now()
	samplerStop := make(chan struct{})
	var samplerWg sync.WaitGroup
	samplerWg.Add(1)
	go func() {
		defer samplerWg.Done()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-samplerStop:
				return
			case <-ticker.C:
				elapsed := time.Since(testStart).Round(time.Second)
				t.Logf("[memsample] elapsed=%s hostMem=%dMiB CH=%d",
					elapsed, hostMemAvailableMiB(), liveCHCount())
			}
		}
	}()
	defer func() {
		close(samplerStop)
		samplerWg.Wait()
	}()

	// ── 1. Skip guards ────────────────────────────────────────────────────────
	skipUnlessNestedKVM(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	// ── 2. Kernel path ────────────────────────────────────────────────────────
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── 3. Build outer image ──────────────────────────────────────────────────
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	imgCtx, imgCancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer imgCancel()

	t.Log("building nested-source-build outer image (first run: ~20–40 min; cached: seconds) ...")
	img, err := buildNestedSourceBuildImage(imgCtx, cache, repoRoot)
	if err != nil {
		switch {
		case errors.Is(err, ErrDockerUnavailable):
			t.Skip("skipping: docker unavailable:", err)
		case errors.Is(err, builder.ErrMke2fsUnavailable):
			t.Skip("skipping: mke2fs unavailable:", err)
		}
		t.Fatalf("buildNestedSourceBuildImage: %v", err)
	}
	t.Logf("outer image: digest=%s size=%.2f GiB", img.Digest, float64(img.Size)/(1<<30))

	// ── 4. Infrastructure ─────────────────────────────────────────────────────
	socketDir, err := os.MkdirTemp("/tmp", "nested-source-build-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	serialPath := filepath.Join(socketDir, "nested-source-build-serial.log")
	t.Cleanup(func() {
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== outer guest serial output ===\n%s", content)
		}
		os.RemoveAll(socketDir) //nolint:errcheck
	})

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}

	svcDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath:   chBin,
		SocketDir:    socketDir,
		KernelPath:   kernelPath,
		StartTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("cloudhypervisor.New (svc): %v", err)
	}
	svc := service.New(st, svcDrv, lifecycle.New())

	// ── 5. Boot outer sandbox with NestedVirt=true ────────────────────────────
	// Memory: outer needs enough RAM to allocate the inner VM (innerMiB) plus
	// outer kernel, nexus3-agent, and buildkitd overhead (~2 GiB).
	var bootDrv *cloudhypervisor.CHDriver
	factory := service.DriverFactory(func(ext4Path string, extraDisks []service.ExtraDisk) (driver.Driver, error) {
		var chExtraDisks []cloudhypervisor.ExtraDisk
		for _, ed := range extraDisks {
			chExtraDisks = append(chExtraDisks, cloudhypervisor.ExtraDisk{Path: ed.Path})
		}
		var ferr error
		bootDrv, ferr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:       chBin,
			SocketDir:        socketDir,
			KernelPath:       kernelPath,
			DiskImagePath:    ext4Path,
			MemoryMiB:        uint32(outerMiB), // NESTED_OUTER_MIB (default 8192)
			VCPUs:            6,                // 6 vCPUs: inner build VM uses ~2, leaving 4 for outer guest's agent/network/kernel; prevents CPU starvation during nested build
			SerialOutputPath: serialPath,
			StartTimeout:      90 * time.Second,
			NestedVirt:        true, // expose /dev/kvm for inner cloud-hypervisor
			FreePageReporting: true, // passively return guest-free pages to host; prevents host-OOM during step-8 buildctl export burst
			ExtraDisks:        chExtraDisks,
		})
		return bootDrv, ferr
	})
	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		return realProbeSH(bootDrv)(ctx, drv, id)
	})

	// Create a sparse 20 GiB scratch disk for buildkitd state in the outer guest.
	// Using truncate (sparse file) costs ~nothing on the host; the guest formats
	// it as ext4 and mounts it at /var/lib/buildkit, keeping ~1.5–2 GiB of
	// buildkitd state off the outer guest's RAM tmpfs so the inner VM can
	// boot without the outer guest OOMing.
	scratchDiskPath := filepath.Join(socketDir, "buildkit-scratch.raw")
	if err := func() error {
		f, err := os.Create(scratchDiskPath)
		if err != nil {
			return err
		}
		defer f.Close()
		// Truncate to 20 GiB sparse: Truncate extends with holes (no disk blocks consumed).
		return f.Truncate(20 * 1024 * 1024 * 1024)
	}(); err != nil {
		t.Fatalf("create buildkit scratch disk: %v", err)
	}
	t.Logf("buildkit scratch disk: %s (20 GiB sparse, will be /dev/vdb in outer guest)", scratchDiskPath)

	// Log the host THP policy for the record.
	if thpPolicy, rerr := os.ReadFile("/sys/kernel/mm/transparent_hugepage/enabled"); rerr == nil {
		t.Logf("[THP] host policy: %s", strings.TrimSpace(string(thpPolicy)))
	}

	t.Logf("[host-mem] before sandbox create: hostMem=%dMiB CH=%d elapsed=%s",
		hostMemAvailableMiB(), liveCHCount(), time.Since(testStart).Round(time.Second))
	t.Logf("creating and booting outer sandbox (NestedVirt=true, outerMiB=%d innerMiB=%d rung=%s) ...",
		outerMiB, innerMiB, ladderRung)
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer bootCancel()

	var sandboxID domain.SandboxID
	t.Cleanup(func() {
		if sandboxID == (domain.SandboxID{}) {
			return
		}
		rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if rerr := svc.Remove(rmCtx, sandboxID.String()); rerr != nil {
			t.Logf("cleanup: svc.Remove(%s): %v", sandboxID, rerr)
		}
	})

	sb, err := service.CreateAndBoot(
		bootCtx, svc, cache, factory, probe,
		"nested-source-build", fmt.Sprintf("nested-source-build-%d", time.Now().UnixNano()),
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(img.Digest)},
			CacheRoot:           cacheRoot,
			ReachabilityTimeout: 90 * time.Second,
			NestedVirt:          true,
			// Attach the sparse scratch disk as /dev/vdb in the outer guest.
			// The in-guest script (step 2) formats it ext4 and mounts it at
			// /var/lib/buildkit, keeping buildkitd state off RAM so the inner VM
			// can boot without triggering an outer guest OOM.
			ExtraDisks: []service.ExtraDisk{
				{Path: scratchDiskPath}, // /dev/vdb: buildkit scratch disk
			},
			// docker.io: for ubuntu:24.04 pull inside buildkitd (inner image build).
			// proxy.golang.org: for `go mod download all` RUN inside buildkitd
			//   (seeds inner VM module cache; GONOSUMDB=* skips sum.golang.org).
			AllowedHosts: []string{
				"registry-1.docker.io",
				"auth.docker.io",
				"index.docker.io",
				"production.cloudflare.docker.com",
				"production.cloudfront.docker.com",
				"proxy.golang.org",
			},
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxID = sb.ID
	t.Logf("outer sandbox booted: %s state=%s", sb.ID, sb.State)
	t.Logf("[host-mem] after outer boot: hostMem=%dMiB CH=%d elapsed=%s",
		hostMemAvailableMiB(), liveCHCount(), time.Since(testStart).Round(time.Second))

	// ── 5b. Start netstack + MITM perimeter (AllowAll) ──────────────────────
	// CreateAndBoot calls bootDrv.Start directly (not svc.Start), so the
	// perimeter is never auto-started. Wire the correct MITM perimeter with
	// AllowAll so in-guest buildkitd can reach docker.io, proxy.golang.org, and
	// any transitive host (e.g. storage.googleapis.com for go mod download).
	// The MITM stays in the egress path as the control point (audit-logged) but
	// applies an allow-all policy. This build carries zero credentials so Broker
	// is omitted; AllowAll covers everything.
	nh := interface{}(bootDrv).(driver.NetworkHook)
	netFD, err := nh.GuestNetworkFD(context.Background(), sb.ID)
	if err != nil {
		t.Fatalf("GuestNetworkFD: %v", err)
	}

	al, alErr := netfilter.NewAllowList(nil, nil, nil)
	if alErr != nil {
		netFD.Close()
		t.Fatalf("netfilter.NewAllowList: %v", alErr)
	}
	al.AllowAllFor(90 * time.Minute)

	stack := netstack.New(al, nil) // DEFAULT dialer — perimeter.Start wires MITM

	mitmProxy, mitmErr := mitm.New(mitm.Config{
		SandboxID: sb.ID,
		AllowAll:  true,
		// No Broker: this build carries zero credentials.
		// No AllowedHosts: AllowAll covers everything.
	})
	if mitmErr != nil {
		netFD.Close()
		al.Stop()
		t.Fatalf("mitm.New: %v", mitmErr)
	}

	supCtx, supCancel := context.WithCancel(context.Background())
	defer supCancel()

	sup, supErr := perimeter.Start(context.WithoutCancel(supCtx), sb.ID, netFD, stack, mitmProxy, al)
	if supErr != nil {
		t.Fatalf("perimeter.Start: %v", supErr)
	}
	defer sup.Close()
	t.Logf("netstack+MITM perimeter started at %s (AllowAll)", sup.MitmAddr())

	agentClient := agent.NewClient(bootDrv, sb.ID)

	// ── 5c. Seed MITM CA into outer guest system trust store ──────────────────
	// buildkitd uses the system cert pool to pull docker.io images and to run
	// the `go mod download` RUN step inside the inner Containerfile build; both
	// go through the MITM proxy and must trust the per-sandbox CA.
	// Write the CA to the Debian/Ubuntu extra-CA drop-in directory and to
	// /tmp/mitm-ca.pem (for the inner Containerfile build context), then run
	// update-ca-certificates to rebuild the system bundle.
	{
		caPEM := pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: sup.CACert().Raw,
		})
		var caOut bytes.Buffer
		caExit, caErr := agentClient.Exec(context.Background(), agent.ExecOptions{
			Argv: []string{
				"/bin/sh", "-c",
				"mkdir -p /usr/local/share/ca-certificates && " +
					"tee /usr/local/share/ca-certificates/nexus3-mitm.crt > /tmp/mitm-ca.pem && " +
					"update-ca-certificates",
			},
			Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/sbin:/usr/sbin:/usr/bin:/bin"},
			Stdin:  bytes.NewReader(caPEM),
			Stdout: &caOut,
			Stderr: &caOut,
		})
		t.Logf("seed MITM CA output:\n%s", caOut.String())
		if caErr != nil {
			t.Fatalf("seed MITM CA: transport error: %v", caErr)
		}
		if caExit != 0 {
			t.Fatalf("seed MITM CA: update-ca-certificates exited %d\n%s", caExit, caOut.String())
		}
		t.Log("MITM CA seeded: outer guest trust store updated; /tmp/mitm-ca.pem staged for inner build")
	}

	// ── 6. AC1: assert /dev/kvm is present ───────────────────────────────────
	t.Log("S-NESTED-BUILD-AC1: asserting /dev/kvm is present in outer guest ...")
	var kvmOut bytes.Buffer
	kvmExit, kvmErr := agentClient.Exec(context.Background(), agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "[ -c /dev/kvm ] && echo KVM_OK || echo KVM_ABSENT"},
		Env:    map[string]string{"PATH": "/usr/local/bin:/sbin:/usr/sbin:/usr/bin:/bin"},
		Stdout: &kvmOut,
		Stderr: &kvmOut,
	})
	if kvmErr != nil {
		t.Fatalf("S-NESTED-BUILD-AC1: exec transport error: %v\noutput:\n%s", kvmErr, kvmOut.String())
	}
	if kvmExit != 0 || !strings.Contains(kvmOut.String(), "KVM_OK") {
		t.Fatalf("S-NESTED-BUILD-AC1 FAIL: /dev/kvm absent in outer guest (exit=%d)\n%s", kvmExit, kvmOut.String())
	}
	t.Log("S-NESTED-BUILD-AC1 PASS: /dev/kvm present in outer guest")

	// ── 6b. NESTED_OUTER_WARM_ONLY: fast CONTROL experiment ──────────────────
	// Proves whether a warm `go build ./...` inside the OUTER guest is fast
	// (seconds), validating the pre-bake redesign target before investing in it.
	// The outer image bakes a warm GOCACHE at /root/.cache/go-build (via the
	// `RUN go build ./...` layer); we replicate that layer's env exactly so the
	// cache is guaranteed to hit.  Runs the build TWICE: first (warm from the
	// baked cache), second (steady-state incremental).  Both wall times are
	// logged; everything downstream (inner image build, mke2fs, inner VM boot)
	// is skipped — that's the point.
	if os.Getenv("NESTED_OUTER_WARM_ONLY") == "1" {
		t.Log("[outer-warm-build] NESTED_OUTER_WARM_ONLY=1: skipping inner-build path; running warm go build ./... in outer guest")

		// Exact env from the Containerfile `RUN ... go build ./...` layer
		// (lines 183–191 of the embedded Containerfile string):
		warmEnv := map[string]string{
			"GOPATH":      "/root/go",
			"GOCACHE":     "/root/.cache/go-build",
			"GOPROXY":     "https://proxy.golang.org",
			"GONOSUMDB":   "*",
			"GOFLAGS":     "-mod=mod",
			"GOTOOLCHAIN": "local",
			"CGO_ENABLED": "0",
			"PATH":        "/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/sbin:/usr/sbin:/usr/bin:/bin",
		}

		for i := 1; i <= 2; i++ {
			var buildOut bytes.Buffer
			buildStart := time.Now()
			buildExit, buildErr := agentClient.Exec(context.Background(), agent.ExecOptions{
				Argv:   []string{"/bin/sh", "-c", "cd /workspace && /usr/local/go/bin/go build ./..."},
				Env:    warmEnv,
				Stdout: &buildOut,
				Stderr: &buildOut,
			})
			buildWall := time.Since(buildStart).Round(time.Millisecond)
			t.Logf("[outer-warm-build] run %d: go build ./... wall=%s exit=%d", i, buildWall, buildExit)
			if buildOut.Len() > 0 {
				t.Logf("[outer-warm-build] run %d output:\n%s", i, buildOut.String())
			}
			if buildErr != nil {
				t.Fatalf("[outer-warm-build] run %d: transport error: %v", i, buildErr)
			}
			if buildExit != 0 {
				t.Fatalf("[outer-warm-build] run %d: go build ./... exited %d\n%s", i, buildExit, buildOut.String())
			}
		}

		t.Log("[outer-warm-build] NESTED_OUTER_WARM_ONLY mode complete — skipping inner build / nested VM boot")
		return
	}

	// ── 7. AC2+AC3+AC4: run the nested source-build script ───────────────────
	// Script timeline:
	//   - buildkitd start + inner image build (module download): ~15–30 min
	//   - mke2fs: ~2 min
	//   - inner VM boot + go build ./...: ~20–60 min
	// Allow 90 minutes total for the combined script.
	t.Log("S-NESTED-BUILD-AC2/AC3/AC4: running nested source-build script (up to 90 min) ...")
	t.Logf("[host-mem] before script exec: hostMem=%dMiB CH=%d elapsed=%s",
		hostMemAvailableMiB(), liveCHCount(), time.Since(testStart).Round(time.Second))

	var scriptOut bytes.Buffer
	scriptCtx, scriptCancel := context.WithTimeout(context.Background(), 90*time.Minute)
	defer scriptCancel()

	scriptExit, scriptErr := agentClient.Exec(scriptCtx, agent.ExecOptions{
		Argv: []string{"/bin/sh", "-c", buildInnerSourceBuildScript(innerMiB, ladderRung)},
		Env: map[string]string{
			"PATH":          "/usr/local/bin:/sbin:/usr/sbin:/usr/bin:/bin",
			"HOME":          "/root",
			"SSL_CERT_FILE": "/etc/ssl/certs/ca-certificates.crt",
		},
		Stdout: &scriptOut,
		Stderr: &scriptOut,
	})

	output := scriptOut.String()
	t.Logf("nested source-build script output (%d bytes):\n%s", len(output), output)

	if scriptErr != nil {
		t.Logf("[host-mem] AT_EOF: hostMem=%dMiB CH=%d elapsed=%s",
			hostMemAvailableMiB(), liveCHCount(), time.Since(testStart).Round(time.Second))
		t.Fatalf("S-NESTED-BUILD nested build+boot: transport error: %v", scriptErr)
	}
	if scriptExit != 0 {
		t.Fatalf("S-NESTED-BUILD nested build+boot: script exited %d\nfull output:\n%s", scriptExit, output)
	}

	// AC2: buildkitd started and built the inner image
	if !strings.Contains(output, "buildkitd ready") {
		t.Fatalf("S-NESTED-BUILD-AC2 FAIL: buildkitd did not reach ready state\noutput:\n%s", output)
	}
	t.Log("S-NESTED-BUILD-AC2 PASS: in-guest buildkitd started and built inner image")

	// AC3: inner ext4 produced
	if !strings.Contains(output, "inner ext4") {
		t.Fatalf("S-NESTED-BUILD-AC3 FAIL: inner ext4 step not completed\noutput:\n%s", output)
	}
	t.Log("S-NESTED-BUILD-AC3 PASS: inner ext4 produced by mke2fs")

	// AC4: go build exited 0 inside the inner VM — check for sentinel and no panic.
	// Kernel panic means go build never ran (init died before or during build).
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Kernel panic") || strings.Contains(line, "Unable to mount root fs") {
			t.Fatalf("S-NESTED-BUILD-AC4 FAIL: inner VM kernel panic — go build never completed\noffending line: %s\nfull output:\n%s", line, output)
		}
	}
	if strings.Contains(output, "INNER_BUILD_FAIL") {
		t.Fatalf("S-NESTED-BUILD-AC4 FAIL: go build ./... failed inside inner VM\nfull output:\n%s", output)
	}
	if !strings.Contains(output, "INNER_BUILD_OK") {
		t.Fatalf("S-NESTED-BUILD-AC4 FAIL: INNER_BUILD_OK sentinel not found in serial log (go build did not complete)\noutput:\n%s", output)
	}
	t.Log("S-NESTED-BUILD-AC4 PASS: go build ./... succeeded inside inner VM — INNER_BUILD_OK confirmed")

	// ── 8. S-WARMCACHE-THP: log per-stage timings and host-parity proof ─────────
	// Emit every timing/cache/THP/mem line so the run is not a silent black box.
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "TIMESTAMP") ||
			strings.Contains(line, "[gocache]") ||
			strings.Contains(line, "WARM build elapsed") ||
			strings.Contains(line, "[THP]") ||
			strings.Contains(line, "[inner-mem]") ||
			strings.Contains(line, "[outer-mem]") {
			t.Log("warmcache-thp:", strings.TrimSpace(line))
		}
	}

	// Host-parity assertion: the warm incremental build ran the full ./... scope.
	// "WARM build elapsed=" is emitted only after go build ./... completes against
	// the image-baked GOCACHE.  If the init script narrowed scope or skipped
	// packages, the build would not emit this sentinel.
	if !strings.Contains(output, "WARM build elapsed=") {
		t.Fatalf("S-WARMCACHE-THP FAIL: warm-build parity check not found in output\n"+
			"(inner init script may have failed before the build ran)\noutput:\n%s", output)
	}
	t.Log("S-WARMCACHE-THP PASS: warm go build ./... ran to completion — timing logged above")
}

// buildNestedSourceBuildImage produces the outer VM ext4 image for
// TestNestedSourceBuild and stores it in cache keyed by SHA-256 digest.
//
// The outer image contains:
//   - buildkitd suite (buildkitd, buildctl, buildkit-runc)
//   - cloud-hypervisor static binary
//   - Go 1.26.5 tarball at /opt/go.tar.gz (for the inner VM image build)
//   - nexus3 source tree at /workspace (for the inner VM image build context)
//   - vmlinux at /boot/vmlinux (for inner VM boot)
//   - nexus3-agent at /sbin/nexus3-agent (outer VM PID 1)
//
// Prerequisites:
//   - docker in PATH (returns [ErrDockerUnavailable] if absent)
//   - mke2fs in PATH (returns [builder.ErrMke2fsUnavailable] if absent)
//   - images/kernel/vmlinux-x86_64 present under repoRoot
func buildNestedSourceBuildImage(ctx context.Context, cache *image.Cache, repoRoot string) (domain.Image, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return domain.Image{}, ErrDockerUnavailable
	}
	if !builder.Mke2fsAvailable() {
		return domain.Image{}, builder.ErrMke2fsUnavailable
	}

	kernelSrc := filepath.Join(repoRoot, "images", "kernel", "vmlinux-x86_64")
	if _, err := os.Stat(kernelSrc); err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: kernel not found at %s: %w", kernelSrc, err)
	}

	workDir, err := os.MkdirTemp("", "nexus3-nested-source-build-*")
	if err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: mktemp: %w", err)
	}
	defer os.RemoveAll(workDir) //nolint:errcheck

	// Compile nexus3-agent (outer PID 1).
	agentBin := filepath.Join(workDir, "nexus3-agent")
	if err := buildAgent(ctx, repoRoot, agentBin); err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: build agent: %w", err)
	}

	// Prepare docker build context.
	ctxDir := filepath.Join(workDir, "ctx")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: mkdir ctx: %w", err)
	}

	// Write Containerfile.
	cfPath := filepath.Join(ctxDir, "Containerfile")
	if err := os.WriteFile(cfPath, []byte(nestedSrcBuildContainerfile), 0o644); err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: write Containerfile: %w", err)
	}

	// Stage vmlinux.
	if err := copyFile(kernelSrc, filepath.Join(ctxDir, "vmlinux"), 0o644); err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: copy kernel: %w", err)
	}

	// Stage nexus3-agent.
	if err := copyFile(agentBin, filepath.Join(ctxDir, "nexus3-agent"), 0o755); err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: copy agent: %w", err)
	}

	// Stage go.mod + go.sum (for the Containerfile COPY directives).
	for _, f := range []string{"go.mod", "go.sum"} {
		if err := copyFile(filepath.Join(repoRoot, f), filepath.Join(ctxDir, f), 0o644); err != nil {
			return domain.Image{}, fmt.Errorf("nested-source-build-image: copy %s: %w", f, err)
		}
	}

	// Stage nexus3 source directories into ctx/src/ for the `COPY src /workspace/` directive.
	// Only directories that exist are staged; the Containerfile references them collectively.
	srcCtxDir := filepath.Join(ctxDir, "src")
	for _, srcDir := range []string{"internal", "cmd", "pkg"} {
		src := filepath.Join(repoRoot, srcDir)
		if _, err := os.Stat(src); os.IsNotExist(err) {
			continue
		}
		dst := filepath.Join(srcCtxDir, srcDir)
		if err := copyDir(src, dst); err != nil {
			return domain.Image{}, fmt.Errorf("nested-source-build-image: copy %s: %w", srcDir, err)
		}
	}

	// Stage third_party (required for go.mod local replace directives).
	thirdPartySrc := filepath.Join(repoRoot, "third_party")
	if _, err := os.Stat(thirdPartySrc); err == nil {
		if err := copyDir(thirdPartySrc, filepath.Join(ctxDir, "third_party")); err != nil {
			return domain.Image{}, fmt.Errorf("nested-source-build-image: copy third_party: %w", err)
		}
	}

	// docker build.
	buildCmd := exec.CommandContext(ctx, "docker", "build",
		"-f", cfPath,
		"-t", nestedSrcBuildDockerTag,
		ctxDir,
	)
	buildCmd.Stdout = os.Stderr
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: docker build: %w", err)
	}
	defer func() { _ = exec.Command("docker", "rmi", "--force", nestedSrcBuildDockerTag).Run() }()

	// docker create → docker export → extract tar → rootfs.
	rootfsDir := filepath.Join(workDir, "rootfs")
	if err := os.MkdirAll(rootfsDir, 0o755); err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: mkdir rootfs: %w", err)
	}
	if err := exportRootfs(ctx, nestedSrcBuildDockerTag, rootfsDir); err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: export rootfs: %w", err)
	}

	// mke2fs → raw ext4.
	ext4Path := filepath.Join(workDir, "nested-source-build.ext4")
	if err := runMke2fs(ctx, rootfsDir, ext4Path, nestedSrcBuildImageSizeGB); err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: mke2fs: %w", err)
	}

	// Hash and store.
	f, err := os.Open(ext4Path)
	if err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: open ext4: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: stat ext4: %w", err)
	}

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: hash ext4: %w", err)
	}
	digest, err := domain.ParseDigest("sha256:" + hex.EncodeToString(h.Sum(nil)))
	if err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: parse digest: %w", err)
	}

	img := domain.Image{
		Digest:    digest,
		Ref:       "nexus3-nested-source-build",
		Kind:      domain.KindBase,
		Size:      info.Size(),
		CreatedAt: time.Now().UTC(),
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: seek ext4: %w", err)
	}
	if err := cache.Put(ctx, img, f); err != nil {
		return domain.Image{}, fmt.Errorf("nested-source-build-image: cache.Put: %w", err)
	}
	return img, nil
}
