import { RpcTransportCore } from './rpc/connection';
import {
  WorkspaceClientConfig,
  ConnectionState,
  DisconnectReason,
} from './types';
import { WorkspaceManager } from './workspace-manager';
import { PTYOperations } from './pty';
import { NodeWebSocketTransport } from './transport/node-websocket';
import type { BrowserWebSocketTransport } from './transport/browser-websocket';
import type { RPCSchema } from './rpc/schema';

type WsTransport = NodeWebSocketTransport | BrowserWebSocketTransport;

export class WorkspaceClient {
  private transport: WsTransport | null = null;
  private core = new RpcTransportCore({
    onParseError: (error) => console.error('Failed to parse RPC response:', error),
    onReconnectScheduled: ({ attempt, delay }) =>
      console.log(`Attempting to reconnect in ${delay}ms (attempt ${attempt})`),
    onMaxReconnectAttempts: () => console.error('Max reconnection attempts reached'),
    onReconnectConnectSuccess: () => console.log('Successfully reconnected'),
    onReconnectConnectFailure: (error) => console.error('Reconnection failed:', error),
  });
  private config: {
    endpoint: string;
    workspaceId?: string;
    token: string;
    reconnect: boolean;
  };
  private state: ConnectionState = 'disconnected';
  private disconnectCallbacks: Array<() => void> = [];
  private reconnectEnabled = true;

  public readonly shell: PTYOperations;
  public readonly workspaces: WorkspaceManager;

  constructor(config: WorkspaceClientConfig) {
    this.config = {
      endpoint: config.endpoint,
      workspaceId: config.workspaceId,
      token: config.token,
      reconnect: config.reconnect ?? true,
    };

    const bundleProvider = (): string | Promise<string> => {
      if (typeof (globalThis as { WebSocket?: unknown }).WebSocket !== 'undefined') {
        return '';
      }
      if (typeof process !== 'undefined' && process.versions?.node) {
        try {
          const { buildConfigBundle } = require('./bundle') as typeof import('./bundle');
          return buildConfigBundle();
        } catch {
          return '';
        }
      }
      return import('./bundle').then((m) => m.buildConfigBundle());
    };

    this.shell = new PTYOperations(this);
    this.workspaces = new WorkspaceManager(this, bundleProvider);
  }

  get isConnected(): boolean {
    return this.state === 'connected';
  }

  get connectionState(): ConnectionState {
    return this.state;
  }

  private async createTransport(): Promise<WsTransport> {
    if (typeof (globalThis as { WebSocket?: unknown }).WebSocket !== 'undefined') {
      const { BrowserWebSocketTransport } = await import('./transport/browser-websocket');
      return new BrowserWebSocketTransport();
    }
    const { NodeWebSocketTransport: NodeCtor } = await import('./transport/node-websocket');
    return new NodeCtor();
  }

  async connect(): Promise<void> {
    if (this.state === 'connected' || this.state === 'connecting') {
      return;
    }

    this.state = 'connecting';

    try {
      if (!this.transport) {
        if (typeof process !== 'undefined' && process.versions?.node) {
          this.transport = new NodeWebSocketTransport();
        } else {
          this.transport = await this.createTransport();
        }
      }

      const url = new URL(this.config.endpoint);
      if (this.config.workspaceId && this.config.workspaceId.trim() !== '') {
        url.searchParams.set('workspaceId', this.config.workspaceId);
      }
      url.searchParams.set('token', this.config.token);

      const t = this.transport;

      await new Promise<void>((resolve, reject) => {
        t.onOpen = () => {
          this.state = 'connected';
          this.core.resetReconnectAttempts();
          resolve();
        };
        t.onMessage = (data) => {
          this.core.handleMessage(data);
        };
        t.onClose = (code: number, reason: string) => {
          const disconnectReason: DisconnectReason = {
            code,
            reason,
          };
          this.handleDisconnect(disconnectReason);
        };
        t.onError = (error: Error) => {
          if (this.state === 'connecting') {
            reject(error);
          } else {
            console.error('WebSocket error:', error.message);
          }
        };
        t.connect(url.toString());
      });
    } catch (error) {
      this.transport = null;
      this.state = 'disconnected';
      throw error;
    }
  }

  async disconnect(): Promise<void> {
    this.reconnectEnabled = false;
    this.core.clearReconnectTimer();

    if (this.transport) {
      this.transport.disconnect();
      this.transport = null;
    }

    this.state = 'disconnected';
    this.core.rejectAllPending('Connection closed');
  }

  async [Symbol.asyncDispose](): Promise<void> {
    await this.disconnect();
  }

  onDisconnect(callback: () => void): void {
    this.disconnectCallbacks.push(callback);
  }

  onNotification(method: string, callback: (params: unknown) => void): () => void {
    return this.core.onNotification(method, callback);
  }

  async request<M extends keyof RPCSchema>(
    method: M,
    params: RPCSchema[M][0]
  ): Promise<RPCSchema[M][1]>;
  async request<T = unknown>(method: string, params?: Record<string, unknown>): Promise<T>;
  async request(method: string, params?: Record<string, unknown>): Promise<unknown> {
    return this.core.request(
      method,
      params,
      (data) => this.transport!.send(data),
      () => this.transport !== null && this.transport.isOpen()
    );
  }

  private handleDisconnect(reason: DisconnectReason): void {
    this.transport = null;
    this.state = 'disconnected';

    this.core.handleDisconnect(reason);

    this.disconnectCallbacks.forEach((callback) => callback());

    if (this.reconnectEnabled && this.config.reconnect) {
      this.state = 'reconnecting';
      this.core.scheduleReconnect(() => this.connect(), {
        enabled: true,
        maxAttempts: 10,
        baseDelay: 1000,
      });
    }
  }
}
