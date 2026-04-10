import fs from 'node:fs/promises';
import path from 'node:path';
import { createGitFixture, cleanupFixture } from '../harness/fixtures';
import { startSession } from '../harness/session';
import { isLinuxTapUnsupported, skipTest } from '../harness/assertions';
import { spotlightComposeCaseIds } from './test-ids';

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
      const caps = await session.client.workspace.capabilities();
      const spotlightCap = caps.find((cap) => cap.name === 'spotlight.tunnel');
      if (!spotlightCap?.available) {
        skipTest('spotlight.tunnel capability unavailable on this daemon');
        return;
      }

      let handle;
      try {
        handle = await session.client.workspace.create({
          repo: fixture.repoDir,
          workspaceName: 'spotlight-case',
          agentProfile: 'default',
        });
      } catch (error) {
        if (isLinuxTapUnsupported(error)) {
          skipTest('spotlight compose requires Linux TAP support in this environment');
          return;
        }
        throw error;
      }
      workspaceId = handle.id;

      const applied = await handle.spotlight.applyComposePorts();
      if (applied.forwards.length === 0 && applied.errors.length > 0) {
        skipTest(`compose discovery unavailable in environment: ${applied.errors[0].message}`);
        return;
      }

      const list = await handle.spotlight.list();
      expect(list.forwards.length).toBeGreaterThan(0);
      const webForward = list.forwards.find((fwd) => fwd.localPort === 18080);
      expect(webForward).toBeDefined();

      const closed = await handle.spotlight.close(webForward!.id);
      expect(closed).toBe(true);

      const afterClose = await handle.spotlight.list();
      expect(afterClose.forwards.some((fwd) => fwd.id === webForward!.id)).toBe(false);
    } finally {
      if (workspaceId !== '') {
        await session.client.workspace.remove(workspaceId);
      }
      await session.stop();
      await cleanupFixture(fixture);
    }
  });
});
