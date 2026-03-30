import {
  WorkspaceCreateResult,
  WorkspaceCreateSpec,
  WorkspaceListResult,
  WorkspaceOpenResult,
  WorkspaceRemoveResult,
} from './types';
import { WorkspaceHandle, type RPCClient } from './workspace-handle';

export class WorkspaceManager {
  private client: RPCClient;

  constructor(client: RPCClient) {
    this.client = client;
  }

  async create(spec: WorkspaceCreateSpec): Promise<WorkspaceHandle> {
    const result = await this.client.request<WorkspaceCreateResult>('workspace.create', { spec });
    return new WorkspaceHandle(this.client, result.workspace);
  }

  async open(id: string): Promise<WorkspaceHandle> {
    const result = await this.client.request<WorkspaceOpenResult>('workspace.open', { id });
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
}
