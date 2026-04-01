import { WorkspaceClient } from './client';
import { ExecOptions, ExecResult, ExecParams, ExecResultData } from './types';

interface RPCClient {
  request<T = unknown>(method: string, params?: Record<string, unknown>): Promise<T>;
}

export class ExecOperations {
  private client: RPCClient;
  private defaultParams: Record<string, unknown>;

  constructor(client: WorkspaceClient | RPCClient, defaultParams: Record<string, unknown> = {}) {
    this.client = client;
    this.defaultParams = defaultParams;
  }

  async exec(command: string, args: string[] = [], options: ExecOptions = {}): Promise<ExecResult> {
    const params: ExecParams = {
      command,
      args,
      options,
      ...this.defaultParams,
    };

    const result = await this.client.request<ExecResultData>('exec', params);

    return {
      stdout: result.stdout,
      stderr: result.stderr,
      exitCode: result.exit_code,
    };
  }
}
