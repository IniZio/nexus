import fs from 'node:fs/promises';
import path from 'node:path';
import { createGitFixture, cleanupFixture } from '../harness/fixtures';
import { startSession } from '../harness/session';
import { onWorkspaceCreateRuntimeError } from '../harness/assertions';
import { lifecycleHooksCaseIds } from './test-ids';

export const CASE_TEST_IDS = lifecycleHooksCaseIds;

describe('lifecycle hooks e2e', () => {
  it('runs setup/start/teardown in expected order across daemon start-stop', async () => {
    const fixture = await createGitFixture('lifecycle-hooks');
    const workspaceConfigDir = path.join(fixture.repoDir, '.nexus');
    const logPath = path.join(fixture.repoDir, 'lifecycle.log');

    await fs.mkdir(workspaceConfigDir, { recursive: true });
    await fs.writeFile(
      path.join(workspaceConfigDir, 'workspace.json'),
      JSON.stringify(
        {
          version: 1,
          lifecycle: {
            onSetup: [`printf "1-setup\\n" >> ${shellQuote(logPath)}`],
            onStart: [`printf "2-start\\n" >> ${shellQuote(logPath)}`],
            onTeardown: [`printf "3-teardown\\n" >> ${shellQuote(logPath)}`],
          },
        },
        null,
        2
      ),
      'utf8'
    );

    try {
      const session = await startSession({ forceManaged: true });
      let workspaceId = '';

      try {
        try {
          const ws = await session.client.workspaces.create({
            repo: fixture.repoDir,
            workspaceName: 'lifecycle-case',
            agentProfile: 'default',
          });
          workspaceId = ws.id;
        } catch (error) {
          if (onWorkspaceCreateRuntimeError(error, 'lifecycle hooks runtime path unavailable in this environment')) {
            return;
          }
          throw error;
        }

        if (workspaceId !== '') {
          await session.client.workspaces.remove(workspaceId);
          workspaceId = '';
        }

        const lines = await waitForLogLines(logPath, 3, 15000);
        expect(lines).toEqual(['1-setup', '2-start', '3-teardown']);
      } finally {
        if (workspaceId !== '') {
          await session.client.workspaces.remove(workspaceId);
        }
        await session.stop();
      }
    } finally {
      await cleanupFixture(fixture);
    }
  });
});

function shellQuote(value: string): string {
  return `'${value.split("'").join(`'"'"'`)}'`;
}

async function waitForLogLines(logPath: string, expectedCount: number, timeoutMs: number): Promise<string[]> {
  const startedAt = Date.now();

  while (Date.now() - startedAt < timeoutMs) {
    let raw = '';
    try {
      raw = await fs.readFile(logPath, 'utf8');
    } catch {
      raw = '';
    }

    const lines = raw
      .split('\n')
      .map((line) => line.trim())
      .filter((line) => line !== '');

    if (lines.length >= expectedCount) {
      return lines;
    }

    await new Promise((resolve) => setTimeout(resolve, 250));
  }

  const finalRaw = await fs.readFile(logPath, 'utf8').catch(() => '');
  return finalRaw
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => line !== '');
}
