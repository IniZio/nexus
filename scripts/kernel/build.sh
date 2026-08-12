#!/usr/bin/env bash
set -euo pipefail

# Build the nexus3 guest kernel (Linux 6.12.76, x86_64) from upstream source.
#
# Produces a vmlinux ELF with:
#   - libkrunfw microVM base config (PVH boot, virtio-MMIO)
#   - Cloud Hypervisor compat layer (virtio-PCI, ACPI, 8250 serial — built-in)
#   - Docker/netfilter networking (bridge, nf_tables, iptables, NAT, IPVS)
#   - Nested-virtualisation support (CONFIG_KVM + CONFIG_KVM_INTEL/AMD for /dev/kvm passthrough)
#
# Reproducibility: once scripts/kernel/config-6.12.76 is committed, subsequent
# builds SKIP the libkrunfw fetch and use the committed config directly.
# Only the first bootstrap run fetches libkrunfw.
#
# Reproducibility caveat: this build is CONFIG-reproducible (pinned source tarball +
# committed .config), but NOT byte-for-byte reproducible. The kernel ELF embeds a build
# timestamp and BuildID, so a fresh build produces a DIFFERENT sha256 than the shipped
# artifact. Achieving byte-identical output additionally requires a pinned toolchain and
# SOURCE_DATE_EPOCH / KBUILD_BUILD_TIMESTAMP (known future work — NOT set here).
#
# Upstream tarball:
#   URL:    https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-6.12.76.tar.xz
#   sha256: (see images/kernel/PINNED.md)
#
# libkrunfw base config (one-time bootstrap, result committed as config-6.12.76):
#   URL: https://raw.githubusercontent.com/smol-machines/libkrunfw/main/config-libkrunfw_x86_64
#
# Requires: build-essential libncurses-dev bison flex libssl-dev bc libelf-dev binutils
#
# Usage:
#   build.sh [output-vmlinux-path]
#
# Examples:
#   build.sh                                    # → images/kernel/vmlinux-x86_64
#   build.sh /custom/path/vmlinux-x86_64
#   BUILD_DIR=/fast/disk/build build.sh

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# ── tunables ──────────────────────────────────────────────────────────────────

KERNEL_VERSION="6.12.76"
KERNEL_TARBALL="linux-${KERNEL_VERSION}.tar.xz"
KERNEL_URL="https://cdn.kernel.org/pub/linux/kernel/v6.x/${KERNEL_TARBALL}"
KERNEL_SHA256="bbb43e834c46e6bd49a5c28f22e679a937443404e1f653204d4b24929f3ad896"

LIBKRUNFW_CONFIG_URL="https://raw.githubusercontent.com/smol-machines/libkrunfw/main/config-libkrunfw_x86_64"

# Committed .config — used for reproducible rebuilds (skips libkrunfw fetch)
COMMITTED_CONFIG="${SCRIPT_DIR}/config-6.12.76"

# Build directory (scratch — NOT inside the repo)
BUILD_DIR="${BUILD_DIR:-/tmp/nexus3-kernel-build}/x86_64"

JOBS="$(nproc 2>/dev/null || echo 4)"

