# e2fsprogs static binaries

Builds statically-linked `mke2fs`, `e2fsck`, and `resize2fs` from e2fsprogs 1.47.2
source using musl libc on Alpine Linux. The result replaces the host e2fsprogs
dependency (zero-host-deps design, sections D1/D2a/S3).

## Pinned inputs

| Item | Value |
|------|-------|
| e2fsprogs version | 1.47.2 |
| tarball URL | `https://mirrors.edge.kernel.org/pub/linux/kernel/people/tytso/e2fsprogs/v1.47.2/e2fsprogs-1.47.2.tar.xz` |
| tarball sha256 | `08242e64ca0e8194d9c1caad49762b19209a06318199b63ce74ae4ef2d74e63c` |
| sha256 source | upstream `sha256sums.asc` signed by Theodore Ts'o |
| Alpine image (amd64) | `docker.io/library/alpine@sha256:3c81aa9a3d770b316568f4499e30461a5cd3fbd7180bd89e28e34894c7845832` |
| Alpine index digest | `sha256:ce64758a109eb420d874a118f87920e625e12d3634e03b4a5573fd9f6e5d3507` |

## Build via nexus sandbox (host-side)

The build runs inside a nexus microVM to avoid any host toolchain dependency.
The only host prerequisites are `nexus` CLI and network access.

```sh
# 1. create output dir and write Containerfile
mkdir -p /var/tmp/z3/e2fs/amd64
mkdir -p /var/tmp/z3/e2fs-ctx/.nexus
cat > /var/tmp/z3/e2fs-ctx/.nexus/Containerfile <<'EOF'
FROM alpine@sha256:3c81aa9a3d770b316568f4499e30461a5cd3fbd7180bd89e28e34894c7845832
RUN apk add --no-cache gcc musl-dev make wget xz util-linux-dev linux-headers file
EOF

# 2. create sandbox (name must be exactly z3build/e2fs)
TMPDIR=/var/tmp/z3 nexus create z3build/e2fs \
    --file /var/tmp/z3/e2fs-ctx \
    --no-share-settings --no-user-mounts --no-sandbox-tools \
    --mount /var/tmp/z3:/out

# 3. copy build script into /out and run
cp scripts/e2fsprogs/build.sh /var/tmp/z3/build.sh
nexus exec z3build/e2fs -- sh /out/build.sh

# 4. verify and clean up
file /var/tmp/z3/e2fs/amd64/mke2fs
/var/tmp/z3/e2fs/amd64/mke2fs -V
nexus sandbox rm z3build/e2fs
```

Binaries land at `/var/tmp/z3/e2fs/amd64/{mke2fs,e2fsck,resize2fs}`.

## Configure flags

```
./configure \
  --disable-nls \
  --disable-fuse2fs \
  --without-libarchive \
  --disable-uuidd \
  --enable-libuuid \
  --enable-libblkid \
  CFLAGS="-O2 -ffile-prefix-map=<srcdir>=." \
  LDFLAGS='-static'
```

`--without-libarchive` removes the tarball-input path from `mke2fs -d`; directory
input (`mke2fs -d <dir>`) works without libarchive and is what nexus uses.

## Reproducibility

`SOURCE_DATE_EPOCH=1735689600` (2025-01-01T00:00:00Z, same as tarball release date)
is exported before the build. `LC_ALL=C TZ=UTC` and `-ffile-prefix-map` remove
remaining sources of non-determinism.

Two independent builds from clean source trees on 2026-09-28 produced byte-identical
stripped ELFs for all three binaries.

`SOURCE_DATE_EPOCH` is handled in `lib/ext2fs/initialize.c:128` and
`lib/ext2fs/openfs.c:152` — it sets `fs->now` and enables `EXT2_FLAG2_USE_FAKE_TIME`,
which pins filesystem creation timestamps. The same env var is also respected by
the C compiler's debug-info timestamp embedding.

## Research findings

**Alpine `e2fsprogs-static`**: no such package exists in Alpine 3.21 repos.
`apk search e2fsprogs-static` returns nothing. Alpine ships `e2fsprogs` (dynamic
executables) and `e2fsprogs-dev` (headers + `.a` libs), but no pre-built static
executables. Building from source with `LDFLAGS=-static` is the only option.

**`mke2fs -d <dir>` without libarchive**: works correctly. The `--without-libarchive`
flag removes only the tarball-input code path (`mke2fs -d <file.tar>`). Directory
input (`mke2fs -d <dir>`) is implemented separately in `misc/create_inode.c` using
direct POSIX calls and is unaffected. Verified by running `mke2fs -d <dir>` and
`e2fsck -fn` on the result — exit 0, no errors.

**`SOURCE_DATE_EPOCH` for fs timestamps**: honored (`lib/ext2fs/initialize.c:128`).
Different values produce different filesystem images; the same value produces the
same mkfs/inode timestamps. However, the filesystem UUID is generated randomly by
default, so full image sha256 reproducibility also requires `-U <fixed-uuid>`. The
binary reproducibility of the built executables is fully achieved with
`SOURCE_DATE_EPOCH` alone (two builds produce identical ELFs).

## Shipped binaries (amd64, 2026-09-28)

| Binary | sha256 | size |
|--------|--------|------|
| mke2fs | `660ed81e6b025adf8c5158f5b1157fc93aa2508508144cf9dc5fab392145f4a9` | 526 KiB |
| e2fsck | `3941a0b8844abc31a3c66f5486958d650e3f5ae9908b1e3dad72a7393ed21a37` | 735 KiB |
| resize2fs | `c260e90ef1586188704f4cb31e03ae09574079f8d3fdb6556083544e96f44911` | 404 KiB |

## TODO

- arm64: out of scope for Z3. Requires a separate Alpine arm64 VM or musl cross-compile
  toolchain (`aarch64-linux-musl-gcc`). The same `build.sh` should work unchanged
  inside an arm64 Alpine sandbox.
