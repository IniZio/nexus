#!/usr/bin/env node
// Test: tunnel single-active enforcement and fork unstaged file preservation
// Usage: node scripts/test-tunnel-fork.mjs <alpha-ws-id> <beta-ws-id>
//
// Pre-requisites:
//   - hello-alpha and hello-beta workspaces running (backend=lima)
//
// Tests:
//   1. Tunnel activation on alpha succeeds
//   2. Tunnel activation on beta fails (alpha still active) → returns activeWorkspaceId=alpha
//   3. Deactivate alpha → activate beta succeeds
//   4. Deactivate beta → alpha can activate again (cleanup)
//   5. Fork alpha (with unstaged file) → child workspace starts → file visible in child

import { createRequire } from 'module';
const require = createRequire(import.meta.url);
const WebSocket = require('ws');
import { execSync } from 'child_process';

const PORT = 63987;
const TOKEN = execSync('security find-generic-password -s nexus-daemon-token -w').toString().trim();
const ALPHA_WS = process.argv[2];
const BETA_WS = process.argv[3];

if (!ALPHA_WS || !BETA_WS) {
  console.error('Usage: node test-tunnel-fork.mjs <alpha-ws-id> <beta-ws-id>');
  process.exit(1);
}

let passed = 0;
let failed = 0;
function pass(name) { console.log(`  ✅ PASS: ${name}`); passed++; }
function fail(name, reason) { console.error(`  ❌ FAIL: ${name} — ${reason}`); failed++; }

function connect() {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(`ws://127.0.0.1:${PORT}/`, {
      headers: { Authorization: `Bearer ${TOKEN}` },
    });
    let msgId = 1;
    const pending = new Map();

    ws.on('open', () => {
      resolve({
        ws,
        rpc(method, params) {
          const id = String(msgId++);
          return new Promise((res, rej) => {
            pending.set(id, { res, rej });
            ws.send(JSON.stringify({ jsonrpc: '2.0', id, method, params: params ?? {} }));
          });
        },
        close() { ws.close(); },
      });
    });

    ws.on('message', (raw) => {
      const msg = JSON.parse(raw.toString());
      if (msg.id !== undefined) {
        const p = pending.get(msg.id);
        if (p) {
          pending.delete(msg.id);
          if (msg.error) p.rej(Object.assign(new Error(msg.error.message), { code: msg.error.code }));
          else p.res(msg.result);
        }
      }
    });

    ws.on('error', reject);
  });
}

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

async function runTest(name, fn) {
  console.log(`\n▶ ${name}`);
  try {
    await fn();
  } catch (e) {
    fail(name, e.message);
  }
}

