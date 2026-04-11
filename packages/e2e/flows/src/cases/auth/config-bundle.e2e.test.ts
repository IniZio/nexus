import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import { buildConfigBundle } from '@nexus/sdk';
import { createGitFixture, cleanupFixture } from '../../harness/fixtures';
import { startSession, type DaemonSession } from '../../harness/session';
import { onDaemonStartError, onWorkspaceCreateRuntimeError } from '../../harness/assertions';
import type { WorkspaceHandle } from '@nexus/sdk';

const RUNTIME = process.env.NEXUS_E2E_RUNTIME ?? 'seatbelt';

describe(`config bundle e2e (${RUNTIME})`, () => {
  it('extracts bundle credential files into workspace $HOME', async () => {
    const fixture = await createGitFixture('config-bundle-e2e');
    let session: DaemonSession;
    try {
      session = await startSession();
    } catch (error) {
      await cleanupFixture(fixture);
      onDaemonStartError(error, 'unable to start daemon session');
      return;
    }

    const fakeHome = fs.mkdtempSync(path.join(os.tmpdir(), 'nexus-e2e-bundle-'));
    let ws: WorkspaceHandle | null = null;
    try {
      fs.mkdirSync(path.join(fakeHome, '.codex'), { recursive: true });
      fs.writeFileSync(
        path.join(fakeHome, '.codex', 'auth.json'),
        JSON.stringify({ token: 'bundle-e2e-sentinel-token' }),
      );
      fs.mkdirSync(path.join(fakeHome, '.codex', 'skills'), { recursive: true });
      fs.writeFileSync(
        path.join(fakeHome, '.codex', 'skills', 'test-skill.md'),
        '# Test Skill\nThis verifies skill directories are bundled.',
      );

      const bundle = await buildConfigBundle(fakeHome);
      expect(bundle).toBeTruthy();

      try {
        ws = await session.client.workspaces.create({
          repo: fixture.repoDir,
          workspaceName: 'config-bundle-e2e',
          agentProfile: 'default',
          configBundle: bundle,
        });
      } catch (error) {
        if (onWorkspaceCreateRuntimeError(error, 'config bundle runtime path unavailable in this environment')) {
          return;
        }
        throw error;
      }

      const authResult = await ws.exec('sh', ['-lc', 'cat ~/.codex/auth.json']);
      expect(authResult.exitCode).toBe(0);
      expect(authResult.stdout).toContain('bundle-e2e-sentinel-token');

      const skillResult = await ws.exec('sh', ['-lc', 'cat ~/.codex/skills/test-skill.md']);
      expect(skillResult.exitCode).toBe(0);
      expect(skillResult.stdout).toContain('Test Skill');
    } finally {
      if (ws) {
        await session.client.workspaces.remove(ws.id).catch(() => {});
      }
      fs.rmSync(fakeHome, { recursive: true, force: true });
      await session.stop().catch(() => {});
      await cleanupFixture(fixture);
    }
  }, 120_000);

  it('creates workspace cleanly when bundle is empty string', async () => {
    const fixture = await createGitFixture('config-bundle-empty-e2e');
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
      try {
        ws = await session.client.workspaces.create({
          repo: fixture.repoDir,
          workspaceName: 'config-bundle-empty-e2e',
          agentProfile: 'default',
          configBundle: '',
        });
      } catch (error) {
        if (onWorkspaceCreateRuntimeError(error, 'config bundle empty runtime path unavailable')) {
          return;
        }
        throw error;
      }

      const result = await ws.exec('sh', ['-lc', 'echo workspace-ok']);
      expect(result.stdout.trim()).toBe('workspace-ok');
    } finally {
      if (ws) {
        await session.client.workspaces.remove(ws.id).catch(() => {});
      }
      await session.stop().catch(() => {});
      await cleanupFixture(fixture);
    }
  }, 120_000);

  it('creates workspace cleanly without any configBundle field', async () => {
    const fixture = await createGitFixture('config-bundle-absent-e2e');
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
      try {
        ws = await session.client.workspaces.create({
          repo: fixture.repoDir,
          workspaceName: 'config-bundle-absent-e2e',
          agentProfile: 'default',
        });
      } catch (error) {
        if (onWorkspaceCreateRuntimeError(error, 'config bundle absent runtime path unavailable')) {
          return;
        }
        throw error;
      }

      const result = await ws.exec('sh', ['-lc', 'echo workspace-ok']);
      expect(result.stdout.trim()).toBe('workspace-ok');
    } finally {
      if (ws) {
        await session.client.workspaces.remove(ws.id).catch(() => {});
      }
      await session.stop().catch(() => {});
      await cleanupFixture(fixture);
    }
  }, 120_000);
});
