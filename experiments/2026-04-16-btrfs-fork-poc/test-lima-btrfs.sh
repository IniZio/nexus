#!/usr/bin/env bash
# test-lima-btrfs.sh — Validate btrfs subvolume snapshot in the nexus Lima guest.
# Run from the macOS host. Requires: limactl, nexus Lima instance running.

set -euo pipefail

LIMA_INSTANCE="${LIMA_INSTANCE:-nexus}"

run() {
    limactl shell "$LIMA_INSTANCE" -- bash -c "$1"
}

echo "==> OS / kernel"
run 'lsb_release -ds && uname -r'

echo ""
echo "==> btrfs kernel config"
run 'cat /boot/config-$(uname -r) | grep BTRFS'

echo ""
echo "==> btrfs-progs version"
run 'which btrfs && btrfs --version'

echo ""
echo "==> Creating test btrfs volume"
run 'sudo truncate -s 2G /tmp/btrfs-test.img && sudo mkfs.btrfs -f /tmp/btrfs-test.img'
run 'sudo mkdir -p /mnt/btrfs-test && sudo mount -o loop /tmp/btrfs-test.img /mnt/btrfs-test'

echo ""
echo "==> CoW divergence test"
run '
  sudo btrfs subvolume create /mnt/btrfs-test/workspace-aaa
  echo "hello from parent" | sudo tee /mnt/btrfs-test/workspace-aaa/file.txt
  sudo btrfs subvolume snapshot /mnt/btrfs-test/workspace-aaa /mnt/btrfs-test/workspace-bbb
  echo "diverge parent" | sudo tee -a /mnt/btrfs-test/workspace-aaa/file.txt
  echo "diverge child"  | sudo tee    /mnt/btrfs-test/workspace-bbb/child.txt
  echo "=== parent ===" && cat /mnt/btrfs-test/workspace-aaa/file.txt
  echo "=== child ===" && cat /mnt/btrfs-test/workspace-bbb/file.txt
  ls /mnt/btrfs-test/workspace-bbb/
'

echo ""
echo "==> Snapshot timing (100 MB subvolume)"
run '
  sudo btrfs subvolume create /mnt/btrfs-test/big
  sudo dd if=/dev/urandom of=/mnt/btrfs-test/big/data.bin bs=1M count=100 status=progress
  time sudo btrfs subvolume snapshot /mnt/btrfs-test/big /mnt/btrfs-test/big-fork
'

echo ""
echo "==> Cleanup"
run 'sudo umount /mnt/btrfs-test && sudo rm -f /tmp/btrfs-test.img'

echo ""
echo "DONE"
