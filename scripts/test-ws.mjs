#!/usr/bin/env node
// Headless test script: drives workspace lifecycle via WebSocket RPC
// Usage: node scripts/test-ws.mjs [workspace-id]

import { createRequire } from 'module';
const require = createRequire(import.meta.url);
const WebSocket = require('ws');
import { execSync } from 'child_process';

const PORT = 63987;
const TOKEN = execSync('security find-generic-password -s nexus-daemon-token -w').toString().trim();
const WS_ID = process.argv[2] || 'ws-1776360149563854000';

let msgId = 1;

function rpc(ws, method, params) {
  const id = String(msgId++);
  const msg = JSON.stringify({ jsonrpc: '2.0', id, method, params: params ?? {} });
  console.log(`\n→ [${method}]`, JSON.stringify(params));
  ws.send(msg);
  return id;
}

const ws = new WebSocket(`ws://127.0.0.1:${PORT}/`, {
  headers: { Authorization: `Bearer ${TOKEN}` },
});

const pending = new Map();

ws.on('open', async () => {
  console.log('Connected to daemon\n');

  // Skip workspace.list, go directly to workspace.start
  console.log('--- Calling workspace.start ---');
  rpc(ws, 'workspace.start', { id: WS_ID });
});

ws.on('message', (raw) => {
  const msg = JSON.parse(raw.toString());
  if (msg.error) {
    console.error(`✗ [id=${msg.id}] error:`, JSON.stringify(msg.error));
  } else if (msg.result !== undefined) {
    console.log(`✓ [id=${msg.id}] result:`, JSON.stringify(msg.result, null, 2).slice(0, 800));
  }

  // State machine
  if (msg.id === '1') {
    if (msg.error) {
      console.error('workspace.start failed — stopping');
      ws.close();
      process.exit(1);
    }
    // After start, try pty.open
    console.log('\n--- Calling pty.open ---');
    rpc(ws, 'pty.open', {
      workspaceId: WS_ID,
      cols: 80,
      rows: 24,
    });
  } else if (msg.id === '2') {
    if (msg.error) {
      console.error('pty.open failed — stopping');
      ws.close();
      process.exit(1);
    }
    const ptyId = msg.result?.id;
    console.log(`\npty.open succeeded, ptyId=${ptyId}`);
    console.log('Waiting 3s for shell prompt...');
    // Attach to pty output
    setTimeout(() => {
      console.log('\n--- All steps passed. Closing. ---');
      ws.close();
    }, 3000);
  }
});

ws.on('notification', (method, params) => {
  console.log(`← notification [${method}]`, JSON.stringify(params).slice(0, 200));
});

ws.on('close', () => {
  console.log('\nConnection closed.');
});

ws.on('error', (err) => {
  console.error('WebSocket error:', err.message);
  process.exit(1);
});
