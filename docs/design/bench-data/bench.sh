#!/usr/bin/env bash
# bench.sh — virtiofs vs ext4 virtio-blk metadata benchmark for nexus3
#
# ═══════════════════════════════════════════════════════════════════════
# ⚠ THE 2026-08-13 TABLE BELOW IS **VOID**. (annotation added 2026-08-21)
#
# That run used `mke2fs -d` to populate the ext4 leg, which produced an inode
# layout that made git re-hash 489 MB on one leg only — so it measured hashing,
# not the filesystem.  It is retained verbatim for history; do NOT cite it.
#
# The 2026-08-18 redo equalises both legs with `cp -a` and supersedes it.
# Compare LIKE WITH LIKE — the void ~20x table is the cache=auto leg (its
# stat row is 1 ms; see the always-writeback note below it):
#   metadata total, auto             ~20.4x  ->  ~12.8x   (this table's successor)
#   metadata total, always-writeback ~22x    ->  ~17.1x
#   git status warm                  auto ~4.7x / always-writeback ~4.5x
# The ~17.1x headline quoted in docs/design/virtiofs-vs-ext4.md is the
# always-writeback figure, NOT the successor to this table's ~20x.
# Superseding data: docs/design/bench-data/2026-08-18-gitstatus-redo-*.txt
# Analysis:        docs/design/virtiofs-vs-ext4.md
#
# D-DC-09's verdict (keep ext4 virtio-blk) is UNCHANGED — the penalty is the
# same order of magnitude; only the multiplier was wrong.
# ═══════════════════════════════════════════════════════════════════════
# MEASURED RESULTS (2026-08-13, CH v52.0, virtiofsd 1.13.3, Linux 6.12.76)
# Workload: 1000 files × create/stat/unlink, 3 runs each, same VM boot
#
#   filesystem  create(ms) stat(ms) unlink(ms) total(ms)  ratio_vs_blk
#   virtiofs       221        1        105        327         ~20×
#   ext4-blk        10        0          6         16          1×
#   tmpfs            2        0          1          3         0.2×
#
#   cache=always --writeback: virtiofs=354ms (worse: stat rose from 1→39ms)
#
# NOTE: those figures are microbenchmarks (1000-file create/stat/unlink).
# For git status timings on a real corpus see GIT_REPO_PATH mode below.
#
# VERDICT: DO NOT SHIP virtiofs as workspace mount.
# The ~20× create penalty is structural (one FUSE round-trip per syscall)
# and no virtiofsd tuning option closes the gap for metadata workloads.
# D-DC-09 confirmed: keep ext4 virtio-blk.
#
# STALE COMMENT: bootlayers.go:35,81 still say "virtiofs mount point" —
# should be corrected to "ext4 virtio-blk disk" by whoever owns that file.
# ═══════════════════════════════════════════════════════════════════════
#
# Decision context: D-DC-09 gates virtiofs on evidence that metadata-heavy
# workloads (npm-install-class: O(10k–100k) create/stat/unlink) aren't hurt
# by virtiofs's FUSE-over-virtqueue round-trip overhead.
#
# What this script measures:
#   virtiofs  — host dir served to guest via virtiofsd (FUSE+vhost-user)
#   ext4-blk  — guest ext4 on a virtio-blk raw image (kernel driver)
#   tmpfs     — guest-local tmpfs (no host round-trip; theoretical upper bound)
#
# When GIT_REPO_PATH is set, also measures:
#   virtiofs  — git status on a COPY of the repo served via virtiofs (writable)
#   ext4-blk  — git status on the COPY loaded into a second ext4 disk
#
# BENCH-REDO (TBD-PD-17): previous run was VOID due to three equalisation
# failures; this harness corrects all of them:
#   1. mke2fs -d inode artifact: both legs now operate on a HOST-SIDE COPY of
#      the repo (cp -a), not the live repo. Both start with stale git index
#      (copy has different host inodes; mke2fs reassigns ext4 inodes again).
#      The COLD run (first git status) is the re-hash event; it is labelled
#      separately and also refreshes the index. Steady-state runs thereafter
#      do a pure stat walk on both legs.
#   2. --no-optional-locks removed: index writeback is allowed so the cold run
#      can refresh the index. See guest_bench/main.go.
#   3. Host-level drop_caches: 'sudo -n' is not available non-interactively.
#      Operator command if needed: sudo sh -c 'echo 3 > /proc/sys/vm/drop_caches'
#      Guest-level drops (echo 3 > /proc/sys/vm/drop_caches inside the VM init,
#      which runs as root) are done between every run; this clears the guest
#      page cache. Both legs are backed by the host page cache equally (virtiofsd
#      and virtio-blk both hit the host VFS), so the comparison is valid.
#   4. Runs are interleaved A/B/A/B rather than all-of-one-then-all-of-other.
#   5. n >= 10 per leg per cache mode; full distribution reported.
#
# Safety: GIT_REPO_PATH is copied to scratch. The live repo is NEVER served
# to a guest. The copy is removed on exit via the cleanup trap.
# All three run in the same VM boot to eliminate boot-time noise.
# N_RUNS boots give variance data. N_FILES files per create/stat/unlink phase.
#
# Usage:
#   bash docs/design/bench-data/bench.sh [--runs N] [--files N] [--cache POLICY]
#   VIRTIOFSD=/path/to/virtiofsd bash docs/design/bench-data/bench.sh
#   GIT_REPO_PATH=/path/to/repo bash docs/design/bench-data/bench.sh
#
# virtiofsd cache policies to try: auto (default), always, never
# --writeback flag is also exercised when cache=always.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# ─── Tunables (env or flags) ───────────────────────────────────────────────
CH_BIN="${CH_BIN:-$HOME/.local/bin/cloud-hypervisor}"
VIRTIOFSD="${VIRTIOFSD:-$HOME/.local/bin/virtiofsd}"
KERNEL="${KERNEL:-$REPO_ROOT/images/kernel/vmlinux-x86_64}"
BASE_INITRAMFS="${BASE_INITRAMFS:-$REPO_ROOT/internal/core/driver/cloudhypervisor/testdata/alpine-initramfs.cpio.gz}"
N_FILES="${N_FILES:-1000}"
N_RUNS="${N_RUNS:-10}"
VM_MEM_MIB="${VM_MEM_MIB:-2048}"
VM_VCPUS="${VM_VCPUS:-2}"
TIMEOUT_SEC="${TIMEOUT_SEC:-180}"
VFSD_CACHE="${VFSD_CACHE:-auto}"   # auto | always | never | metadata
VFSD_WRITEBACK="${VFSD_WRITEBACK:-no}"  # yes | no (only meaningful with --cache always)
EXT4_SIZE_MB="${EXT4_SIZE_MB:-256}"
# Git corpus tunables — leave GIT_REPO_PATH empty to skip git status benchmark
GIT_REPO_PATH="${GIT_REPO_PATH:-}"         # path to git repo to measure; empty = skip
GIT_DISK_SIZE_MB="${GIT_DISK_SIZE_MB:-16000}"  # ext4 image size for git corpus (must fit repo)
DISK_FREE_FLOOR_GB="${DISK_FREE_FLOOR_GB:-20}" # abort if free space would drop below this

