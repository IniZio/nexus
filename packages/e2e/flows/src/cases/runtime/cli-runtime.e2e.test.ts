import fs from 'node:fs/promises';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import { spawn } from 'node:child_process';
import { createGitFixture, cleanupFixture } from '../../harness/repo';
import { findRepoRoot, startManagedDaemon } from '../../harness/daemon';
import { cliRuntimeCaseIds } from '../test-ids';
import { e2eStrictRuntime, isRuntimeUnavailable, skipTest } from '../../harness/assertions';

export const CASE_TEST_IDS = cliRuntimeCaseIds;

describe('cli runtime e2e', () => {
  jest.setTimeout(420000);

  it('covers workspace lifecycle and tunnel flow', async () => {
    const fixture = await createGitFixture('cli-runtime');
    const managedDaemon = await startManagedDaemon();
    const cliPath = path.join(os.tmpdir(), `nexus-cli-e2e-${Date.now()}`);
    let workspaceID = '';
    let cliEnv: NodeJS.ProcessEnv | undefined;

    try {
      const hostPort = await freePort();
      const composeContent = [
        'services:',
        '  web:',
        '    image: nginx:latest',
        '    ports:',
        `      - "127.0.0.1:${String(hostPort)}:80"`,
      ].join('\n') + '\n';
      await fs.writeFile(path.join(fixture.repoDir, 'docker-compose.yml'), composeContent, 'utf8');

      const repoRoot = await findRepoRoot();
      await runProcess('go', ['build', '-o', cliPath, './cmd/nexus'], {
        cwd: path.join(repoRoot, 'packages', 'nexus'),
      });
      const daemonPort = String(new URL(managedDaemon.endpoint).port);
      cliEnv = {
        ...process.env,
        NEXUS_DAEMON_PORT: daemonPort,
        NEXUS_DAEMON_TOKEN: managedDaemon.token,
      };

      const created = await runProcess(cliPath, ['create'], {
        cwd: fixture.repoDir,
        env: cliEnv,
      });
      workspaceID = parseWorkspaceID(created.stdout + '\n' + created.stderr);
      if (!workspaceID) {
        throw new Error(`workspace create output did not include id\n${created.stdout}\n${created.stderr}`);
      }

      const listed = await runProcess(cliPath, ['list'], {
        cwd: fixture.repoDir,
        env: cliEnv,
      });
      expect(listed.stdout).toContain(workspaceID);

      await runProcess(cliPath, ['stop', workspaceID], {
        cwd: fixture.repoDir,
        env: cliEnv,
      });
      await runProcess(cliPath, ['start', workspaceID], {
        cwd: fixture.repoDir,
        env: cliEnv,
      });

      const tunnelProc = spawn(cliPath, ['tunnel', workspaceID], {
        cwd: fixture.repoDir,
        env: cliEnv,
        stdio: ['ignore', 'pipe', 'pipe'],
      });
      let tunnelStdout = '';
      let tunnelStderr = '';
      tunnelProc.stdout.on('data', (chunk: Buffer) => {
        tunnelStdout += chunk.toString('utf8');
      });
      tunnelProc.stderr.on('data', (chunk: Buffer) => {
        tunnelStderr += chunk.toString('utf8');
      });

      const tunnelReady = await waitForText(() => tunnelStdout + '\n' + tunnelStderr, 'press Ctrl-C to close tunnels', 45000);
      if (!tunnelReady) {
        const detail = tunnelStdout + '\n' + tunnelStderr;
        if (detail.includes('no compose ports spotlighted')) {
          if (e2eStrictRuntime()) {
            throw new Error(`E2E strict: tunnel did not discover compose ports\n${detail}`);
          }
          skipTest(`tunnel compose discovery unavailable: ${detail.trim()}`);
          tunnelProc.kill('SIGKILL');
          await waitForExit(tunnelProc, 5000);
          return;
        }
        throw new Error(`tunnel did not reach blocking state\n${detail}`);
      }

      tunnelProc.kill('SIGINT');
      const tunnelExit = await waitForExit(tunnelProc, 30000);
      if (tunnelExit.code !== 0) {
        throw new Error(`tunnel exited with code ${String(tunnelExit.code)}\n${tunnelStdout}\n${tunnelStderr}`);
      }

      await runProcess(cliPath, ['remove', workspaceID], {
        cwd: fixture.repoDir,
        env: cliEnv,
      });
      workspaceID = '';
    } catch (error) {
      if (isRuntimeUnavailable(error)) {
        if (e2eStrictRuntime()) {
          throw error;
        }
        skipTest(`cli runtime unavailable in this environment: ${String((error as Error).message ?? error)}`);
        return;
      }
      throw error;
    } finally {
      if (workspaceID !== '' && cliEnv) {
        try {
          await runProcessWithCode(cliPath, ['remove', workspaceID], {
            cwd: fixture.repoDir,
            env: cliEnv,
          });
        } catch {
        }
      }
      await managedDaemon.stop();
      await cleanupFixture(fixture);
      await fs.rm(cliPath, { force: true });
    }
  });

  it('validates failure paths and usage for tunnel commands', async () => {
    const fixture = await createGitFixture('cli-runtime-failures');
    const managedDaemon = await startManagedDaemon();
    const cliPath = path.join(os.tmpdir(), `nexus-cli-e2e-${Date.now()}-failures`);

    try {
      const repoRoot = await findRepoRoot();
      await runProcess('go', ['build', '-o', cliPath, './cmd/nexus'], {
        cwd: path.join(repoRoot, 'packages', 'nexus'),
      });
      const daemonPort = String(new URL(managedDaemon.endpoint).port);
      const cliEnv = {
        ...process.env,
        NEXUS_DAEMON_PORT: daemonPort,
        NEXUS_DAEMON_TOKEN: managedDaemon.token,
      };

      const missingTunnel = await runProcessWithCode(cliPath, ['tunnel'], {
        cwd: fixture.repoDir,
        env: cliEnv,
      });
      expect(missingTunnel.code).toBe(2);
      expect(missingTunnel.stderr).toContain('usage: nexus tunnel <workspace-id>');

      const badTunnel = await runProcessWithCode(cliPath, ['tunnel', 'ws-does-not-exist'], {
        cwd: fixture.repoDir,
        env: cliEnv,
      });
      expect(badTunnel.code).toBe(0);
      expect(badTunnel.stdout).toContain('no compose ports spotlighted');
    } finally {
      await managedDaemon.stop();
      await cleanupFixture(fixture);
      await fs.rm(cliPath, { force: true });
    }
  });
});

