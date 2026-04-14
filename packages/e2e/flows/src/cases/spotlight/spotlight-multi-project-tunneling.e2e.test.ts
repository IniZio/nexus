import type { NodeInfo } from '@nexus/sdk';
import { createGitFixture, cleanupFixture } from '../../harness/repo';
import { startSession } from '../../harness/session';
import {
  assertCapabilityOrSkip,
  onWorkspaceCreateRuntimeError,
} from '../../harness/assertions';

export const spotlightMultiProjectTunnelingCaseIds = {
  multipleProjectsSeparateTunnels: 'spotlight.multi-project.multiple-projects-separate-tunnels',
  workspacePortIsolation: 'spotlight.multi-project.workspace-port-isolation',
  tunnelLifecycleIndependence: 'spotlight.multi-project.tunnel-lifecycle-independence',
} as const;

export const CASE_TEST_IDS = spotlightMultiProjectTunnelingCaseIds;

describe('spotlight multi-project tunneling e2e', () => {
  it('supports tunnels in multiple projects simultaneously', async () => {
    const project1 = await createGitFixture('multi-project-1');
    const project2 = await createGitFixture('multi-project-2');

    const session = await startSession();
    let workspace1Id = '';
    let workspace2Id = '';

    try {
      const { capabilities: caps } = await session.client.request<NodeInfo>('node.info', {});
      if (!assertCapabilityOrSkip(caps, 'spotlight.tunnel', 'spotlight.tunnel capability unavailable on this daemon')) {
        return;
      }

      let handle1, handle2;
      try {
        handle1 = await session.client.workspaces.create({
          repo: project1.repoDir,
          workspaceName: 'project-1-workspace',
          agentProfile: 'default',
        });
        handle2 = await session.client.workspaces.create({
          repo: project2.repoDir,
          workspaceName: 'project-2-workspace',
          agentProfile: 'default',
        });
      } catch (error) {
        if (onWorkspaceCreateRuntimeError(error, 'multi-project test requires runtime environment')) {
          return;
        }
        throw error;
      }

      workspace1Id = handle1.id;
      workspace2Id = handle2.id;

      const forward1 = await handle1.tunnel.add({
        service: 'project-1-service',
        remotePort: 11001,
        localPort: 11001,
      });

      const forward2 = await handle2.tunnel.add({
        service: 'project-2-service',
        remotePort: 12001,
        localPort: 12001,
      });

      const list1 = await handle1.tunnel.list();
      const list2 = await handle2.tunnel.list();

      expect(list1.forwards).toHaveLength(1);
      expect(list2.forwards).toHaveLength(1);

      expect(list1.forwards[0].id).toBe(forward1.id);
      expect(list2.forwards[0].id).toBe(forward2.id);

      expect(list1.forwards[0].localPort).toBe(11001);
      expect(list2.forwards[0].localPort).toBe(12001);

      const crossCheck = list2.forwards.find(f => f.id === forward1.id);
      expect(crossCheck).toBeUndefined();

      await handle1.tunnel.stop(forward1.id);

      const list1After = await handle1.tunnel.list();
      const list2After = await handle2.tunnel.list();

      expect(list1After.forwards).toHaveLength(0);
      expect(list2After.forwards).toHaveLength(1);
      expect(list2After.forwards[0].id).toBe(forward2.id);

      await handle2.tunnel.stop(forward2.id);

      const list2Final = await handle2.tunnel.list();
      expect(list2Final.forwards).toHaveLength(0);
    } finally {
      if (workspace1Id !== '') {
        await session.client.workspaces.remove(workspace1Id);
      }
      if (workspace2Id !== '') {
        await session.client.workspaces.remove(workspace2Id);
      }
      await session.stop();
      await cleanupFixture(project1);
      await cleanupFixture(project2);
    }
  }, 30000);

  it('supports multiple workspaces in same project with independent tunnels', async () => {
    const project = await createGitFixture('same-project-multi-workspace');

    const session = await startSession();
    let workspace1Id = '';
    let workspace2Id = '';

    try {
      const { capabilities: caps } = await session.client.request<NodeInfo>('node.info', {});
      if (!assertCapabilityOrSkip(caps, 'spotlight.tunnel', 'spotlight.tunnel capability unavailable on this daemon')) {
        return;
      }

      let handle1, handle2;
      try {
        handle1 = await session.client.workspaces.create({
          repo: project.repoDir,
          workspaceName: 'workspace-1',
          agentProfile: 'default',
        });
        handle2 = await session.client.workspaces.create({
          repo: project.repoDir,
          workspaceName: 'workspace-2',
          agentProfile: 'default',
        });
      } catch (error) {
        if (onWorkspaceCreateRuntimeError(error, 'multi-workspace test requires runtime environment')) {
          return;
        }
        throw error;
      }

      workspace1Id = handle1.id;
      workspace2Id = handle2.id;

      expect(workspace1Id).not.toBe(workspace2Id);

      const forward1a = await handle1.tunnel.add({
        service: 'ws1-service-a',
        remotePort: 13001,
        localPort: 13001,
      });

      const forward1b = await handle1.tunnel.add({
        service: 'ws1-service-b',
        remotePort: 13002,
        localPort: 13002,
      });

      const forward2 = await handle2.tunnel.add({
        service: 'ws2-service',
        remotePort: 14001,
        localPort: 14001,
      });

      const list1 = await handle1.tunnel.list();
      const list2 = await handle2.tunnel.list();

      expect(list1.forwards).toHaveLength(2);
      expect(list1.forwards.map(f => f.id).sort()).toEqual([forward1a.id, forward1b.id].sort());

      expect(list2.forwards).toHaveLength(1);
      expect(list2.forwards[0].id).toBe(forward2.id);

      await handle1.tunnel.stop(forward1a.id);

      const list1After = await handle1.tunnel.list();
      expect(list1After.forwards).toHaveLength(1);
      expect(list1After.forwards[0].id).toBe(forward1b.id);

      const list2After = await handle2.tunnel.list();
      expect(list2After.forwards).toHaveLength(1);

      await handle1.tunnel.stop(forward1b.id);
      await handle2.tunnel.stop(forward2.id);

      const list1Final = await handle1.tunnel.list();
      const list2Final = await handle2.tunnel.list();

      expect(list1Final.forwards).toHaveLength(0);
      expect(list2Final.forwards).toHaveLength(0);
    } finally {
      if (workspace1Id !== '') {
        await session.client.workspaces.remove(workspace1Id);
      }
      if (workspace2Id !== '') {
        await session.client.workspaces.remove(workspace2Id);
      }
      await session.stop();
      await cleanupFixture(project);
    }
  }, 30000);

  it('verifies tunnel listing returns correct metadata', async () => {
    const project = await createGitFixture('tunnel-metadata');

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
          repo: project.repoDir,
          workspaceName: 'metadata-test-workspace',
          agentProfile: 'default',
        });
      } catch (error) {
        if (onWorkspaceCreateRuntimeError(error, 'metadata test requires runtime environment')) {
          return;
        }
        throw error;
      }

      workspaceId = handle.id;

      const forward = await handle.tunnel.add({
        service: 'metadata-test-service',
        remotePort: 15001,
        localPort: 15001,
      });

      expect(forward.id).toBeDefined();
      expect(forward.workspaceId).toBe(workspaceId);
      expect(forward.service).toBe('metadata-test-service');
      expect(forward.remotePort).toBe(15001);
      expect(forward.localPort).toBe(15001);
      expect(forward.createdAt).toBeDefined();

      const list = await handle.tunnel.list();
      expect(list.forwards).toHaveLength(1);

      const listed = list.forwards[0];
      expect(listed.id).toBe(forward.id);
      expect(listed.workspaceId).toBe(workspaceId);
      expect(listed.service).toBe('metadata-test-service');
      expect(listed.remotePort).toBe(15001);
      expect(listed.localPort).toBe(15001);

      await handle.tunnel.stop(forward.id);
    } finally {
      if (workspaceId !== '') {
        await session.client.workspaces.remove(workspaceId);
      }
      await session.stop();
      await cleanupFixture(project);
    }
  }, 20000);
});