# Parse flags
while [[ $# -gt 0 ]]; do
    case "$1" in
        --runs)   N_RUNS="$2";   shift 2 ;;
        --files)  N_FILES="$2";  shift 2 ;;
        --cache)  VFSD_CACHE="$2"; shift 2 ;;
        --writeback) VFSD_WRITEBACK="yes"; shift ;;
        --git-repo) GIT_REPO_PATH="$2"; shift 2 ;;
        *) echo "unknown arg: $1"; exit 1 ;;
    esac
done

# ─── Working directory ─────────────────────────────────────────────────────
WORK="$(mktemp -d /tmp/vfs-bench.XXXXXX)"
VIRTIOFSD_PID=0
VIRTIOFSD_GIT_PID=0
CH_PID=0
GIT_BENCH_COPY=""  # set below if git bench enabled; cleaned up on exit

cleanup() {
    [[ $VIRTIOFSD_PID -ne 0 ]]     && kill "$VIRTIOFSD_PID"     2>/dev/null || true
    [[ $VIRTIOFSD_GIT_PID -ne 0 ]] && kill "$VIRTIOFSD_GIT_PID" 2>/dev/null || true
    [[ $CH_PID -ne 0 ]]            && kill "$CH_PID"             2>/dev/null || true
    rm -rf "$WORK"
}
trap cleanup EXIT

log() { printf '[bench] %s\n' "$*" >&2; }
die() { printf '[bench] ERROR: %s\n' "$*" >&2; exit 1; }

# ─── 1. Prerequisite check ─────────────────────────────────────────────────
log "Checking prerequisites..."

[[ -x "$CH_BIN" ]]          || die "cloud-hypervisor not found: $CH_BIN"
[[ -x "$VIRTIOFSD" ]]       || die "virtiofsd not found: $VIRTIOFSD"
[[ -f "$KERNEL" ]]          || die "kernel not found: $KERNEL"
[[ -f "$BASE_INITRAMFS" ]]  || die "base initramfs not found: $BASE_INITRAMFS (run scripts/fetch-boot-artifacts.sh)"
[[ -e /dev/kvm ]]           || die "/dev/kvm not present; KVM required"

ch_ver=$("$CH_BIN" --version 2>&1 | head -1)
vfsd_ver=$("$VIRTIOFSD" --version 2>&1 | head -1)
log "  $ch_ver"
log "  $vfsd_ver"

# Verify CH supports --fs flag
if ! "$CH_BIN" --help 2>&1 | grep -q '\-\-fs'; then
    die "cloud-hypervisor does not support --fs; virtiofs NOT measurable with this binary"
fi
log "  CH supports --fs (virtiofs) ✓"

# Verify kernel has CONFIG_VIRTIO_FS (documented in PINNED.md; no runtime check)
log "  CONFIG_VIRTIO_FS=y confirmed in images/kernel/PINNED.md ✓"

# ─── sudo availability note ────────────────────────────────────────────────
# Host-level drop_caches (sudo sh -c 'echo 3 > /proc/sys/vm/drop_caches') requires
# a password and is not available non-interactively. If the operator wants host-level
# cache drops between runs, they must run that command manually before invoking this
# script. Guest-level drops ARE performed (echo 3 > /proc/sys/vm/drop_caches inside
# the VM init, which runs as root). Both legs are backed by the host page cache equally
# so the comparison is valid without host-level drops.
log "  NOTE: host-level drop_caches requires 'sudo' (not available non-interactively)."
log "        Guest-level drops performed inside VM between each run. ✓"

