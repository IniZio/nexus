export type ConnectionStatus = 'connected' | 'disconnected' | 'connecting' | 'error';

export interface WorkspaceClientOptions {
  endpoint: string;
  workspaceId: string;
  token: string;
  reconnect?: boolean;
  reconnectInterval?: number;
}

export interface FileOperation {
  type: 'read' | 'write' | 'delete' | 'list';
  path: string;
  content?: string;
}

export interface WorkspaceEvent {
  type: 'status' | 'activity' | 'file_operation' | 'error';
  data: any;
  timestamp: number;
}

export class WorkspaceClient {
  workspaceId: string;
  status: { state: string };
  fs: {
    readFile: (path: string) => Promise<string>;
    writeFile: (path: string, content: string) => Promise<void>;
    deleteFile: (path: string) => Promise<void>;
    listFiles: (path: string) => Promise<string[]>;
  };
  private options: WorkspaceClientOptions;
  private _status: ConnectionStatus = 'disconnected';
  private ws: any = null;

  constructor(options: WorkspaceClientOptions) {
    this.options = options;
    this.workspaceId = options.workspaceId;
    this.status = { state: 'disconnected' };
    this.fs = {
      readFile: this.readFile.bind(this),
      writeFile: this.writeFile.bind(this),
      deleteFile: this.deleteFile.bind(this),
      listFiles: this.listFiles.bind(this),
    };
  }

  async connect(): Promise<void> {
    this._status = 'connecting';
    this.status.state = 'connecting';
    console.log(`[Mock] Connecting to ${this.options.endpoint}`);
    this._status = 'connected';
    this.status.state = 'connected';
  }

  async disconnect(): Promise<void> {
    this._status = 'disconnected';
    this.status.state = 'disconnected';
    console.log('[Mock] Disconnected');
  }

  async setStatus(status: 'active' | 'idle' | 'disconnected'): Promise<void> {
    this.status.state = status;
    console.log(`[Mock] Status set to: ${status}`);
  }

  getStatus(): { state: string } {
    return { state: this._status };
  }

  async ping(): Promise<void> {
    console.log('[Mock] Ping');
  }

  async exec(command: string, options?: { timeout?: number }): Promise<{ stdout: string; stderr: string; exitCode: number }> {
    console.log(`[Mock] Executing command: ${command}`);
    return { stdout: 'mock result', stderr: '', exitCode: 0 };
  }

  async executeCommand(command: string, args?: any): Promise<any> {
    console.log(`[Mock] Executing command: ${command}`);
    return { success: true, result: 'mock result' };
  }

  async readFile(path: string): Promise<string> {
    console.log(`[Mock] Reading file: ${path}`);
    return '';
  }

  async writeFile(path: string, content: string): Promise<void> {
    console.log(`[Mock] Writing file: ${path}`);
  }

  async deleteFile(path: string): Promise<void> {
    console.log(`[Mock] Deleting file: ${path}`);
  }

  async listFiles(path: string): Promise<string[]> {
    console.log(`[Mock] Listing files: ${path}`);
    return [];
  }

  on(event: string, handler: (...args: any[]) => void): void {
    console.log(`[Mock] Event listener registered: ${event}`);
  }

  off(event: string, handler: (...args: any[]) => void): void {
    console.log(`[Mock] Event listener removed: ${event}`);
  }
}

export function createWorkspaceClient(options: WorkspaceClientOptions): WorkspaceClient {
  return new WorkspaceClient(options);
}
