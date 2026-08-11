#!/usr/bin/env bash
# start.sh — create a nexus3 microVM sandbox (Orca recipe entry point)
set -euo pipefail

NEXUS3_IMAGE="${NEXUS3_IMAGE:-nexus3-agent-base}"
NEXUS3_DEDICATED_CRED_STORE="${NEXUS3_DEDICATED_CRED_STORE:-$HOME/.config/nexus3/creds.json}"
TMPDIR="${TMPDIR:-/tmp}"

exec env \
  NEXUS3_IMAGE="$NEXUS3_IMAGE" \
  NEXUS3_DEDICATED_CRED_STORE="$NEXUS3_DEDICATED_CRED_STORE" \
  TMPDIR="$TMPDIR" \
  nexus3 orca create "$@"
