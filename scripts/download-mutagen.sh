#!/bin/bash
set -e

VERSION="0.18.0"
OUT_DIR="packages/workspace-daemon/internal/sync/mutagen/bin"

mkdir -p $OUT_DIR

for platform in darwin-amd64 darwin-arm64 linux-amd64 linux-arm64; do
    URL="https://github.com/mutagen-io/mutagen/releases/download/v${VERSION}/mutagen_${platform}_v${VERSION}.tar.gz"
    echo "Downloading mutagen for $platform..."
    curl -L $URL | tar -xz -C $OUT_DIR mutagen
    mv $OUT_DIR/mutagen $OUT_DIR/mutagen-${platform}
    echo "Done: mutagen-$platform"
done

echo "All Mutagen binaries downloaded successfully!"
