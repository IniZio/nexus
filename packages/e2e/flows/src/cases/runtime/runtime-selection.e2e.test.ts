import { WorkspaceHandle } from '@nexus/sdk';
import { createGitFixture, cleanupFixture } from '../../harness/repo';
import { startSession } from '../../harness/session';
import { assertCapabilityOrSkip, onWorkspaceCreateRuntimeError } from '../../harness/assertions';
import { runtimeSelectionCaseIds } from '../test-ids';

export const CASE_TEST_IDS = runtimeSelectionCaseIds;

describe('runtime selection e2e', () => {
  jest.setTimeout(420000);
  (process.platform === 'linux' ? it : it.skip)('pass -> firecracker when override is forced', async () => {
    const fixture = await createGitFixture('runtime-selection-pass');
    const session = await withTimeout(startSession({
      forceManaged: true,
      managed: {
        extraEnv: {
          NEXUS_INTERNAL_ENABLE_PREFLIGHT_OVERRIDE: '1',
          NEXUS_INTERNAL_PREFLIGHT_OVERRIDE: 'pass',
        },
      },
    }), 'startSession(pass)');

    let ws: WorkspaceHandle | null = null;
    try {
      const caps = await session.client.workspaces.capabilities();
      if (!assertCapabilityOrSkip(caps, 'runtime.firecracker', 'runtime.firecracker capability unavailable on this host')) {
        return;
      }

      try {
        ws = await withTimeout(session.client.workspaces.create({
          repo: fixture.repoDir,
          workspaceName: 'runtime-pass',
          agentProfile: 'default',
        }), 'workspace.create(pass)');
      } catch (error) {
        if (onWorkspaceCreateRuntimeError(error, 'firecracker runtime path unavailable in this environment')) {
          return;
        }
        throw error;
      }

      const rows = await session.client.workspaces.list();
      const created = rows.find((row) => row.id === ws?.id);
      expect(created?.backend).toBe('firecracker');
    } finally {
      if (ws) {
        await session.client.workspaces.remove(ws.id);
      }
      await session.stop();
      await cleanupFixture(fixture);
    }
  });

  (process.platform === 'darwin' ? it : it.skip)('unsupported_nested_virt -> seatbelt', async () => {
    const fixture = await createGitFixture('runtime-selection-nested');
    const session = await withTimeout(startSession({
      forceManaged: true,
      managed: {
        extraEnv: {
          NEXUS_INTERNAL_ENABLE_PREFLIGHT_OVERRIDE: '1',
          NEXUS_INTERNAL_PREFLIGHT_OVERRIDE: 'unsupported_nested_virt',
        },
      },
    }), 'startSession(unsupported_nested_virt)');

    let ws: WorkspaceHandle | null = null;
    try {
      const caps = await session.client.workspaces.capabilities();
      if (!assertCapabilityOrSkip(caps, 'runtime.seatbelt', 'runtime.seatbelt capability unavailable on this host')) {
        return;
      }

      try {
        ws = await withTimeout(session.client.workspaces.create({
          repo: fixture.repoDir,
          workspaceName: 'runtime-nested',
          agentProfile: 'default',
        }), 'workspace.create(unsupported_nested_virt)');
      } catch (error) {
        if (onWorkspaceCreateRuntimeError(error, 'seatbelt runtime path unavailable in this environment')) {
          return;
        }
        throw error;
      }

      const rows = await session.client.workspaces.list();
      const created = rows.find((row) => row.id === ws?.id);
      expect(created?.backend).toBe('seatbelt');
    } finally {
      if (ws) {
        await session.client.workspaces.remove(ws.id);
      }
      await session.stop();
      await cleanupFixture(fixture);
    }
  });

  it('hard_fail -> create failure', async () => {
    const fixture = await createGitFixture('runtime-selection-hard-fail');
    const session = await withTimeout(startSession({
      forceManaged: true,
      managed: {
        extraEnv: {
          NEXUS_INTERNAL_ENABLE_PREFLIGHT_OVERRIDE: '1',
          NEXUS_INTERNAL_PREFLIGHT_OVERRIDE: 'hard_fail',
        },
      },
    }), 'startSession(hard_fail)');

    try {
      await expect(
        withTimeout(session.client.workspaces.create({
          repo: fixture.repoDir,
          workspaceName: 'runtime-hard-fail',
          agentProfile: 'default',
        }), 'workspace.create(hard_fail)')
      ).rejects.toThrow(/hard_fail/);
    } finally {
      await session.stop();
      await cleanupFixture(fixture);
    }
  });

  it('installable_missing with setup failure -> create failure', async () => {
    const fixture = await createGitFixture('runtime-selection-installable');
    const session = await withTimeout(startSession({
      forceManaged: true,
      managed: {
        extraEnv: {
          NEXUS_INTERNAL_ENABLE_PREFLIGHT_OVERRIDE: '1',
          NEXUS_INTERNAL_PREFLIGHT_OVERRIDE: 'installable_missing',
        },
      },
    }), 'startSession(installable_missing)');

    try {
      await expect(
        withTimeout(session.client.workspaces.create({
          repo: fixture.repoDir,
          workspaceName: 'runtime-installable',
          agentProfile: 'default',
        }), 'workspace.create(installable_missing)')
      ).rejects.toThrow(/installable_missing/);
    } finally {
      await session.stop();
      await cleanupFixture(fixture);
    }
  });
});

const RUNTIME_E2E_OP_MS = 300000;

async function withTimeout<T>(promise: Promise<T>, label: string, timeoutMs = RUNTIME_E2E_OP_MS): Promise<T> {
  return Promise.race([
    promise,
    new Promise<T>((_, reject) => {
      setTimeout(() => reject(new Error(`${label} timed out after ${String(timeoutMs)}ms`)), timeoutMs);
    }),
  ]);
}
