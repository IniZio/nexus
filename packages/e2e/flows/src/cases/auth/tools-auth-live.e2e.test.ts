import { WorkspaceHandle, buildConfigBundle } from '@nexus/sdk';
import { createGitFixture, cleanupFixture } from '../../harness/repo';
import { startSession, type DaemonSession } from '../../harness/session';
import { onDaemonStartError, onWorkspaceCreateRuntimeError } from '../../harness/assertions';
import { liveToolsAuthCaseIds } from '../test-ids';

export const CASE_TEST_IDS = liveToolsAuthCaseIds;

const liveModelsEnabled = (): boolean => process.env.NEXUS_E2E_LIVE_MODELS === '1';

const trimEnv = (name: string): string => (process.env[name] ?? '').trim();

const shellQuote = (value: string): string => `'${value.replace(/'/g, `'\"'\"'`)}'`;

const timestampTag = (): string => String(Date.now());

type MinimaxAuth = {
  binding: string;
  value: string;
  model: string;
};

function minimaxAuthConfig(): MinimaxAuth | null {
  const opencodeKey = trimEnv('NEXUS_E2E_AUTH_OPENCODE_API_KEY');
  if (opencodeKey !== '') {
    return {
      binding: 'opencode',
      value: opencodeKey,
      model: trimEnv('NEXUS_E2E_OPENCODE_MINIMAX_MODEL') || 'minimax-coding-plan/MiniMax-M2.7-highspeed',
    };
  }

  const openrouterKey = trimEnv('NEXUS_E2E_AUTH_OPENROUTER_API_KEY');
  if (openrouterKey !== '') {
    return {
      binding: 'openrouter',
      value: openrouterKey,
      model: trimEnv('NEXUS_E2E_OPENCODE_MINIMAX_MODEL') || 'minimax-coding-plan/MiniMax-M2.7-highspeed',
    };
  }

  const minimaxKey = trimEnv('NEXUS_E2E_AUTH_MINIMAX_API_KEY');
  if (minimaxKey !== '') {
    return {
      binding: 'minimax',
      value: minimaxKey,
      model: trimEnv('NEXUS_E2E_OPENCODE_MINIMAX_MODEL') || 'minimax-coding-plan/MiniMax-M2.7-highspeed',
    };
  }

  return null;
}

