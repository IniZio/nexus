import { ExecOperations } from './exec';
import { FSOperations } from './fs';
import {
  SpotlightExposeOptions,
  SpotlightApplyDefaultsResult,
  SpotlightApplyComposePortsResult,
  SpotlightForward,
  SpotlightListResult,
  WorkspaceInfo,
  WorkspaceReadyCheck,
  WorkspaceReadyResult,
  WorkspaceRecord,
} from './types';

export interface RPCClient {
  request<T = unknown>(method: string, params?: Record<string, unknown>): Promise<T>;
}

interface SpotlightClient {
  expose(options: SpotlightExposeOptions): Promise<SpotlightForward>;
  list(): Promise<SpotlightListResult>;
  close(id: string): Promise<boolean>;
  applyDefaults(): Promise<SpotlightApplyDefaultsResult>;
  applyComposePorts(): Promise<SpotlightApplyComposePortsResult>;
}

class WorkspaceSpotlightClient implements SpotlightClient {
  private client: RPCClient;
  private workspaceId: string;

  constructor(client: RPCClient, workspaceId: string) {
    this.client = client;
    this.workspaceId = workspaceId;
  }

  async expose(options: SpotlightExposeOptions): Promise<SpotlightForward> {
    const result = await this.client.request<{ forward: SpotlightForward }>('spotlight.expose', {
      spec: {
        workspaceId: this.workspaceId,
        service: options.service,
        remotePort: options.remotePort,
        localPort: options.localPort,
        host: options.host,
      },
    });

    return result.forward;
  }

  async list(): Promise<SpotlightListResult> {
    return this.client.request<SpotlightListResult>('spotlight.list', {
      workspaceId: this.workspaceId,
    });
  }

  async close(id: string): Promise<boolean> {
    const result = await this.client.request<{ closed: boolean }>('spotlight.close', { id });
    return result.closed;
  }

  async applyDefaults(): Promise<SpotlightApplyDefaultsResult> {
    return this.client.request<SpotlightApplyDefaultsResult>('spotlight.applyDefaults', {
      workspaceId: this.workspaceId,
    });
  }

  async applyComposePorts(): Promise<SpotlightApplyComposePortsResult> {
    return this.client.request<SpotlightApplyComposePortsResult>('spotlight.applyComposePorts', {
      workspaceId: this.workspaceId,
    });
  }
}

export class WorkspaceHandle {
  private client: RPCClient;
  private record: WorkspaceRecord;

  public readonly exec: ExecOperations;
  public readonly fs: FSOperations;
  public readonly spotlight: SpotlightClient;

  constructor(client: RPCClient, record: WorkspaceRecord) {
    this.client = client;
    this.record = record;
    const scopedParams = { workspaceId: record.id };
    this.exec = new ExecOperations(client as never, scopedParams);
    this.fs = new FSOperations(client as never, scopedParams);
    this.spotlight = new WorkspaceSpotlightClient(client, record.id);
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
}
