#!/usr/bin/env bash
# fetch-boot-artifacts.sh — fetch and verify boot artifacts for the
# Cloud Hypervisor integration test.
#
# Usage: bash scripts/fetch-boot-artifacts.sh
#   Run from anywhere; the script resolves the repo root via its own path.
#
# Idempotent: files that already exist and pass their SHA-256 check are
# left untouched (not re-downloaded).
#
# What this fetches
# -----------------
# vmlinux-x86_64   Primary kernel for the integration test. ELF vmlinux
#                  (PVH entry point) from cloud-hypervisor/linux releases.
#                  Empirically verified with CH v52.0 on 2026-08-05.
#
# bzImage-x86_64   Alternate kernel in compressed bzImage format.
#                  Also verified with CH v52.0 (both formats accepted).
#
# alpine-initramfs.cpio.gz
#                  Minimal initramfs used by TestBootToUserspace to verify
#                  that the VM reaches real userspace (/init). Built from
#                  Alpine Linux 3.20.0 minirootfs with a custom /init that
#                  mounts devtmpfs/proc and loops forever, printing a marker
#                  line on the serial console for the test to assert on.
#
# All artifacts are stored in:
#   internal/core/driver/cloudhypervisor/testdata/
# and are gitignored — do not commit them.

set -euo pipefail

# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------

# Kernel release tag in cloud-hypervisor/linux
KERNEL_RELEASE="ch-release-v6.16.9-20260508"
KERNEL_BASE_URL="https://github.com/cloud-hypervisor/linux/releases/download/${KERNEL_RELEASE}"

VMLINUX_URL="${KERNEL_BASE_URL}/vmlinux-x86_64"
VMLINUX_SHA256="9d3570b47d5abb069ca00edfbfcef4c68306a9c3d078a01f10082b258f1001b8"

BZIMAGE_URL="${KERNEL_BASE_URL}/bzImage-x86_64"
BZIMAGE_SHA256="58088758f601a04ef85b09cf23db5530d51edc039ed47afbf2264c5b762cb568"

ALPINE_VERSION="3.20.0"
ALPINE_TARBALL="alpine-minirootfs-${ALPINE_VERSION}-x86_64.tar.gz"
ALPINE_URL="https://dl-cdn.alpinelinux.org/alpine/v3.20/releases/x86_64/${ALPINE_TARBALL}"
ALPINE_SHA256="602efda518516787c716320bd46a3f50e83a74bb749e55483c2f4a9c9f8b9a38"

# ---------------------------------------------------------------------------
# Resolve directories
# ---------------------------------------------------------------------------

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
TESTDATA_DIR="${REPO_ROOT}/internal/core/driver/cloudhypervisor/testdata"

mkdir -p "${TESTDATA_DIR}"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

need_cmd() {
    local cmd="$1"
    if ! command -v "$cmd" &>/dev/null; then
        echo "ERROR: required command not found: $cmd" >&2
        exit 1
    fi
}

# download_and_verify <url> <dest_path> <expected_sha256> <label>
download_and_verify() {
    local url="$1" dest="$2" expected_sha="$3" label="$4"

    if [[ -f "$dest" ]]; then
        local actual
        actual="$(sha256sum "$dest" | awk '{print $1}')"
        if [[ "$actual" == "$expected_sha" ]]; then
            echo "[OK]  $label already present and checksum matches — skipping download"
            return 0
        else
            echo "[WARN] $label exists but checksum mismatch; re-downloading"
            echo "       expected: $expected_sha"
            echo "       actual:   $actual"
            rm -f "$dest"
        fi
    fi

    echo "[DL]  Downloading $label ..."
    if command -v wget &>/dev/null; then
        wget -q --show-progress -O "$dest" "$url" 2>&1 || { rm -f "$dest"; echo "ERROR: wget failed for $url" >&2; exit 1; }
    elif command -v curl &>/dev/null; then
        curl -fL --progress-bar -o "$dest" "$url" || { rm -f "$dest"; echo "ERROR: curl failed for $url" >&2; exit 1; }
    else
        echo "ERROR: neither wget nor curl is available" >&2
        exit 1
    fi

    echo "[CHK] Verifying SHA-256 for $label ..."
    local actual
    actual="$(sha256sum "$dest" | awk '{print $1}')"
    if [[ "$actual" != "$expected_sha" ]]; then
        echo "ERROR: SHA-256 mismatch for $label" >&2
        echo "  expected: $expected_sha" >&2
        echo "  actual:   $actual" >&2
        rm -f "$dest"
        exit 1
    fi
    echo "[OK]  $label checksum verified"
}

