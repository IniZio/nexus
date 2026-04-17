#!/usr/bin/env node
// Headless test: verifies PTY session connects to Lima guest
// Usage: node scripts/test-pty-shell.mjs [workspace-id]

import { createRequire } from 'module';
const require = createRequire(import.meta.url);
const WebSocket = require('ws');
import { execSync } from 'child_process';

const PORT = 63987;
const TOKEN = execSync('security find-generic-password -s nexus-daemon-token -w').toString().trim();
const WS_ID = process.argv[2];

if (!WS_ID) {
  console.error('Usage: node test-pty-shell.mjs <workspace-id>');
  process.exit(1);
}

let msgId = 1;
let ptySessionId = null;
const outputBuf = [];

function rpc(ws, method, params) {
  const id = String(msgId++);
  const msg = JSON.stringify({ jsonrpc: '2.0', id, method, params: params ?? {} });
  console.log(`→ [${method}]`, JSON.stringify(params));
  ws.send(msg);
  return id;
}

const ws = new WebSocket(`ws://127.0.0.1:${PORT}/`, {
  headers: { Authorization: `Bearer ${TOKEN}` },
});

ws.on('open', () => {
  console.log('Connected.\n--- workspace.start ---');
  rpc(ws, 'workspace.start', { id: WS_ID });
});

ws.on('message', (raw) => {
  const msg = JSON.parse(raw.toString());

  // Notifications (PTY output)
  if (msg.method === 'pty.data') {
    const data = msg.params?.data ?? '';
    process.stdout.write(data);
    outputBuf.push(data);
    return;
  }

  if (msg.error) {
    console.error(`✗ [id=${msg.id}] error:`, JSON.stringify(msg.error));
    ws.close();
    process.exit(1);
  }

  if (msg.id === '1') {
    console.log('✓ workspace.start ok\n--- pty.open ---');
    rpc(ws, 'pty.open', { workspaceId: WS_ID, cols: 80, rows: 24 });
  } else if (msg.id === '2') {
    ptySessionId = msg.result?.sessionId;
    console.log(`✓ pty.open ok, sessionId=${ptySessionId}`);
    console.log('\n--- Waiting 8s for shell to start, then sending "hostname; pwd" ---\n');
    setTimeout(() => {
      rpc(ws, 'pty.write', { sessionId: ptySessionId, data: 'hostname; pwd\n' });
    }, 8000);
    // Check output after 15s
    setTimeout(() => {
      const allOutput = outputBuf.join('');
      console.log('\n\n--- Captured PTY output (last 500 chars) ---');
      console.log(allOutput.slice(-500));
      const isLimaGuest = allOutput.includes('lima') || allOutput.includes('nexus') || 
                          (allOutput.includes('/workspace') && !allOutput.includes('/Users/newman'));
      const isHostShell = allOutput.includes('/Users/newman') && !allOutput.includes('/workspace');
      if (isLimaGuest) {
        console.log('\n✅ PASS: Shell is running in Lima guest');
      } else if (isHostShell) {
        console.log('\n❌ FAIL: Shell appears to be on host (not Lima guest)');
        process.exit(1);
      } else {
        console.log('\n⚠️  UNCLEAR: Could not determine shell environment from output');
        console.log('Full output:', JSON.stringify(allOutput));
      }
      ws.close();
    }, 15000);
  }
});

ws.on('close', () => console.log('\nConnection closed.'));
ws.on('error', (err) => { console.error('WS error:', err.message); process.exit(1); });
