import path from 'node:path';
import fs from 'node:fs/promises';
import os from 'node:os';
import { spawn } from 'node:child_process';

export type TempFixture = {
  rootDir: string;
};

export type GitFixture = TempFixture & {
  repoDir: string;
};

export async function createTempFixture(prefix = 'nexus-e2e-sdk-runtime'): Promise<TempFixture> {
  const rootDir = await fs.mkdtemp(path.join(os.tmpdir(), `${prefix}-`));
  return { rootDir };
}

export async function cleanupFixture(fixture: TempFixture): Promise<void> {
  await fs.rm(fixture.rootDir, { recursive: true, force: true });
}

export async function createGitFixture(prefix = 'nexus-e2e-git-fixture'): Promise<GitFixture> {
  const rootDir = await fs.mkdtemp(path.join(os.tmpdir(), `${prefix}-`));
  const repoDir = path.join(rootDir, 'repo');
  await fs.mkdir(repoDir, { recursive: true });

  await runCmd('git', ['init'], repoDir);
  await runCmd('git', ['config', 'user.email', 'nexus-e2e@example.test'], repoDir);
  await runCmd('git', ['config', 'user.name', 'Nexus E2E'], repoDir);

  await fs.writeFile(path.join(repoDir, 'README.md'), '# fixture\n', 'utf8');
  await runCmd('git', ['add', '.'], repoDir);
  await runCmd('git', ['commit', '-m', 'init fixture'], repoDir);

  await fs.mkdir(path.join(repoDir, '.nexus'), { recursive: true });
  await fs.writeFile(path.join(repoDir, '.nexus', 'workspace.json'), '{"version":1}\n', 'utf8');

  return { rootDir, repoDir };
}

export async function runCmd(command: string, args: string[], cwd: string): Promise<{ stdout: string; stderr: string }> {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, { cwd, stdio: ['ignore', 'pipe', 'pipe'] });
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
