#!/usr/bin/env bash
# test-firecracker-btrfs.sh — Attempt to validate btrfs support in the Firecracker kernel.
#
# The Firecracker kernel used by nexus is the Firecracker CI upstream kernel
# (vmlinux-5.10.239 from s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.13/).
# This script checks the kernel binary for btrfs support and reports the result.
#
# Run from the macOS host (requires: limactl, nexus Lima instance).

set -euo pipefail

LIMA_INSTANCE="${LIMA_INSTANCE:-nexus}"
KERNEL_URL="https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.13/x86_64/vmlinux-5.10.239"
KERNEL_CACHE="/tmp/nexus-fc-vmlinux.bin"

run() {
    limactl shell "$LIMA_INSTANCE" -- bash -c "$1"
}

echo "==> Checking for Firecracker kernel at $KERNEL_CACHE"
run "test -f $KERNEL_CACHE || wget -q -O $KERNEL_CACHE '$KERNEL_URL'"
run "ls -lh $KERNEL_CACHE"

echo ""
echo "==> Scanning kernel binary for btrfs references"
BTRFS_REFS=$(run "strings $KERNEL_CACHE | grep -i btrfs | wc -l")
echo "btrfs string references in kernel: $BTRFS_REFS"

echo ""
echo "==> Attempting to extract embedded kernel config"
run 'python3 -c "
import gzip, sys
data = open(\"/tmp/nexus-fc-vmlinux.bin\", \"rb\").read()
marker = b\"IKCFG_ST\"
idx = data.find(marker)
if idx < 0:
    print(\"No IKCFG_ST marker found — embedded config not available\")
    sys.exit(0)
# Scan for gzip magic near the marker
for offset in range(idx, idx+256):
    if data[offset:offset+2] == b\"\\x1f\\x8b\":
        try:
            cfg = gzip.decompress(data[offset:])
            btrfs_lines = [l for l in cfg.decode(errors=\"replace\").splitlines() if \"BTRFS\" in l]
            if btrfs_lines:
                for l in btrfs_lines: print(l)
            else:
                print(\"CONFIG_BTRFS_FS not found in embedded config\")
            break
        except Exception:
            pass
else:
    print(\"Could not decompress embedded config\")
"'

echo ""
echo "==> Conclusion"
if [ "$BTRFS_REFS" = "0" ]; then
    echo "RESULT: btrfs NOT compiled into Firecracker kernel vmlinux-5.10.239"
    echo "ACTION: A custom kernel with CONFIG_BTRFS_FS=y must be built for Firecracker btrfs support."
else
    echo "RESULT: btrfs references found — further analysis needed"
fi
