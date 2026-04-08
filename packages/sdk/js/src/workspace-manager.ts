import {
  AuthRelayMintParams,
  AuthRelayMintResult,
  AuthRelayRevokeResult,
  CapabilitiesListResult,
  Capability,
  WorkspaceCreateResult,
  WorkspaceCreateSpec,
  WorkspaceListResult,
  WorkspaceOpenResult,
  WorkspaceRemoveResult,
  WorkspaceRestoreResult,
  WorkspaceRelationsListResult,
  WorkspaceForkResult,
  WorkspacePauseResult,
  WorkspaceResumeResult,
  WorkspaceStartResult,
  WorkspaceStopResult,
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

  async relations(repoId?: string): Promise<WorkspaceRelationsListResult['relations']> {
    const result = await this.client.request<WorkspaceRelationsListResult>('workspace.relations.list', {
      repoId,
    });
    return result.relations;
  }

  async remove(id: string): Promise<boolean> {
    const result = await this.client.request<WorkspaceRemoveResult>('workspace.remove', { id });
    return result.removed;
  }

  async stop(id: string): Promise<boolean> {
    const result = await this.client.request<WorkspaceStopResult>('workspace.stop', { id });
    return result.stopped;
  }

  async start(id: string): Promise<boolean> {
    const result = await this.client.request<WorkspaceStartResult>('workspace.start', { id });
    return result.started;
  }

  async restore(id: string): Promise<WorkspaceHandle> {
    const result = await this.client.request<WorkspaceRestoreResult>('workspace.restore', { id });
    return new WorkspaceHandle(this.client, result.workspace);
  }

  async pause(id: string): Promise<boolean> {
    const result = await this.client.request<WorkspacePauseResult>('workspace.pause', { id });
    return result.paused;
  }

  async resume(id: string): Promise<boolean> {
    const result = await this.client.request<WorkspaceResumeResult>('workspace.resume', { id });
    return result.resumed;
  }

  async fork(id: string, childWorkspaceName?: string, childRef?: string): Promise<WorkspaceHandle> {
    const result = await this.client.request<WorkspaceForkResult>('workspace.fork', { id, childWorkspaceName, childRef });
    return new WorkspaceHandle(this.client, result.workspace);
  }

  async mintAuthRelay(params: AuthRelayMintParams): Promise<string> {
    const result = await this.client.request<AuthRelayMintResult>('authrelay.mint', {
      workspaceId: params.workspaceId,
      binding: params.binding,
      ttlSeconds: params.ttlSeconds,
    });
    return result.token;
  }

  async revokeAuthRelay(token: string): Promise<boolean> {
    const result = await this.client.request<AuthRelayRevokeResult>('authrelay.revoke', { token });
    return result.revoked;
  }

  async capabilities(): Promise<Capability[]> {
    const result = await this.client.request<CapabilitiesListResult>('capabilities.list', {});
    return result.capabilities;
  }
}
