#!/usr/bin/env bash
# PROTOTYPE — builds the nexus3-host:latest image from the repo.
#
# Build context is the nexus3 repo root so the Go source and images/kernel/*
# are available to the multi-stage Dockerfile.
#
# virtiofsd is the one binary that still comes from the host (no pre-built
# GitLab binary release — see the TODO in the Dockerfile).  Everything else
# is built or downloaded inside the image.
set -euo pipefail
cd "$(dirname "$0")"

REPO_ROOT="$(git rev-parse --show-toplevel)"
PROTO_DIR="$REPO_ROOT/examples/nexus3-in-docker"

# Stage virtiofsd from the host (static-pie; SHA: 7ef50584…)
# TODO: remove once the Dockerfile builds virtiofsd from source via Rust.
mkdir -p "$PROTO_DIR/stage"
VIRTIOFSD_SRC="${VIRTIOFSD_BIN:-$(command -v virtiofsd)}"
cp -v "$VIRTIOFSD_SRC" "$PROTO_DIR/stage/virtiofsd"
chmod +x "$PROTO_DIR/stage/virtiofsd"

echo "Building nexus3-host:latest from repo root …"
docker build \
    -f "$PROTO_DIR/Dockerfile" \
    -t nexus3-host:latest \
    "$REPO_ROOT"

echo ""
echo "OK -> nexus3-host:latest"
echo "Image size: $(docker image inspect nexus3-host:latest --format '{{.Size}}' | numfmt --to=iec 2>/dev/null || docker image inspect nexus3-host:latest --format '{{.Size}}')"
