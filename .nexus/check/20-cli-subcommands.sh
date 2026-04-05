#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN_DIR="${ROOT}/.nexus/run"
BIN="${BIN_DIR}/nexus-self"

mkdir -p "${BIN_DIR}"

go build -o "${BIN}" ./packages/nexus/cmd/nexus

"${BIN}" init --project-root "${ROOT}" --runtime local --force >/dev/null

if "${BIN}" exec --project-root "${ROOT}" --timeout 15s -- bash -lc 'printf ok' | grep -q 'ok'; then
  echo "exec subcommand smoke test passed"
else
  echo "exec subcommand smoke test failed"
  exit 1
fi

if "${BIN}" doctor --project-root "${ROOT}" --suite default --report-json "${BIN_DIR}/doctor-report.json" >/dev/null; then
  echo "doctor subcommand smoke test passed"
else
  echo "doctor subcommand smoke test failed"
  exit 1
fi
