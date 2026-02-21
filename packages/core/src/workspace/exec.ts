import { WorkspaceClient } from './client.js';
import { ExecOptions, ExecResult, ExecParams, ExecResultData } from './types.js';

export class ExecOperations {
  private client: WorkspaceClient;

  constructor(client: WorkspaceClient) {
    this.client = client;
  }

  async exec(command: string, args: string[] = [], options: ExecOptions = {}): Promise<ExecResult> {
    const params: ExecParams = {
      command,
      args,
      options,
    };

    const result = await this.client.request<ExecResultData>('exec', params);

    return {
      stdout: result.stdout,
      stderr: result.stderr,
      exitCode: result.exit_code,
    };
  }
}
