import {
  SpotlightApplyComposePortsError,
  SpotlightExposeOptions,
  SpotlightForward,
} from './types';
import type { RPCClient } from './workspace-handle';

export type TunnelHandle = SpotlightForward & {
  stop: () => Promise<boolean>;
};

export type TunnelListResult = {
  forwards: TunnelHandle[];
};

export type TunnelApplyDefaultsResult = {
  forwards: TunnelHandle[];
};

export type TunnelApplyComposePortsResult = {
  forwards: TunnelHandle[];
  errors: SpotlightApplyComposePortsError[];
};

export class TunnelOperations {
  private client: RPCClient;
  private workspaceId?: string;

  constructor(client: RPCClient, defaultParams: Record<string, unknown> = {}) {
    this.client = client;
    this.workspaceId = typeof defaultParams.workspaceId === 'string' ? defaultParams.workspaceId : undefined;
  }

  async start(workspaceId: string, options: SpotlightExposeOptions): Promise<TunnelHandle>;
  async start(options: SpotlightExposeOptions): Promise<TunnelHandle>;
  async start(workspaceOrOptions: string | SpotlightExposeOptions, maybeOptions?: SpotlightExposeOptions): Promise<TunnelHandle> {
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

    return this.attachStop(result.forward);
  }

  async list(workspaceId?: string): Promise<TunnelListResult> {
    const resolvedWorkspaceID = this.resolveWorkspaceID(workspaceId);
    const result = await this.client.request<{ forwards: SpotlightForward[] }>('spotlight.list', { workspaceId: resolvedWorkspaceID });
    return {
      forwards: result.forwards.map((forward) => this.attachStop(forward)),
    };
  }

  async stop(id: string): Promise<boolean> {
    const result = await this.client.request<{ closed: boolean }>('spotlight.close', { id });
    return result.closed;
  }

  async applyDefaults(workspaceId?: string): Promise<TunnelApplyDefaultsResult> {
    const resolvedWorkspaceID = this.resolveWorkspaceID(workspaceId);
    const result = await this.client.request<{ forwards: SpotlightForward[] }>('spotlight.applyDefaults', { workspaceId: resolvedWorkspaceID });
    return {
      forwards: result.forwards.map((forward) => this.attachStop(forward)),
    };
  }

  async applyComposePorts(workspaceId?: string): Promise<TunnelApplyComposePortsResult> {
    const resolvedWorkspaceID = this.resolveWorkspaceID(workspaceId);
    const result = await this.client.request<{ forwards: SpotlightForward[]; errors: SpotlightApplyComposePortsError[] }>(
      'spotlight.applyComposePorts',
      { workspaceId: resolvedWorkspaceID }
    );
    return {
      forwards: result.forwards.map((forward) => this.attachStop(forward)),
      errors: result.errors,
    };
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
      throw new Error('workspaceId is required for tunnel.expose');
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

    throw new Error('workspaceId is required for tunnel operation');
  }

  private attachStop(forward: SpotlightForward): TunnelHandle {
    return {
      ...forward,
      stop: async () => this.stop(forward.id),
    };
  }
}
