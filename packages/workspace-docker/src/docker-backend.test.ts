import { describe, it, expect, vi, beforeEach } from 'vitest';
import { DockerBackend } from '../src/docker-backend.js';
import { DockerClient } from '../src/docker-client.js';
import { PortManager } from '../src/port-manager.js';
import { LifecycleManager } from '../src/lifecycle-manager.js';
import { FileSync } from '../src/file-sync.js';

vi.mock('../src/docker-client.js');
vi.mock('../src/lifecycle-manager.js');
vi.mock('../src/port-manager.js');
vi.mock('../src/file-sync.js');

describe('DockerBackend', () => {
  let backend: DockerBackend;
  let mockDocker: DockerClient;
  let mockLifecycle: LifecycleManager;
  let mockPortManager: PortManager;
  let mockFileSync: FileSync;

  beforeEach(() => {
    mockDocker = {
      pullImage: vi.fn().mockResolvedValue(undefined),
      createContainer: vi.fn().mockResolvedValue('container-123'),
      startContainer: vi.fn().mockResolvedValue(undefined),
      stopContainer: vi.fn().mockResolvedValue(undefined),
      removeContainer: vi.fn().mockResolvedValue(undefined),
      inspectContainer: vi.fn().mockResolvedValue({ id: 'container-123', state: 'running' }),
    } as unknown as DockerClient;

    mockLifecycle = {
      waitForHealthy: vi.fn().mockResolvedValue(undefined),
    } as unknown as LifecycleManager;

    mockPortManager = {
      allocatePorts: vi.fn().mockResolvedValue([
        { name: 'main', protocol: 'tcp', containerPort: 3000, hostPort: 32800, visibility: 'private' },
      ]),
      releasePorts: vi.fn().mockResolvedValue(undefined),
    } as unknown as PortManager;

    mockFileSync = {
      syncToContainer: vi.fn().mockResolvedValue(undefined),
    } as unknown as FileSync;

    backend = new DockerBackend({
      stateDir: '/tmp/nexus-test',
    });

    (backend as unknown as { docker: DockerClient }).docker = mockDocker;
    (backend as unknown as { lifecycle: LifecycleManager }).lifecycle = mockLifecycle;
    (backend as unknown as { portManager: PortManager }).portManager = mockPortManager;
    (backend as unknown as { fileSync: FileSync }).fileSync = mockFileSync;
  });

  describe('createWorkspace', () => {
    it('should create a workspace with container', async () => {
      const config = {
        image: 'node:18',
        env: { NODE_ENV: 'development' },
        envFiles: [],
        volumes: [],
        services: [],
        hooks: {},
        ide: { default: 'vscode' as const, extensions: [], settings: {} },
        idleTimeout: 30,
        shutdownBehavior: 'stop' as const,
      };

      const workspace = await backend.createWorkspace('ws-1', 'test-workspace', config, '/tmp/test');

      expect(mockDocker.pullImage).toHaveBeenCalledWith('node:18');
      expect(mockDocker.createContainer).toHaveBeenCalled();
      expect(mockFileSync.syncToContainer).toHaveBeenCalled();
      expect(workspace.name).toBe('test-workspace');
      expect(workspace.status).toBe('stopped');
      expect(workspace.backend).toBe('docker');
    });

    it('should allocate ports for workspace', async () => {
      const config = {
        image: 'node:18',
        env: {},
        envFiles: [],
        volumes: [],
        services: [],
        hooks: {},
        ide: { default: 'vscode' as const, extensions: [], settings: {} },
        idleTimeout: 30,
        shutdownBehavior: 'stop' as const,
      };

      await backend.createWorkspace('ws-1', 'test-workspace', config, '/tmp/test');

      expect(mockPortManager.allocatePorts).toHaveBeenCalledWith('ws-1', config);
    });
  });

  describe('startWorkspace', () => {
    it('should start a stopped workspace', async () => {
      const workspace = {
        id: 'ws-1',
        name: 'test-workspace',
        status: 'stopped' as const,
        backend: 'docker' as const,
        backendConfig: { containerId: 'container-123', metadata: {} },
        repository: { url: '', provider: 'other' as const, localPath: '', defaultBranch: '', currentCommit: '' },
        branch: 'main',
        worktreePath: '/tmp/test',
        resources: { cpu: { cores: 2 }, memory: { bytes: 4e9 }, storage: { bytes: 20e9 } },
        ports: [],
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
        lastActiveAt: new Date().toISOString(),
        config: {} as never,
        labels: {},
        annotations: {},
      };

      const result = await backend.startWorkspace(workspace);

      expect(mockDocker.startContainer).toHaveBeenCalledWith('container-123');
      expect(mockLifecycle.waitForHealthy).toHaveBeenCalled();
      expect(result.status).toBe('running');
    });

    it('should throw error for invalid transition', async () => {
      const workspace = {
        id: 'ws-1',
        name: 'test-workspace',
        status: 'running' as const,
        backend: 'docker' as const,
        backendConfig: { containerId: 'container-123', metadata: {} },
        repository: { url: '', provider: 'other' as const, localPath: '', defaultBranch: '', currentCommit: '' },
        branch: 'main',
        worktreePath: '/tmp/test',
        resources: { cpu: { cores: 2 }, memory: { bytes: 4e9 }, storage: { bytes: 20e9 } },
        ports: [],
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
        lastActiveAt: new Date().toISOString(),
        config: {} as never,
        labels: {},
        annotations: {},
      };

      await expect(backend.startWorkspace(workspace)).rejects.toThrow();
    });
  });

  describe('stopWorkspace', () => {
    it('should stop a running workspace', async () => {
      const workspace = {
        id: 'ws-1',
        name: 'test-workspace',
        status: 'running' as const,
        backend: 'docker' as const,
        backendConfig: { containerId: 'container-123', metadata: {} },
        repository: { url: '', provider: 'other' as const, localPath: '', defaultBranch: '', currentCommit: '' },
        branch: 'main',
        worktreePath: '/tmp/test',
        resources: { cpu: { cores: 2 }, memory: { bytes: 4e9 }, storage: { bytes: 20e9 } },
        ports: [],
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
        lastActiveAt: new Date().toISOString(),
        config: {} as never,
        labels: {},
        annotations: {},
      };

      const result = await backend.stopWorkspace(workspace);

      expect(mockDocker.stopContainer).toHaveBeenCalledWith('container-123');
      expect(result.status).toBe('stopped');
    });
  });

  describe('deleteWorkspace', () => {
    it('should delete workspace and release ports', async () => {
      const workspace = {
        id: 'ws-1',
        name: 'test-workspace',
        status: 'stopped' as const,
        backend: 'docker' as const,
        backendConfig: { containerId: 'container-123', metadata: {} },
        repository: { url: '', provider: 'other' as const, localPath: '', defaultBranch: '', currentCommit: '' },
        branch: 'main',
        worktreePath: '/tmp/test',
        resources: { cpu: { cores: 2 }, memory: { bytes: 4e9 }, storage: { bytes: 20e9 } },
        ports: [],
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
        lastActiveAt: new Date().toISOString(),
        config: {} as never,
        labels: {},
        annotations: {},
      };

      await backend.deleteWorkspace(workspace);

      expect(mockDocker.stopContainer).toHaveBeenCalledWith('container-123', 5);
      expect(mockDocker.removeContainer).toHaveBeenCalledWith('container-123', true);
      expect(mockPortManager.releasePorts).toHaveBeenCalledWith('ws-1');
    });
  });

  describe('getWorkspaceStatus', () => {
    it('should return running status for running container', async () => {
      const workspace = {
        id: 'ws-1',
        name: 'test-workspace',
        status: 'running' as const,
        backend: 'docker' as const,
        backendConfig: { containerId: 'container-123', metadata: {} },
        repository: { url: '', provider: 'other' as const, localPath: '', defaultBranch: '', currentCommit: '' },
        branch: 'main',
        worktreePath: '/tmp/test',
        resources: { cpu: { cores: 2 }, memory: { bytes: 4e9 }, storage: { bytes: 20e9 } },
        ports: [],
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
        lastActiveAt: new Date().toISOString(),
        config: {} as never,
        labels: {},
        annotations: {},
      };

      const status = await backend.getWorkspaceStatus(workspace);

      expect(status).toBe('running');
    });

    it('should return error status when container not found', async () => {
      const workspace = {
        id: 'ws-1',
        name: 'test-workspace',
        status: 'running' as const,
        backend: 'docker' as const,
        backendConfig: { containerId: 'container-123', metadata: {} },
        repository: { url: '', provider: 'other' as const, localPath: '', defaultBranch: '', currentCommit: '' },
        branch: 'main',
        worktreePath: '/tmp/test',
        resources: { cpu: { cores: 2 }, memory: { bytes: 4e9 }, storage: { bytes: 20e9 } },
        ports: [],
        createdAt: new Date().toISOString(),
        updatedAt: new Date().toISOString(),
        lastActiveAt: new Date().toISOString(),
        config: {} as never,
        labels: {},
        annotations: {},
      };

      mockDocker.inspectContainer = vi.fn().mockRejectedValue(new Error('Not found'));

      const status = await backend.getWorkspaceStatus(workspace);

      expect(status).toBe('error');
    });
  });
});