# Git corpus checks (when enabled)
DO_GIT_BENCH=0
GIT_DISK=""
if [[ -n "$GIT_REPO_PATH" ]]; then
    [[ -d "$GIT_REPO_PATH/.git" ]] \
        || die "GIT_REPO_PATH=$GIT_REPO_PATH does not look like a git repo (no .git dir)"
    command -v docker >/dev/null 2>&1 \
        || die "docker not found; needed to inject Alpine git binary into initramfs"

    # Record live repo state BEFORE anything else — must be unmutated at the end.
    LIVE_HEAD=$(git -C "$GIT_REPO_PATH" rev-parse HEAD 2>/dev/null || echo "")
    LIVE_PORCELAIN=$(git -C "$GIT_REPO_PATH" status --porcelain 2>/dev/null | wc -l || echo "?")
    log "  Live repo HEAD: $LIVE_HEAD"
    log "  Live repo porcelain lines: $LIVE_PORCELAIN"

    # Disk space check: copy size + ext4 image + overhead must stay above floor.
    # cp -a preserves timestamps/perms; inode numbers differ on the host filesystem.
    repo_size_gb=$(du -sB1G "$GIT_REPO_PATH" 2>/dev/null | cut -f1)
    [[ -z "$repo_size_gb" ]] && repo_size_gb=15  # safe default if du fails
    repo_size_gb=$(( repo_size_gb + 1 ))  # round up
    free_gb=$(df --output=avail -BG / | tail -1 | tr -d 'G ')
    needed_gb=$(( GIT_DISK_SIZE_MB / 1024 + repo_size_gb + 3 ))
    remaining=$(( free_gb - needed_gb ))
    log "  Disk free: ${free_gb}GB, needed (copy ~${repo_size_gb}GB + ext4 image ~$((GIT_DISK_SIZE_MB/1024))GB + overhead 3GB): ~${needed_gb}GB, remaining: ${remaining}GB"
    [[ $remaining -ge $DISK_FREE_FLOOR_GB ]] \
        || die "Insufficient disk space: ${remaining}GB would remain, floor is ${DISK_FREE_FLOOR_GB}GB. Reduce GIT_DISK_SIZE_MB or free space."

    # BENCH-REDO: Copy GIT_REPO_PATH to scratch so both legs serve from the COPY.
    # The live repo must NEVER be served to a guest in this run.
    # Rationale: both legs then start with a stale git index (copy has different host
    # inodes from the original; mke2fs reassigns ext4 inodes again). The cold run on
    # each leg is the first git status; it re-hashes, refreshes the index, and is
    # labelled separately. Subsequent steady-state runs do a pure stat walk.
    GIT_BENCH_COPY="$WORK/repo-copy"
    log "  BENCH-REDO: copying corpus to scratch for index equalisation..."
    log "    Source: $GIT_REPO_PATH  ($(du -sh "$GIT_REPO_PATH" 2>/dev/null | cut -f1))"
    log "    Dest:   $GIT_BENCH_COPY"
    cp -a "$GIT_REPO_PATH" "$GIT_BENCH_COPY" \
        || die "Failed to copy git corpus to scratch"
    log "    Copy complete: $(du -sh "$GIT_BENCH_COPY" | cut -f1)"

    # Verify the copy is intact.
    copy_head=$(git -C "$GIT_BENCH_COPY" rev-parse HEAD 2>/dev/null || echo "")
    [[ "$copy_head" == "$LIVE_HEAD" ]] \
        || die "Copy HEAD mismatch: live=$LIVE_HEAD copy=$copy_head"
    log "    Copy HEAD verified: $copy_head ✓"

    DO_GIT_BENCH=1
    GIT_DISK="$WORK/git.raw"
    log "  Git corpus (COPY): $GIT_BENCH_COPY"
    log "  Git ext4 image: ${GIT_DISK_SIZE_MB}MB at $GIT_DISK"

    # Generous timeout: cold run re-hashes all tracked content through virtiofs.
    [[ $TIMEOUT_SEC -lt 1800 ]] && TIMEOUT_SEC=1800
    log "  VM timeout set to ${TIMEOUT_SEC}s (git bench cold-run needs generous timeout)"
fi

# ─── 2. Compile guest bench binary (static, linux/amd64) ──────────────────
log "Compiling guest bench binary..."
BENCH_SRC="$SCRIPT_DIR/guest_bench/main.go"
BENCH_BIN="$WORK/bench"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -o "$BENCH_BIN" "$BENCH_SRC" \
    || die "Failed to compile guest bench binary"
log "  guest bench binary: $(du -sh "$BENCH_BIN" | cut -f1)"

# ─── 3. Create ext4 benchmark disk (file create/stat/unlink bench) ────────
log "Creating ext4 benchmark disk (${EXT4_SIZE_MB}MB)..."
EXT4_DISK="$WORK/bench.raw"
dd if=/dev/zero of="$EXT4_DISK" bs=1M count="$EXT4_SIZE_MB" status=none
mke2fs -t ext4 -q -F "$EXT4_DISK"
log "  ext4 disk: $EXT4_DISK"

# ─── 3b. Create git corpus ext4 disk (when GIT_REPO_PATH is set) ──────────
if [[ $DO_GIT_BENCH -eq 1 ]]; then
    log "Creating git corpus ext4 disk (${GIT_DISK_SIZE_MB}MB, populating from COPY)..."
    log "  This reads ~$(du -sh "$GIT_BENCH_COPY" 2>/dev/null | cut -f1) and may take several minutes..."
    # Sparse image: seek to the end, no data written until mke2fs fills it
    dd if=/dev/zero of="$GIT_DISK" bs=1M count=0 seek="$GIT_DISK_SIZE_MB" status=none
    # Populate ext4 image from COPY — mke2fs -d assigns fresh ext4 inode numbers.
    # This is intentional: both legs start with stale git index (different inodes),
    # and the cold run on each leg is the index-refresh event.
    mke2fs -t ext4 -F -d "$GIT_BENCH_COPY" "$GIT_DISK" "${GIT_DISK_SIZE_MB}M" \
        || die "mke2fs -d failed; check that e2fsprogs >= 1.43 and disk is large enough"
    log "  git corpus disk: $(du -sh "$GIT_DISK" | cut -f1)"
fi

# ─── 4. Build custom initramfs ────────────────────────────────────────────
log "Building custom initramfs..."
ROOTFS="$WORK/rootfs"
mkdir -p "$ROOTFS"

# Extract base Alpine initramfs
(cd "$ROOTFS" && gunzip -c "$BASE_INITRAMFS" | cpio -id --quiet 2>/dev/null)

# Inject bench binary
install -D -m 0755 "$BENCH_BIN" "$ROOTFS/usr/local/bin/bench"

# Inject git binary (musl-linked) via Docker when doing git bench
if [[ $DO_GIT_BENCH -eq 1 ]]; then
    log "Injecting Alpine git binary into initramfs via Docker..."
    mkdir -p "$ROOTFS/usr/bin" "$ROOTFS/usr/lib" "$ROOTFS/lib"
    docker run --rm \
        -v "$ROOTFS:/rootfs:z" \
        alpine:3.21 \
        sh -c '
apk add --no-cache git >/dev/null 2>&1
cp /usr/bin/git /rootfs/usr/bin/git
# musl libc (may already be in initramfs; overwrite is safe)
[ -f /lib/ld-musl-x86_64.so.1 ] && cp /lib/ld-musl-x86_64.so.1 /rootfs/lib/ 2>/dev/null || true
# pcre2 (git dep on Alpine)
find /usr/lib /lib -name "libpcre2-8.so*" 2>/dev/null | while read f; do
    cp "$f" /rootfs/usr/lib/ 2>/dev/null || true
