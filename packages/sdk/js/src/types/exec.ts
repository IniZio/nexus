export interface ExecOptions {
  cwd?: string;
  env?: Record<string, string>;
  timeout?: number;
  authRelayToken?: string;
}

export interface ExecResult {
  stdout: string;
  stderr: string;
  exitCode: number;
}

export interface ExecParams {
  command: string;
  args?: string[];
  options?: ExecOptions;
  [key: string]: unknown;
}

export interface ExecResultData {
  stdout: string;
  stderr: string;
  exit_code: number;
}