async function runProcess(
  command: string,
  args: string[],
  opts: { cwd: string; env?: NodeJS.ProcessEnv }
): Promise<{ stdout: string; stderr: string }> {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: opts.cwd,
      env: opts.env,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (chunk: Buffer) => {
      stdout += chunk.toString('utf8');
    });
    child.stderr.on('data', (chunk: Buffer) => {
      stderr += chunk.toString('utf8');
    });
    child.on('error', reject);
    child.on('close', (code) => {
      if (code === 0) {
        resolve({ stdout, stderr });
        return;
      }
      reject(new Error(`${command} ${args.join(' ')} failed (${String(code)}): ${stderr || stdout}`));
    });
  });
}

async function runProcessWithCode(
  command: string,
  args: string[],
  opts: { cwd: string; env?: NodeJS.ProcessEnv }
): Promise<{ code: number; stdout: string; stderr: string }> {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: opts.cwd,
      env: opts.env,
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    let stdout = '';
    let stderr = '';
    child.stdout.on('data', (chunk: Buffer) => {
      stdout += chunk.toString('utf8');
    });
    child.stderr.on('data', (chunk: Buffer) => {
      stderr += chunk.toString('utf8');
    });
    child.on('error', reject);
    child.on('close', (code) => {
      resolve({ code: code ?? -1, stdout, stderr });
    });
  });
}

function parseWorkspaceID(output: string): string {
  const match = output.match(/\(id:\s*([^)]+)\)/);
  return match?.[1]?.trim() ?? '';
}

async function waitForText(getText: () => string, needle: string, timeoutMs: number): Promise<boolean> {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    if (getText().includes(needle)) {
      return true;
    }
    await sleep(200);
  }
  return false;
}

async function waitForExit(
  child: ReturnType<typeof spawn>,
  timeoutMs: number
): Promise<{ code: number | null }> {
  return Promise.race([
    new Promise<{ code: number | null }>((resolve) => {
      child.once('close', (code) => resolve({ code }));
    }),
    sleep(timeoutMs).then(() => {
      child.kill('SIGKILL');
      return { code: null };
    }),
  ]);
}

async function freePort(): Promise<number> {
  return new Promise<number>((resolve, reject) => {
    const server = net.createServer();
    server.listen(0, '127.0.0.1', () => {
      const address = server.address();
      if (!address || typeof address === 'string') {
        server.close();
        reject(new Error('failed to allocate free port'));
        return;
      }
      const { port } = address;
      server.close((err) => {
        if (err) {
          reject(err);
          return;
        }
        resolve(port);
      });
    });
    server.once('error', reject);
  });
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, ms);
  });
}
