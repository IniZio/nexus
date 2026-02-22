import { PortAllocationError, PortExhaustedError } from '@nexus/workspace-core';
import type { PortMapping, WorkspaceConfig } from '@nexus/workspace-core';
import { randomInt } from 'node:crypto';
import { readFileSync, writeFileSync, existsSync, mkdirSync } from 'node:fs';
import { join, dirname } from 'node:path';

interface PortEntry {
  workspaceId: string;
  service: string;
  hostPort: number;
  containerPort: number;
  protocol: 'tcp' | 'udp';
  state: 'allocated' | 'released';
}

export class PortManager {
  private readonly range: { start: number; end: number };
  private readonly stateFile: string;
  private ports = new Map<number, PortEntry>();

  constructor(range?: { start: number; end: number }, stateDir?: string) {
    this.range = range ?? { start: 32800, end: 34999 };
    const baseDir = stateDir ?? '/var/lib/nexus';
    this.stateFile = join(baseDir, 'ports.json');
    this.loadState();
  }

  async allocatePorts(workspaceId: string, config: WorkspaceConfig): Promise<PortMapping[]> {
    const ports: PortMapping[] = [];
    const services = config.services ?? [{ name: 'main', ports: [{ name: 'main', protocol: 'tcp', containerPort: 3000, hostPort: 0, visibility: 'private' }] }];

    for (const service of services) {
      for (const port of service.ports) {
        const hostPort = port.hostPort === 0 
          ? await this.findAvailablePort()
          : port.hostPort;

        if (this.ports.has(hostPort)) {
          throw new PortAllocationError(hostPort, 'Port already allocated');
        }

        const entry: PortEntry = {
          workspaceId,
          service: service.name,
          hostPort,
          containerPort: port.containerPort,
          protocol: port.protocol,
          state: 'allocated',
        };

        this.ports.set(hostPort, entry);
        ports.push({
          name: port.name,
          protocol: port.protocol,
          containerPort: port.containerPort,
          hostPort,
          visibility: port.visibility,
        });
      }
    }

    this.saveState();
    return ports;
  }

  async releasePorts(workspaceId: string): Promise<void> {
    for (const [hostPort, entry] of this.ports) {
      if (entry.workspaceId === workspaceId) {
        this.ports.set(hostPort, { ...entry, state: 'released' });
      }
    }

    for (const [hostPort, entry] of this.ports) {
      if (entry.workspaceId === workspaceId && entry.state === 'released') {
        this.ports.delete(hostPort);
      }
    }

    this.saveState();
  }

  async getAllocatedPorts(): Promise<PortMapping[]> {
    return Array.from(this.ports.values())
      .filter(p => p.state === 'allocated')
      .map(p => ({
        name: p.service,
        protocol: p.protocol,
        containerPort: p.containerPort,
        hostPort: p.hostPort,
        visibility: 'private' as const,
      }));
  }

  private async findAvailablePort(): Promise<number> {
    const attempts = this.range.end - this.range.start;
    
    for (let i = 0; i < attempts; i++) {
      const port = this.range.start + randomInt(this.range.end - this.range.start);
      if (!this.ports.has(port)) {
        return port;
      }
    }

    throw new PortExhaustedError();
  }

  private loadState(): void {
    try {
      if (existsSync(this.stateFile)) {
        const data = JSON.parse(readFileSync(this.stateFile, 'utf-8'));
        for (const entry of data.ports ?? []) {
          if (entry.state === 'allocated') {
            this.ports.set(entry.hostPort, entry);
          }
        }
      }
    } catch {
      this.ports = new Map();
    }
  }

  private saveState(): void {
    try {
      const dir = dirname(this.stateFile);
      if (!existsSync(dir)) {
        mkdirSync(dir, { recursive: true });
      }
      writeFileSync(
        this.stateFile,
        JSON.stringify({
          ports: Array.from(this.ports.values()),
          updatedAt: new Date().toISOString(),
        }, null, 2),
      );
    } catch (err) {
      console.error('Failed to save port state:', err);
    }
  }
}
