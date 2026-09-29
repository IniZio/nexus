#!/usr/bin/env sh
# Static musl virtiofsd + fakeowner.patch, run inside Alpine 3.20 (see PINNED.md)
set -eu

TAG=v1.13.3
TAG_SHA=13ee2e13024eaf40cdedb4bcb0a49d5695ade0b8
REPO_URL=https://gitlab.com/virtio-fs/virtiofsd.git
PATCH_SHA256=f2c7a7f8b2e75fa1850447f14b87bfc66ad0494bcc748e802b6298681ee8ecbf
RUST_VERSION="${RUST_VERSION:-1.98.1}"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PATCH="${PATCH:-$SCRIPT_DIR/fakeowner.patch}"
OUT="${OUT:-/out/virtiofsd}"
BUILD_DIR="${BUILD_DIR:-/build/virtiofsd}"

export SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-1735689600}"
export LC_ALL=C TZ=UTC

echo "==> apk deps"
apk add --no-cache build-base git curl pkgconf \
    libseccomp-dev libseccomp-static libcap-ng-dev libcap-ng-static

echo "==> rustup $RUST_VERSION (musl host)"
curl -fsSL https://sh.rustup.rs | sh -s -- -y --profile minimal --default-toolchain "$RUST_VERSION"
PATH="$HOME/.cargo/bin:$PATH"

mkdir -p "$BUILD_DIR" "$(dirname "$OUT")"
rm -rf "$BUILD_DIR/src"

echo "==> clone virtiofsd $TAG"
git clone --depth=1 --branch "$TAG" "$REPO_URL" "$BUILD_DIR/src"
actual=$(git -C "$BUILD_DIR/src" rev-parse "$TAG")
if [ "$actual" != "$TAG_SHA" ]; then
    echo "ERROR: tag SHA mismatch: got $actual expected $TAG_SHA" >&2
    exit 1
fi

echo "==> verify + apply patch"
actual_patch=$(sha256sum "$PATCH" | awk '{print $1}')
if [ "$actual_patch" != "$PATCH_SHA256" ]; then
    echo "ERROR: patch SHA mismatch: got $actual_patch expected $PATCH_SHA256" >&2
    exit 1
fi
git -C "$BUILD_DIR/src" apply "$PATCH"

echo "==> build"
export LIBSECCOMP_LINK_TYPE=static LIBSECCOMP_LIB_PATH=/usr/lib
export LIBCAPNG_LINK_TYPE=static LIBCAPNG_LIB_PATH=/usr/lib
export RUSTFLAGS="-C target-feature=+crt-static --remap-path-prefix=$BUILD_DIR/src=. --remap-path-prefix=$HOME/.cargo=/cargo"
cargo build --release --locked --manifest-path "$BUILD_DIR/src/Cargo.toml"

cp "$BUILD_DIR/src/target/release/virtiofsd" "$OUT"
chmod 0755 "$OUT"
sha256sum "$OUT"
echo "==> done"