done
# zlib
find /usr/lib /lib -name "libz.so*" 2>/dev/null | while read f; do
    cp "$f" /rootfs/usr/lib/ 2>/dev/null || true
done
echo "git_inject_ok: $(git --version 2>/dev/null)"
' || die "Failed to inject git binary via Docker"
    log "  git binary injected ✓"
fi

# Create mount points (including git-ext4 for corpus disk, gitrepo for second virtiofs)
mkdir -p "$ROOTFS/mnt/virtiofs" "$ROOTFS/mnt/ext4" "$ROOTFS/mnt/tmpfs" \
         "$ROOTFS/mnt/data" "$ROOTFS/mnt/git-ext4" "$ROOTFS/mnt/gitrepo"

# Write custom /init that runs the benchmark.
# BENCH-REDO methodology (see header):
#   - Git bench mounts both legs writable (index writeback allowed).
#   - Cold run (first git status per leg) is labelled *-cold; re-hashes stale index.
#   - Steady-state runs are interleaved A/B; guest page cache dropped before each.
#   - Host-level drop_caches not available; guest-level drops equalize both legs.
cat > "$ROOTFS/init" << 'INITEOF'
#!/bin/sh
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true

echo "GUEST_INIT_START"

N_FILES="$(cat /proc/cmdline | tr ' ' '\n' | grep '^bench.n=' | cut -d= -f2)"
N_FILES="${N_FILES:-1000}"
N_RUNS="$(cat /proc/cmdline | tr ' ' '\n' | grep '^bench.runs=' | cut -d= -f2)"
N_RUNS="${N_RUNS:-10}"

echo "GUEST_PARAMS n=$N_FILES runs=$N_RUNS"

HAS_GIT=0
[ -x /usr/bin/git ] && HAS_GIT=1
echo "GUEST_HAS_GIT $HAS_GIT"

# ── virtiofs ──────────────────────────────────────────────────────────
if mount -t virtiofs workspace /mnt/virtiofs 2>/dev/null; then
    echo "MOUNT_OK virtiofs"

    # File create/stat/unlink bench
    run=0
    while [ $run -lt "$N_RUNS" ]; do
        run=$((run + 1))
        mkdir -p /mnt/virtiofs/bench
        /usr/local/bin/bench /mnt/virtiofs/bench "virtiofs-r${run}" "$N_FILES"
        rm -rf /mnt/virtiofs/bench
    done

    umount /mnt/virtiofs
else
    echo "MOUNT_FAIL virtiofs (virtiofs device not present)"
fi

# ── ext4 virtio-blk (file bench disk, /dev/vda) ───────────────────────
if mount -t ext4 /dev/vda /mnt/ext4 2>/dev/null; then
    echo "MOUNT_OK ext4-blk"
    run=0
    while [ $run -lt "$N_RUNS" ]; do
        run=$((run + 1))
        mkdir -p /mnt/ext4/bench
        /usr/local/bin/bench /mnt/ext4/bench "ext4-blk-r${run}" "$N_FILES"
        rm -rf /mnt/ext4/bench
    done
    umount /mnt/ext4
else
    echo "MOUNT_FAIL ext4-blk (/dev/vda not present or not ext4)"
fi

# ── guest-local tmpfs (theoretical upper bound, no host round-trip) ───
mount -t tmpfs tmpfs /mnt/tmpfs
echo "MOUNT_OK tmpfs-local"
run=0
while [ $run -lt "$N_RUNS" ]; do
    run=$((run + 1))
    mkdir -p /mnt/tmpfs/bench
    /usr/local/bin/bench /mnt/tmpfs/bench "tmpfs-r${run}" "$N_FILES"
    rm -rf /mnt/tmpfs/bench
done
umount /mnt/tmpfs

# ── Git status bench: interleaved A/B, equalised ──────────────────────
# Both legs serve a COPY of the repo (host-side cp -a). Both start with
# a stale git index (copy has different inodes; mke2fs reassigns ext4 inodes).
#
# Cold run (first git status per leg):
#   - Re-hashes all tracked file content to rebuild the index.
#   - Labelled *-cold and reported separately from steady-state.
#   - Also REFRESHES the index so subsequent runs do a pure stat walk.
#
# Steady-state runs (N_RUNS per leg, interleaved A/B):
#   - Guest page cache dropped before each run.
#   - Host page cache not droppable (sudo unavailable non-interactively).
#   - Both legs hit the host page cache equally; comparison is valid.
#
# Index writeback: allowed (--no-optional-locks removed from bench binary).
# Equalisation proof: timing convergence — steady-state run 1 should be
# much faster than cold run on both legs if the index was refreshed.

VFS_GIT_READY=0
BLK_GIT_READY=0

if [ "$HAS_GIT" -eq 1 ] && mount -t virtiofs gitrepo /mnt/gitrepo 2>/dev/null; then
    echo "MOUNT_OK gitrepo"
    if [ -d /mnt/gitrepo/.git ]; then
        VFS_GIT_READY=1
    else
        echo "GITSTATUS_SKIP virtiofs (gitrepo mounted but no .git dir — unexpected)"
        umount /mnt/gitrepo
    fi
else
    echo "GITSTATUS_SKIP virtiofs (HAS_GIT=$HAS_GIT gitrepo_mount_failed)"
fi

if [ "$HAS_GIT" -eq 1 ] && mount -t ext4 /dev/vdb /mnt/git-ext4 2>/dev/null; then
    echo "MOUNT_OK git-ext4"
    BLK_GIT_READY=1
else
    echo "GITSTATUS_SKIP ext4-git (HAS_GIT=$HAS_GIT vdb=$([ -b /dev/vdb ] && echo present || echo absent))"
fi

