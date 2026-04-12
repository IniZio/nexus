import type { NodeInfo } from '@nexus/sdk';
import { createGitFixture, cleanupFixture } from '../../harness/repo';
import { startSession } from '../../harness/session';
import {
  assertCapabilityOrSkip,
  e2eStrictRuntime,
  onWorkspaceCreateRuntimeError,
  skipTest,
} from '../../harness/assertions';

export const spotlightLivePortDetectionCaseIds = {
  autoDetectsNewPort: 'spotlight.live-port.auto-detects-new-port',
  cleanupRemovesStalePort: 'spotlight.live-port.cleanup-removes-stale-port',
  maintainsSourceField: 'spotlight.live-port.maintains-source-field',
} as const;

export const CASE_TEST_IDS = spotlightLivePortDetectionCaseIds;

describe('spotlight live port detection e2e', () => {
  it('sets correct source field for manual forwards', async () => {
    const fixture = await createGitFixture('live-port-source');
    const session = await startSession();
    let workspaceId = '';
    try {
      const { capabilities: caps } = await session.client.request<NodeInfo>('node.info', {});
      if (!assertCapabilityOrSkip(caps, 'spotlight.tunnel', 'spotlight.tunnel capability unavailable on this daemon')) {
        return;
      }

      let handle;
      try {
        handle = await session.client.workspaces.create({
          repo: fixture.repoDir,
          workspaceName: 'source-test-case',
          agentProfile: 'default',
        });
      } catch (error) {
        if (onWorkspaceCreateRuntimeError(error, 'source test requires runtime environment')) {
          return;
        }
        throw error;
      }
      workspaceId = handle.id;

      const manualForward = await handle.tunnel.add({
        service: 'manual-test',
        remotePort: 9001,
        localPort: 9001,
      });

      expect((manualForward as any).source).toBe('manual');

      const list = await handle.tunnel.list();
      const found = list.forwards.find(f => f.id === manualForward.id);
      expect(found).toBeDefined();
      expect((found as any).source).toBe('manual');

      await handle.tunnel.stop(manualForward.id);
    } finally {
      if (workspaceId !== '') {
        await session.client.workspaces.remove(workspaceId);
      }
      await session.stop();
      await cleanupFixture(fixture);
    }
  }, 20000);

  it('verifies port monitoring capability is available', async () => {
    const fixture = await createGitFixture('live-port-capability');
    const session = await startSession();
    let workspaceId = '';
    try {
      const { capabilities: caps } = await session.client.request<NodeInfo>('node.info', {});
      
      const hasTunnel = caps.some(c => c.name === 'spotlight.tunnel' && c.available);
      const hasSeatbelt = caps.some(c => c.name === 'runtime.seatbelt' && c.available);
      
      if (!hasTunnel) {
        skipTest('spotlight.tunnel capability unavailable on this daemon');
        return;
      }

      let handle;
      try {
        handle = await session.client.workspaces.create({
          repo: fixture.repoDir,
          workspaceName: 'live-port-capability-case',
          agentProfile: 'default',
        });
      } catch (error) {
        if (onWorkspaceCreateRuntimeError(error, 'capability test requires runtime environment')) {
          return;
        }
        throw error;
      }
      workspaceId = handle.id;

      await session.client.workspaces.start(workspaceId);
      
      const list = await handle.tunnel.list();
      expect(list.forwards).toBeDefined();
      expect(Array.isArray(list.forwards)).toBe(true);
      
      if (!hasSeatbelt && e2eStrictRuntime()) {
        throw new Error('E2E strict: runtime.seatbelt capability required for live port detection');
      }
      
      if (!hasSeatbelt) {
        skipTest('runtime.seatbelt unavailable - live port detection disabled');
      }
    } finally {
      if (workspaceId !== '') {
        try {
          await session.client.workspaces.remove(workspaceId);
        } catch {
          // Ignore cleanup errors
        }
      }
      await session.stop();
      await cleanupFixture(fixture);
    }
  }, 20000);
});
