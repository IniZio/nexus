import type { DisconnectReason, RPCRequest, RPCResponse } from '../types/rpc';

type Pending = { resolve: (value: unknown) => void; reject: (reason: Error) => void };

export type RpcTransportHooks = {
  onParseError?: (error: unknown) => void;
  onReconnectScheduled?: (info: { attempt: number; delay: number }) => void;
  onMaxReconnectAttempts?: () => void;
  onReconnectConnectSuccess?: () => void;
  onReconnectConnectFailure?: (error: unknown) => void;
};

export class RpcTransportCore {
  private requestMap = new Map<string, Pending>();
  private notificationCallbacks = new Map<string, Array<(params: unknown) => void>>();
  private requestId = 0;
  private reconnectAttempts = 0;
  private reconnectTimeout: ReturnType<typeof setTimeout> | null = null;

  constructor(private hooks: RpcTransportHooks = {}) {}

  generateRequestId(): string {
    this.requestId++;
    return `req-${Date.now()}-${this.requestId}`;
  }

  handleMessage(data: string): void {
    try {
      const response = JSON.parse(data) as RPCResponse;
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
      if (!response.id && response.method) {
        const callbacks = this.notificationCallbacks.get(response.method) ?? [];
        for (const cb of callbacks) {
          cb(response.params);
        }
      }
    } catch (error) {
      this.hooks.onParseError?.(error);
    }
  }

  rejectAllPending(message: string): void {
    const err = new Error(message);
    this.requestMap.forEach(({ reject }) => {
      reject(err);
    });
    this.requestMap.clear();
  }

  handleDisconnect(reason: DisconnectReason): void {
    this.rejectAllPending(`Connection closed: ${reason.reason}`);
  }

  request<T>(
    method: string,
    params: Record<string, unknown> | undefined,
    send: (data: string) => void,
    ready: () => boolean,
    notReadyMessage = 'Not connected to workspace'
  ): Promise<T> {
    if (!ready()) {
      return Promise.reject(new Error(notReadyMessage));
    }
    const id = this.generateRequestId();
    const req: RPCRequest = {
      jsonrpc: '2.0',
      id,
      method,
      params,
    };
    return new Promise<T>((resolve, reject) => {
      this.requestMap.set(id, { resolve: resolve as (value: unknown) => void, reject });
      try {
        send(JSON.stringify(req));
      } catch (error) {
        this.requestMap.delete(id);
        reject(error instanceof Error ? error : new Error(String(error)));
      }
    });
  }

  onNotification(method: string, callback: (params: unknown) => void): () => void {
    const existing = this.notificationCallbacks.get(method) ?? [];
    existing.push(callback);
    this.notificationCallbacks.set(method, existing);
    return () => {
      const callbacks = this.notificationCallbacks.get(method) ?? [];
      const next = callbacks.filter((cb) => cb !== callback);
      if (next.length === 0) {
        this.notificationCallbacks.delete(method);
        return;
      }
      this.notificationCallbacks.set(method, next);
    };
  }

  clearReconnectTimer(): void {
    if (this.reconnectTimeout) {
      clearTimeout(this.reconnectTimeout);
      this.reconnectTimeout = null;
    }
  }

  resetReconnectAttempts(): void {
    this.reconnectAttempts = 0;
  }

  scheduleReconnect(
    connect: () => Promise<void>,
    cfg: { enabled: boolean; maxAttempts: number; baseDelay: number }
  ): void {
    if (!cfg.enabled) {
      return;
    }
    if (this.reconnectAttempts >= cfg.maxAttempts) {
      this.hooks.onMaxReconnectAttempts?.();
      return;
    }
    this.reconnectAttempts++;
    const delay = Math.min(cfg.baseDelay * Math.pow(2, this.reconnectAttempts - 1), 30000);
    this.hooks.onReconnectScheduled?.({ attempt: this.reconnectAttempts, delay });
    this.reconnectTimeout = setTimeout(async () => {
      try {
        await connect();
        this.hooks.onReconnectConnectSuccess?.();
      } catch (error) {
        this.hooks.onReconnectConnectFailure?.(error);
      }
    }, delay);
  }
}
