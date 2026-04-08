import {
  SpotlightApplyComposePortsResult,
  SpotlightApplyDefaultsResult,
  SpotlightExposeOptions,
  SpotlightForward,
  SpotlightListResult,
} from './types';
import type { RPCClient } from './workspace-handle';

export class SpotlightOperations {
  private client: RPCClient;
  private workspaceId?: string;

  constructor(client: RPCClient, defaultParams: Record<string, unknown> = {}) {
    this.client = client;
    this.workspaceId = typeof defaultParams.workspaceId === 'string' ? defaultParams.workspaceId : undefined;
  }

  async expose(workspaceId: string, options: SpotlightExposeOptions): Promise<SpotlightForward>;
  async expose(options: SpotlightExposeOptions): Promise<SpotlightForward>;
  async expose(workspaceOrOptions: string | SpotlightExposeOptions, maybeOptions?: SpotlightExposeOptions): Promise<SpotlightForward> {
    const { workspaceId, options } = this.resolveWorkspaceAndOptions(workspaceOrOptions, maybeOptions);
    const result = await this.client.request<{ forward: SpotlightForward }>('spotlight.expose', {
      spec: {
        workspaceId,
        service: options.service,
        remotePort: options.remotePort,
        localPort: options.localPort,
        host: options.host,
      },
    });

    return result.forward;
  }

  async list(workspaceId?: string): Promise<SpotlightListResult> {
    const resolvedWorkspaceID = this.resolveWorkspaceID(workspaceId);
    return this.client.request<SpotlightListResult>('spotlight.list', { workspaceId: resolvedWorkspaceID });
  }

  async close(id: string): Promise<boolean> {
    const result = await this.client.request<{ closed: boolean }>('spotlight.close', { id });
    return result.closed;
  }

  async applyDefaults(workspaceId?: string): Promise<SpotlightApplyDefaultsResult> {
    const resolvedWorkspaceID = this.resolveWorkspaceID(workspaceId);
    return this.client.request<SpotlightApplyDefaultsResult>('spotlight.applyDefaults', { workspaceId: resolvedWorkspaceID });
  }

  async applyComposePorts(workspaceId?: string): Promise<SpotlightApplyComposePortsResult> {
    const resolvedWorkspaceID = this.resolveWorkspaceID(workspaceId);
    return this.client.request<SpotlightApplyComposePortsResult>('spotlight.applyComposePorts', { workspaceId: resolvedWorkspaceID });
  }

  private resolveWorkspaceAndOptions(workspaceOrOptions: string | SpotlightExposeOptions, maybeOptions?: SpotlightExposeOptions): {
    workspaceId: string;
    options: SpotlightExposeOptions;
  } {
    if (typeof workspaceOrOptions === 'string') {
      if (!maybeOptions) {
        throw new Error('options are required when workspaceId is provided');
      }
      return { workspaceId: workspaceOrOptions, options: maybeOptions };
    }

    const scopedWorkspaceID = this.resolveWorkspaceID();
    if (!scopedWorkspaceID) {
      throw new Error('workspaceId is required for spotlight.expose');
    }

    return { workspaceId: scopedWorkspaceID, options: workspaceOrOptions };
  }

  private resolveWorkspaceID(workspaceId?: string): string {
    if (workspaceId && workspaceId.trim() !== '') {
      return workspaceId;
    }

    if (this.workspaceId && this.workspaceId.trim() !== '') {
      return this.workspaceId;
    }

    throw new Error('workspaceId is required for spotlight operation');
  }
}
