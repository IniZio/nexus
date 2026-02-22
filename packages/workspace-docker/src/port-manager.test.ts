import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { PortManager } from '../src/port-manager.js';
import { existsSync, mkdirSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';

describe('PortManager', () => {
  let portManager: PortManager;
  let testDir: string;

  beforeEach(() => {
    testDir = join(tmpdir(), `nexus-port-test-${Date.now()}`);
    mkdirSync(testDir, { recursive: true });
    portManager = new PortManager({ start: 32800, end: 32810 }, testDir);
  });

  afterEach(() => {
    try {
      if (existsSync(testDir)) {
        rmSync(testDir, { recursive: true, force: true });
      }
    } catch {}
  });

  describe('allocatePorts', () => {
    it('should allocate ports for a workspace', async () => {
      const config = {
        image: 'node:18',
        env: {},
        envFiles: [],
        volumes: [],
        services: [
          {
            name: 'main',
            image: 'node:18',
            ports: [{ name: 'main', protocol: 'tcp' as const, containerPort: 3000, hostPort: 0, visibility: 'private' as const }],
            env: {},
            volumes: [],
            dependsOn: [],
          },
        ],
        hooks: {},
        ide: { default: 'vscode' as const, extensions: [], settings: {} },
        idleTimeout: 30,
        shutdownBehavior: 'stop' as const,
      };

      const ports = await portManager.allocatePorts('ws-1', config);

      expect(ports).toHaveLength(1);
      expect(ports[0].hostPort).toBeGreaterThanOrEqual(32800);
      expect(ports[0].hostPort).toBeLessThanOrEqual(32810);
      expect(ports[0].containerPort).toBe(3000);
    });

    it('should allocate sequential ports for multiple services', async () => {
      const config = {
        image: 'node:18',
        env: {},
        envFiles: [],
        volumes: [],
        services: [
          {
            name: 'api',
            image: 'node:18',
            ports: [{ name: 'api', protocol: 'tcp' as const, containerPort: 3000, hostPort: 0, visibility: 'private' as const }],
            env: {},
            volumes: [],
            dependsOn: [],
          },
          {
            name: 'db',
            image: 'postgres:15',
            ports: [{ name: 'db', protocol: 'tcp' as const, containerPort: 5432, hostPort: 0, visibility: 'private' as const }],
            env: {},
            volumes: [],
            dependsOn: [],
          },
        ],
        hooks: {},
        ide: { default: 'vscode' as const, extensions: [], settings: {} },
        idleTimeout: 30,
        shutdownBehavior: 'stop' as const,
      };

      const ports = await portManager.allocatePorts('ws-1', config);

      expect(ports).toHaveLength(2);
      expect(ports[0].hostPort).not.toBe(ports[1].hostPort);
    });

    it('should use specified host port when provided', async () => {
      const config = {
        image: 'node:18',
        env: {},
        envFiles: [],
        volumes: [],
        services: [
          {
            name: 'main',
            image: 'node:18',
            ports: [{ name: 'main', protocol: 'tcp' as const, containerPort: 3000, hostPort: 32805, visibility: 'private' as const }],
            env: {},
            volumes: [],
            dependsOn: [],
          },
        ],
        hooks: {},
        ide: { default: 'vscode' as const, extensions: [], settings: {} },
        idleTimeout: 30,
        shutdownBehavior: 'stop' as const,
      };

      const ports = await portManager.allocatePorts('ws-1', config);

      expect(ports[0].hostPort).toBe(32805);
    });
  });

  describe('releasePorts', () => {
    it('should release ports for a workspace', async () => {
      const config = {
        image: 'node:18',
        env: {},
        envFiles: [],
        volumes: [],
        services: [
          {
            name: 'main',
            image: 'node:18',
            ports: [{ name: 'main', protocol: 'tcp' as const, containerPort: 3000, hostPort: 0, visibility: 'private' as const }],
            env: {},
            volumes: [],
            dependsOn: [],
          },
        ],
        hooks: {},
        ide: { default: 'vscode' as const, extensions: [], settings: {} },
        idleTimeout: 30,
        shutdownBehavior: 'stop' as const,
      };

      await portManager.allocatePorts('ws-1', config);
      await portManager.releasePorts('ws-1');

      const allocated = await portManager.getAllocatedPorts();
      expect(allocated).toHaveLength(0);
    });
  });

  describe('getAllocatedPorts', () => {
    it('should return all allocated ports', async () => {
      const config = {
        image: 'node:18',
        env: {},
        envFiles: [],
        volumes: [],
        services: [
          {
            name: 'main',
            image: 'node:18',
            ports: [{ name: 'main', protocol: 'tcp' as const, containerPort: 3000, hostPort: 0, visibility: 'private' as const }],
            env: {},
            volumes: [],
            dependsOn: [],
          },
        ],
        hooks: {},
        ide: { default: 'vscode' as const, extensions: [], settings: {} },
        idleTimeout: 30,
        shutdownBehavior: 'stop' as const,
      };

      await portManager.allocatePorts('ws-1', config);
      const allocated = await portManager.getAllocatedPorts();

      expect(allocated).toHaveLength(1);
    });
  });
});
