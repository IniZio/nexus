import { DockerClient } from './docker-client.js';
import { ContainerError } from '@nexus/workspace-core';
import type { WorkspaceConfig, HealthCheckConfig } from '@nexus/workspace-core';

export interface LifecycleConfig {
  healthCheckInterval: number;
  healthCheckTimeout: number;
  maxHealthCheckRetries: number;
  shutdownTimeout: number;
  autoRestart: boolean;
}

const DEFAULT_CONFIG: LifecycleConfig = {
  healthCheckInterval: 5000,
  healthCheckTimeout: 30000,
  maxHealthCheckRetries: 60,
  shutdownTimeout: 30000,
  autoRestart: false,
};

export class LifecycleManager {
  private readonly docker: DockerClient;
  private readonly config: LifecycleConfig;
  private readonly monitors = new Map<string, NodeJS.Timeout>();

  constructor(docker: DockerClient, config?: Partial<LifecycleConfig>) {
    this.docker = docker;
    this.config = { ...DEFAULT_CONFIG, ...config };
  }

  async waitForHealthy(
    containerId: string,
    workspaceConfig: WorkspaceConfig,
    onHealthCheck?: (status: 'healthy' | 'unhealthy') => void,
  ): Promise<void> {
    const healthCheck = workspaceConfig.services?.[0]?.healthCheck;
    
    if (healthCheck) {
      await this.waitForHealthCheck(containerId, healthCheck, onHealthCheck);
    } else {
      await this.waitForContainerRunning(containerId);
    }
  }

  private async waitForContainerRunning(containerId: string): Promise<void> {
    const start = Date.now();
    while (Date.now() - start < this.config.healthCheckTimeout) {
      const info = await this.docker.inspectContainer(containerId);
      if (info.state === 'running') {
        return;
      }
      await this.sleep(this.config.healthCheckInterval);
    }
    throw new ContainerError(containerId, 'Container failed to start within timeout');
  }

  private async waitForHealthCheck(
    containerId: string,
    healthCheck: HealthCheckConfig,
    onHealthCheck?: (status: 'healthy' | 'unhealthy') => void,
  ): Promise<void> {
    const start = Date.now();
    let retries = 0;

    while (Date.now() - start < this.config.healthCheckTimeout) {
      const info = await this.docker.inspectContainer(containerId);

      if (info.state !== 'running') {
        throw new ContainerError(containerId, 'Container stopped during health check');
      }

      const healthStatus = (info as unknown as { Health?: string }).Health;
      if (healthStatus === 'healthy') {
        onHealthCheck?.('healthy');
        return;
      }

      retries++;
      if (retries >= healthCheck.retries) {
        onHealthCheck?.('unhealthy');
        throw new ContainerError(containerId, `Health check failed after ${retries} retries`);
      }

      await this.sleep(this.config.healthCheckInterval);
    }

    throw new ContainerError(containerId, 'Health check timeout');
  }

  async startMonitoring(
    containerId: string,
    onCrash?: (exitCode: number) => void,
  ): Promise<void> {
    const monitor = setInterval(async () => {
      try {
        const info = await this.docker.inspectContainer(containerId);
        
        if (info.state === 'exited' || info.state === 'dead') {
          onCrash?.(0);
          
          if (this.config.autoRestart && info.state === 'exited') {
            await this.docker.startContainer(containerId);
          }
        }
      } catch {
        onCrash?.(1);
      }
    }, this.config.healthCheckInterval);

    this.monitors.set(containerId, monitor);
  }

  async stopMonitoring(containerId: string): Promise<void> {
    const monitor = this.monitors.get(containerId);
    if (monitor) {
      clearInterval(monitor);
      this.monitors.delete(containerId);
    }
  }

  async gracefulShutdown(containerId: string): Promise<void> {
    const monitor = this.monitors.get(containerId);
    if (monitor) {
      clearInterval(monitor);
      this.monitors.delete(containerId);
    }

    await this.docker.stopContainer(containerId, Math.floor(this.config.shutdownTimeout / 1000));
  }

  async checkContainerHealth(containerId: string): Promise<{
    healthy: boolean;
    state: string;
    exitCode?: number;
  }> {
    try {
      const info = await this.docker.inspectContainer(containerId);
      return {
        healthy: info.state === 'running',
        state: info.state,
      };
    } catch {
      return {
        healthy: false,
        state: 'unknown',
      };
    }
  }

  private sleep(ms: number): Promise<void> {
    return new Promise((resolve) => setTimeout(resolve, ms));
  }
}
