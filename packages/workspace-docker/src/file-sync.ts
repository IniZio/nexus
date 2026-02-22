import { existsSync, readFileSync, readdirSync, statSync, copyFileSync, mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { WorkspaceConfig } from '@nexus/workspace-core';

const DEFAULT_IGNORES = [
  '.git',
  'node_modules',
  '.DS_Store',
  '*.log',
  '.env.local',
  '.env.*.local',
];

export interface FileSyncOptions {
  ignorePatterns?: string[];
  excludeVolumes?: boolean;
}

export class FileSync {
  private readonly ignorePatterns: string[];

  constructor(options?: FileSyncOptions) {
    this.ignorePatterns = options?.ignorePatterns ?? DEFAULT_IGNORES;
  }

  async syncToContainer(
    hostPath: string,
    containerId: string,
    config: WorkspaceConfig,
  ): Promise<void> {
    if (!existsSync(hostPath)) {
      return;
    }

    const targetDir = '/workspace';
    await this.copyDirectory(hostPath, containerId, targetDir);

    await this.syncEnvFiles(hostPath, containerId, config);
  }

  async syncFromContainer(
    containerId: string,
    containerPath: string,
    hostPath: string,
  ): Promise<void> {
    const tempDir = `/tmp/nexus-sync-${Date.now()}`;
    
    const { execFile } = await import('node:child_process');
    const { promisify } = await import('node:util');
    const execAsync = promisify(execFile);

    try {
      await execAsync('docker', ['cp', `${containerId}:${containerPath}`, tempDir]);
      
      if (existsSync(tempDir)) {
        const items = readdirSync(tempDir);
        for (const item of items) {
          const src = join(tempDir, item);
          const dest = join(hostPath, item);
          if (statSync(src).isDirectory()) {
            this.copyDirectoryRecursive(src, dest);
          } else {
            copyFileSync(src, dest);
          }
        }
      }
    } catch (err) {
      console.error('Failed to sync from container:', err);
    }
  }

  private async copyDirectory(
    hostPath: string,
    containerId: string,
    containerTarget: string,
  ): Promise<void> {
    const { spawn } = await import('node:child_process');

    const files = this.collectFiles(hostPath);
    const tarCmd = this.createTarCommand(files);

    await new Promise<void>((resolve, reject) => {
      const tarProcess = spawn('sh', ['-c', tarCmd], {
        cwd: hostPath,
        stdio: ['ignore', 'pipe', 'pipe'],
      });

      const dockerProcess = spawn('docker', ['cp', '-', `${containerId}:${containerTarget}`], {
        stdio: ['pipe', 'pipe', 'pipe'],
      });

      if (tarProcess.stdout && dockerProcess.stdin) {
        tarProcess.stdout.pipe(dockerProcess.stdin);
      }

      tarProcess.on('close', (code) => {
        if (code !== 0) reject(new Error(`tar exited with code ${code}`));
      });
      dockerProcess.on('close', (code) => {
        if (code !== 0) reject(new Error(`docker cp exited with code ${code}`));
        else resolve();
      });
    });
  }

  private collectFiles(dir: string, base = ''): string[] {
    const files: string[] = [];
    
    if (!existsSync(dir)) return files;

    const entries = readdirSync(dir);
    
    for (const entry of entries) {
      const fullPath = join(dir, entry);
      const relativePath = base ? join(base, entry) : entry;

      if (this.shouldIgnore(relativePath)) {
        continue;
      }

      const stat = statSync(fullPath);
      if (stat.isDirectory()) {
        files.push(...this.collectFiles(fullPath, relativePath));
      } else {
        files.push(relativePath);
      }
    }

    return files;
  }

  private shouldIgnore(path: string): boolean {
    for (const pattern of this.ignorePatterns) {
      if (this.matchIgnorePattern(path, pattern)) {
        return true;
      }
    }
    return false;
  }

  private matchIgnorePattern(path: string, pattern: string): boolean {
    if (pattern.startsWith('*.')) {
      const ext = pattern.slice(1);
      return path.endsWith(ext);
    }
    return path === pattern || path.startsWith(pattern + '/');
  }

  private createTarCommand(files: string[]): string {
    if (files.length === 0) {
      return 'tar cf - --files-from /dev/null';
    }
    const fileList = files.map(f => `"${f}"`).join(' ');
    return `tar cf - ${fileList}`;
  }

  private copyDirectoryRecursive(src: string, dest: string): void {
    if (!existsSync(dest)) {
      mkdirSync(dest, { recursive: true });
    }

    const entries = readdirSync(src);
    for (const entry of entries) {
      const srcPath = join(src, entry);
      const destPath = join(dest, entry);
      const stat = statSync(srcPath);

      if (stat.isDirectory()) {
        this.copyDirectoryRecursive(srcPath, destPath);
      } else {
        copyFileSync(srcPath, destPath);
      }
    }
  }

  private async syncEnvFiles(
    hostPath: string,
    containerId: string,
    config: WorkspaceConfig,
  ): Promise<void> {
    const envFiles = config.envFiles ?? [];
    const envContent: string[] = [];

    for (const envFile of envFiles) {
      const envPath = join(hostPath, envFile);
      if (existsSync(envPath)) {
        const content = readFileSync(envPath, 'utf-8');
        envContent.push(content);
      }
    }

    if (envContent.length > 0 || Object.keys(config.env).length > 0) {
      const mergedEnv = this.mergeEnvVars(envContent, config.env);
      const envFileContent = Object.entries(mergedEnv)
        .map(([key, value]) => `${key}=${value}`)
        .join('\n');

      const { execFile } = await import('node:child_process');
      const { promisify } = await import('node:util');
      const execAsync = promisify(execFile);

      const tempPath = `/tmp/nexus-env-${Date.now()}.tmp`;
      writeFileSync(tempPath, envFileContent);

      try {
        await execAsync('docker', ['cp', tempPath, `${containerId}:/workspace/.env`]);
      } finally {
        try {
          const { unlinkSync } = await import('node:fs');
          unlinkSync(tempPath);
        } catch {}
      }
    }
  }

  private mergeEnvVars(
    envFileContents: string[],
    explicitEnv: Record<string, string>,
  ): Record<string, string> {
    const merged: Record<string, string> = {};

    for (const content of envFileContents) {
      for (const line of content.split('\n')) {
        const trimmed = line.trim();
        if (trimmed && !trimmed.startsWith('#')) {
          const eqIndex = trimmed.indexOf('=');
          if (eqIndex > 0) {
            const key = trimmed.slice(0, eqIndex);
            const value = trimmed.slice(eqIndex + 1);
            merged[key] = value;
          }
        }
      }
    }

    return { ...merged, ...explicitEnv };
  }
}
