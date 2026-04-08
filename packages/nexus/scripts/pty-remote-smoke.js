#!/usr/bin/env node

const fs = require("fs");

function now() {
  return new Date().toISOString();
}

function toInt(value, fallback) {
  const n = Number(value);
  if (!Number.isFinite(n)) {
    return fallback;
  }
  return n;
}

const args = process.argv.slice(2);
const workspaceId = args[0] || process.env.NEXUS_WORKSPACE_ID || "";
const endpoint = process.env.NEXUS_DAEMON_WS || "ws://127.0.0.1:8084";
const token = process.env.NEXUS_DAEMON_TOKEN || "dev-token";
const outputPath = process.env.NEXUS_PTY_SMOKE_LOG || "pty-remote-smoke.log";
const cols = toInt(process.env.NEXUS_PTY_COLS, 120);
const rows = toInt(process.env.NEXUS_PTY_ROWS, 30);
const timeoutMs = toInt(process.env.NEXUS_PTY_TIMEOUT_MS, 30000);

if (!workspaceId) {
  console.error("usage: node scripts/pty-remote-smoke.js <workspaceId>");
  process.exit(2);
}

const transcript = [];

function log(event, detail) {
  const line = { ts: now(), event, detail };
  transcript.push(line);
  const text = `[${line.ts}] ${event} ${JSON.stringify(detail)}`;
  console.log(text);
}

function writeTranscript() {
  const lines = transcript.map((line) => JSON.stringify(line)).join("\n") + "\n";
  fs.writeFileSync(outputPath, lines, "utf8");
}

function sendRPC(ws, id, method, params) {
  ws.send(
    JSON.stringify({
      jsonrpc: "2.0",
      id,
      method,
      params,
    })
  );
}

const ws = new WebSocket(`${endpoint}/?token=${encodeURIComponent(token)}`);
const runID = Date.now();

let sessionId = "";
let closeRequested = false;
let sawMarker = false;

const deadline = setTimeout(() => {
  log("timeout", { timeoutMs });
  try {
    ws.close();
  } catch (_err) {
    // no-op
  }
  writeTranscript();
  process.exit(1);
}, timeoutMs);

ws.onopen = () => {
  log("ws.open", { endpoint, workspaceId });
  sendRPC(ws, `open-${runID}`, "pty.open", { workspaceId, cols, rows });
};

ws.onerror = (err) => {
  log("ws.error", { message: String(err && err.message ? err.message : err) });
};

ws.onclose = (event) => {
  log("ws.close", { code: event.code, reason: event.reason || "" });
};

ws.onmessage = (event) => {
  let msg;
  try {
    msg = JSON.parse(String(event.data));
  } catch (err) {
    log("rpc.parse_error", { raw: String(event.data), error: String(err) });
    return;
  }

  if (msg.error) {
    log("rpc.error", msg.error);
    clearTimeout(deadline);
    writeTranscript();
    process.exit(1);
    return;
  }

  if (msg.result && msg.result.sessionId) {
    sessionId = msg.result.sessionId;
    log("pty.open.ok", { sessionId });
    sendRPC(ws, `write-${runID}`, "pty.write", {
      sessionId,
      data: "echo __NEXUS_PTY_SMOKE_OK__; pwd\n",
    });
    return;
  }

  if (msg.result && msg.result.ok === true) {
    log("pty.action.ok", { id: msg.id || "(no-id)" });
    return;
  }

  if (msg.result && msg.result.closed === true) {
    log("pty.close.ok", { sessionId });
    clearTimeout(deadline);
    writeTranscript();
    process.exit(sawMarker ? 0 : 1);
    return;
  }

  if (msg.method === "pty.data") {
    const raw = String(msg.params && msg.params.data ? msg.params.data : "");
    const snippet = raw.slice(0, 120).replace(/\n/g, "\\n");
    log("pty.data", { snippet });

    if (raw.includes("__NEXUS_PTY_SMOKE_OK__") && !closeRequested) {
      sawMarker = true;
      sendRPC(ws, `resize-${runID}`, "pty.resize", { sessionId, cols: 100, rows: 24 });
      sendRPC(ws, `close-${runID}`, "pty.close", { sessionId });
      closeRequested = true;
    }
    return;
  }

  if (msg.method === "pty.exit") {
    log("pty.exit", msg.params || {});
    if (closeRequested) {
      clearTimeout(deadline);
      writeTranscript();
      process.exit(sawMarker ? 0 : 1);
    }
  }
};

process.on("exit", () => {
  writeTranscript();
});
