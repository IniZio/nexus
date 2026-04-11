import { RpcTransportCore } from './rpc/connection';
import {
  WorkspaceClientConfig,
  ConnectionState,
  DisconnectReason,
} from './types';
import { WorkspaceManager } from './workspace-manager';
import { PTYOperations } from './pty';
import { NodeWebSocketTransport } from './transport/node-websocket';

export class WorkspaceClient {
  private transport: NodeWebSocketTransport | null = null;
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

        const t = new NodeWebSocketTransport();
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
        this.transport = t;
        t.connect(url.toString());
      } catch (error) {
        this.transport = null;
        this.state = 'disconnected';
        reject(error);
      }
    });
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

  async request<T = unknown>(method: string, params?: Record<string, unknown>): Promise<T> {
    return this.core.request<T>(
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
        maxAttempts: this.config.maxReconnectAttempts,
        baseDelay: this.config.reconnectDelay,
      });
    }
  }
}
