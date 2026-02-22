import * as fs from 'node:fs';
import * as fsp from 'node:fs/promises';
import * as path from 'node:path';
import { type WorkspaceState, type WorkspaceStatus, isValidTransition } from './types.js';
import { StateCorruptionError, StateLockError, WorkspaceNotFoundError, WorkspaceAlreadyExistsError, WorkspaceInvalidTransitionError } from './errors.js';

interface TransactionLogEntry {
  timestamp: string;
  operation: 'create' | 'update' | 'delete';
  workspaceId: string;
  workspaceName: string;
  previousStatus?: WorkspaceStatus;
  newStatus?: WorkspaceStatus;
}

interface LockHandle {
  fd: number;
  release: () => Promise<void>;
}

export class StateStore {
  private readonly stateDir: string;
  private readonly lockDir: string;
  private readonly transactionLogPath: string;

  constructor(baseDir: string) {
    this.stateDir = path.join(baseDir, 'workspaces');
    this.lockDir = path.join(baseDir, 'locks');
    this.transactionLogPath = path.join(baseDir, 'transaction.log');
  }

  async init(): Promise<void> {
    await fsp.mkdir(this.stateDir, { recursive: true });
    await fsp.mkdir(this.lockDir, { recursive: true });
  }

  private statePath(name: string): string {
    return path.join(this.stateDir, `${name}.json`);
  }

  private backupPath(name: string): string {
    return path.join(this.stateDir, `${name}.json.bak`);
  }

  private lockPath(name: string): string {
    return path.join(this.lockDir, `${name}.lock`);
  }

  private async acquireLock(name: string, timeoutMs = 5000): Promise<LockHandle> {
    const lockFile = this.lockPath(name);
    const start = Date.now();

    while (Date.now() - start < timeoutMs) {
      try {
        const fd = fs.openSync(lockFile, fs.constants.O_CREAT | fs.constants.O_EXCL | fs.constants.O_WRONLY);
        fs.writeSync(fd, `${process.pid}\n${Date.now()}`);
        return {
          fd,
          release: async () => {
            try {
              fs.closeSync(fd);
            } catch {
              // already closed
            }
            try {
              await fsp.unlink(lockFile);
            } catch {
              // already removed
            }
          },
        };
      } catch (err: unknown) {
        if ((err as NodeJS.ErrnoException).code === 'EEXIST') {
          const stale = await this.isLockStale(lockFile);
          if (stale) {
            await fsp.unlink(lockFile).catch(() => {});
            continue;
          }
          await new Promise((resolve) => setTimeout(resolve, 50));
          continue;
        }
        throw err;
      }
    }

    throw new StateLockError(name);
  }

  private async isLockStale(lockFile: string): Promise<boolean> {
    try {
      const content = await fsp.readFile(lockFile, 'utf8');
      const [pidStr, timestampStr] = content.split('\n');
      const pid = parseInt(pidStr, 10);
      const timestamp = parseInt(timestampStr, 10);

      if (Date.now() - timestamp > 30000) {
        return true;
      }

      try {
        process.kill(pid, 0);
        return false;
      } catch {
        return true;
      }
    } catch {
      return true;
    }
  }

  private validate(state: WorkspaceState): void {
    if (!state.id || typeof state.id !== 'string') {
      throw new StateCorruptionError(state.name ?? 'unknown', 'Missing or invalid id');
    }
    if (!state.name || typeof state.name !== 'string') {
      throw new StateCorruptionError(state.id ?? 'unknown', 'Missing or invalid name');
    }
    if (!state.status) {
      throw new StateCorruptionError(state.name, 'Missing status');
    }
  }

  private async writeTransactionLog(entry: TransactionLogEntry): Promise<void> {
    const line = JSON.stringify(entry) + '\n';
    await fsp.appendFile(this.transactionLogPath, line, 'utf8');
  }

  async getWorkspace(name: string): Promise<WorkspaceState> {
    const filePath = this.statePath(name);
    try {
      const data = await fsp.readFile(filePath, 'utf8');
      const state = JSON.parse(data) as WorkspaceState;
      this.validate(state);
      return state;
    } catch (err: unknown) {
      if ((err as NodeJS.ErrnoException).code === 'ENOENT') {
        throw new WorkspaceNotFoundError(name);
      }
      if (err instanceof StateCorruptionError) {
        throw err;
      }
      if (err instanceof SyntaxError) {
        throw new StateCorruptionError(name, 'Invalid JSON', err);
      }
      throw err;
    }
  }

  async saveWorkspace(state: WorkspaceState): Promise<void> {
    this.validate(state);
    const lock = await this.acquireLock(state.name);
    try {
      const filePath = this.statePath(state.name);

      const exists = await fsp.access(filePath).then(() => true).catch(() => false);
      if (exists) {
        await fsp.copyFile(filePath, this.backupPath(state.name));
      }

      state.updatedAt = new Date().toISOString();
      const data = JSON.stringify(state, null, 2);
      const tmpPath = filePath + '.tmp';
      await fsp.writeFile(tmpPath, data, 'utf8');
      await fsp.rename(tmpPath, filePath);

      await this.writeTransactionLog({
        timestamp: state.updatedAt,
        operation: exists ? 'update' : 'create',
        workspaceId: state.id,
        workspaceName: state.name,
        newStatus: state.status,
      });
    } finally {
      await lock.release();
    }
  }

  async createWorkspace(state: WorkspaceState): Promise<void> {
    this.validate(state);
    const filePath = this.statePath(state.name);

    const exists = await fsp.access(filePath).then(() => true).catch(() => false);
    if (exists) {
      throw new WorkspaceAlreadyExistsError(state.name);
    }

    await this.saveWorkspace(state);
  }

  async updateStatus(name: string, newStatus: WorkspaceStatus, statusMessage?: string): Promise<WorkspaceState> {
    const state = await this.getWorkspace(name);
    if (!isValidTransition(state.status, newStatus)) {
      throw new WorkspaceInvalidTransitionError(name, state.status, newStatus);
    }

    state.status = newStatus;
    if (statusMessage !== undefined) {
      (state as WorkspaceState & { statusMessage?: string }).statusMessage = statusMessage;
    }
    await this.saveWorkspace(state);
    return state;
  }

  async listWorkspaces(): Promise<WorkspaceState[]> {
    try {
      const files = await fsp.readdir(this.stateDir);
      const states: WorkspaceState[] = [];

      for (const file of files) {
        if (!file.endsWith('.json') || file.endsWith('.json.bak') || file.endsWith('.json.tmp')) {
          continue;
        }
        const name = file.replace('.json', '');
        try {
          const state = await this.getWorkspace(name);
          states.push(state);
        } catch {
          // skip corrupt states
        }
      }

      return states.sort((a, b) => a.name.localeCompare(b.name));
    } catch (err: unknown) {
      if ((err as NodeJS.ErrnoException).code === 'ENOENT') {
        return [];
      }
      throw err;
    }
  }

  async deleteWorkspace(name: string): Promise<void> {
    const lock = await this.acquireLock(name);
    try {
      const state = await this.getWorkspace(name);
      const filePath = this.statePath(name);

      await fsp.copyFile(filePath, this.backupPath(name));
      await fsp.unlink(filePath);

      await this.writeTransactionLog({
        timestamp: new Date().toISOString(),
        operation: 'delete',
        workspaceId: state.id,
        workspaceName: name,
        previousStatus: state.status,
      });
    } finally {
      await lock.release();
    }
  }

  async workspaceExists(name: string): Promise<boolean> {
    try {
      await fsp.access(this.statePath(name));
      return true;
    } catch {
      return false;
    }
  }
}
