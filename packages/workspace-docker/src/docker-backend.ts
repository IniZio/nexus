import {
  Workspace,
  WorkspaceConfig,
  WorkspaceStatus,
  WorkspaceStartError,
  WorkspaceInvalidTransitionError,
  ContainerError,
  isValidTransition,
} from '@nexus/workspace-core';
import { DockerClient, type ContainerSpec } from './docker-client.js';
import { LifecycleManager } from './lifecycle-manager.js';
import { PortManager } from './port-manager.js';
import { FileSync } from './file-sync.js';

export interface DockerBackendOptions {
  stateDir?: string;
  dockerSocket?: string;
  portRange?: { start: number; end: number };
}

export class DockerBackend {
  private readonly docker: DockerClient;
  private readonly lifecycle: LifecycleManager;
  private readonly portManager: PortManager;
  private readonly fileSync: FileSync;
  private readonly stateDir: string;

  constructor(options: DockerBackendOptions = {}) {
    this.stateDir = options.stateDir ?? '/var/lib/nexus/workspaces';
    this.docker = new DockerClient({ socketPath: options.dockerSocket });
    this.portManager = new PortManager(options.portRange);
    this.lifecycle = new LifecycleManager(this.docker);
    this.fileSync = new FileSync();
  }

  async createWorkspace(
    id: string,
    name: string,
    config: WorkspaceConfig,
    worktreePath: string,
  ): Promise<Workspace> {
    const ports = await this.portManager.allocatePorts(id, config);

    const containerSpec: ContainerSpec = {
      name: `nexus-${name}`,
      image: config.image,
      ports,
      env: this.buildEnvVars(config),
      volumes: config.volumes,
      resources: this.configToResources(config),
      workdir: '/workspace',
      labels: {
        'nexus.workspace': id,
        'nexus.workspace.name': name,
      },
    };

    await this.docker.pullImage(config.image);
    const containerId = await this.docker.createContainer(containerSpec);

    await this.fileSync.syncToContainer(worktreePath, containerId, config);

    const workspace: Workspace = {
      id,
      name,
      status: 'stopped',
      backend: 'docker',
      backendConfig: {
        containerId,
        metadata: {},
      },
      repository: { url: '', provider: 'other', localPath: worktreePath, defaultBranch: 'main', currentCommit: '' },
      branch: 'main',
      worktreePath,
      resources: this.configToResources(config),
      ports,
      createdAt: new Date().toISOString(),
      updatedAt: new Date().toISOString(),
      lastActiveAt: new Date().toISOString(),
      config,
      labels: containerSpec.labels!,
      annotations: {},
    };

    return workspace;
  }

  async startWorkspace(workspace: Workspace): Promise<Workspace> {
    if (!isValidTransition(workspace.status, 'running')) {
      throw new WorkspaceInvalidTransitionError(
        workspace.name,
        workspace.status,
        'running',
      );
    }

    const containerId = workspace.backendConfig.containerId;
    if (!containerId) {
      throw new WorkspaceStartError(workspace.name, new Error('No container ID'));
    }

    await this.docker.startContainer(containerId);
    await this.lifecycle.waitForHealthy(containerId, workspace.config);

    const updated = {
      ...workspace,
      status: 'running' as WorkspaceStatus,
      updatedAt: new Date().toISOString(),
      lastActiveAt: new Date().toISOString(),
    };

    return updated;
  }

  async stopWorkspace(workspace: Workspace): Promise<Workspace> {
    if (!isValidTransition(workspace.status, 'stopped')) {
      throw new WorkspaceInvalidTransitionError(
        workspace.name,
        workspace.status,
        'stopped',
      );
    }

    const containerId = workspace.backendConfig.containerId;
    if (!containerId) {
      return { ...workspace, status: 'stopped' as WorkspaceStatus, updatedAt: new Date().toISOString() };
    }

    await this.docker.stopContainer(containerId);

    return {
      ...workspace,
      status: 'stopped',
      updatedAt: new Date().toISOString(),
    };
  }

  async deleteWorkspace(workspace: Workspace): Promise<void> {
    const containerId = workspace.backendConfig.containerId;
    if (containerId) {
      await this.docker.stopContainer(containerId, 5);
      await this.docker.removeContainer(containerId, true);
    }

    await this.portManager.releasePorts(workspace.id);
  }

  async getWorkspaceStatus(workspace: Workspace): Promise<WorkspaceStatus> {
    const containerId = workspace.backendConfig.containerId;
    if (!containerId) {
      return workspace.status;
    }

    try {
      const info = await this.docker.inspectContainer(containerId);
      return this.mapContainerState(info.state);
    } catch {
      return 'error';
    }
  }

  async execCommand(
    workspace: Workspace,
    command: string[],
    options?: { user?: string; workdir?: string },
  ): Promise<{ stdout: string; stderr: string; exitCode: number }> {
    const containerId = workspace.backendConfig.containerId;
    if (!containerId) {
      throw new ContainerError(workspace.id, 'No container ID');
    }

    const args = ['docker', 'exec'];
    if (options?.user) args.push('-u', options.user);
    if (options?.workdir) args.push('-w', options.workdir);
    args.push(containerId, ...command);

    const { execFile } = await import('node:child_process');
    const { promisify } = await import('node:util');
    const execAsync = promisify(execFile);

    try {
      const cmd = args.join(' ');
      const { stdout, stderr } = await execAsync(cmd, { shell: '/bin/sh', timeout: 60000 });
      return { stdout, stderr, exitCode: 0 };
    } catch (err) {
      const error = err as { code?: number; stdout?: string; stderr?: string };
      return {
        stdout: error.stdout ?? '',
        stderr: error.stderr ?? (error as Error).message,
        exitCode: error.code ?? 1,
      };
    }
  }

  async getLogs(workspace: Workspace, options?: { tail?: number; since?: number }): Promise<string> {
    const containerId = workspace.backendConfig.containerId;
    if (!containerId) {
      throw new ContainerError(workspace.id, 'No container ID');
    }

    const args = ['docker', 'logs'];
    if (options?.tail) args.push('--tail', String(options.tail));
    if (options?.since) args.push('--since', String(options.since));
    args.push(containerId);

    const { execFile } = await import('node:child_process');
    const { promisify } = await import('node:util');
    const execAsync = promisify(execFile);

    try {
      const cmd = args.join(' ');
      const { stdout } = await execAsync(cmd, { shell: '/bin/sh' });
      return stdout;
    } catch (err) {
      return (err as Error).message;
    }
  }

  private buildEnvVars(config: WorkspaceConfig): Record<string, string> {
    const env: Record<string, string> = { ...config.env };
    return env;
  }

  private configToResources(_config: WorkspaceConfig): Workspace['resources'] {
    return {
      cpu: { cores: 2 },
      memory: { bytes: 4 * 1024 * 1024 * 1024 },
      storage: { bytes: 20 * 1024 * 1024 * 1024 },
    };
  }

  private mapContainerState(state: string): WorkspaceStatus {
    switch (state) {
      case 'running':
        return 'running';
      case 'exited':
        return 'stopped';
      case 'paused':
        return 'paused';
      case 'restarting':
      case 'removing':
        return 'pending';
      case 'dead':
        return 'error';
      default:
        return 'pending';
    }
  }
}
