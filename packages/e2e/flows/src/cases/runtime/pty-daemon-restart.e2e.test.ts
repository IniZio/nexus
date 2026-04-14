import type { WorkspaceHandle } from '@nexus/sdk';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { createGitFixture, cleanupFixture } from '../../harness/repo';
import { connectSDKClient, startManagedDaemon } from '../../harness/daemon';
import { onWorkspaceCreateRuntimeError } from '../../harness/assertions';
import { ptyPersistenceCaseIds } from '../test-ids';

export const CASE_TEST_IDS = ptyPersistenceCaseIds;

describe('pty persistence e2e', () => {
  jest.setTimeout(420000);

  it('recovers tmux-backed PTY sessions after daemon restart', async () => {
    const fixture = await createGitFixture('pty-daemon-restart');
    const sharedWorkspaceDir = await fs.mkdtemp(path.join(os.tmpdir(), 'nexus-e2e-pty-restart-'));
    const daemon1 = await startManagedDaemon({ workspaceDir: sharedWorkspaceDir });
    let daemon1Stopped = false;
    let daemon2: Awaited<ReturnType<typeof startManagedDaemon>> | null = null;
    let ws: WorkspaceHandle | null = null;

    try {
      const client1 = await connectSDKClient({ endpoint: daemon1.endpoint, token: daemon1.token });
      let sessionId = '';
      try {
        try {
          ws = await client1.workspaces.create({
            repo: fixture.repoDir,
            workspaceName: 'pty-restart',
            agentProfile: 'default',
          });
        } catch (error) {
          if (onWorkspaceCreateRuntimeError(error, 'pty restart case runtime unavailable in this environment')) {
            return;
          }
          throw error;
        }

        ws = await client1.workspaces.start(ws.id);
        sessionId = await client1.shell.open({ workspaceId: ws.id, cols: 80, rows: 24, useTmux: true, name: 'Main' });
      } finally {
        await client1.disconnect();
      }

      await daemon1.stop();
      daemon1Stopped = true;
      daemon2 = await startManagedDaemon({ workspaceDir: daemon1.workspaceDir });

      const client2 = await connectSDKClient({ endpoint: daemon2.endpoint, token: daemon2.token });
      try {
        const sessions = await waitForRecoveredSession(client2, ws!.id, sessionId, 15_000);
        expect(sessions.some((s) => s.id === sessionId && s.isTmux)).toBe(true);

        const attached = await client2.request<{ attached: boolean }>('pty.attach', { sessionId });
        expect(attached.attached).toBe(true);

        const writeOk = await client2.shell.write(sessionId, 'echo daemon-restart-recovered\n');
        expect(writeOk).toBe(true);
      } finally {
        await client2.disconnect();
      }
    } finally {
      if (!daemon1Stopped) {
        await daemon1.stop().catch(() => undefined);
      }
      if (daemon2) {
        const cleanupClient = await connectSDKClient({ endpoint: daemon2.endpoint, token: daemon2.token }).catch(() => null);
        if (cleanupClient && ws) {
          try {
            await cleanupClient.workspaces.remove(ws.id);
          } catch {
          }
          await cleanupClient.disconnect();
        }
        await daemon2.stop();
      }
      await fs.rm(sharedWorkspaceDir, { recursive: true, force: true }).catch(() => undefined);
      await cleanupFixture(fixture);
    }
  });
});

async function waitForRecoveredSession(
  client: Awaited<ReturnType<typeof connectSDKClient>>,
  workspaceId: string,
  sessionId: string,
  timeoutMs: number
) {
  const started = Date.now();
  while (Date.now() - started < timeoutMs) {
    const sessions = await client.shell.list(workspaceId);
    if (Array.isArray(sessions) && sessions.some((s) => s.id === sessionId)) {
      return sessions;
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
  }
  throw new Error(`timed out waiting for recovered session ${sessionId}`);
}
