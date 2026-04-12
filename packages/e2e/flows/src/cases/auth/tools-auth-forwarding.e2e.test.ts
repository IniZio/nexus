import { WorkspaceHandle } from '@nexus/sdk';
import { createGitFixture, cleanupFixture } from '../../harness/repo';
import { startSession, type DaemonSession } from '../../harness/session';
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
        created = await session.client.request<{ workspace: { id: string } }>('workspace.create', {
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

      ws = await session.client.workspaces.start(created.workspace.id);

      expect(ws.id).toBe(created.workspace.id);

      const token = await session.client
        .request<{ token: string }>('authrelay.mint', {
          workspaceId: ws.id,
          binding: 'github',
          ttlSeconds: 60,
        })
        .then((r) => r.token);
      expect(token).not.toBe('');

      const authResult = await session.client.request<{ stdout: string; stderr: string; exit_code: number }>('exec.exec', {
        workspaceId: ws.id,
        command: 'sh',
        args: [
          '-lc',
          'printf "%s|%s" "$NEXUS_AUTH_BINDING:$NEXUS_AUTH_VALUE" "$(test -n "$PATH" && echo path-ok || echo path-missing)"; test "$GITHUB_TOKEN" = "$NEXUS_AUTH_VALUE" && test "$GH_TOKEN" = "$NEXUS_AUTH_VALUE" && echo gh-env-ok',
        ],
        options: { authRelayToken: token },
      });
      const authExec = { stdout: authResult.stdout, stderr: authResult.stderr, exitCode: authResult.exit_code };
      expect(authExec.exitCode).toBe(0);
      expect(authExec.stdout).toContain('github:newman');
      expect(authExec.stdout).toContain('path-ok');
      expect(authExec.stdout).toContain('gh-env-ok');

      const ocToken = await session.client
        .request<{ token: string }>('authrelay.mint', {
          workspaceId: ws.id,
          binding: 'opencode',
          ttlSeconds: 60,
        })
        .then((r) => r.token);
      const ocResult = await session.client.request<{ stdout: string; stderr: string; exit_code: number }>('exec.exec', {
        workspaceId: ws.id,
        command: 'sh',
        args: ['-lc', 'printf "%s" "$NEXUS_AUTH_BINDING:$NEXUS_AUTH_VALUE"; test "$OPENCODE_API_KEY" = "$NEXUS_AUTH_VALUE" && echo oc-key-ok'],
        options: { authRelayToken: ocToken },
      });
      const ocExec = { stdout: ocResult.stdout, stderr: ocResult.stderr, exitCode: ocResult.exit_code };
      expect(ocExec.exitCode).toBe(0);
      expect(ocExec.stdout).toContain('opencode:relay-opencode-test');
      expect(ocExec.stdout).toContain('oc-key-ok');

      const cxToken = await session.client
        .request<{ token: string }>('authrelay.mint', {
          workspaceId: ws.id,
          binding: 'codex',
          ttlSeconds: 60,
        })
        .then((r) => r.token);
      const cxResult = await session.client.request<{ stdout: string; stderr: string; exit_code: number }>('exec.exec', {
        workspaceId: ws.id,
        command: 'sh',
        args: ['-lc', 'printf "%s" "$NEXUS_AUTH_BINDING:$NEXUS_AUTH_VALUE"; test "$OPENAI_API_KEY" = "$NEXUS_AUTH_VALUE" && echo openai-env-ok'],
        options: { authRelayToken: cxToken },
      });
      const cxExec = { stdout: cxResult.stdout, stderr: cxResult.stderr, exitCode: cxResult.exit_code };
      expect(cxExec.exitCode).toBe(0);
      expect(cxExec.stdout).toContain('codex:sk-e2e-relay-codex');
      expect(cxExec.stdout).toContain('openai-env-ok');

      const revoked = await session.client.request<{ revoked: boolean }>('authrelay.revoke', { token }).then((r) => r.revoked);
      expect(revoked).toBe(true);

      await expect(
        session.client.request<{ stdout: string; stderr: string; exit_code: number }>('exec.exec', {
          workspaceId: ws.id,
          command: 'sh',
          args: ['-lc', 'echo should-not-run'],
          options: { authRelayToken: token },
        })
      ).rejects.toThrow(/invalid auth relay token/i);
    } finally {
      if (ws) {
        await session.client.workspaces.remove(ws.id);
      }
      await session.stop();
      await cleanupFixture(fixture);
    }
  });
});
