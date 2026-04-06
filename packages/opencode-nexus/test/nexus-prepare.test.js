import test from 'node:test';
import assert from 'node:assert/strict';

import { prepareNexusWorkspace } from '../src/nexus-prepare.js';

test('prepareNexusWorkspace returns workspace metadata on success', async () => {
  const result = await prepareNexusWorkspace(
    { goal: 'Add API route', cwd: '/repo' },
    {
      runCommand: async () => ({
        code: 0,
        stdout: JSON.stringify({ workspacePath: '/tmp/ws', workspaceId: 'ws-1' }),
        stderr: '',
      }),
    },
  );

  assert.equal(result.ok, true);
  assert.equal(result.workspace.workspacePath, '/tmp/ws');
  assert.equal(result.workspace.workspaceId, 'ws-1');
});

test('prepareNexusWorkspace fails gracefully when nexus is unavailable', async () => {
  const result = await prepareNexusWorkspace(
    { goal: 'Add API route', cwd: '/repo' },
    {
      runCommand: async () => ({
        code: 127,
        stdout: '',
        stderr: 'nexus: command not found',
      }),
    },
  );

  assert.equal(result.ok, false);
  assert.equal(result.reasonCode, 'NEXUS_UNAVAILABLE');
});
