import { WorkspaceHandle } from '@nexus/sdk';
import { createGitFixture, cleanupFixture } from '../harness/fixtures';
import { rpcRequest } from '../harness/rpc';
import { startSession, type DaemonSession } from '../harness/session';
import { isLinuxTapUnsupported, skipTest } from '../harness/assertions';
import { toolsAuthForwardingCaseIds } from './test-ids';

export const CASE_TEST_IDS = toolsAuthForwardingCaseIds;

describe('tools auth forwarding e2e', () => {
  it('mints relay token, forwards auth into exec, and revokes token', async () => {
    const fixture = await createGitFixture('tools-auth-forwarding');
    let session: DaemonSession;
    try {
      session = await startSession();
    } catch (error) {
      await cleanupFixture(fixture);
      skipTest(`unable to start daemon session: ${(error as Error).message}`);
      return;
    }

    let ws: WorkspaceHandle | null = null;
    try {
      let created;
      try {
        created = await rpcRequest<{ workspace: { id: string } }>(session.client, 'workspace.create', {
          spec: {
            repo: fixture.repoDir,
            workspaceName: 'auth-forwarding-case',
            agentProfile: 'default',
            authBinding: {
              github: 'newman',
            },
          },
        });
      } catch (error) {
        if (isLinuxTapUnsupported(error)) {
          skipTest('tools auth forwarding requires Linux TAP support in this environment');
          return;
        }
        throw error;
      }

      ws = await session.client.workspace.open(created.workspace.id);

      expect(ws.id).toBe(created.workspace.id);

      const token = await session.client.workspace.mintAuthRelay({
        workspaceId: ws.id,
        binding: 'github',
        ttlSeconds: 60,
      });
      expect(token).not.toBe('');

      const authExec = await ws.exec.exec('sh', ['-lc', 'printf "%s" "$NEXUS_AUTH_BINDING:$NEXUS_AUTH_VALUE"'], {
        authRelayToken: token,
      });
      expect(authExec.exitCode).toBe(0);
      expect(authExec.stdout).toContain('github:newman');

      const revoked = await session.client.workspace.revokeAuthRelay(token);
      expect(revoked).toBe(true);

      await expect(ws.exec.exec('sh', ['-lc', 'echo should-not-run'], { authRelayToken: token })).rejects.toThrow(
        /invalid auth relay token/
      );
    } finally {
      if (ws) {
        await session.client.workspace.remove(ws.id);
      }
      await session.stop();
      await cleanupFixture(fixture);
    }
  });
});