describe('tools auth live e2e', () => {
  const liveIt = liveModelsEnabled() ? it : it.skip;

  liveIt('runs opencode (copilot + minimax) and codex exec with host-synced credentials', async () => {
    const githubToken = trimEnv('NEXUS_E2E_AUTH_GITHUB_TOKEN');
    const openaiKey = trimEnv('NEXUS_E2E_AUTH_OPENAI_API_KEY');
    const minimax = minimaxAuthConfig();
    const copilotModel = trimEnv('NEXUS_E2E_OPENCODE_COPILOT_MODEL') || 'github-copilot/gpt-5-mini';
    const minimaxModel = minimax?.model || trimEnv('NEXUS_E2E_OPENCODE_MINIMAX_MODEL') || 'minimax-coding-plan/MiniMax-M2.7-highspeed';
    const useRelay = githubToken !== '' && openaiKey !== '' && minimax !== null;
    const fixture = await createGitFixture('tools-auth-live');
    let session: DaemonSession;
    try {
      session = await startSession();
    } catch (error) {
      await cleanupFixture(fixture);
      onDaemonStartError(error, 'unable to start daemon session for live auth case');
      return;
    }

    let ws: WorkspaceHandle | null = null;
    try {
      const authBinding: Record<string, string> = {};
      if (useRelay && minimax) {
        authBinding.github = githubToken;
        authBinding.openai = openaiKey;
        authBinding[minimax.binding] = minimax.value;
      }

      let created;
      try {
        const configBundle = await buildConfigBundle().catch(() => '');
        created = await session.client.request<{ workspace: { id: string } }>('workspace.create', {
          spec: {
            repo: fixture.repoDir,
            workspaceName: 'tools-auth-live-case',
            agentProfile: 'default',
            authBinding,
            configBundle: configBundle || undefined,
          },
        });
      } catch (error) {
        if (onWorkspaceCreateRuntimeError(error, 'tools auth live runtime path unavailable in this environment')) {
          return;
        }
        throw error;
      }

      ws = await session.client.workspaces.start(created.workspace.id);
      expect(ws.id).toBe(created.workspace.id);

      const copilotMarker = `NEXUS-COPILOT-${timestampTag()}`;
      const copilotPrompt = `Reply with exactly: ${copilotMarker}`;
      const copilotCmd = `opencode run --format json -m ${shellQuote(copilotModel)} ${shellQuote(copilotPrompt)} 2>&1`;
      let copilotRun: { stdout: string; stderr: string; exitCode: number };
      if (useRelay) {
        const copilotToken = await session.client
          .request<{ token: string }>('authrelay.mint', {
            workspaceId: ws.id,
            binding: 'github',
            ttlSeconds: 180,
          })
          .then((r) => r.token);
        const copilotResult = await session.client.request<{ stdout: string; stderr: string; exit_code: number }>('exec.exec', {
          workspaceId: ws.id,
          command: 'sh',
          args: ['-lc', copilotCmd],
          options: { authRelayToken: copilotToken, timeout: 240 },
        });
        copilotRun = { stdout: copilotResult.stdout, stderr: copilotResult.stderr, exitCode: copilotResult.exit_code };
      } else {
        copilotRun = await ws.exec('sh', ['-lc', copilotCmd], { timeout: 240 });
      }
      const copilotOutput = `${copilotRun.stdout}\n${copilotRun.stderr}`.trim();
      expect(copilotRun.exitCode).toBe(0);
      expect(copilotOutput.length).toBeGreaterThan(0);
      expect(copilotOutput).toContain('"type":"text"');
      expect(copilotOutput).toContain(copilotMarker);

      const minimaxMarker = `NEXUS-MINIMAX-${timestampTag()}`;
      const minimaxPrompt = `Reply with exactly: ${minimaxMarker}`;
      const minimaxCmd = `opencode run --format json -m ${shellQuote(minimaxModel)} ${shellQuote(minimaxPrompt)} 2>&1`;
      let minimaxRun: { stdout: string; stderr: string; exitCode: number };
      if (useRelay) {
        const minimaxToken = await session.client
          .request<{ token: string }>('authrelay.mint', {
            workspaceId: ws.id,
            binding: minimax!.binding,
            ttlSeconds: 180,
          })
          .then((r) => r.token);
        const minimaxResult = await session.client.request<{ stdout: string; stderr: string; exit_code: number }>('exec.exec', {
          workspaceId: ws.id,
          command: 'sh',
          args: ['-lc', minimaxCmd],
          options: { authRelayToken: minimaxToken, timeout: 240 },
        });
        minimaxRun = { stdout: minimaxResult.stdout, stderr: minimaxResult.stderr, exitCode: minimaxResult.exit_code };
      } else {
        minimaxRun = await ws.exec('sh', ['-lc', minimaxCmd], { timeout: 240 });
      }
      const minimaxOutput = `${minimaxRun.stdout}\n${minimaxRun.stderr}`.trim();
      expect(minimaxRun.exitCode).toBe(0);
      expect(minimaxOutput.length).toBeGreaterThan(0);
      expect(minimaxOutput).toContain('"type":"text"');
      expect(minimaxOutput).toContain(minimaxMarker);

      const codexMarker = `NEXUS-CODEX-${timestampTag()}`;
      const codexPrompt = `Reply with exactly: ${codexMarker}`;
      const codexCmd = `codex exec --skip-git-repo-check ${shellQuote(codexPrompt)} 2>&1`;
      let codexRun: { stdout: string; stderr: string; exitCode: number };
      if (useRelay) {
        const codexToken = await session.client
          .request<{ token: string }>('authrelay.mint', {
            workspaceId: ws.id,
            binding: 'openai',
            ttlSeconds: 180,
          })
          .then((r) => r.token);
        const codexResult = await session.client.request<{ stdout: string; stderr: string; exit_code: number }>('exec.exec', {
          workspaceId: ws.id,
          command: 'sh',
          args: ['-lc', codexCmd],
          options: { authRelayToken: codexToken, timeout: 240 },
        });
        codexRun = { stdout: codexResult.stdout, stderr: codexResult.stderr, exitCode: codexResult.exit_code };
      } else {
        codexRun = await ws.exec('sh', ['-lc', codexCmd], { timeout: 240 });
      }
      const codexOutput = `${codexRun.stdout}\n${codexRun.stderr}`.trim();
      if (codexRun.exitCode !== 0) {
        throw new Error(`codex exec failed with exit ${codexRun.exitCode}: ${codexOutput}`);
      }
      expect(codexOutput.length).toBeGreaterThan(0);
      expect(codexOutput).toContain(codexMarker);
    } finally {
      if (ws) {
        await session.client.workspaces.remove(ws.id);
      }
      await session.stop();
      await cleanupFixture(fixture);
    }
  }, 300_000);
});
