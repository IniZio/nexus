#!/usr/bin/env sh
# Build static mke2fs/e2fsck/resize2fs (e2fsprogs 1.47.2) inside Alpine 3.21.
# See README.md for host-side nexus sandbox invocation and pinned digests.

set -eu

TARBALL_URL='https://mirrors.edge.kernel.org/pub/linux/kernel/people/tytso/e2fsprogs/v1.47.2/e2fsprogs-1.47.2.tar.xz'
TARBALL_SHA='08242e64ca0e8194d9c1caad49762b19209a06318199b63ce74ae4ef2d74e63c'

SOURCE_DATE_EPOCH="${SOURCE_DATE_EPOCH:-1735689600}"
export SOURCE_DATE_EPOCH
export LC_ALL=C
export TZ=UTC

OUT="${OUT:-/out/e2fs/amd64}"
BUILD_DIR="${BUILD_DIR:-/build/e2fs}"

mkdir -p "$OUT" "$BUILD_DIR"

echo "==> download e2fsprogs-1.47.2.tar.xz"
wget -q -O "$BUILD_DIR/e2fsprogs-1.47.2.tar.xz" "$TARBALL_URL"

echo "==> verify sha256"
actual=$(sha256sum "$BUILD_DIR/e2fsprogs-1.47.2.tar.xz" | awk '{print $1}')
if [ "$actual" != "$TARBALL_SHA" ]; then
    echo "ERROR: tarball sha256 mismatch: got $actual expected $TARBALL_SHA" >&2
    exit 1
fi
echo "    verified: $actual"

echo "==> extract"
tar -C "$BUILD_DIR" -xf "$BUILD_DIR/e2fsprogs-1.47.2.tar.xz"

echo "==> configure"
cd "$BUILD_DIR/e2fsprogs-1.47.2"
./configure \
    --disable-nls \
    --disable-fuse2fs \
    --without-libarchive \
    --disable-uuidd \
    --enable-libuuid \
    --enable-libblkid \
    CFLAGS="-O2 -ffile-prefix-map=$BUILD_DIR/e2fsprogs-1.47.2=." \
    LDFLAGS='-static'

echo "==> build"
make -j"$(nproc 2>/dev/null || echo 4)"

echo "==> strip and install"
strip misc/mke2fs e2fsck/e2fsck resize/resize2fs
cp misc/mke2fs    "$OUT/mke2fs"
cp e2fsck/e2fsck  "$OUT/e2fsck"
cp resize/resize2fs "$OUT/resize2fs"

echo "==> sha256"
sha256sum "$OUT/mke2fs" "$OUT/e2fsck" "$OUT/resize2fs"
echo "==> sizes"
ls -la "$OUT/mke2fs" "$OUT/e2fsck" "$OUT/resize2fs"
echo "==> done"
