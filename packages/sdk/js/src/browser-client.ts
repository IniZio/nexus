import {
  WorkspaceClientConfig,
  ConnectionState,
  RPCRequest,
  RPCResponse,
  DisconnectReason,
} from './types';
import { FSOperations } from './fs';
import { ExecOperations } from './exec';
import { SpotlightOperations } from './spotlight';
import { WorkspaceManager } from './workspace-manager';

type WSLike = {
  readyState: number;
  send: (data: string) => void;
  close: (code?: number, reason?: string) => void;
  addEventListener?: (event: string, handler: (...args: unknown[]) => void) => void;
  on?: (event: string, handler: (...args: unknown[]) => void) => void;
};

export class BrowserWorkspaceClient {
  private ws: WSLike | null = null;
  private config: {
    endpoint: string;
    workspaceId?: string;
    token: string;
    reconnect: boolean;
    reconnectDelay: number;
    maxReconnectAttempts: number;
  };
  private state: ConnectionState = 'disconnected';
  private reconnectAttempts = 0;
  private requestMap: Map<string, { resolve: (value: unknown) => void; reject: (reason: Error) => void }> = new Map();
  private disconnectCallbacks: Array<() => void> = [];
  private reconnectTimeout: ReturnType<typeof setTimeout> | null = null;
  private reconnectEnabled = true;
  private requestId = 0;

  public readonly fs: FSOperations;
  public readonly exec: ExecOperations;
  public readonly spotlight: SpotlightOperations;
  public readonly workspace: WorkspaceManager;

  constructor(config: WorkspaceClientConfig) {
    this.config = {
      endpoint: config.endpoint,
      workspaceId: config.workspaceId,
      token: config.token,
      reconnect: config.reconnect ?? true,
      reconnectDelay: config.reconnectDelay ?? 1000,
      maxReconnectAttempts: config.maxReconnectAttempts ?? 10,
    };

    this.fs = new FSOperations(this, this.config.workspaceId ? { workspaceId: this.config.workspaceId } : {});
    this.exec = new ExecOperations(this, this.config.workspaceId ? { workspaceId: this.config.workspaceId } : {});
    this.spotlight = new SpotlightOperations(this, this.config.workspaceId ? { workspaceId: this.config.workspaceId } : {});
    this.workspace = new WorkspaceManager(this);
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
          this.reconnectAttempts = 0;
          resolve();
        };
        const onMessage = (evt: { data?: unknown } | string) => {
          const raw = typeof evt === 'string' ? evt : (evt as { data?: unknown }).data;
          this.handleMessage(this.coerceMessage(raw));
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
    if (this.reconnectTimeout) {
      clearTimeout(this.reconnectTimeout);
      this.reconnectTimeout = null;
    }
    if (this.ws) {
      this.ws.close(1000, 'Client disconnect');
      this.ws = null;
    }
    this.state = 'disconnected';
    this.requestMap.forEach(({ reject }) => reject(new Error('Connection closed')));
    this.requestMap.clear();
  }

  onDisconnect(callback: () => void): void {
    this.disconnectCallbacks.push(callback);
  }

  async request<T = unknown>(method: string, params?: Record<string, unknown>): Promise<T> {
    if (!this.ws || this.ws.readyState !== 1) {
      throw new Error('Not connected to workspace');
    }

    const id = this.generateRequestId();
    const request: RPCRequest = {
      jsonrpc: '2.0',
      id,
      method,
      params,
    };

    return new Promise<T>((resolve, reject) => {
      this.requestMap.set(id, { resolve: resolve as (value: unknown) => void, reject });
      try {
        this.ws!.send(JSON.stringify(request));
      } catch (error) {
        this.requestMap.delete(id);
        reject(error instanceof Error ? error : new Error('failed to send request'));
      }
    });
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

  private generateRequestId(): string {
    this.requestId++;
    return `req-${Date.now()}-${this.requestId}`;
  }

  private handleMessage(data: string): void {
    try {
      const response: RPCResponse = JSON.parse(data);
      if (!response.id) {
        return;
      }
      const pending = this.requestMap.get(response.id);
      if (!pending) {
        return;
      }
      this.requestMap.delete(response.id);
      if (response.error) {
        pending.reject(new Error(response.error.message));
      } else {
        pending.resolve(response.result);
      }
    } catch {
      // ignore malformed messages
    }
  }

  private handleDisconnect(reason: DisconnectReason): void {
    this.ws = null;
    this.state = 'disconnected';
    this.requestMap.forEach(({ reject }) => reject(new Error(`Connection closed: ${reason.reason}`)));
    this.requestMap.clear();
    this.disconnectCallbacks.forEach((callback) => callback());
    if (this.reconnectEnabled && this.config.reconnect) {
      this.attemptReconnect();
    }
  }

  private attemptReconnect(): void {
    if (this.reconnectAttempts >= this.config.maxReconnectAttempts) {
      return;
    }
    this.state = 'reconnecting';
    this.reconnectAttempts++;
    const delay = Math.min(this.config.reconnectDelay * Math.pow(2, this.reconnectAttempts - 1), 30000);
    this.reconnectTimeout = setTimeout(async () => {
      try {
        await this.connect();
      } catch {
        // handled by retry flow
      }
    }, delay);
  }
}

export { BrowserWorkspaceClient as BrowserClient };
