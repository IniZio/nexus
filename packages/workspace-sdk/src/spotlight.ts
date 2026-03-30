import {
  SpotlightApplyComposePortsResult,
  SpotlightExposeOptions,
  SpotlightForward,
  SpotlightListResult,
} from './types';
import type { RPCClient } from './workspace-handle';

export class SpotlightOperations {
  private client: RPCClient;

  constructor(client: RPCClient) {
    this.client = client;
  }

  async expose(workspaceId: string, options: SpotlightExposeOptions): Promise<SpotlightForward> {
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
    return this.client.request<SpotlightListResult>('spotlight.list', { workspaceId });
  }

  async close(id: string): Promise<boolean> {
    const result = await this.client.request<{ closed: boolean }>('spotlight.close', { id });
    return result.closed;
  }

  async applyComposePorts(workspaceId: string): Promise<SpotlightApplyComposePortsResult> {
    return this.client.request<SpotlightApplyComposePortsResult>('spotlight.applyComposePorts', { workspaceId });
  }
}
