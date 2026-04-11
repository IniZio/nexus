import WebSocket from 'ws';
import { RpcTransportCore } from './rpc/connection';
import {
  WorkspaceClientConfig,
  ConnectionState,
  DisconnectReason,
} from './types';
import { WorkspaceManager } from './workspace-manager';
import { PTYOperations } from './pty';

export class WorkspaceClient {
  private ws: WebSocket | null = null;
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
    reconnectDelay: number;
    maxReconnectAttempts: number;
  };
  private state: ConnectionState = 'disconnected';
  private disconnectCallbacks: Array<() => void> = [];
  private reconnectEnabled = true;

  public readonly ssh: PTYOperations;
  public readonly workspaces: WorkspaceManager;

  constructor(config: WorkspaceClientConfig) {
    this.config = {
      endpoint: config.endpoint,
      workspaceId: config.workspaceId,
      token: config.token,
      reconnect: config.reconnect ?? true,
      reconnectDelay: config.reconnectDelay ?? 1000,
      maxReconnectAttempts: config.maxReconnectAttempts ?? 10,
    };

    this.ssh = new PTYOperations(this);
    this.workspaces = new WorkspaceManager(this);
  }

  get isConnected(): boolean {
    return this.state === 'connected';
  }

  get connectionState(): ConnectionState {
    return this.state;
  }

  async connect(): Promise<void> {
    if (this.state === 'connected' || this.state === 'connecting') {
      return;
    }

    this.state = 'connecting';

    return new Promise((resolve, reject) => {
      try {
        const url = new URL(this.config.endpoint);
        if (this.config.workspaceId && this.config.workspaceId.trim() !== '') {
          url.searchParams.set('workspaceId', this.config.workspaceId);
        }
        url.searchParams.set('token', this.config.token);

        this.ws = new WebSocket(url.toString());

        this.ws.on('open', () => {
          this.state = 'connected';
          this.core.resetReconnectAttempts();
          resolve();
        });

        this.ws.on('message', (data: Buffer) => {
          this.core.handleMessage(data.toString());
        });

        this.ws.on('close', (code: number, reason: Buffer) => {
          const disconnectReason: DisconnectReason = {
            code,
            reason: reason.toString(),
          };
          this.handleDisconnect(disconnectReason);
        });

        this.ws.on('error', (error: Error) => {
          if (this.state === 'connecting') {
            reject(error);
          } else {
            console.error('WebSocket error:', error.message);
          }
        });
      } catch (error) {
        this.state = 'disconnected';
        reject(error);
      }
    });
  }

  async disconnect(): Promise<void> {
    this.reconnectEnabled = false;
    this.core.clearReconnectTimer();

    if (this.ws) {
      this.ws.close(1000, 'Client disconnect');
      this.ws = null;
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

  async request<T = unknown>(method: string, params?: Record<string, unknown>): Promise<T> {
    return this.core.request<T>(
      method,
      params,
      (data) => this.ws!.send(data),
      () => this.ws !== null && this.ws.readyState === WebSocket.OPEN
    );
  }

  private handleDisconnect(reason: DisconnectReason): void {
    this.ws = null;
    this.state = 'disconnected';

    this.core.handleDisconnect(reason);

    this.disconnectCallbacks.forEach((callback) => callback());

    if (this.reconnectEnabled && this.config.reconnect) {
      this.state = 'reconnecting';
      this.core.scheduleReconnect(() => this.connect(), {
        enabled: true,
        maxAttempts: this.config.maxReconnectAttempts,
        baseDelay: this.config.reconnectDelay,
      });
    }
  }
}
