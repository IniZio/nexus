import {
  PTYCloseResult,
  PTYAttachParams,
  PTYAttachResult,
  PTYDataEvent,
  PTYExitEvent,
  PTYGetParams,
  PTYGetResult,
  PTYListParams,
  PTYListResult,
  PTYOpenParams,
  PTYOpenResult,
  PTYRenameParams,
  PTYRenameResult,
  PTYResizeResult,
  PTYSessionInfo,
  PTYTmuxCommandParams,
  PTYTmuxCommandResult,
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

  async attach(sessionId: string): Promise<boolean> {
    const params: PTYAttachParams = { sessionId };
    const result = await this.client.request<PTYAttachResult>('pty.attach', params as unknown as Record<string, unknown>);
    return result.attached;
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

  // Multi-tab session management

  /**
   * List all PTY sessions for a workspace
   */
  async list(workspaceId: string): Promise<PTYSessionInfo[]> {
    const params: PTYListParams = { workspaceId };
    const result = await this.client.request<PTYListResult>('pty.list', params as unknown as Record<string, unknown>);
    return result.sessions;
  }

  /**
   * Get info about a specific PTY session
   */
  async get(sessionId: string): Promise<PTYSessionInfo> {
    const params: PTYGetParams = { sessionId };
    const result = await this.client.request<PTYGetResult>('pty.get', params as unknown as Record<string, unknown>);
    return result.session;
  }

  /**
   * Rename a PTY session (updates the tab name)
   */
  async rename(sessionId: string, name: string): Promise<boolean> {
    const params: PTYRenameParams = { sessionId, name };
    const result = await this.client.request<PTYRenameResult>('pty.rename', params as unknown as Record<string, unknown>);
    return result.success;
  }

  // Tmux support

  /**
   * Execute a tmux command on a tmux-based session
   * Common commands: "new-window", "select-window", "list-windows", "kill-window"
   */
  async tmuxCommand(sessionId: string, command: string, args?: string[]): Promise<PTYTmuxCommandResult> {
    const params: PTYTmuxCommandParams = { sessionId, command, args };
    return await this.client.request<PTYTmuxCommandResult>('pty.tmux', params as unknown as Record<string, unknown>);
  }
}
