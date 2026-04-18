#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ENV_FILE="${GITHUB_WORKSPACE:-$ROOT}/.nexus-e2e-env.sh"
SKIP_FILE="${GITHUB_WORKSPACE:-$ROOT}/.nexus-e2e-skip"

if [[ -f "$SKIP_FILE" ]] || [[ ! -f "$ENV_FILE" ]]; then
  echo "flows e2e: SKIP (CLI was not built)"
  exit 0
fi

set -a
# shellcheck source=/dev/null
source "$ENV_FILE"
set +a

pnpm --filter @nexus/e2e-flows test:ci -- src/cases/runtime/runtime-selection.e2e.test.ts