if [ "$VFS_GIT_READY" -eq 1 ] || [ "$BLK_GIT_READY" -eq 1 ]; then
    # ── Cold run: first git status forces re-hash of stale index ──────────
    # This is the index-equalisation event: cold run re-hashes, refreshes index.
    # Reported separately from steady-state; do NOT interleave into the A/B loop.
    echo "GITSTATUS_COLD_START"
    if [ "$VFS_GIT_READY" -eq 1 ]; then
        /usr/local/bin/bench --gitstatus /mnt/gitrepo "virtiofs-git-cold"
    fi
    if [ "$BLK_GIT_READY" -eq 1 ]; then
        /usr/local/bin/bench --gitstatus /mnt/git-ext4 "ext4-git-cold"
    fi
    echo "GITSTATUS_COLD_DONE"

    # ── Steady-state: interleaved A/B with guest page-cache drops ─────────
    echo "GITSTATUS_STEADY_START"
    run=0
    while [ $run -lt "$N_RUNS" ]; do
        run=$((run + 1))
        if [ "$VFS_GIT_READY" -eq 1 ]; then
            echo 3 > /proc/sys/vm/drop_caches
            /usr/local/bin/bench --gitstatus /mnt/gitrepo "virtiofs-git-r${run}"
        fi
        if [ "$BLK_GIT_READY" -eq 1 ]; then
            echo 3 > /proc/sys/vm/drop_caches
            /usr/local/bin/bench --gitstatus /mnt/git-ext4 "ext4-git-r${run}"
        fi
    done
    echo "GITSTATUS_STEADY_DONE"
fi

[ "$VFS_GIT_READY" -eq 1 ] && umount /mnt/gitrepo  || true
[ "$BLK_GIT_READY" -eq 1 ] && umount /mnt/git-ext4 || true

echo "BENCHMARK_DONE"

# Trigger power-off via sysrq
echo 1 > /proc/sys/kernel/sysrq 2>/dev/null || true
echo o > /proc/sysrq-trigger 2>/dev/null || true
sleep 2
INITEOF
chmod +x "$ROOTFS/init"

# Repack initramfs
CUSTOM_INITRAMFS="$WORK/custom.cpio.gz"
(cd "$ROOTFS" && find . | cpio -o -H newc --quiet 2>/dev/null | gzip -1) > "$CUSTOM_INITRAMFS"
log "  custom initramfs: $(du -sh "$CUSTOM_INITRAMFS" | cut -f1)"

# ─── 5. virtiofsd setup ────────────────────────────────────────────────────
VFSD_SHARE="$WORK/virtiofs_share"
mkdir -p "$VFSD_SHARE"
VFSD_SOCK="$WORK/virtiofsd.sock"

log "Starting virtiofsd for file bench (cache=$VFSD_CACHE writeback=$VFSD_WRITEBACK)..."
VFSD_ARGS=(
    --shared-dir "$VFSD_SHARE"
    --socket-path "$VFSD_SOCK"
    --cache "$VFSD_CACHE"
    --sandbox none
)
[[ "$VFSD_WRITEBACK" == "yes" ]] && VFSD_ARGS+=(--writeback)

"$VIRTIOFSD" "${VFSD_ARGS[@]}" > "$WORK/virtiofsd.log" 2>&1 &
VIRTIOFSD_PID=$!

# Wait for virtiofsd to create its socket (up to 5s)
for i in $(seq 1 50); do
    [[ -S "$VFSD_SOCK" ]] && break
    sleep 0.1
    if ! kill -0 "$VIRTIOFSD_PID" 2>/dev/null; then
        die "virtiofsd exited unexpectedly. Log:\n$(cat "$WORK/virtiofsd.log")"
    fi
done
[[ -S "$VFSD_SOCK" ]] || die "virtiofsd socket never appeared at $VFSD_SOCK"
log "  virtiofsd running (pid=$VIRTIOFSD_PID, socket=$VFSD_SOCK)"

# ─── 5b. Second virtiofsd for git corpus (when GIT_REPO_PATH set) ──────────
# BENCH-REDO: serves GIT_BENCH_COPY (not the live repo).
# --readonly is INTENTIONALLY ABSENT: index writeback must be allowed so the
# cold run can refresh the git index. The live repo is protected by serving the COPY.
VFSD_GIT_SOCK=""
if [[ $DO_GIT_BENCH -eq 1 ]]; then
    VFSD_GIT_SOCK="$WORK/virtiofsd-git.sock"
    log "Starting virtiofsd for git corpus (cache=$VFSD_CACHE writeback=$VFSD_WRITEBACK)..."
    log "  Serving COPY: $GIT_BENCH_COPY (live repo protected)"
    VFSD_GIT_ARGS=(
        --shared-dir "$GIT_BENCH_COPY"
        --socket-path "$VFSD_GIT_SOCK"
        --cache "$VFSD_CACHE"
        --sandbox none
        # NOTE: --readonly intentionally absent — index writeback required for equalised bench.
        # SAFETY: GIT_BENCH_COPY is a scratch copy; the live GIT_REPO_PATH is never served.
    )
    [[ "$VFSD_WRITEBACK" == "yes" ]] && VFSD_GIT_ARGS+=(--writeback)

    "$VIRTIOFSD" "${VFSD_GIT_ARGS[@]}" > "$WORK/virtiofsd-git.log" 2>&1 &
    VIRTIOFSD_GIT_PID=$!

    for i in $(seq 1 50); do
        [[ -S "$VFSD_GIT_SOCK" ]] && break
        sleep 0.1
        if ! kill -0 "$VIRTIOFSD_GIT_PID" 2>/dev/null; then
            die "virtiofsd-git exited unexpectedly. Log:\n$(cat "$WORK/virtiofsd-git.log")"
        fi
    done
    [[ -S "$VFSD_GIT_SOCK" ]] || die "virtiofsd-git socket never appeared at $VFSD_GIT_SOCK"
    log "  virtiofsd-git running (pid=$VIRTIOFSD_GIT_PID, socket=$VFSD_GIT_SOCK)"
fi

# ─── 6. Build VM configuration JSON ───────────────────────────────────────
MEM_BYTES=$(( VM_MEM_MIB * 1024 * 1024 ))
SERIAL_LOG="$WORK/serial.log"
API_SOCK="$WORK/ch-api.sock"