# ---------------------------------------------------------------------------
# Pre-flight checks
# ---------------------------------------------------------------------------

need_cmd sha256sum

# ---------------------------------------------------------------------------
# 1. vmlinux-x86_64 (primary test kernel)
# ---------------------------------------------------------------------------

download_and_verify \
    "$VMLINUX_URL" \
    "${TESTDATA_DIR}/vmlinux-x86_64" \
    "$VMLINUX_SHA256" \
    "vmlinux-x86_64 (kernel ${KERNEL_RELEASE})"

# ---------------------------------------------------------------------------
# 2. bzImage-x86_64 (alternate format, documented as also working with v52)
# ---------------------------------------------------------------------------

download_and_verify \
    "$BZIMAGE_URL" \
    "${TESTDATA_DIR}/bzImage-x86_64" \
    "$BZIMAGE_SHA256" \
    "bzImage-x86_64 (kernel ${KERNEL_RELEASE})"

# ---------------------------------------------------------------------------
# 3. Alpine initramfs (used by TestBootToUserspace)
# ---------------------------------------------------------------------------

INITRAMFS_DEST="${TESTDATA_DIR}/alpine-initramfs.cpio.gz"

if [[ -f "$INITRAMFS_DEST" ]]; then
    echo "[OK]  alpine-initramfs.cpio.gz already present — skipping rebuild"
else
    need_cmd cpio
    need_cmd gzip

    WORK_DIR="$(mktemp -d)"
    trap 'rm -rf "$WORK_DIR"' EXIT

    # Download Alpine minirootfs
    download_and_verify \
        "$ALPINE_URL" \
        "${WORK_DIR}/${ALPINE_TARBALL}" \
        "$ALPINE_SHA256" \
        "alpine-minirootfs-${ALPINE_VERSION}-x86_64.tar.gz"

    echo "[BUILD] Extracting Alpine minirootfs ..."
    mkdir -p "${WORK_DIR}/rootfs"
    tar -xzf "${WORK_DIR}/${ALPINE_TARBALL}" -C "${WORK_DIR}/rootfs"

    # Inject an /init that configures the network and loops forever.
    #
    # Network: bring up the first HARDWARE-BACKED interface (has a sysfs
    # "device" symlink; skips virtual ifaces like dummy0) and run udhcpc.
    # The DHCP discovers carry the guest NIC MAC onto the TAP so
    # TestNetnsRuntime_KVMProof observes frames from the guest — the kernel
    # has no CONFIG_IP_PNP, so cmdline "ip=dhcp" does nothing; userspace must
    # drive DHCP. udhcpc failing (no reply) is fine: the discovers themselves
    # are the frames under test.
    #
    # WARNING: Do NOT use poweroff or reboot here — that drives the VM to
    # "Shutdown" state, which the driver maps to driver.Unknown, breaking the
    # pause/resume test. This init keeps the VM in "Running" indefinitely.
    cat > "${WORK_DIR}/rootfs/init" << 'INITEOF'
#!/bin/sh
mount -t devtmpfs devtmpfs /dev 2>/dev/null || true
mount -t proc proc /proc 2>/dev/null || true
mount -t sysfs sysfs /sys 2>/dev/null || true
IFACE=""
for d in /sys/class/net/*; do
    n=$(basename "$d")
    [ "$n" = "lo" ] && continue
    [ -e "$d/device" ] || continue
    IFACE="$n"
    break
done
if [ -n "$IFACE" ]; then
    ip link set "$IFACE" up 2>/dev/null || true
    udhcpc -i "$IFACE" -n -q -t 3 -T 2 2>/dev/null || true
fi
echo "nexus3-test-vm: init reached — sleeping forever"
while true; do
    sleep 60
done
INITEOF
    chmod +x "${WORK_DIR}/rootfs/init"

    echo "[BUILD] Creating cpio.gz ..."
    (cd "${WORK_DIR}/rootfs" && find . | cpio -o -H newc 2>/dev/null | gzip) \
        > "$INITRAMFS_DEST"

    echo "[OK]  alpine-initramfs.cpio.gz built"
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------

echo ""
echo "=== Boot artifacts ready ==="
echo ""
ls -lh \
    "${TESTDATA_DIR}/vmlinux-x86_64" \
    "${TESTDATA_DIR}/bzImage-x86_64" \
    "${TESTDATA_DIR}/alpine-initramfs.cpio.gz"
echo ""
echo "To run the integration test:"
echo "  go test -tags integration ./internal/core/driver/cloudhypervisor/... -v -count=1"
