import { WorkspaceHandle } from '@nexus/sdk';
import { createGitFixture, cleanupFixture } from '../../harness/repo';
import { rpcRequest, startSession, type DaemonSession } from '../../harness/session';
import { onDaemonStartError, onWorkspaceCreateRuntimeError } from '../../harness/assertions';
import { toolsAuthForwardingCaseIds } from '../test-ids';

export const CASE_TEST_IDS = toolsAuthForwardingCaseIds;

describe('tools auth forwarding e2e', () => {
  it('mints relay token, forwards auth into exec, and revokes token', async () => {
    const fixture = await createGitFixture('tools-auth-forwarding');
    let session: DaemonSession;
    try {
      session = await startSession();
    } catch (error) {
      await cleanupFixture(fixture);
      onDaemonStartError(error, 'unable to start daemon session');
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
              opencode: 'relay-opencode-test',
              codex: 'sk-e2e-relay-codex',
            },
          },
        });
      } catch (error) {
        if (onWorkspaceCreateRuntimeError(error, 'tools auth forwarding runtime path unavailable in this environment')) {
          return;
        }
        throw error;
      }

      ws = await session.client.workspaces.open(created.workspace.id);

      expect(ws.id).toBe(created.workspace.id);

      const token = await session.client.workspaces.mintAuthRelay({
        workspaceId: ws.id,
        binding: 'github',
        ttlSeconds: 60,
      });
      expect(token).not.toBe('');

      const authExec = await ws.exec('sh', ['-lc', 'printf "%s|%s" "$NEXUS_AUTH_BINDING:$NEXUS_AUTH_VALUE" "$(test -n "$PATH" && echo path-ok || echo path-missing)"; test "$GITHUB_TOKEN" = "$NEXUS_AUTH_VALUE" && test "$GH_TOKEN" = "$NEXUS_AUTH_VALUE" && echo gh-env-ok'], {
        authRelayToken: token,
      });
      expect(authExec.exitCode).toBe(0);
      expect(authExec.stdout).toContain('github:newman');
      expect(authExec.stdout).toContain('path-ok');
      expect(authExec.stdout).toContain('gh-env-ok');

      const ocToken = await session.client.workspaces.mintAuthRelay({
        workspaceId: ws.id,
        binding: 'opencode',
        ttlSeconds: 60,
      });
      const ocExec = await ws.exec('sh', ['-lc', 'printf "%s" "$NEXUS_AUTH_BINDING:$NEXUS_AUTH_VALUE"; test "$OPENCODE_API_KEY" = "$NEXUS_AUTH_VALUE" && echo oc-key-ok'], {
        authRelayToken: ocToken,
      });
      expect(ocExec.exitCode).toBe(0);
      expect(ocExec.stdout).toContain('opencode:relay-opencode-test');
      expect(ocExec.stdout).toContain('oc-key-ok');

      const cxToken = await session.client.workspaces.mintAuthRelay({
        workspaceId: ws.id,
        binding: 'codex',
        ttlSeconds: 60,
      });
      const cxExec = await ws.exec('sh', ['-lc', 'printf "%s" "$NEXUS_AUTH_BINDING:$NEXUS_AUTH_VALUE"; test "$OPENAI_API_KEY" = "$NEXUS_AUTH_VALUE" && echo openai-env-ok'], {
        authRelayToken: cxToken,
      });
      expect(cxExec.exitCode).toBe(0);
      expect(cxExec.stdout).toContain('codex:sk-e2e-relay-codex');
      expect(cxExec.stdout).toContain('openai-env-ok');

      const revoked = await session.client.workspaces.revokeAuthRelay(token);
      expect(revoked).toBe(true);

      await expect(ws.exec('sh', ['-lc', 'echo should-not-run'], { authRelayToken: token })).rejects.toThrow(
        /invalid auth relay token/i
      );
    } finally {
      if (ws) {
        await session.client.workspaces.remove(ws.id);
      }
      await session.stop();
      await cleanupFixture(fixture);
    }
  });
});