# Output destination
OUTPUT_PATH="${1:-${REPO_ROOT}/images/kernel/vmlinux-x86_64}"
# Resolve to absolute path before we cd into build dir
case "$OUTPUT_PATH" in
  /*) : ;;
  *) OUTPUT_PATH="$(pwd)/$OUTPUT_PATH" ;;
esac

echo "=== nexus3 guest kernel build ==="
echo "Version:     ${KERNEL_VERSION}"
echo "Output:      ${OUTPUT_PATH}"
echo "Build dir:   ${BUILD_DIR}"
echo "Jobs:        ${JOBS}"
echo ""

mkdir -p "${BUILD_DIR}"
cd "${BUILD_DIR}"

# ── Step 1: Download kernel source ───────────────────────────────────────────
if [[ ! -f "${KERNEL_TARBALL}" ]]; then
    echo "[1/6] Downloading kernel source..."
    curl -fsSL --retry 3 --connect-timeout 30 --max-time 600 \
      "${KERNEL_URL}" -o "${KERNEL_TARBALL}"
    echo "      Verifying sha256..."
    echo "${KERNEL_SHA256}  ${KERNEL_TARBALL}" | sha256sum --check --status
    echo "      sha256 OK"
else
    echo "[1/6] Tarball already present: ${KERNEL_TARBALL}"
    echo "      Verifying sha256..."
    echo "${KERNEL_SHA256}  ${KERNEL_TARBALL}" | sha256sum --check --status
    echo "      sha256 OK"
fi

# ── Step 2: Extract ───────────────────────────────────────────────────────────
if [[ ! -d "linux-${KERNEL_VERSION}" ]]; then
    echo "[2/6] Extracting kernel source..."
    tar -xf "${KERNEL_TARBALL}"
else
    echo "[2/6] Source tree already extracted."
fi

cd "linux-${KERNEL_VERSION}"

# ── Step 3: Build .config ─────────────────────────────────────────────────────
if [[ -f "${COMMITTED_CONFIG}" ]]; then
    # Reproducible path: use committed config (no network fetch needed)
    echo "[3/6] Using committed config: ${COMMITTED_CONFIG}"
    cp "${COMMITTED_CONFIG}" .config
else
    # Bootstrap path: fetch libkrunfw base + apply overlays
    echo "[3/6] Fetching libkrunfw base config (bootstrap — run once, then commit config-6.12.76)..."
    curl -fsSL --retry 3 --connect-timeout 30 --max-time 60 \
      "${LIBKRUNFW_CONFIG_URL}" -o .config

    echo "      Applying Cloud Hypervisor + Docker networking overlay..."
    cat >> .config <<'OVERLAY'

# ── initramfs support (optional but future-proofs initramfs boot) ──────────
CONFIG_BLK_DEV_INITRD=y

# ── Cloud Hypervisor as the HOST VMM (virtio-PCI + ACPI + 8250 serial) ──
# libkrunfw's base config is virtio-MMIO only. Cloud Hypervisor exposes ALL
# devices over virtio-PCI on an ACPI-enumerated PCI host bridge (no MMIO
# transport), so a microVM kernel boots under CH but sees no console/disk/vsock
# and hangs. These options are ADDITIVE and safe for libkrun (which presents no
# PCI host bridge, so the subsystem enumerates nothing and the MMIO devices are
# used instead) — the same single kernel boots under BOTH VMMs. virtio-blk,
# virtio-console, and vsock are =y so root mounts and the agent's vsock control
# channel work without loading modules from a not-yet-mounted rootfs.
CONFIG_PCI=y
CONFIG_PCI_MMCONFIG=y
CONFIG_PCI_MSI=y
CONFIG_PCIEPORTBUS=y
CONFIG_X86_X2APIC=y
CONFIG_ACPI=y
CONFIG_PVH=y
CONFIG_VIRTIO_PCI=y
CONFIG_VIRTIO_PCI_LEGACY=y
CONFIG_VIRTIO_BLK=y
CONFIG_VIRTIO_NET=y
CONFIG_VIRTIO_CONSOLE=y
CONFIG_HVC_DRIVER=y
CONFIG_SERIAL_8250=y
CONFIG_SERIAL_8250_CONSOLE=y
CONFIG_SERIAL_8250_PCI=y
CONFIG_SERIAL_CORE=y
CONFIG_SERIAL_CORE_CONSOLE=y
CONFIG_SERIAL_EARLYCON=y
CONFIG_VSOCKETS=y
CONFIG_VIRTIO_VSOCKETS=y

# ── Docker / bridge networking ────────────────────────────────────────────
CONFIG_BRIDGE=y
CONFIG_BRIDGE_NETFILTER=y

# Netfilter core
CONFIG_NETFILTER=y
CONFIG_NETFILTER_ADVANCED=y
CONFIG_NF_CONNTRACK=y
CONFIG_NF_CONNTRACK_EVENTS=y
CONFIG_NF_NAT=y
CONFIG_NF_NAT_MASQUERADE=y

# IPv4 netfilter
CONFIG_IP_NF_IPTABLES=y
CONFIG_IP_NF_FILTER=y
CONFIG_IP_NF_NAT=y
CONFIG_IP_NF_RAW=y
CONFIG_IP_NF_TARGET_MASQUERADE=y
CONFIG_IP_NF_TARGET_REJECT=y

# Matches/targets Docker uses
CONFIG_NETFILTER_XT_TABLES=y
CONFIG_NETFILTER_XT_MATCH_ADDRTYPE=y
CONFIG_NETFILTER_XT_MATCH_CONNTRACK=y
CONFIG_NETFILTER_XT_MATCH_IPVS=y

# Additional xtables matches/targets for Podman/netavark bridge networking
# (xt_comment is mandatory — netavark labels every iptables rule with it)
CONFIG_NETFILTER_XT_MATCH_COMMENT=y
CONFIG_NETFILTER_XT_MATCH_MARK=y
CONFIG_NETFILTER_XT_MATCH_STATE=y
CONFIG_NETFILTER_XT_MATCH_MULTIPORT=y
CONFIG_NETFILTER_XT_TARGET_REDIRECT=y
CONFIG_NETFILTER_XT_TARGET_LOG=y

# Native nftables expressions — REQUIRED by podman/netavark's default firewall
# driver. netavark (>= 1.14) builds an `inet netavark` table whose hostport and
# local-address handling uses a `fib daddr type local` match. The libkrunfw base
# config enables NF_TABLES + NAT/masq/conntrack expressions but NOT the FIB
# expression, so netavark's single atomic ruleset apply aborts with
# 'nft: Could not process rule: No such file or directory' (ENOENT) and EVERY
# bridge-network container create fails. Only `--network=host` works without
# these. Enabling the FIB family (plus re-asserting the rest for clarity) makes
# the default podman bridge and multi-service compose work out of the box.
CONFIG_NF_TABLES=y
CONFIG_NF_TABLES_INET=y
CONFIG_NF_TABLES_IPV4=y
CONFIG_NF_TABLES_IPV6=y
CONFIG_NFT_CT=y
CONFIG_NFT_NAT=y
CONFIG_NFT_MASQ=y
CONFIG_NFT_REJECT=y
CONFIG_NFT_REJECT_INET=y
CONFIG_NFT_COMPAT=y
CONFIG_NFT_FIB=y
CONFIG_NFT_FIB_IPV4=y
CONFIG_NFT_FIB_IPV6=y
CONFIG_NFT_FIB_INET=y

# IPVS (for kube-proxy / some Docker setups)
CONFIG_IP_VS=y
CONFIG_IP_VS_NFCT=y
CONFIG_IP_VS_PROTO_TCP=y
CONFIG_IP_VS_PROTO_UDP=y
CONFIG_IP_VS_RR=y

# Dummy device for Docker bridge testing
CONFIG_DUMMY=y

# Enable forwarding
CONFIG_IP_FORWARD=y

# XFS filesystem (required for nested daemon workdir reflink)
CONFIG_XFS_FS=y
CONFIG_XFS_QUOTA=y
CONFIG_XFS_POSIX_ACL=y
CONFIG_XFS_RT=y
CONFIG_XFS_ONLINE_SCRUB=y
CONFIG_XFS_ONLINE_REPAIR=y

# KVM — explicitly enable nested virtualization
CONFIG_KVM=y
CONFIG_KVM_INTEL=y
CONFIG_KVM_AMD=y
CONFIG_KVM_GUEST=y

# virtio-fs (used by libkrun for root filesystem sharing)
CONFIG_VIRTIO_FS=y

# ── Nested virtualisation: Cloud Hypervisor inside a guest workspace ──────
# nexus3 uses /dev/kvm passthrough (CpusConfig.nested) and Cloud Hypervisor's
# own userspace virtio-vsock/net — no host vhost devices are needed.
# vhost_vsock/vhost_net are intentionally NOT enabled: the committed
# config-6.12.76 has CONFIG_VHOST_MENU disabled, and CONFIG_MODULES=n means
# any =m setting would be silently dropped by olddefconfig regardless.
CONFIG_VIRTIO_MEM=y
CONFIG_VIRTIO_BALLOON=y

# ── Pressure Stall Information (PSI) ──────────────────────────────────────
CONFIG_PSI=y
# CONFIG_PSI_DEFAULT_DISABLED is not set

# ── Swap + zram ────────────────────────────────────────────────────────────
CONFIG_SWAP=y
CONFIG_ZRAM=y
CONFIG_ZSMALLOC=y
OVERLAY
fi

# ── Step 4: Normalize config (resolve deps, new options, set defaults) ────────
echo "[4/6] Running make olddefconfig..."
make olddefconfig "-j${JOBS}"

# Save normalized config for committed reproducibility
if [[ ! -f "${COMMITTED_CONFIG}" ]]; then
    echo "      Saving merged config to ${COMMITTED_CONFIG} ..."
    mkdir -p "$(dirname "${COMMITTED_CONFIG}")"
    cp .config "${COMMITTED_CONFIG}"
    echo "      IMPORTANT: commit scripts/kernel/config-6.12.76 so future rebuilds"
    echo "      are fully reproducible without fetching libkrunfw."
fi

# ── Step 5: Build kernel ──────────────────────────────────────────────────────
echo "[5/6] Building vmlinux (${JOBS} jobs)..."
BUILD_START="$(date +%s)"
make vmlinux "-j${JOBS}"
BUILD_END="$(date +%s)"
BUILD_SECS=$(( BUILD_END - BUILD_START ))

# ── Step 6: Install + strip ───────────────────────────────────────────────────
echo "[6/6] Installing to ${OUTPUT_PATH}..."
mkdir -p "$(dirname "${OUTPUT_PATH}")"
cp vmlinux "${OUTPUT_PATH}"
echo "      Stripping debug symbols..."
strip --strip-debug "${OUTPUT_PATH}"

echo ""
echo "=== Build complete ==="
echo "Output:   ${OUTPUT_PATH}"
echo "Duration: ${BUILD_SECS}s"
echo "Size:     $(du -h "${OUTPUT_PATH}" | cut -f1)"
echo "SHA-256:  $(sha256sum "${OUTPUT_PATH}" | cut -d' ' -f1)"
readelf -h "${OUTPUT_PATH}" 2>/dev/null | grep -E 'Magic|Class|Entry|Machine' || true
