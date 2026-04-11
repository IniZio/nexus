import fs from 'node:fs/promises';
import path from 'node:path';
import { createGitFixture, cleanupFixture } from '../../harness/repo';
import { startSession } from '../../harness/session';
import {
  assertCapabilityOrSkip,
  e2eStrictRuntime,
  onWorkspaceCreateRuntimeError,
  skipTest,
} from '../../harness/assertions';
import { spotlightComposeCaseIds } from '../test-ids';

export const CASE_TEST_IDS = spotlightComposeCaseIds;

describe('spotlight compose e2e', () => {
  it('applies compose ports, then lists and closes a forward', async () => {
    const fixture = await createGitFixture('spotlight-compose');
    const composePath = path.join(fixture.repoDir, 'docker-compose.yml');
    await fs.writeFile(
      composePath,
      [
        'services:',
        '  web:',
        '    image: nginx:latest',
        '    ports:',
        '      - "127.0.0.1:18080:80"',
      ].join('\n') + '\n',
      'utf8'
    );

    const session = await startSession();
    let workspaceId = '';
    try {
      const caps = await session.client.workspaces.capabilities();
      if (!assertCapabilityOrSkip(caps, 'spotlight.tunnel', 'spotlight.tunnel capability unavailable on this daemon')) {
        return;
      }

      let handle;
      try {
        handle = await session.client.workspaces.create({
          repo: fixture.repoDir,
          workspaceName: 'spotlight-case',
          agentProfile: 'default',
        });
      } catch (error) {
        if (onWorkspaceCreateRuntimeError(error, 'spotlight compose runtime path unavailable in this environment')) {
          return;
        }
        throw error;
      }
      workspaceId = handle.id;

      const applied = await handle.tunnel.applyComposePorts();
      if (applied.forwards.length === 0 && applied.errors.length > 0) {
        const detail = applied.errors[0].message;
        if (e2eStrictRuntime()) {
          throw new Error(`E2E strict: compose discovery failed: ${detail}`);
        }
        skipTest(`compose discovery unavailable in environment: ${detail}`);
        return;
      }

      const list = await handle.tunnel.list();
      expect(list.forwards.length).toBeGreaterThan(0);
      const webForward = list.forwards.find((fwd) => fwd.localPort === 18080);
      expect(webForward).toBeDefined();

      const closed = await webForward!.stop();
      expect(closed).toBe(true);

      const afterClose = await handle.tunnel.list();
      expect(afterClose.forwards.some((fwd) => fwd.id === webForward!.id)).toBe(false);
    } finally {
      if (workspaceId !== '') {
        await session.client.workspaces.remove(workspaceId);
      }
      await session.stop();
      await cleanupFixture(fixture);
    }
  });
});
