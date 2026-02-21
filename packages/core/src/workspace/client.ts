import WebSocket from 'ws';
import {
  WorkspaceClientConfig,
  ConnectionState,
  RPCRequest,
  RPCResponse,
  DisconnectReason,
} from './types.js';
import { FSOperations } from './fs.js';
import { ExecOperations } from './exec.js';

export class WorkspaceClient {
  private ws: WebSocket | null = null;
  private config: Required<WorkspaceClientConfig>;
  private state: ConnectionState = 'disconnected';
  private reconnectAttempts = 0;
  private requestMap: Map<string, { resolve: (value: unknown) => void; reject: (reason: Error) => void }> = new Map();
  private disconnectCallbacks: Array<() => void> = [];
  private reconnectTimeout: NodeJS.Timeout | null = null;
  private messageQueue: RPCRequest[] = [];
  private reconnectEnabled = true;
  private requestId = 0;

  public readonly fs: FSOperations;
  public readonly exec: ExecOperations;

  constructor(config: WorkspaceClientConfig) {
    this.config = {
      endpoint: config.endpoint,
      workspaceId: config.workspaceId,
      token: config.token,
      reconnect: config.reconnect ?? true,
      reconnectDelay: config.reconnectDelay ?? 1000,
      maxReconnectAttempts: config.maxReconnectAttempts ?? 10,
    };

    this.fs = new FSOperations(this);
    this.exec = new ExecOperations(this);
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
        url.searchParams.set('workspaceId', this.config.workspaceId);
        url.searchParams.set('token', this.config.token);

        this.ws = new WebSocket(url.toString());

        this.ws.on('open', () => {
          this.state = 'connected';
          this.reconnectAttempts = 0;
          this.processMessageQueue();
          resolve();
        });

        this.ws.on('message', (data: Buffer) => {
          this.handleMessage(data.toString());
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

    if (this.reconnectTimeout) {
      clearTimeout(this.reconnectTimeout);
      this.reconnectTimeout = null;
    }

    if (this.ws) {
      this.ws.close(1000, 'Client disconnect');
      this.ws = null;
    }

    this.state = 'disconnected';
    this.requestMap.forEach(({ reject }) => {
      reject(new Error('Connection closed'));
    });
    this.requestMap.clear();
    this.messageQueue = [];
  }

  onDisconnect(callback: () => void): void {
    this.disconnectCallbacks.push(callback);
  }

  async request<T = unknown>(method: string, params?: Record<string, unknown>): Promise<T> {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
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
        reject(error);
      }
    });
  }

  private generateRequestId(): string {
    this.requestId++;
    return `req-${Date.now()}-${this.requestId}`;
  }

  private handleMessage(data: string): void {
    try {
      const response: RPCResponse = JSON.parse(data);

      if (response.id) {
        const pending = this.requestMap.get(response.id);

        if (pending) {
          this.requestMap.delete(response.id);

          if (response.error) {
            pending.reject(new Error(response.error.message));
          } else {
            pending.resolve(response.result);
          }
        }
      }
    } catch (error) {
      console.error('Failed to parse RPC response:', error);
    }
  }

  private handleDisconnect(reason: DisconnectReason): void {
    this.ws = null;
    this.state = 'disconnected';

    this.requestMap.forEach(({ reject }) => {
      reject(new Error(`Connection closed: ${reason.reason}`));
    });
    this.requestMap.clear();

    this.disconnectCallbacks.forEach((callback) => callback());

    if (this.reconnectEnabled && this.config.reconnect) {
      this.attemptReconnect();
    }
  }

  private attemptReconnect(): void {
    if (this.reconnectAttempts >= this.config.maxReconnectAttempts) {
      console.error('Max reconnection attempts reached');
      return;
    }

    this.state = 'reconnecting';
    this.reconnectAttempts++;

    const delay = this.calculateExponentialBackoff();
    console.log(`Attempting to reconnect in ${delay}ms (attempt ${this.reconnectAttempts})`);

    this.reconnectTimeout = setTimeout(async () => {
      try {
        await this.connect();
        console.log('Successfully reconnected');
      } catch (error) {
        console.error('Reconnection failed:', error);
      }
    }, delay);
  }

  private calculateExponentialBackoff(): number {
    return Math.min(
      this.config.reconnectDelay * Math.pow(2, this.reconnectAttempts - 1),
      30000
    );
  }

  private processMessageQueue(): void {
    while (this.messageQueue.length > 0) {
      const request = this.messageQueue.shift();

      if (request && this.ws && this.ws.readyState === WebSocket.OPEN) {
        try {
          this.ws.send(JSON.stringify(request));
        } catch (error) {
          console.error('Failed to send queued message:', error);
        }
      }
    }
  }
}
