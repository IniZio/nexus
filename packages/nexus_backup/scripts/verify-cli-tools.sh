#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BINDIR="$(mktemp -d "${TMPDIR:-/tmp}/nexus-verify-bin.XXXXXX")"
PORT="${NEXUS_DAEMON_PORT:-}"
if [[ -z "$PORT" ]]; then
  PORT="$(python3 -c 'import socket; s=socket.socket(); s.bind(("",0)); print(s.getsockname()[1]); s.close()')"
fi
export NEXUS_DAEMON_PORT="$PORT"

echo "== build nexus + nexus-daemon (isolated dir) =="
(cd "$ROOT" && go build -o "$BINDIR/nexus" ./cmd/nexus)
(cd "$ROOT" && go build -o "$BINDIR/nexus-daemon" ./cmd/daemon)
export PATH="$BINDIR:$PATH"

TIMEOUT_SSH="${TIMEOUT_SSH:-90s}"

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/nexus-cli-verify.XXXXXX")"
cleanup() {
  if [[ -n "${WS_ID:-}" ]]; then
    "$BINDIR/nexus" remove "$WS_ID" 2>/dev/null || true
  fi
  rm -rf "$WORKDIR" "$BINDIR" 2>/dev/null || true
}
trap cleanup EXIT

echo "== daemon port $NEXUS_DAEMON_PORT (fresh EnsureRunning uses binaries from $BINDIR) =="

echo "== init repo $WORKDIR =="
cd "$WORKDIR"
git init
git config user.email verify@local
git config user.name verify
echo "# cli verify" > README.md
git add README.md
git commit -m init
"$BINDIR/nexus" init --force

echo "== workspace create =="
OUT="$("$BINDIR/nexus" create 2>&1)"
echo "$OUT"
WS_ID="$(echo "$OUT" | sed -n 's/.*(id: \(ws-[^)]*\)).*/\1/p' | head -1)"
if [[ -z "$WS_ID" ]]; then
  echo "failed to parse workspace id" >&2
  exit 1
fi

echo "== workspace start $WS_ID =="
"$BINDIR/nexus" start "$WS_ID"

run_ssh() {
  local label="$1"
  shift
  echo ""
  echo "== $label (ssh --timeout $TIMEOUT_SSH) =="
  SECONDS=0
  set +e
  "$BINDIR/nexus" ssh "$WS_ID" --timeout "$TIMEOUT_SSH" --command "$@"
  ec=$?
  set -e
  echo "-- exit code $ec wall ${SECONDS}s --"
  if [[ "$ec" -eq 0 ]]; then
    echo "PASS"
  elif [[ "$ec" -eq 124 ]]; then
    echo "FAIL: timed out (no clean pty.exit within $TIMEOUT_SSH)"
  else
    echo "FAIL: exit $ec"
  fi
  return "$ec"
}

set +e
run_ssh "smoke: echo" 'echo HI'
ec0=$?

run_ssh "codex: version + exec help" 'command -v codex; codex --version; codex exec -h 2>&1 | head -6'
ec1=$?

run_ssh "opencode: version + copilot model lines" 'command -v opencode; opencode --version; opencode models 2>&1 | grep -i github-copilot | head -5'
ec2=$?

echo ""
echo "== optional: opencode run (copilot) — may fail without API auth =="
"$BINDIR/nexus" ssh "$WS_ID" --timeout 120s --command 'opencode run -m github-copilot/gpt-4o-mini --format json "Reply with exactly: OK-PROBE" 2>&1 | head -25' || true

set -e
echo ""
echo "== summary: echo=$ec0 codex=$ec1 opencode_models=$ec2 =="
if [[ "$ec0" -eq 0 && "$ec1" -eq 0 && "$ec2" -eq 0 ]]; then
  echo "ALL CHECKS PASSED"
  exit 0
fi
exit 1
