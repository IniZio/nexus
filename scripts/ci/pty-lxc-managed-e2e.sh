#!/usr/bin/env bash
set -euo pipefail

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "Skipping pty-lxc-managed-e2e (requires macOS)"
  exit 0
fi

if ! command -v limactl >/dev/null 2>&1; then
  brew install lima
fi

cd "$(dirname "$0")/../../packages/nexus"

PROJECT_ROOT="$(mktemp -d)"
go run ./cmd/nexus init --project-root "${PROJECT_ROOT}" --force

PORT=8095
TOKEN=ci-token
WS_DIR="$(mktemp -d)"
DAEMON_LOG="/tmp/nexus-daemon-lxc-managed-local.log"
SMOKE_LOG="pty-remote-smoke-lxc-managed-local.log"

go run ./cmd/daemon --port "${PORT}" --workspace-dir "${WS_DIR}" --token "${TOKEN}" >"${DAEMON_LOG}" 2>&1 &
DAEMON_PID=$!

cleanup() {
  if kill -0 "${DAEMON_PID}" >/dev/null 2>&1; then
    kill "${DAEMON_PID}" >/dev/null 2>&1 || true
    wait "${DAEMON_PID}" 2>/dev/null || true
  fi
  rm -rf "${WS_DIR}" "${PROJECT_ROOT}"
}
trap cleanup EXIT

for _ in $(seq 1 50); do
  if curl -fsS "http://127.0.0.1:${PORT}/healthz" >/dev/null; then
    break
  fi
  sleep 1
done
curl -fsS "http://127.0.0.1:${PORT}/healthz" >/dev/null

WORKSPACE_ID=$(PORT="${PORT}" TOKEN="${TOKEN}" node - <<'NODE'
const port = process.env.PORT;
const token = process.env.TOKEN;
const ws = new WebSocket(`ws://127.0.0.1:${port}/?token=${encodeURIComponent(token)}`);
const id = `create-${Date.now()}`;

ws.onopen = () => {
  ws.send(JSON.stringify({
    jsonrpc: "2.0",
    id,
    method: "workspace.create",
    params: {
      spec: {
        repo: "https://example.com/repo.git",
        ref: "main",
        workspaceName: `ci-lxc-${Date.now()}`,
        agentProfile: "codex",
        policy: {},
        backend: "lxc",
      },
    },
  }));
};

ws.onmessage = (event) => {
  const msg = JSON.parse(String(event.data));
  if (msg.error) {
    console.error(`workspace.create error: ${JSON.stringify(msg.error)}`);
    process.exit(1);
    return;
  }
  const workspaceId = msg.result && msg.result.workspace && msg.result.workspace.id;
  if (!workspaceId) {
    console.error("workspace.create missing workspace id");
    process.exit(1);
    return;
  }
  console.log(workspaceId);
  process.exit(0);
};

ws.onerror = (err) => {
  console.error(`workspace.create websocket error: ${String(err)}`);
  process.exit(1);
};
NODE
)

NEXUS_DAEMON_WS="ws://127.0.0.1:${PORT}" \
NEXUS_DAEMON_TOKEN="${TOKEN}" \
NEXUS_PTY_SMOKE_LOG="${SMOKE_LOG}" \
node scripts/pty-remote-smoke.js "${WORKSPACE_ID}"
