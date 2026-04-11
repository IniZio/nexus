import { WorkspaceClient } from '@nexus/sdk';
import fs from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { spawn, type ChildProcess } from 'node:child_process';

export type DaemonEnvConfig = {
  endpoint: string;
  token: string;
};

export type ManagedDaemon = {
  endpoint: string;
  token: string;
  workspaceDir: string;
  stop: () => Promise<void>;
};

export type ManagedDaemonOptions = {
  extraEnv?: Record<string, string>;
  workspaceDir?: string;
};

export function getDaemonEnvConfig(): DaemonEnvConfig | null {
  const endpoint = process.env.NEXUS_DAEMON_WS;
  const token = process.env.NEXUS_DAEMON_TOKEN;

  if (!endpoint || !token) {
    return null;
  }

  return { endpoint, token };
}

export async function connectSDKClient(config?: DaemonEnvConfig): Promise<WorkspaceClient> {
  const resolved = config ?? getDaemonEnvConfig();
  if (!resolved) {
    throw new Error('Missing daemon env. Set NEXUS_DAEMON_WS and NEXUS_DAEMON_TOKEN.');
  }

  const client = new WorkspaceClient({
    endpoint: resolved.endpoint,
    token: resolved.token,
    reconnect: false,
  });

  await client.connect();
  return client;
}

export async function startManagedDaemon(options: ManagedDaemonOptions = {}): Promise<ManagedDaemon> {
  const createdWorkspaceDir = !options.workspaceDir;
  const workspaceDir = options.workspaceDir ?? await fs.mkdtemp(path.join(os.tmpdir(), 'nexus-e2e-daemon-'));
  const repoRoot = await findRepoRoot();
  const daemonCwd = path.join(repoRoot, 'packages', 'nexus');
  const port = await getFreePort();
  const token = `e2e-token-${Date.now()}`;

  const env: NodeJS.ProcessEnv = {
    ...process.env,
    ...(options.extraEnv ?? {}),
  };

  const child = spawn('go', ['run', './cmd/daemon', '--port', String(port), '--workspace-dir', workspaceDir, '--token', token], {
    cwd: daemonCwd,
    env,
    stdio: 'ignore',
  });

  try {
    await waitForHealth(port, child, 40000);
  } catch (error) {
    await stopProcess(child);
    if (createdWorkspaceDir) {
      await fs.rm(workspaceDir, { recursive: true, force: true });
    }
    throw error;
  }

  return {
    endpoint: `ws://127.0.0.1:${port}`,
    token,
    workspaceDir,
    stop: async () => {
      await stopProcess(child);
      if (createdWorkspaceDir) {
        await fs.rm(workspaceDir, { recursive: true, force: true });
      }
    },
  };
}

async function waitForHealth(port: number, child: ChildProcess, timeoutMs: number): Promise<void> {
  const startedAt = Date.now();
  while (Date.now() - startedAt < timeoutMs) {
    if (child.exitCode !== null) {
      throw new Error(`daemon exited early with code ${String(child.exitCode)}`);
    }
    try {
      const response = await fetch(`http://127.0.0.1:${port}/healthz`);
      if (response.ok) {
        return;
      }
    } catch {
    }
    await sleep(400);
  }
  throw new Error(`timed out waiting for daemon healthz on port ${String(port)}`);
}

async function stopProcess(child: ChildProcess): Promise<void> {
  if (child.exitCode !== null) {
    return;
  }

  child.kill('SIGTERM');
  const exitedOnTerm = await Promise.race([
    new Promise<boolean>((resolve) => child.once('exit', () => resolve(true))),
    sleep(5000).then(() => false),
  ]);

  if (exitedOnTerm || child.exitCode !== null) {
    return;
  }

  child.kill('SIGKILL');
  await Promise.race([
    new Promise<void>((resolve) => child.once('exit', () => resolve())),
    sleep(3000),
  ]);
}

async function findRepoRoot(): Promise<string> {
  let current = process.cwd();
  for (let i = 0; i < 8; i += 1) {
    const marker = path.join(current, 'pnpm-workspace.yaml');
    try {
      await fs.access(marker);
      return current;
    } catch {
    }
    const parent = path.dirname(current);
    if (parent === current) {
      break;
    }
    current = parent;
  }
  throw new Error('unable to locate repo root (pnpm-workspace.yaml)');
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, ms);
  });
}

async function getFreePort(): Promise<number> {
  const net = await import('node:net');
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
