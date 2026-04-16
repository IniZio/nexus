#!/usr/bin/env node
// End-to-end correctness tests for Lima workspace PTY
// Tests: multiple terminals, stop/start persistence, fork
// Usage: node scripts/test-e2e.mjs <workspace-id>

import { createRequire } from 'module';
const require = createRequire(import.meta.url);
const WebSocket = require('ws');
import { execSync } from 'child_process';

const PORT = 63987;
const TOKEN = execSync('security find-generic-password -s nexus-daemon-token -w').toString().trim();
const WS_ID = process.argv[2];

if (!WS_ID) {
  console.error('Usage: node test-e2e.mjs <workspace-id>');
  process.exit(1);
}

let passed = 0;
let failed = 0;
function pass(name) { console.log(`  ✅ PASS: ${name}`); passed++; }
function fail(name, reason) { console.error(`  ❌ FAIL: ${name} — ${reason}`); failed++; }

// Connect a WebSocket and return {ws, send, waitFor}
function connect() {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(`ws://127.0.0.1:${PORT}/`, {
      headers: { Authorization: `Bearer ${TOKEN}` },
    });
    let msgId = 1;
    const pending = new Map(); // id -> resolve
    const notifications = []; // all notifications
    const notifWaiters = []; // waiters for method

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
        waitForNotification(method, timeoutMs = 20000) {
          return new Promise((res, rej) => {
            const t = setTimeout(() => rej(new Error(`timeout waiting for ${method}`)), timeoutMs);
            const existing = notifications.find(n => n.method === method);
            if (existing) { clearTimeout(t); return res(existing); }
            notifWaiters.push({ method, resolve: (n) => { clearTimeout(t); res(n); } });
          });
        },
        collectOutput(sessionId, durationMs) {
          const buf = [];
          const handler = (msg) => {
            if (msg.method === 'pty.data' && msg.params?.sessionId === sessionId) {
              buf.push(msg.params.data);
            }
          };
          notifWaiters.push({ method: 'pty.data', multi: true, sessionId, resolve: handler });
          return new Promise(res => setTimeout(() => res(buf.join('')), durationMs));
        },
        close() { ws.close(); }
      });
    });
    ws.on('error', reject);
    ws.on('message', (raw) => {
      const msg = JSON.parse(raw.toString());
      if (!msg.id) {
        // notification
        notifications.push(msg);
        notifWaiters.forEach(w => {
          if (w.method === msg.method) {
            if (w.multi) {
              if (!w.sessionId || msg.params?.sessionId === w.sessionId) w.resolve(msg);
            } else {
              w.resolve(msg);
            }
          }
        });
        notifWaiters.splice(0, notifWaiters.length, ...notifWaiters.filter(w => w.multi || w.resolved));
        return;
      }
      const p = pending.get(msg.id);
      if (p) {
        pending.delete(msg.id);
        if (msg.error) p.rej(new Error(JSON.stringify(msg.error)));
        else p.res(msg.result);
      }
    });
  });
}

function sleep(ms) { return new Promise(r => setTimeout(r, ms)); }

async function sendAndCollect(client, sessionId, cmd, waitMs = 5000) {
  await client.rpc('pty.write', { sessionId, data: cmd + '\n' });
  await sleep(waitMs);
}

async function runTest(name, fn) {
  console.log(`\n▶ ${name}`);
  try {
    await fn();
  } catch (e) {
    fail(name, e.message);
  }
}

