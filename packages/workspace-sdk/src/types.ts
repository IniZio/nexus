export interface WorkspaceClientConfig {
  endpoint: string;
  workspaceId: string;
  token: string;
  reconnect?: boolean;
  reconnectDelay?: number;
  maxReconnectAttempts?: number;
}

export interface FileStats {
  isFile: boolean;
  isDirectory: boolean;
  size: number;
  mtime: string;
  ctime: string;
  mode: number;
}

export interface ExecOptions {
  cwd?: string;
  env?: Record<string, string>;
  timeout?: number;
}

export interface ExecResult {
  stdout: string;
  stderr: string;
  exitCode: number;
}

export interface RPCRequest {
  jsonrpc: '2.0';
  id: string;
  method: string;
  params?: Record<string, unknown>;
}

export interface RPCResponse {
  jsonrpc: '2.0';
  id: string;
  result?: unknown;
  error?: RPCError;
}

export interface RPCError {
  code: number;
  message: string;
  data?: unknown;
}

export interface DisconnectReason {
  code: number;
  reason: string;
}

export type ConnectionState = 'disconnected' | 'connecting' | 'connected' | 'reconnecting';

export interface FSReadFileParams {
  path: string;
  encoding?: string;
  [key: string]: unknown;
}

export interface FSWriteFileParams {
  path: string;
  content: string | Buffer;
  encoding?: string;
  [key: string]: unknown;
}

export interface FSExistsParams {
  path: string;
  [key: string]: unknown;
}

export interface FSReaddirParams {
  path: string;
  [key: string]: unknown;
}

export interface FSMkdirParams {
  path: string;
  recursive?: boolean;
  [key: string]: unknown;
}

export interface FSRmParams {
  path: string;
  recursive?: boolean;
  [key: string]: unknown;
}

export interface FSStatParams {
  path: string;
  [key: string]: unknown;
}

export interface ExecParams {
  command: string;
  args?: string[];
  options?: ExecOptions;
  [key: string]: unknown;
}

export interface FSWriteFileParams {
  path: string;
  content: string | Buffer;
  encoding?: string;
}

export interface FSExistsParams {
  path: string;
}

export interface FSReaddirParams {
  path: string;
}

export interface FSMkdirParams {
  path: string;
  recursive?: boolean;
}

export interface FSRmParams {
  path: string;
  recursive?: boolean;
}

export interface FSStatParams {
  path: string;
}

export interface FSReadFileResult {
  content: string | Buffer;
  encoding: string;
}

export interface FSWriteFileResult {
  success: boolean;
}

export interface FSExistsResult {
  exists: boolean;
}

export interface FSReaddirResult {
  entries: string[];
}

export interface FSMkdirResult {
  success: boolean;
}

export interface FSRmResult {
  success: boolean;
}

export interface FSStatResult {
  stats: FileStats;
}

export interface ExecParams {
  command: string;
  args?: string[];
  options?: ExecOptions;
}

export interface ExecResultData {
  stdout: string;
  stderr: string;
  exit_code: number;
}

export type RequestHandler = (params?: Record<string, unknown>) => Promise<unknown>;