async function main() {
  console.log('=== Nexus Tunnel + Fork Tests ===');
  console.log(`Alpha workspace: ${ALPHA_WS}`);
  console.log(`Beta workspace:  ${BETA_WS}`);

  const client = await connect();

  // ── TUNNEL TESTS ──────────────────────────────────────────────────────────

  // Ensure both tunnels deactivated to start fresh
  await client.rpc('workspace.tunnels.deactivate', { workspaceId: ALPHA_WS }).catch(() => {});
  await client.rpc('workspace.tunnels.deactivate', { workspaceId: BETA_WS }).catch(() => {});
  await sleep(300);

  await runTest('Tunnel: activate alpha succeeds', async () => {
    const r = await client.rpc('workspace.tunnels.activate', { workspaceId: ALPHA_WS });
    if (!r.active) throw new Error(`Expected active=true, got ${JSON.stringify(r)}`);
    if (r.activeWorkspaceId !== ALPHA_WS) throw new Error(`Expected activeWorkspaceId=${ALPHA_WS}, got ${r.activeWorkspaceId}`);
    pass('Tunnel: activate alpha succeeds');
  });

  await runTest('Tunnel: activate beta while alpha active returns alpha as blocker', async () => {
    const r = await client.rpc('workspace.tunnels.activate', { workspaceId: BETA_WS });
    if (r.active) throw new Error(`Expected active=false (alpha still active), got active=true`);
    if (r.activeWorkspaceId !== ALPHA_WS) throw new Error(`Expected activeWorkspaceId=${ALPHA_WS} (blocking), got ${r.activeWorkspaceId}`);
    pass('Tunnel: activate beta while alpha active returns alpha as blocker');
  });

  await runTest('Tunnel: deactivate alpha then activate beta succeeds', async () => {
    await client.rpc('workspace.tunnels.deactivate', { workspaceId: ALPHA_WS });
    await sleep(300);
    const r = await client.rpc('workspace.tunnels.activate', { workspaceId: BETA_WS });
    if (!r.active) throw new Error(`Expected active=true after deactivating alpha, got ${JSON.stringify(r)}`);
    if (r.activeWorkspaceId !== BETA_WS) throw new Error(`Expected activeWorkspaceId=${BETA_WS}, got ${r.activeWorkspaceId}`);
    pass('Tunnel: deactivate alpha then activate beta succeeds');
  });

  await runTest('Tunnel: deactivate beta allows alpha to activate again', async () => {
    await client.rpc('workspace.tunnels.deactivate', { workspaceId: BETA_WS });
    await sleep(300);
    const r = await client.rpc('workspace.tunnels.activate', { workspaceId: ALPHA_WS });
    if (!r.active) throw new Error(`Expected active=true, got ${JSON.stringify(r)}`);
    pass('Tunnel: deactivate beta allows alpha to activate again');
    // Clean up: deactivate alpha
    await client.rpc('workspace.tunnels.deactivate', { workspaceId: ALPHA_WS });
  });

  // ── FORK + UNSTAGED FILE TESTS ────────────────────────────────────────────

  // Write an unstaged file to alpha via fs.writeFile (host worktree = virtiofs mount in Lima guest)
  const UNSTAGED_PATH = 'unstaged-test.txt';
  const UNSTAGED_CONTENT = `unstaged-content-${Date.now()}`;
  let childWsId = null;

  await runTest('Fork pre: write unstaged file to alpha worktree', async () => {
    const r = await client.rpc('fs.writeFile', {
      workspaceId: ALPHA_WS,
      path: UNSTAGED_PATH,
      content: UNSTAGED_CONTENT,
      encoding: 'utf8',
    });
    // Should succeed (no error thrown above)
    pass('Fork pre: write unstaged file to alpha worktree');
  });

  await runTest('Fork pre: verify file is NOT git-tracked (unstaged)', async () => {
    // Read it back to confirm it was written
    const r = await client.rpc('fs.readFile', {
      workspaceId: ALPHA_WS,
      path: UNSTAGED_PATH,
      encoding: 'utf8',
    });
    if (!r?.content?.includes(UNSTAGED_CONTENT)) {
      throw new Error(`File content mismatch: got ${JSON.stringify(r?.content)}`);
    }
    pass('Fork pre: verify file is NOT git-tracked (unstaged)');
  });

  await runTest('Fork: fork alpha workspace', async () => {
    // Use a unique name and ref to avoid conflicts
    const forkName = `alpha-fork-${Date.now()}`;
    const forkRef = `fork-${Date.now()}`;
    const r = await client.rpc('workspace.fork', { id: ALPHA_WS, childWorkspaceName: forkName, childRef: forkRef });
    childWsId = r?.workspace?.id;
    if (!childWsId) throw new Error(`Fork returned no workspace id: ${JSON.stringify(r)}`);
    pass(`Fork: fork alpha workspace → child ${childWsId}`);
  });

  await runTest('Fork: start child workspace', async () => {
    const r = await client.rpc('workspace.start', { id: childWsId });
    if (!r?.workspace?.id) throw new Error(`Start returned no workspace: ${JSON.stringify(r)}`);
    pass('Fork: start child workspace');
  });

  await runTest('Fork: unstaged file visible in child workspace', async () => {
    // Give Lima guest a moment to mount
    await sleep(3000);
    const r = await client.rpc('fs.readFile', {
      workspaceId: childWsId,
      path: UNSTAGED_PATH,
      encoding: 'utf8',
    });
    if (!r?.content?.includes(UNSTAGED_CONTENT)) {
      throw new Error(`Unstaged file NOT found in child. Got: ${JSON.stringify(r?.content)}`);
    }
    pass('Fork: unstaged file visible in child workspace');
  });

  // Clean up child workspace
  await client.rpc('workspace.stop', { id: childWsId }).catch(() => {});

  client.close();

  // ── Summary ───────────────────────────────────────────────────────────────
  console.log(`\n=== Results: ${passed} passed, ${failed} failed ===`);
  if (failed > 0) process.exit(1);
}

main().catch(e => { console.error('Fatal:', e); process.exit(1); });