// ── Test 1: Multiple concurrent terminals ─────────────────────────────────────
async function testMultipleTerminals() {
  await runTest('Multiple concurrent terminals on same workspace', async () => {
    const c1 = await connect();
    const c2 = await connect();
    try {
      await c1.rpc('workspace.start', { id: WS_ID });
      await c2.rpc('workspace.start', { id: WS_ID });

      const r1 = await c1.rpc('pty.open', { workspaceId: WS_ID, cols: 80, rows: 24 });
      const r2 = await c2.rpc('pty.open', { workspaceId: WS_ID, cols: 80, rows: 24 });

      if (!r1?.sessionId) throw new Error('pty.open #1 returned no sessionId');
      if (!r2?.sessionId) throw new Error('pty.open #2 returned no sessionId');
      if (r1.sessionId === r2.sessionId) throw new Error('both terminals share the same sessionId');
      pass('two pty.open return distinct sessionIds');

      // Wait for shells to start
      await sleep(6000);

      // Write unique markers to each terminal
      const marker1 = 'TERM1_MARKER_' + Date.now();
      const marker2 = 'TERM2_MARKER_' + Date.now();

      const buf1 = [];
      const buf2 = [];

      // Collect output by listening for pty.data
      const collectFn1 = (raw) => {
        const m = JSON.parse(raw.toString());
        if (m.method === 'pty.data' && m.params?.sessionId === r1.sessionId) buf1.push(m.params.data);
      };
      const collectFn2 = (raw) => {
        const m = JSON.parse(raw.toString());
        if (m.method === 'pty.data' && m.params?.sessionId === r2.sessionId) buf2.push(m.params.data);
      };
      c1.ws.on('message', collectFn1);
      c2.ws.on('message', collectFn2);

      await c1.rpc('pty.write', { sessionId: r1.sessionId, data: `echo ${marker1}\n` });
      await c2.rpc('pty.write', { sessionId: r2.sessionId, data: `echo ${marker2}\n` });

      await sleep(4000);

      const out1 = buf1.join('');
      const out2 = buf2.join('');

      if (!out1.includes(marker1)) throw new Error(`terminal 1 output missing ${marker1}. got: ${JSON.stringify(out1.slice(-300))}`);
      if (!out2.includes(marker2)) throw new Error(`terminal 2 output missing ${marker2}. got: ${JSON.stringify(out2.slice(-300))}`);
      if (out1.includes(marker2)) throw new Error('terminal 1 received terminal 2 output (cross-contamination)');
      if (out2.includes(marker1)) throw new Error('terminal 2 received terminal 1 output (cross-contamination)');

      pass('each terminal receives only its own output');

      // Both shells in Lima guest
      const buf1h = [];
      const buf2h = [];
      c1.ws.on('message', (raw) => {
        const m = JSON.parse(raw.toString());
        if (m.method === 'pty.data' && m.params?.sessionId === r1.sessionId) buf1h.push(m.params.data);
      });
      c2.ws.on('message', (raw) => {
        const m = JSON.parse(raw.toString());
        if (m.method === 'pty.data' && m.params?.sessionId === r2.sessionId) buf2h.push(m.params.data);
      });

      await c1.rpc('pty.write', { sessionId: r1.sessionId, data: 'hostname\n' });
      await c2.rpc('pty.write', { sessionId: r2.sessionId, data: 'hostname\n' });
      await sleep(3000);

      const h1 = buf1h.join('') + buf1.join('');
      const h2 = buf2h.join('') + buf2.join('');
      if (!h1.includes('nexus') && !h1.includes('lima')) throw new Error(`terminal 1 not in Lima guest. output: ${JSON.stringify(h1.slice(-200))}`);
      if (!h2.includes('nexus') && !h2.includes('lima')) throw new Error(`terminal 2 not in Lima guest. output: ${JSON.stringify(h2.slice(-200))}`);
      pass('both terminals running in Lima guest');

    } finally {
      c1.close();
      c2.close();
    }
  });
}

