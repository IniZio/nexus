import { RpcTransportCore } from './rpc/connection';
import {
  WorkspaceClientConfig,
  ConnectionState,
  DisconnectReason,
} from './types';
import { WorkspaceManager } from './workspace-manager';
import { PTYOperations } from './pty';

type WSLike = {
  readyState: number;
  send: (data: string) => void;
  close: (code?: number, reason?: string) => void;
  addEventListener?: (event: string, handler: (...args: unknown[]) => void) => void;
  on?: (event: string, handler: (...args: unknown[]) => void) => void;
};

export class BrowserWorkspaceClient {
  private ws: WSLike | null = null;
  private core = new RpcTransportCore({
    onParseError: (err) => console.warn('[nexus/browser] RPC parse error:', err),
    onReconnectScheduled: ({ delay, attempt }) =>
      console.debug(`[nexus/browser] reconnect in ${delay}ms (attempt ${attempt})`),
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

        const WSCtor = (globalThis as { WebSocket?: new (url: string) => WSLike }).WebSocket;
        if (!WSCtor) {
          throw new Error('WebSocket is not available in this runtime');
        }
        this.ws = new WSCtor(url.toString());

        const onOpen = () => {
          this.state = 'connected';
          this.core.resetReconnectAttempts();
          resolve();
        };
        const onMessage = (evt: { data?: unknown } | string) => {
          const raw = typeof evt === 'string' ? evt : (evt as { data?: unknown }).data;
          this.core.handleMessage(this.coerceMessage(raw));
        };
        const onClose = (evt: { code?: number; reason?: string } | number, reasonMaybe?: string) => {
          const code = typeof evt === 'number' ? evt : evt.code ?? 1000;
          const reason = typeof evt === 'number' ? reasonMaybe ?? '' : evt.reason ?? '';
          this.handleDisconnect({ code, reason });
        };
        const onError = (error: unknown) => {
          if (this.state === 'connecting') {
            reject(error instanceof Error ? error : new Error('WebSocket connection error'));
          }
        };

        if (this.ws.addEventListener) {
          this.ws.addEventListener('open', onOpen);
          this.ws.addEventListener('message', onMessage as (...args: unknown[]) => void);
          this.ws.addEventListener('close', onClose as (...args: unknown[]) => void);
          this.ws.addEventListener('error', onError as (...args: unknown[]) => void);
        } else if (this.ws.on) {
          this.ws.on('open', onOpen);
          this.ws.on('message', onMessage as (...args: unknown[]) => void);
          this.ws.on('close', onClose as (...args: unknown[]) => void);
          this.ws.on('error', onError);
        }
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
      () => this.ws !== null && this.ws.readyState === 1,
      'Not connected to workspace'
    );
  }

  private coerceMessage(raw: unknown): string {
    if (typeof raw === 'string') {
      return raw;
    }
    if (raw && typeof raw === 'object' && 'toString' in raw) {
      return String(raw);
    }
    return '';
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
