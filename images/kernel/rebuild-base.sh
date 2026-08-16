#!/usr/bin/env bash
# rebuild-base.sh — rebuild the nexus3 agent binary in images/kernel/
#                   and optionally rebuild the nexus3-agent-base ext4 image.
#
# Usage:
#   images/kernel/rebuild-base.sh            # rebuild agent binary only
#   images/kernel/rebuild-base.sh --image    # rebuild agent binary + full image
#
# The agent binary (CGO_ENABLED=0) is written to images/kernel/nexus3-agent.
# The --image flag triggers a full image rebuild via the integration test
# harness (TestBuildAgentBaseImage), which requires docker and mke2fs.
#
# Rebuild rule
# ────────────
# Run this script whenever ANY of the following change:
#   • cmd/nexus3-agent/**  (agent source code)
#   • internal/core/agent/**  (agent library code)
# Run it with --image to also update the nexus3-agent-base image cache entry.
# Without --image, only images/kernel/nexus3-agent is updated; sandboxes that
# use --rootfs will pick up the new binary immediately.  Sandboxes that use
# --image nexus3-agent-base still need the full image rebuild.
#
# Staleness detection
# ───────────────────
# Every built image embeds a build tag in the agent binary:
#   YYYYMMDD-<git-short-sha>   (e.g. 20260815-abc1234)
# At boot, the agent logs:
#   nexus3-agent: starting (pid=1 build=20260815-abc1234)
# Compare this tag to the host's current git HEAD:
#   git rev-parse --short=7 HEAD
# If they differ, the image is stale and must be rebuilt.

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

REBUILD_IMAGE=false
for arg in "$@"; do
  case "$arg" in
    --image) REBUILD_IMAGE=true ;;
    -h|--help)
      sed -n '2,40p' "$0" | grep '^#' | sed 's/^# \?//'
      exit 0 ;;
    *) echo "unknown flag: $arg"; exit 1 ;;
  esac
done

BUILD_DATE=$(date -u +%Y%m%d)
GIT_SHA=$(git rev-parse --short=7 HEAD 2>/dev/null || echo "unknown")
BUILD_TAG="${BUILD_DATE}-${GIT_SHA}"

echo "nexus3-agent: building (CGO_ENABLED=0 GOOS=linux GOARCH=amd64) build=${BUILD_TAG}"

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -ldflags "-X main.agentBuildTag=${BUILD_TAG}" \
  -o images/kernel/nexus3-agent \
  ./cmd/nexus3-agent

BINARY_SIZE=$(stat -c%s images/kernel/nexus3-agent)
echo "nexus3-agent: built → images/kernel/nexus3-agent (${BINARY_SIZE} bytes, build=${BUILD_TAG})"

if [[ "$REBUILD_IMAGE" == "true" ]]; then
  if ! command -v docker &>/dev/null; then
    echo "ERROR: --image requires docker in PATH"
    exit 1
  fi
  if ! command -v mke2fs &>/dev/null; then
    echo "ERROR: --image requires mke2fs in PATH (install e2fsprogs)"
    exit 1
  fi
  echo "nexus3-agent-base: rebuilding full image via cmd/rebuild-agent-base..."
  # The rebuild tool compiles the agent with a fresh stamp, then calls
  # selfhost.BuildAgentBaseImage to build the full image (Node.js + Claude Code)
  # and register it in the production image cache.
  TMPDIR=/tmp go run ./cmd/rebuild-agent-base
  echo ""
  echo "nexus3-agent-base: image rebuild complete."
  echo "The new image is in the production cache. To ensure it is resolved first"
  echo "by 'sandbox create --image nexus3-agent-base', remove stale entries:"
  echo ""
  echo "  go run ./cmd/nexus3 image ls  # see all cached images"
  echo ""
  echo "Stale entries (ref=nexus3-agent-base, created 2026-08-11) can be removed"
  echo "by deleting their sha256/ subdirectories from ~/.local/state/nexus3/images/"
else
  echo ""
  echo "Agent binary rebuilt. To also rebuild the nexus3-agent-base image, run:"
  echo "  images/kernel/rebuild-base.sh --image"
fi
