import { ExecOperations } from './exec';
import { FSOperations } from './fs';
import { TunnelOperations } from './spotlight';
import type { RPCClient } from './rpc/types';
import {
  ExecOptions,
  WorkspaceInfo,
  WorkspaceReadyCheck,
  WorkspaceReadyResult,
  WorkspaceRecord,
} from './types';

export type { RPCClient } from './rpc/types';

export class WorkspaceHandle {
  private client: RPCClient;
  private record: WorkspaceRecord;
  private readonly execOps: ExecOperations;
  private readonly fsOps: FSOperations;
  public readonly tunnel: TunnelOperations;

  constructor(client: RPCClient, record: WorkspaceRecord) {
    this.client = client;
    this.record = record;
    const scopedParams = { workspaceId: record.id };
    this.execOps = new ExecOperations(client, scopedParams);
    this.fsOps = new FSOperations(client, scopedParams);
    this.tunnel = new TunnelOperations(client, scopedParams);
  }

  get id(): string {
    return this.record.id;
  }

  get state(): string {
    return this.record.state;
  }

  get rootPath(): string {
    return this.record.rootPath;
  }

  async info(): Promise<WorkspaceInfo> {
    return this.client.request<WorkspaceInfo>('workspace.info', { workspaceId: this.record.id });
  }

  async ready(checks: WorkspaceReadyCheck[], options?: { timeoutMs?: number; intervalMs?: number }): Promise<WorkspaceReadyResult> {
    return this.client.request<WorkspaceReadyResult>('workspace.ready', {
      workspaceId: this.record.id,
      checks,
      timeoutMs: options?.timeoutMs,
      intervalMs: options?.intervalMs,
    });
  }

  async readyProfile(profile: string, options?: { timeoutMs?: number; intervalMs?: number }): Promise<WorkspaceReadyResult> {
    return this.client.request<WorkspaceReadyResult>('workspace.ready', {
      workspaceId: this.record.id,
      profile,
      timeoutMs: options?.timeoutMs,
      intervalMs: options?.intervalMs,
    });
  }

  async git(action: string, params?: Record<string, unknown>): Promise<unknown> {
    return this.client.request('git.command', {
      workspaceId: this.record.id,
      action,
      params,
    });
  }

  async service(action: string, params?: Record<string, unknown>): Promise<unknown> {
    return this.client.request('service.command', {
      workspaceId: this.record.id,
      action,
      params,
    });
  }

  async start(): Promise<boolean> {
    const result = await this.client.request<{ started: boolean }>('workspace.start', { id: this.record.id });
    return result.started;
  }

  async stop(): Promise<boolean> {
    const result = await this.client.request<{ stopped: boolean }>('workspace.stop', { id: this.record.id });
    return result.stopped;
  }

  async remove(): Promise<boolean> {
    const result = await this.client.request<{ removed: boolean }>('workspace.remove', { id: this.record.id });
    return result.removed;
  }

  async exec(command: string, args: string[] = [], options: ExecOptions = {}) {
    return this.execOps.exec(command, args, options);
  }

  async readFile(path: string, encoding: string = 'utf8') {
    return this.fsOps.readFile(path, encoding);
  }

  async writeFile(path: string, content: string | Buffer) {
    await this.fsOps.writeFile(path, content);
  }

  async exists(path: string) {
    return this.fsOps.exists(path);
  }

  async readdir(path: string) {
    return this.fsOps.readdir(path);
  }

  async mkdir(path: string, recursive: boolean = false) {
    await this.fsOps.mkdir(path, recursive);
  }

  async rm(path: string, recursive: boolean = false) {
    await this.fsOps.rm(path, recursive);
  }

  async stat(path: string) {
    return this.fsOps.stat(path);
  }

}
