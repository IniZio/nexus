import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import {
  type BackendType,
  type PortMapping,
  type ResourceAllocation,
  type VolumeConfig,
  DockerDaemonError,
  ContainerError,
  BackendUnavailableError,
} from '@nexus/workspace-core';

const execFileAsync = promisify(execFile);

export interface ContainerSpec {
  name: string;
  image: string;
  ports: PortMapping[];
  env: Record<string, string>;
  volumes: VolumeConfig[];
  resources: ResourceAllocation;
  workdir?: string;
  labels?: Record<string, string>;
}

export interface ContainerInfo {
  id: string;
  name: string;
  state: string;
  status: string;
  image: string;
  ports: Array<{ hostPort: number; containerPort: number; protocol: string }>;
}

export interface DockerClientOptions {
  socketPath?: string;
  timeout?: number;
}

export class DockerClient {
  private readonly socketPath: string;
  private readonly timeout: number;

  constructor(options: DockerClientOptions = {}) {
    this.socketPath = options.socketPath ?? '/var/run/docker.sock';
    this.timeout = options.timeout ?? 30000;
  }

  async ping(): Promise<boolean> {
    try {
      await this.exec(['docker', 'info', '--format', '{{.ServerVersion}}']);
      return true;
    } catch {
      return false;
    }
  }

  async ensureAvailable(): Promise<void> {
    const available = await this.ping();
    if (!available) {
      throw new BackendUnavailableError('docker');
    }
  }

  async createContainer(spec: ContainerSpec): Promise<string> {
    await this.ensureAvailable();

    const args: string[] = ['docker', 'create', '--name', `nexus-${spec.name}`];

    for (const [key, value] of Object.entries(spec.env)) {
      args.push('-e', `${key}=${value}`);
    }

    for (const port of spec.ports) {
      args.push('-p', `${port.hostPort}:${port.containerPort}/${port.protocol}`);
    }

    for (const vol of spec.volumes) {
      if (vol.readOnly) {
        args.push('-v', `${vol.source}:${vol.target}:ro`);
      } else {
        args.push('-v', `${vol.source}:${vol.target}`);
      }
    }

    if (spec.resources.cpu.limit) {
      args.push('--cpus', String(spec.resources.cpu.limit));
    }
    if (spec.resources.memory.limit) {
      args.push('--memory', String(spec.resources.memory.limit));
    }

    for (const [key, value] of Object.entries(spec.labels ?? {})) {
      args.push('--label', `${key}=${value}`);
    }

    if (spec.workdir) {
      args.push('-w', spec.workdir);
    }

    args.push(spec.image);

    try {
      const { stdout } = await this.exec(args);
      return stdout.trim();
    } catch (err) {
      throw new ContainerError(spec.name, 'Failed to create container', err as Error);
    }
  }

  async startContainer(containerId: string): Promise<void> {
    try {
      await this.exec(['docker', 'start', containerId]);
    } catch (err) {
      throw new ContainerError(containerId, 'Failed to start container', err as Error);
    }
  }

  async stopContainer(containerId: string, timeout = 10): Promise<void> {
    try {
      await this.exec(['docker', 'stop', '-t', String(timeout), containerId]);
    } catch (err) {
      throw new ContainerError(containerId, 'Failed to stop container', err as Error);
    }
  }

  async removeContainer(containerId: string, force = false): Promise<void> {
    const args = ['docker', 'rm'];
    if (force) args.push('-f');
    args.push(containerId);

    try {
      await this.exec(args);
    } catch (err) {
      throw new ContainerError(containerId, 'Failed to remove container', err as Error);
    }
  }

  async inspectContainer(containerId: string): Promise<ContainerInfo> {
    try {
      const { stdout } = await this.exec([
        'docker', 'inspect', '--format',
        '{{json .}}',
        containerId,
      ]);
      const data = JSON.parse(stdout);
      return {
        id: data.Id,
        name: data.Name?.replace(/^\//, '') ?? '',
        state: data.State?.Status ?? 'unknown',
        status: data.State?.Status ?? 'unknown',
        image: data.Config?.Image ?? '',
        ports: [],
      };
    } catch (err) {
      throw new ContainerError(containerId, 'Failed to inspect container', err as Error);
    }
  }

  async listContainers(labelFilter?: string): Promise<ContainerInfo[]> {
    const args = ['docker', 'ps', '-a', '--format', '{{json .}}'];
    if (labelFilter) {
      args.push('--filter', `label=${labelFilter}`);
    }

    try {
      const { stdout } = await this.exec(args);
      if (!stdout.trim()) return [];

      return stdout.trim().split('\n').map((line) => {
        const data = JSON.parse(line);
        return {
          id: data.ID,
          name: data.Names,
          state: data.State,
          status: data.Status,
          image: data.Image,
          ports: [],
        };
      });
    } catch (err) {
      throw new DockerDaemonError('Failed to list containers', err as Error);
    }
  }

  async pullImage(image: string): Promise<void> {
    try {
      await this.exec(['docker', 'pull', image], 120000);
    } catch (err) {
      throw new DockerDaemonError(`Failed to pull image: ${image}`, err as Error);
    }
  }

  async createNetwork(name: string): Promise<string> {
    try {
      const { stdout } = await this.exec(['docker', 'network', 'create', name]);
      return stdout.trim();
    } catch (err) {
      throw new DockerDaemonError(`Failed to create network: ${name}`, err as Error);
    }
  }

  async removeNetwork(name: string): Promise<void> {
    try {
      await this.exec(['docker', 'network', 'rm', name]);
    } catch (err) {
      throw new DockerDaemonError(`Failed to remove network: ${name}`, err as Error);
    }
  }

  private async exec(args: string[], timeout?: number): Promise<{ stdout: string; stderr: string }> {
    const [cmd, ...rest] = args;
    try {
      return await execFileAsync(cmd, rest, {
        timeout: timeout ?? this.timeout,
        maxBuffer: 10 * 1024 * 1024,
      });
    } catch (err) {
      throw new DockerDaemonError((err as Error).message, err as Error);
    }
  }
}

export const BACKEND_TYPE: BackendType = 'docker';
