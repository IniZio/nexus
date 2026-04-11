#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"

export NEXUS_E2E_LIVE_MODELS=1
export NEXUS_E2E_STRICT_RUNTIME="${NEXUS_E2E_STRICT_RUNTIME:-1}"

echo "== host credential sync live auth e2e =="
echo "copilot model=${NEXUS_E2E_OPENCODE_COPILOT_MODEL:-github-copilot/gpt-5-mini}"
echo "minimax model=${NEXUS_E2E_OPENCODE_MINIMAX_MODEL:-minimax-coding-plan/MiniMax-M2.7-highspeed}"
echo "strict runtime=${NEXUS_E2E_STRICT_RUNTIME}"
echo "relay override mode enabled when NEXUS_E2E_AUTH_* variables are provided"

cd "$ROOT"
pnpm --filter @nexus/e2e-flows test -- --runTestsByPath src/cases/tools-auth-live.e2e.test.ts
