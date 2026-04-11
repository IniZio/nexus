#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
NEXUS_MOD="$ROOT/packages/nexus"
NEXUS_BIN="${NEXUS_BIN:-}"
if [[ -z "$NEXUS_BIN" ]]; then
  NEXUS_BIN="$(mktemp "${TMPDIR:-/tmp}/nexus-verify-codex.XXXXXX")"
  (cd "$NEXUS_MOD" && go build -o "$NEXUS_BIN" ./cmd/nexus)
  trap 'rm -f "$NEXUS_BIN"' EXIT
fi

lima_fix_workspace_if_broken() {
  if ! command -v limactl >/dev/null 2>&1; then
    return 0
  fi
  if ! limactl list 2>/dev/null | grep -q 'nexus-firecracker'; then
    return 0
  fi
  local w
  w="$(limactl shell nexus-firecracker -- sh -lc 'if [ -L /workspace ] && [ ! -e /workspace ]; then echo broken; elif [ -f /workspace ]; then echo file; else echo ok; fi' 2>/dev/null || true)"
  if [[ "$w" == "broken" ]] || [[ "$w" == "file" ]]; then
    echo "verify-codex-auth: fixing stale /workspace in Lima guest (was: $w)" >&2
    limactl shell nexus-firecracker -- sh -lc 'sudo rm -f /workspace; sudo mkdir -p /workspace' >&2
  fi
}

lima_fix_workspace_if_broken

REPO=$(mktemp -d)
(
  cd "$REPO"
  git init -q
  git config user.email "verify@nexus.test"
  git config user.name "Nexus Verify"
  echo test >README.md
  mkdir -p .nexus
  echo '{"version":1}' >.nexus/workspace.json
  git add .
  git commit -q -m init
)

export NEXUS_CLI_PATH="$NEXUS_BIN"

create_out="$(
  cd "$REPO"
  "$NEXUS_BIN" create 2>&1
)"
printf '%s\n' "$create_out"
ws_id="$(printf '%s\n' "$create_out" | sed -nE 's/.*\(id: ([^)]+)\).*/\1/p' | head -n1)"
if [[ -z "$ws_id" ]]; then
  echo "verify-codex-auth: could not parse workspace id" >&2
  exit 1
fi

cleanup() {
  NEXUS_CLI_PATH="$NEXUS_BIN" "$NEXUS_BIN" remove "$ws_id" >/dev/null 2>&1 || true
}
trap cleanup EXIT

"$NEXUS_BIN" start "$ws_id" >/dev/null

echo "== guest: codex config + login status (no model call) =="
"$NEXUS_BIN" ssh "$ws_id" --command 'set -e; echo HOME=$HOME; ls -la ~/.codex 2>/dev/null | head -15 || true; ls -la ~/.config/codex 2>/dev/null | head -10 || true; command -v codex >/dev/null; codex login status'

if [[ -n "${VERIFY_CODEX_EXEC:-}" ]] && [[ -n "${OPENAI_API_KEY:-}" ]]; then
  echo "== guest: codex exec (model call; VERIFY_CODEX_EXEC set) =="
  "$NEXUS_BIN" ssh "$ws_id" --command 'cd /workspace && codex exec "Reply with exactly: ok" 2>&1' | head -80
else
  echo "== skip codex exec (set VERIFY_CODEX_EXEC=1 and OPENAI_API_KEY for a live model call) =="
fi

echo "== ok =="