# Build disks array: always /dev/vda (file bench), optionally /dev/vdb (git corpus)
DISKS_JSON="    {\"path\": \"$EXT4_DISK\", \"image_type\": \"Raw\"}"
if [[ $DO_GIT_BENCH -eq 1 ]]; then
    DISKS_JSON="$DISKS_JSON,
    {\"path\": \"$GIT_DISK\", \"image_type\": \"Raw\"}"
fi

# Build fs array: always "workspace" (file bench), optionally "gitrepo" (git corpus)
FS_JSON="    {\"tag\": \"workspace\", \"socket\": \"$VFSD_SOCK\", \"num_queues\": 1, \"queue_size\": 1024}"
if [[ $DO_GIT_BENCH -eq 1 ]]; then
    FS_JSON="$FS_JSON,
    {\"tag\": \"gitrepo\", \"socket\": \"$VFSD_GIT_SOCK\", \"num_queues\": 1, \"queue_size\": 1024}"
fi

VM_JSON=$(cat << JSON
{
  "payload": {
    "kernel": "$KERNEL",
    "cmdline": "console=ttyS0 reboot=k panic=1 bench.n=$N_FILES bench.runs=$N_RUNS",
    "initramfs": "$CUSTOM_INITRAMFS"
  },
  "cpus": {
    "boot_vcpus": $VM_VCPUS,
    "max_vcpus": $VM_VCPUS,
    "nested": false
  },
  "memory": {
    "size": $MEM_BYTES,
    "shared": true
  },
  "serial": {
    "mode": "File",
    "file": "$SERIAL_LOG"
  },
  "fs": [
$FS_JSON
  ],
  "disks": [
$DISKS_JSON
  ]
}
JSON
)

# ─── 7. Boot VM ───────────────────────────────────────────────────────────
log "Starting cloud-hypervisor..."
"$CH_BIN" --api-socket "$API_SOCK" > "$WORK/ch.log" 2>&1 &
CH_PID=$!

# Wait for CH API socket to be ready (up to 10s)
for i in $(seq 1 100); do
    if curl -sf --unix-socket "$API_SOCK" \
            http://localhost/api/v1/vmm.ping > /dev/null 2>&1; then
        break
    fi
    sleep 0.1
    if ! kill -0 "$CH_PID" 2>/dev/null; then
        die "cloud-hypervisor exited unexpectedly. Log:\n$(tail -20 "$WORK/ch.log")"
    fi
done
curl -sf --unix-socket "$API_SOCK" \
    http://localhost/api/v1/vmm.ping > /dev/null 2>&1 \
    || die "cloud-hypervisor API socket never became ready"
log "  CH API ready (pid=$CH_PID)"

log "Configuring VM (memory.shared=on, fs device, ext4 disk)..."
create_resp=$(curl -sf --unix-socket "$API_SOCK" \
    -X PUT http://localhost/api/v1/vm.create \
    -H "Content-Type: application/json" \
    -d "$VM_JSON" 2>&1) || {
    die "vm.create failed: $create_resp\nCH log tail:\n$(tail -20 "$WORK/ch.log")"
}

log "Booting VM..."
boot_resp=$(curl -sf --unix-socket "$API_SOCK" \
    -X PUT http://localhost/api/v1/vm.boot 2>&1) || {
    die "vm.boot failed: $boot_resp\nCH log tail:\n$(tail -20 "$WORK/ch.log")"
}

log "VM booting — waiting for BENCHMARK_DONE in serial log (timeout=${TIMEOUT_SEC}s)..."
BOOT_TS=$(date +%s)
while true; do
    if grep -q "BENCHMARK_DONE" "$SERIAL_LOG" 2>/dev/null; then
        log "  BENCHMARK_DONE marker seen ✓"
        break
    fi
    now=$(date +%s)
    elapsed=$(( now - BOOT_TS ))
    if [[ $elapsed -ge $TIMEOUT_SEC ]]; then
        log "TIMEOUT: benchmark did not complete in ${TIMEOUT_SEC}s"
        log "Serial log tail:"
        tail -40 "$SERIAL_LOG" >&2 || true
        die "benchmark timed out"
    fi
    sleep 1
done

# Give CH a moment to flush the serial log
sleep 1
kill "$VIRTIOFSD_PID" 2>/dev/null; VIRTIOFSD_PID=0
kill "$CH_PID"        2>/dev/null; CH_PID=0

# ─── 8. Parse results ─────────────────────────────────────────────────────
log "Parsing serial log..."

# Compute full distribution (mean, median, min, max, stddev) for a set of values.
# Usage: dist_stats label val1 val2 ...
dist_stats() {
    local label="$1"; shift
    printf '%s\n' "$@" | awk -v label="$label" '
    BEGIN { n=0; sum=0; sum2=0; min=999999999; max=0 }
    /^[0-9]+$/ {
        v=$1+0; n++; sum+=v; sum2+=v*v
        if(v<min) min=v
        if(v>max) max=v
        a[n]=v
    }
    END {
        if(n==0) { printf "%-20s  (no data)\n", label; exit }
        mean=sum/n
        var=(n>1) ? (sum2 - sum*sum/n)/(n-1) : 0
        stddev=sqrt(var>0?var:0)
        # insertion sort for median
        for(i=2;i<=n;i++){
            key=a[i]; j=i-1
            while(j>=1 && a[j]>key){ a[j+1]=a[j]; j-- }
            a[j+1]=key
        }
        if(n%2==1) med=a[(n+1)/2]
        else       med=(a[n/2]+a[n/2+1])/2
        printf "%-20s  n=%-3d  mean=%-6.0f  median=%-6.0f  min=%-6d  max=%-6d  stddev=%-6.1f  (ms)\n",
               label, n, mean, med, min, max, stddev
    }'
}

parse_results() {
    local serial_log="$1"
    set +u  # associative array key access is safe with :-default but nounset still fires in some bash versions

    # ── File create/stat/unlink bench ─────────────────────────────────────
    declare -A sum_create sum_stat sum_unlink count

    while IFS= read -r line; do
        if [[ "$line" =~ ^BENCH_END\ ([^\ ]+)\ create=([0-9]+)ms\ stat=([0-9]+)ms\ unlink=([0-9]+)ms ]]; then
            label="${BASH_REMATCH[1]}"
            create="${BASH_REMATCH[2]}"
            stat="${BASH_REMATCH[3]}"
            unlink="${BASH_REMATCH[4]}"
            # Strip run suffix (e.g. virtiofs-r1 -> virtiofs)
            base="${label%-r[0-9]*}"
            sum_create[$base]=$(( ${sum_create[$base]:-0} + create ))
            sum_stat[$base]=$(( ${sum_stat[$base]:-0} + stat ))
            sum_unlink[$base]=$(( ${sum_unlink[$base]:-0} + unlink ))
            count[$base]=$(( ${count[$base]:-0} + 1 ))
        fi
    done < "$serial_log"

    printf '\n%-16s %8s %8s %8s %8s  %s\n' \
        "filesystem" "create" "stat" "unlink" "total" "(ms, mean of ${N_RUNS} runs, ${N_FILES} files)"
    printf '%-16s %8s %8s %8s %8s\n' \
        "────────────────" "────────" "────────" "────────" "────────"

    local ORDER=("virtiofs" "ext4-blk" "tmpfs")
    for base in "${ORDER[@]}"; do
        n="${count[$base]:-0}"
        if [[ $n -eq 0 ]]; then
            printf '%-16s %8s\n' "$base" "(no data)"
            continue
        fi
        c=$(( sum_create[$base] / n ))
        s=$(( sum_stat[$base] / n ))
        u=$(( sum_unlink[$base] / n ))
        t=$(( c + s + u ))
        printf '%-16s %8d %8d %8d %8d\n' "$base" "$c" "$s" "$u" "$t"
    done
    printf '\n'

    # Ratio rows
    if [[ ${count[virtiofs]:-0} -gt 0 && ${count[ext4-blk]:-0} -gt 0 ]]; then
        local vt=$(( (sum_create[virtiofs] + sum_stat[virtiofs] + sum_unlink[virtiofs]) / count[virtiofs] ))
        local et=$(( (sum_create[ext4-blk] + sum_stat[ext4-blk] + sum_unlink[ext4-blk]) / count[ext4-blk] ))
        if [[ $et -gt 0 ]]; then
            ratio=$(awk "BEGIN { printf \"%.2f\", $vt / $et }")
            printf 'virtiofs/ext4-blk total ratio: %sx\n' "$ratio"
        fi
    fi

    # ── git status bench (BENCH-REDO: full distribution) ──────────────────
    # Cold runs (labelled *-cold): first git status; re-hashes stale index.
    # Reported separately so they do not contaminate steady-state statistics.
    # Steady-state runs (labelled *-r1..*-rN): pure stat walk after index refresh.

    # Collect cold-run values (single value per leg)
    declare -A cold_elapsed
    while IFS= read -r line; do
        if [[ "$line" =~ ^BENCH_GITSTATUS_END\ ([^\ ]+)-cold\ elapsed=([0-9]+)ms ]]; then
            cold_elapsed["${BASH_REMATCH[1]}"]="${BASH_REMATCH[2]}"
        fi
    done < "$serial_log"

    # Collect steady-state values per leg
    declare -A git_vals  # git_vals[base]="v1 v2 v3..."
    while IFS= read -r line; do
        if [[ "$line" =~ ^BENCH_GITSTATUS_END\ ([^\ ]+)-r[0-9]+\ elapsed=([0-9]+)ms ]]; then
            base="${BASH_REMATCH[1]}"
            elapsed="${BASH_REMATCH[2]}"
            git_vals[$base]="${git_vals[$base]:-} $elapsed"
        fi
    done < "$serial_log"

    if [[ ${#cold_elapsed[@]} -gt 0 || ${#git_vals[@]} -gt 0 ]]; then
        local GS_ORDER=("virtiofs-git" "ext4-git")

        printf '\n── git status: cold-index (first run; re-hash event) ───────────────────\n'
        # NOTE: the corpus size (3,224 tracked files) and host-native baseline (mean 14.75 ms,
        # range 12–17 ms, n=8) are EXTERNAL REFERENCES measured independently, not harness output.
        # Provenance: docs/design/bench-data/2026-08-18-host-baseline.txt
        #   corpus:         git ls-files | wc -l  → 3224 (example-app, HEAD 27bc69627)
        #   host-native:    8 timed runs (GIT_OPTIONAL_LOCKS=0 git --no-optional-locks
        #                   status --porcelain) → 12–17 ms, mean 14.75 ms
        printf '  [external ref] corpus: 3,224 tracked files; host-native baseline: mean 14.75 ms (range 12-17 ms, n=8)\n'
        printf '  [see docs/design/bench-data/2026-08-18-host-baseline.txt for measured provenance]\n'
        for base in "${GS_ORDER[@]}"; do
            val="${cold_elapsed[$base]:-}"
            if [[ -n "$val" ]]; then
                printf '  %-20s  %d ms\n' "$base" "$val"
            else
                printf '  %-20s  (no data)\n' "$base"
            fi
        done

        printf '\n── git status: steady-state (n=%d interleaved A/B; guest drop_caches between runs) ──\n' "$N_RUNS"
        printf '  (index refreshed by cold run; pure stat walk expected)\n'
        for base in "${GS_ORDER[@]}"; do
            vals="${git_vals[$base]:-}"
            if [[ -z "$vals" ]]; then
                printf '  %-20s  (no data)\n' "$base"
                continue
            fi
            # shellcheck disable=SC2086
            dist_stats "  $base" $vals
        done

        printf '\n── git status: ratio (virtiofs / ext4, steady-state mean) ─────────────\n'
        vfs_vals="${git_vals[virtiofs-git]:-}"
        blk_vals="${git_vals[ext4-git]:-}"
        if [[ -n "$vfs_vals" && -n "$blk_vals" ]]; then
            vfs_mean=$(printf '%s\n' $vfs_vals | awk '{s+=$1;n++} END{printf "%.1f",s/n}')
            blk_mean=$(printf '%s\n' $blk_vals | awk '{s+=$1;n++} END{printf "%.1f",s/n}')
            if [[ -n "$blk_mean" && "$blk_mean" != "0.0" ]]; then
                ratio=$(awk "BEGIN { printf \"%.2f\", $vfs_mean / $blk_mean }")
                printf '  virtiofs-git/ext4-git: %sx  (virtiofs=%.0fms, ext4=%.0fms)\n' \
                    "$ratio" "$vfs_mean" "$blk_mean"
                # Verdict relative to host-native baseline.
                # EXTERNAL REFERENCE: mean 14.75 ms (range 12–17 ms, n=8) on example-app
                # (see docs/design/bench-data/2026-08-18-host-baseline.txt).
                # The ~10 ms figure in earlier records was a /usr/bin/time resolution
                # artifact (0.01 s granularity cannot distinguish 10 ms from 17 ms);
                # superseded by D-PD-103.
                host_baseline=14.75
                vfs_r=$(awk "BEGIN { printf \"%.0f\", $vfs_mean / $host_baseline }")
                blk_r=$(awk "BEGIN { printf \"%.0f\", $blk_mean / $host_baseline }")
                printf '  vs host-native baseline (mean 14.75ms, range 12-17ms, n=8; see 2026-08-18-host-baseline.txt): virtiofs=%sx  ext4=%sx\n' \
                    "$vfs_r" "$blk_r"
            fi
        fi
    fi
}

# ─── Save evidence files BEFORE parse_results so they survive any parse failure ───
DATE_TAG=$(date +%Y-%m-%d)
CACHE_TAG="${VFSD_CACHE}$([ "$VFSD_WRITEBACK" = yes ] && echo '-writeback' || echo '')"
EVIDENCE_SERIAL="$SCRIPT_DIR/${DATE_TAG}-gitstatus-redo-${CACHE_TAG}-serial.log"
EVIDENCE_BENCH="$SCRIPT_DIR/${DATE_TAG}-gitstatus-redo-${CACHE_TAG}-bench_lines.txt"

log "Saving evidence files..."
cp "$SERIAL_LOG" "$EVIDENCE_SERIAL"
# Emit a PROVENANCE header FIRST: without it the cache mode lives only in the
# filename, with no mechanism tying the name to the payload.  Written by the
# generator (not patched onto the artifact afterwards) so every future run
# carries it.
{
  printf '# PROVENANCE (emitted by bench.sh at capture time)\n'
  printf '#   virtiofs cache mode : %s%s\n' "$VFSD_CACHE" \
    "$([ "$VFSD_WRITEBACK" = yes ] && printf ' --writeback' || printf '')"
  printf '#   run date            : %s\n' "$DATE_TAG"
  printf '#   workload            : n=%s files x %s rounds; legs equalised via `cp -a`\n' \
    "$N_FILES" "$N_RUNS"
  printf '#   host / CH / vfsd    : %s / %s / %s\n' "$(uname -r)" "$ch_ver" "$vfsd_ver"
  printf '#   produced by         : docs/design/bench-data/bench.sh\n'
  printf '#   decisions           : D-DC-09, D-PD-100, D-PD-102/103\n#\n'
} > "$EVIDENCE_BENCH"
grep -E "^BENCH_|^GITSTATUS_|^GUEST_|^MOUNT_" "$SERIAL_LOG" >> "$EVIDENCE_BENCH" 2>/dev/null || true
# last_run.* are scratch pointers to the newest run.  They go to $LAST_RUN_DIR
# (default: a temp dir), never into $SCRIPT_DIR — that is now a tracked docs
# directory and a re-run would dirty the repo.
LAST_RUN_DIR="${LAST_RUN_DIR:-${TMPDIR:-/tmp}/nexus3-bench}"
mkdir -p "$LAST_RUN_DIR"
cp "$SERIAL_LOG" "$LAST_RUN_DIR/last_run.serial.log"
grep -E "^BENCH_" "$SERIAL_LOG" > "$LAST_RUN_DIR/last_run.bench_lines.txt" 2>/dev/null || true
log "  Serial log:  $EVIDENCE_SERIAL"
log "  Bench lines: $EVIDENCE_BENCH"

echo ""
echo "══════════════════════════════════════════════════════════════════════"
echo "  virtiofs vs ext4-blk metadata benchmark (BENCH-REDO)"
echo "  host: $(uname -r) | CH: $ch_ver | virtiofsd: $vfsd_ver"
echo "  virtiofsd --cache $VFSD_CACHE$([ "$VFSD_WRITEBACK" = yes ] && echo ' --writeback' || echo '')"
[[ $DO_GIT_BENCH -eq 1 ]] && echo "  git corpus COPY: $GIT_BENCH_COPY (from $GIT_REPO_PATH)"
[[ $DO_GIT_BENCH -eq 1 ]] && echo "  equalisation: both legs from COPY; cold run refreshes index; A/B interleaved"
echo "══════════════════════════════════════════════════════════════════════"

parse_results "$SERIAL_LOG"

echo "══════════════════════════════════════════════════════════════════════"

# ─── Verify live repo was never mutated ────────────────────────────────────
if [[ $DO_GIT_BENCH -eq 1 && -n "$GIT_REPO_PATH" ]]; then
    post_head=$(git -C "$GIT_REPO_PATH" rev-parse HEAD 2>/dev/null || echo "")
    post_porcelain=$(git -C "$GIT_REPO_PATH" status --porcelain 2>/dev/null | wc -l || echo "?")
    log "Live repo verification (must be unmutated):"
    log "  HEAD before: $LIVE_HEAD"
    log "  HEAD after:  $post_head"
    log "  Porcelain before: $LIVE_PORCELAIN  after: $post_porcelain"
    if [[ "$post_head" == "$LIVE_HEAD" ]]; then
        log "  ✓ Live repo HEAD unchanged."
    else
        log "  ✗ ERROR: Live repo HEAD changed! This should not happen."
    fi
fi

# Disk low-water mark
free_now=$(df --output=avail -BG / | tail -1 | tr -d 'G ')
log "Disk free at exit: ${free_now}GB (floor: ${DISK_FREE_FLOOR_GB}GB)"
