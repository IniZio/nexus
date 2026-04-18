#!/usr/bin/env bash
# Usage: download-firecracker.sh <arch> <output-file>
# arch: amd64 or arm64
set -euo pipefail

ARCH="${1:?usage: download-firecracker.sh <arch> <output-file>}"
OUT="${2:?usage: download-firecracker.sh <arch> <output-file>}"
VERSION="v1.13.0"

# Change to the directory containing this script's parent (cmd/daemon)
cd "$(dirname "$0")/.."

case "$ARCH" in
  amd64)
    TARBALL="firecracker-${VERSION}-x86_64.tgz"
    URL="https://github.com/firecracker-microvm/firecracker/releases/download/${VERSION}/${TARBALL}"
    BINARY_IN_TAR="release-${VERSION}-x86_64/firecracker-${VERSION}-x86_64"
    ;;
  arm64)
    TARBALL="firecracker-${VERSION}-aarch64.tgz"
    URL="https://github.com/firecracker-microvm/firecracker/releases/download/${VERSION}/${TARBALL}"
    BINARY_IN_TAR="release-${VERSION}-aarch64/firecracker-${VERSION}-aarch64"
    ;;
  *)
    echo "Unknown arch: $ARCH" >&2
    exit 1
    ;;
esac

if [ -f "$OUT" ]; then
  echo "==> $OUT already exists, skipping download."
  exit 0
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

echo "==> Downloading firecracker ${VERSION} for ${ARCH}..."
curl -fsSL -o "$TMP/$TARBALL" "$URL"
tar -C "$TMP" -xzf "$TMP/$TARBALL" "$BINARY_IN_TAR"
cp "$TMP/$BINARY_IN_TAR" "$OUT"
chmod 755 "$OUT"
echo "==> Saved to $OUT"
