import fs from 'node:fs/promises';
import path from 'node:path';
import { createGitFixture, cleanupFixture, runCmd } from '../../harness/repo';
import { startSession } from '../../harness/session';
import { onWorkspaceCreateRuntimeError } from '../../harness/assertions';
import { worktreeSyncCaseIds } from '../test-ids';

export const CASE_TEST_IDS = worktreeSyncCaseIds;

describe('worktree sync e2e', () => {
  jest.setTimeout(240000);

  it('propagates host and workspace file changes with git status parity', async () => {
    const fixture = await createGitFixture('worktree-sync');
    const session = await startSession();

    let workspaceId = '';
    let workspaceRoot = '';
    try {
      let handle;
      try {
        handle = await session.client.workspaces.create({
          repo: fixture.repoDir,
          workspaceName: 'sync-case',
          agentProfile: 'default',
        });
      } catch (error) {
        if (onWorkspaceCreateRuntimeError(error, 'worktree sync runtime path unavailable in this environment')) {
          return;
        }
        throw error;
      }
      workspaceId = handle.id;
      workspaceRoot = handle.rootPath;

      await runCmd('git', ['init'], workspaceRoot);
      await runCmd('git', ['config', 'user.email', 'nexus-e2e@example.test'], workspaceRoot);
      await runCmd('git', ['config', 'user.name', 'Nexus E2E'], workspaceRoot);

      const tracked = path.join(workspaceRoot, 'tracked.txt');
      await fs.writeFile(tracked, 'initial\n', 'utf8');
      await runCmd('git', ['add', '.'], workspaceRoot);
      await runCmd('git', ['commit', '-m', 'seed workspace repo'], workspaceRoot);

      await fs.writeFile(tracked, 'host-change\n', 'utf8');
      const observedByWorkspace = await handle.readFile('tracked.txt', 'utf8');
      expect(observedByWorkspace).toContain('host-change');

      await handle.writeFile('workspace-created.txt', 'workspace-change\n');
      const observedByHost = await fs.readFile(path.join(workspaceRoot, 'workspace-created.txt'), 'utf8');
      expect(observedByHost).toContain('workspace-change');

      const hostStatus = await runCmd('git', ['status', '--short'], workspaceRoot);
      expect(hostStatus.stdout).toContain('M tracked.txt');
      expect(hostStatus.stdout).toContain('?? workspace-created.txt');

      const workspaceStatus = await handle.exec('git', ['status']);
      const statusStdout = workspaceStatus.stdout;
      expect(statusStdout).toContain('M tracked.txt');
      expect(statusStdout).toContain('?? workspace-created.txt');
    } finally {
      if (workspaceId !== '') {
        await session.client.workspaces.remove(workspaceId);
      }
      await session.stop();
      await cleanupFixture(fixture);
    }
  });
});
