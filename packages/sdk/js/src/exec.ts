import { WorkspaceClient } from './client';
import { ExecOptions, ExecResult, ExecParams, ExecResultData } from './types';

interface RPCClient {
  request<T = unknown>(method: string, params?: Record<string, unknown>): Promise<T>;
}

export class ExecOperations {
  private client: RPCClient;
  private workspaceId?: string;

  constructor(client: WorkspaceClient | RPCClient, defaultParams: Record<string, unknown> = {}) {
    this.client = client;
    this.workspaceId = typeof defaultParams.workspaceId === 'string' ? defaultParams.workspaceId : undefined;
  }

  private params<T extends Record<string, unknown>>(input: T): T {
    if (!this.workspaceId) {
      return input;
    }
    return { ...input, workspaceId: this.workspaceId };
  }

  async exec(command: string, args: string[] = [], options: ExecOptions = {}): Promise<ExecResult> {
    const params: ExecParams = this.params({
      command,
      args,
      options,
    });

    const result = await this.client.request<ExecResultData>('exec', params);

    return {
      stdout: result.stdout,
      stderr: result.stderr,
      exitCode: result.exit_code,
    };
  }
}
