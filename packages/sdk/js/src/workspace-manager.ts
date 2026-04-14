import {
  WorkspaceCreateResult,
  WorkspaceCreateSpec,
  WorkspaceListResult,
  WorkspaceRemoveResult,
  WorkspaceRestoreResult,
  WorkspaceForkResult,
  WorkspaceStartResult,
  WorkspaceStopResult,
} from './types';
import { WorkspaceHandle } from './workspace-handle';
import type { RPCClient } from './rpc/types';

export class WorkspaceManager {
  private client: RPCClient;
  private bundleProvider: () => string | Promise<string>;

  constructor(client: RPCClient, bundleProvider: () => string | Promise<string> = () => '') {
    this.client = client;
    this.bundleProvider = bundleProvider;
  }

  async create(spec: WorkspaceCreateSpec): Promise<WorkspaceHandle> {
    const br = this.bundleProvider();
    const bundle = typeof br === 'string' ? br : await br;
    const params =
      bundle.trim() !== ''
        ? { spec: { ...spec, configBundle: bundle } }
        : { spec };
    const result = await this.client.request<WorkspaceCreateResult>('workspace.create', params);
    return new WorkspaceHandle(this.client, result.workspace);
  }

  async list(): Promise<WorkspaceListResult['workspaces']> {
    const result = await this.client.request<WorkspaceListResult>('workspace.list', {});
    return result.workspaces;
  }

  async remove(id: string): Promise<boolean> {
    const result = await this.client.request<WorkspaceRemoveResult>('workspace.remove', { id });
    return result.removed;
  }

  async stop(id: string): Promise<boolean> {
    const result = await this.client.request<WorkspaceStopResult>('workspace.stop', { id });
    return result.stopped;
  }

  async start(id: string): Promise<WorkspaceHandle> {
    const result = await this.client.request<WorkspaceStartResult>('workspace.start', { id });
    return new WorkspaceHandle(this.client, result.workspace);
  }

  async restore(id: string): Promise<WorkspaceHandle> {
    const result = await this.client.request<WorkspaceRestoreResult>('workspace.restore', { id });
    return new WorkspaceHandle(this.client, result.workspace);
  }

  async fork(id: string, childWorkspaceName?: string, childRef?: string): Promise<WorkspaceHandle> {
    const result = await this.client.request<WorkspaceForkResult>('workspace.fork', { id, childWorkspaceName, childRef });
    return new WorkspaceHandle(this.client, result.workspace);
  }
}
