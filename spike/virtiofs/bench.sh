#!/usr/bin/env bash
# bench.sh — virtiofs vs ext4 virtio-blk metadata benchmark for nexus3
#
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
# All three run in the same VM boot to eliminate boot-time noise.
# N_RUNS boots give variance data. N_FILES files per create/stat/unlink phase.
#
# Usage:
#   bash spike/virtiofs/bench.sh [--runs N] [--files N] [--cache POLICY]
#   VIRTIOFSD=/path/to/virtiofsd bash spike/virtiofs/bench.sh
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
N_RUNS="${N_RUNS:-3}"
VM_MEM_MIB="${VM_MEM_MIB:-2048}"
VM_VCPUS="${VM_VCPUS:-2}"
TIMEOUT_SEC="${TIMEOUT_SEC:-180}"
VFSD_CACHE="${VFSD_CACHE:-auto}"   # auto | always | never | metadata
VFSD_WRITEBACK="${VFSD_WRITEBACK:-no}"  # yes | no (only meaningful with --cache always)
EXT4_SIZE_MB="${EXT4_SIZE_MB:-256}"

# Parse flags
while [[ $# -gt 0 ]]; do
    case "$1" in
        --runs)   N_RUNS="$2";   shift 2 ;;
        --files)  N_FILES="$2";  shift 2 ;;
        --cache)  VFSD_CACHE="$2"; shift 2 ;;
        --writeback) VFSD_WRITEBACK="yes"; shift ;;
        *) echo "unknown arg: $1"; exit 1 ;;
    esac
done

# ─── Working directory ─────────────────────────────────────────────────────
WORK="$(mktemp -d /tmp/vfs-bench.XXXXXX)"
VIRTIOFSD_PID=0
CH_PID=0

cleanup() {
    [[ $VIRTIOFSD_PID -ne 0 ]] && kill "$VIRTIOFSD_PID" 2>/dev/null || true
    [[ $CH_PID -ne 0 ]]        && kill "$CH_PID"        2>/dev/null || true
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

# ─── 2. Compile guest bench binary (static, linux/amd64) ──────────────────
log "Compiling guest bench binary..."
BENCH_SRC="$SCRIPT_DIR/guest_bench/main.go"
BENCH_BIN="$WORK/bench"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -o "$BENCH_BIN" "$BENCH_SRC" \
    || die "Failed to compile guest bench binary"
log "  guest bench binary: $(du -sh "$BENCH_BIN" | cut -f1)"

# ─── 3. Create ext4 data disk ─────────────────────────────────────────────
log "Creating ext4 benchmark disk (${EXT4_SIZE_MB}MB)..."
EXT4_DISK="$WORK/bench.raw"
dd if=/dev/zero of="$EXT4_DISK" bs=1M count="$EXT4_SIZE_MB" status=none
mke2fs -t ext4 -q -F "$EXT4_DISK"
log "  ext4 disk: $EXT4_DISK"

# ─── 4. Build custom initramfs ────────────────────────────────────────────
log "Building custom initramfs..."
ROOTFS="$WORK/rootfs"
mkdir -p "$ROOTFS"

# Extract base Alpine initramfs
(cd "$ROOTFS" && gunzip -c "$BASE_INITRAMFS" | cpio -id --quiet 2>/dev/null)

# Inject bench binary
install -D -m 0755 "$BENCH_BIN" "$ROOTFS/usr/local/bin/bench"

# Create mount points
mkdir -p "$ROOTFS/mnt/virtiofs" "$ROOTFS/mnt/ext4" "$ROOTFS/mnt/tmpfs" "$ROOTFS/mnt/data"

# Write custom /init that runs the benchmark
cat > "$ROOTFS/init" << 'INITEOF'
#!/bin/sh
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true

echo "GUEST_INIT_START"

N_FILES="$(cat /proc/cmdline | tr ' ' '\n' | grep '^bench.n=' | cut -d= -f2)"
N_FILES="${N_FILES:-1000}"
N_RUNS="$(cat /proc/cmdline | tr ' ' '\n' | grep '^bench.runs=' | cut -d= -f2)"
N_RUNS="${N_RUNS:-3}"

echo "GUEST_PARAMS n=$N_FILES runs=$N_RUNS"

# ── virtiofs ──────────────────────────────────────────────────────────
if mount -t virtiofs workspace /mnt/virtiofs 2>/dev/null; then
    echo "MOUNT_OK virtiofs"
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

# ── ext4 virtio-blk ───────────────────────────────────────────────────
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

log "Starting virtiofsd (cache=$VFSD_CACHE writeback=$VFSD_WRITEBACK)..."
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

# ─── 6. Build VM configuration JSON ───────────────────────────────────────
MEM_BYTES=$(( VM_MEM_MIB * 1024 * 1024 ))
SERIAL_LOG="$WORK/serial.log"
API_SOCK="$WORK/ch-api.sock"

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
    {
      "tag": "workspace",
      "socket": "$VFSD_SOCK",
      "num_queues": 1,
      "queue_size": 1024
    }
  ],
  "disks": [
    {
      "path": "$EXT4_DISK",
      "image_type": "Raw"
    }
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

parse_results() {
    local serial_log="$1"

    # Extract all BENCH_END lines; format:
    #   BENCH_END <label> create=<ms>ms stat=<ms>ms unlink=<ms>ms total=<ms>ms

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
        local vc=$(( sum_create[virtiofs] / count[virtiofs] ))
        local ec=$(( sum_create[ext4-blk] / count[ext4-blk] ))
        local vt=$(( (sum_create[virtiofs] + sum_stat[virtiofs] + sum_unlink[virtiofs]) / count[virtiofs] ))
        local et=$(( (sum_create[ext4-blk] + sum_stat[ext4-blk] + sum_unlink[ext4-blk]) / count[ext4-blk] ))
        if [[ $ec -gt 0 ]]; then
            # Use awk for floating-point ratio
            ratio=$(awk "BEGIN { printf \"%.2f\", $vt / $et }")
            printf 'virtiofs/ext4-blk total ratio: %sx\n' "$ratio"
        fi
    fi
}

echo ""
echo "══════════════════════════════════════════════════════════════════════"
echo "  virtiofs vs ext4-blk metadata benchmark"
echo "  host: $(uname -r) | CH: $ch_ver | virtiofsd: $vfsd_ver"
echo "  virtiofsd --cache $VFSD_CACHE$([ "$VFSD_WRITEBACK" = yes ] && echo ' --writeback' || echo '')"
echo "══════════════════════════════════════════════════════════════════════"

parse_results "$SERIAL_LOG"

echo "══════════════════════════════════════════════════════════════════════"
echo ""
log "Full serial log saved to: $WORK/serial.log"
log "Saving serial log copy to: $SCRIPT_DIR/last_run.serial.log"
cp "$SERIAL_LOG" "$SCRIPT_DIR/last_run.serial.log"

# Also save the raw BENCH lines for later analysis
grep "^BENCH_" "$SERIAL_LOG" > "$SCRIPT_DIR/last_run.bench_lines.txt" 2>/dev/null || true
log "Raw bench lines saved to: $SCRIPT_DIR/last_run.bench_lines.txt"
