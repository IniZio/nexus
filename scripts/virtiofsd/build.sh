#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PATCH="$SCRIPT_DIR/fakeowner.patch"

TAG=v1.13.3
TAG_SHA=13ee2e13024eaf40cdedb4bcb0a49d5695ade0b8
REPO_URL=https://gitlab.com/virtio-fs/virtiofsd.git
PATCH_SHA256=0c3f5cb8521f0586ed88e86f188ee74c573dd43cc2b9e35cf11483536228eb8e
CARGO=${CARGO:-${HOME}/.cargo/bin/cargo}
INSTALL_PATH=${HOME}/.local/bin/virtiofsd

NO_INSTALL=0
for arg in "$@"; do
  [ "$arg" = "--no-install" ] && NO_INSTALL=1
done

BUILD_DIR=$(mktemp -d)
trap 'rm -rf "$BUILD_DIR"' EXIT

echo "==> clone virtiofsd $TAG"
git clone --depth=1 --branch "$TAG" "$REPO_URL" "$BUILD_DIR/src"

actual=$(cd "$BUILD_DIR/src" && git rev-parse "$TAG" 2>/dev/null || git rev-parse HEAD)
if [ "$actual" != "$TAG_SHA" ]; then
  echo "ERROR: tag SHA mismatch: got $actual expected $TAG_SHA" >&2
  exit 1
fi
echo "    tag SHA verified: $TAG_SHA"

echo "==> verify patch sha256"
actual_patch=$(sha256sum "$PATCH" | awk '{print $1}')
if [ "$actual_patch" != "$PATCH_SHA256" ]; then
  echo "ERROR: patch SHA mismatch: got $actual_patch expected $PATCH_SHA256" >&2
  exit 1
fi
echo "    patch SHA verified"

echo "==> apply patch"
git -C "$BUILD_DIR/src" apply "$PATCH"

echo "==> build"
LIBDIR=$(dirname "$(ldconfig -p | grep 'libseccomp.so.2 ' | awk '{print $NF}')")
LIB_OVERRIDES="$BUILD_DIR/liblinks"
mkdir "$LIB_OVERRIDES"
ln -sf "$(ldconfig -p | grep 'libseccomp.so.2 ' | awk '{print $NF}')" "$LIB_OVERRIDES/libseccomp.so"
ln -sf "$(ldconfig -p | grep 'libcap-ng.so.0 ' | awk '{print $NF}')" "$LIB_OVERRIDES/libcap-ng.so"

LIBRARY_PATH="$LIB_OVERRIDES:$LIBDIR" "$CARGO" build --release --manifest-path "$BUILD_DIR/src/Cargo.toml"

OUT="$BUILD_DIR/src/target/release/virtiofsd"
echo "==> built: $OUT"

if [ "$NO_INSTALL" = "0" ]; then
  mkdir -p "$(dirname "$INSTALL_PATH")"
  BACKUP="${INSTALL_PATH}.bak-$(date +%s)"
  [ -f "$INSTALL_PATH" ] && cp "$INSTALL_PATH" "$BACKUP" && echo "==> backup: $BACKUP"
  cp "$OUT" "${INSTALL_PATH}.new"
  mv -f "${INSTALL_PATH}.new" "$INSTALL_PATH"
  echo "==> installed: $INSTALL_PATH"
  echo "$INSTALL_PATH"
else
  KEEP=$(mktemp -t virtiofsd-XXXXXX)
  cp "$OUT" "$KEEP"
  chmod +x "$KEEP"
  echo "==> binary (no-install): $KEEP"
  echo "$KEEP"
fi