// ── Test 2: Stop and start ─────────────────────────────────────────────────────
async function testStopStart() {
  await runTest('workspace stop → start → PTY opens in Lima guest', async () => {
    const c = await connect();
    try {
      // Stop
      console.log('  Stopping workspace...');
      await c.rpc('workspace.stop', { id: WS_ID });
      pass('workspace.stop succeeded');

      await sleep(3000);

      // Start
      console.log('  Starting workspace...');
      await c.rpc('workspace.start', { id: WS_ID });
      pass('workspace.start succeeded');

      // Open PTY
      const r = await c.rpc('pty.open', { workspaceId: WS_ID, cols: 80, rows: 24 });
      if (!r?.sessionId) throw new Error('no sessionId after stop+start');
      pass('pty.open succeeded after stop+start');

      await sleep(7000);

      const buf = [];
      c.ws.on('message', (raw) => {
        const m = JSON.parse(raw.toString());
        if (m.method === 'pty.data' && m.params?.sessionId === r.sessionId) buf.push(m.params.data);
      });

      await c.rpc('pty.write', { sessionId: r.sessionId, data: 'hostname; pwd\n' });
      await sleep(4000);

      const out = buf.join('');
      if (!out.includes('nexus') && !out.includes('lima')) throw new Error(`shell not in Lima after stop+start. output: ${JSON.stringify(out.slice(-300))}`);
      if (!out.includes('/workspace')) throw new Error(`cwd not /workspace after stop+start. output: ${JSON.stringify(out.slice(-300))}`);
      pass('shell in Lima guest at /workspace after stop+start');

      // Verify project files still visible
      const buf2 = [];
      c.ws.on('message', (raw) => {
        const m = JSON.parse(raw.toString());
        if (m.method === 'pty.data' && m.params?.sessionId === r.sessionId) buf2.push(m.params.data);
      });
      await c.rpc('pty.write', { sessionId: r.sessionId, data: 'ls /workspace/' + WS_ID.split('-')[0] + '* 2>/dev/null || ls\n' });
      await sleep(3000);
      const lsOut = buf2.join('') + out;
      if (!lsOut.includes('README') && !lsOut.includes('docker-compose') && !lsOut.includes('backend') && !lsOut.includes('Makefile')) {
        throw new Error(`project files not visible after stop+start. ls output: ${JSON.stringify(lsOut.slice(-400))}`);
      }
      pass('project files visible after stop+start');

    } finally {
      c.close();
    }
  });
}

// ── Test 3: Fork ───────────────────────────────────────────────────────────────
async function testFork() {
  await runTest('workspace fork → PTY opens in Lima guest', async () => {
    const c = await connect();
    try {
      await c.rpc('workspace.start', { id: WS_ID });

      // Fork
      console.log('  Forking workspace...');
      const childRef = 'test-fork-' + Date.now();
      const forkResult = await c.rpc('workspace.fork', { id: WS_ID, childRef, childWorkspaceName: childRef });
      const forkedId = forkResult?.id ?? forkResult?.workspace?.id;
      if (!forkedId) throw new Error(`fork returned no id: ${JSON.stringify(forkResult)}`);
      pass(`workspace.fork created ${forkedId}`);

      // Start forked
      await c.rpc('workspace.start', { id: forkedId });
      pass('forked workspace started');

      await sleep(3000);

      // Open PTY on fork
      const r = await c.rpc('pty.open', { workspaceId: forkedId, cols: 80, rows: 24 });
      if (!r?.sessionId) throw new Error('no sessionId on forked workspace');
      pass('pty.open on forked workspace succeeded');

      await sleep(7000);

      const buf = [];
      c.ws.on('message', (raw) => {
        const m = JSON.parse(raw.toString());
        if (m.method === 'pty.data' && m.params?.sessionId === r.sessionId) buf.push(m.params.data);
      });

      await c.rpc('pty.write', { sessionId: r.sessionId, data: 'hostname; pwd\n' });
      await sleep(4000);

      const out = buf.join('');
      if (!out.includes('nexus') && !out.includes('lima')) throw new Error(`forked shell not in Lima. output: ${JSON.stringify(out.slice(-300))}`);
      pass('forked workspace shell runs in Lima guest');

      // Cleanup fork
      try {
        await c.rpc('workspace.stop', { id: forkedId });
        await c.rpc('workspace.delete', { id: forkedId });
        pass('forked workspace cleaned up');
      } catch(e) {
        console.log(`  ⚠ cleanup failed (non-fatal): ${e.message}`);
      }

    } finally {
      c.close();
    }
  });
}

// ── Main ───────────────────────────────────────────────────────────────────────
console.log(`\n=== Nexus Lima E2E Tests ===`);
console.log(`Workspace: ${WS_ID}`);

await testMultipleTerminals();
await testStopStart();
await testFork();

console.log(`\n=== Results: ${passed} passed, ${failed} failed ===`);
if (failed > 0) process.exit(1);
