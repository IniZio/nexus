import { WorkspaceHandle, type Capability } from '@nexus/sdk';
import { createGitFixture, cleanupFixture } from '../harness/fixtures';
import { startSession } from '../harness/session';
import { isRuntimeUnavailable, skipTest } from '../harness/assertions';
import { runtimeSelectionCaseIds } from './test-ids';

export const CASE_TEST_IDS = runtimeSelectionCaseIds;

describe('runtime selection e2e', () => {
  it('pass -> firecracker when override is forced', async () => {
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
      const caps = await session.client.workspace.capabilities();
      if (!capabilityAvailable(caps, 'runtime.firecracker')) {
        skipTest('runtime.firecracker capability unavailable on this host');
        return;
      }

      try {
        ws = await withTimeout(session.client.workspace.create({
          repo: fixture.repoDir,
          workspaceName: 'runtime-pass',
          agentProfile: 'default',
        }), 'workspace.create(pass)');
      } catch (error) {
        if (isRuntimeUnavailable(error)) {
          skipTest('firecracker runtime path unavailable in this environment');
          return;
        }
        throw error;
      }

      const rows = await session.client.workspace.list();
      const created = rows.find((row) => row.id === ws?.id);
      expect(created?.backend).toBe('firecracker');
    } finally {
      if (ws) {
        await session.client.workspace.remove(ws.id);
      }
      await session.stop();
      await cleanupFixture(fixture);
    }
  });

  it('unsupported_nested_virt -> seatbelt', async () => {
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
      const caps = await session.client.workspace.capabilities();
      if (!capabilityAvailable(caps, 'runtime.seatbelt')) {
        skipTest('runtime.seatbelt capability unavailable on this host');
        return;
      }

      try {
        ws = await withTimeout(session.client.workspace.create({
          repo: fixture.repoDir,
          workspaceName: 'runtime-nested',
          agentProfile: 'default',
        }), 'workspace.create(unsupported_nested_virt)');
      } catch (error) {
        if (isRuntimeUnavailable(error)) {
          skipTest('seatbelt runtime path unavailable in this environment');
          return;
        }
        throw error;
      }

      const rows = await session.client.workspace.list();
      const created = rows.find((row) => row.id === ws?.id);
      expect(created?.backend).toBe('seatbelt');
    } finally {
      if (ws) {
        await session.client.workspace.remove(ws.id);
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
        withTimeout(session.client.workspace.create({
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
        withTimeout(session.client.workspace.create({
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

function capabilityAvailable(caps: Capability[], name: string): boolean {
  const found = caps.find((cap) => cap.name === name);
  return Boolean(found?.available);
}

async function withTimeout<T>(promise: Promise<T>, label: string, timeoutMs = 30000): Promise<T> {
  return Promise.race([
    promise,
    new Promise<T>((_, reject) => {
      setTimeout(() => reject(new Error(`${label} timed out after ${String(timeoutMs)}ms`)), timeoutMs);
    }),
  ]);
}
