#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/../../packages/nexus"

go test ./pkg/server -count=1 -run 'TestPTYOpenUsesRemoteConnectorForFirecrackerAndLXC' -v

PORT=8094
TOKEN=ci-token
WS_DIR="$(mktemp -d)"
DAEMON_LOG="/tmp/nexus-daemon-local-ci.log"
SMOKE_LOG="pty-remote-smoke-local-ci.log"

go run ./cmd/daemon --port "${PORT}" --workspace-dir "${WS_DIR}" --token "${TOKEN}" >"${DAEMON_LOG}" 2>&1 &
DAEMON_PID=$!

cleanup() {
  if kill -0 "${DAEMON_PID}" >/dev/null 2>&1; then
    kill "${DAEMON_PID}" >/dev/null 2>&1 || true
    wait "${DAEMON_PID}" 2>/dev/null || true
  fi
  rm -rf "${WS_DIR}"
}
trap cleanup EXIT

for _ in $(seq 1 40); do
  if curl -fsS "http://127.0.0.1:${PORT}/healthz" >/dev/null; then
    break
  fi
  sleep 1
done
curl -fsS "http://127.0.0.1:${PORT}/healthz" >/dev/null

BACKEND="local"
if command -v limactl >/dev/null 2>&1; then
  if limactl list --json nexus-lxc 2>/dev/null | grep -qv '^\[\]$'; then
    BACKEND="lxc"
  elif limactl list --json nexus-firecracker 2>/dev/null | grep -qv '^\[\]$'; then
    BACKEND="lxc"
  fi
fi
echo "selected backend: ${BACKEND}"

WORKSPACE_ID=$(PORT="${PORT}" TOKEN="${TOKEN}" BACKEND="${BACKEND}" node - <<'NODE'
const port = process.env.PORT;
const token = process.env.TOKEN;
const backend = process.env.BACKEND;
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
        workspaceName: `ci-pty-${Date.now()}`,
        agentProfile: "codex",
        policy: {},
        backend,
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
