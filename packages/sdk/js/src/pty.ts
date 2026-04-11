import {
  PTYCloseResult,
  PTYDataEvent,
  PTYExitEvent,
  PTYOpenParams,
  PTYOpenResult,
  PTYResizeResult,
  PTYWriteResult,
} from './types';
import type { RPCClient } from './rpc/types';

export class PTYOperations {
  private client: RPCClient;

  constructor(client: RPCClient) {
    this.client = client;
  }

  async open(params: PTYOpenParams): Promise<string> {
    const result = await this.client.request<PTYOpenResult>('pty.open', params as unknown as Record<string, unknown>);
    return result.sessionId;
  }

  async write(sessionId: string, data: string): Promise<boolean> {
    const result = await this.client.request<PTYWriteResult>('pty.write', { sessionId, data });
    return result.ok;
  }

  async resize(sessionId: string, cols: number, rows: number): Promise<boolean> {
    const result = await this.client.request<PTYResizeResult>('pty.resize', { sessionId, cols, rows });
    return result.ok;
  }

  async close(sessionId: string): Promise<boolean> {
    const result = await this.client.request<PTYCloseResult>('pty.close', { sessionId });
    return result.closed;
  }

  onData(callback: (event: PTYDataEvent) => void): () => void {
    return this.client.onNotification('pty.data', (params: unknown) => {
      const evt = params as PTYDataEvent;
      if (!evt || typeof evt.sessionId !== 'string' || typeof evt.data !== 'string') {
        return;
      }
      callback(evt);
    });
  }

  onExit(callback: (event: PTYExitEvent) => void): () => void {
    return this.client.onNotification('pty.exit', (params: unknown) => {
      const evt = params as PTYExitEvent;
      if (!evt || typeof evt.sessionId !== 'string' || typeof evt.exitCode !== 'number') {
        return;
      }
      callback(evt);
    });
  }
}
