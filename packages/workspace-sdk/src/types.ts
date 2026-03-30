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

export type WorkspaceState = 'setup' | 'start' | 'ready' | 'active' | 'teardown';

export type GitCredentialMode = 'host-helper' | 'ephemeral-helper' | 'none';

export type AuthProfile = 'gitconfig';

export interface WorkspacePolicy {
  authProfiles?: AuthProfile[];
  sshAgentForward?: boolean;
  gitCredentialMode?: GitCredentialMode;
}

export interface WorkspaceCreateSpec {
  repo: string;
  ref?: string;
  workspaceName: string;
  agentProfile: string;
  policy?: WorkspacePolicy;
}

export interface WorkspaceRecord {
  id: string;
  repo: string;
  ref: string;
  workspaceName: string;
  agentProfile: string;
  policy?: WorkspacePolicy;
  state: WorkspaceState;
  rootPath: string;
  createdAt: string;
  updatedAt: string;
}

export interface WorkspaceCreateResult {
  workspace: WorkspaceRecord;
}

export interface WorkspaceOpenResult {
  workspace: WorkspaceRecord;
}

export interface WorkspaceListResult {
  workspaces: WorkspaceRecord[];
}

export interface WorkspaceRemoveResult {
  removed: boolean;
}

export interface SpotlightExposeOptions {
  service: string;
  remotePort: number;
  localPort: number;
  host?: string;
}

export interface SpotlightForward {
  id: string;
  workspaceId: string;
  service: string;
  remotePort: number;
  localPort: number;
  host: string;
  createdAt: string;
}

export interface SpotlightListResult {
  forwards: SpotlightForward[];
}

export interface SpotlightApplyDefaultsResult {
  forwards: SpotlightForward[];
}

export interface SpotlightApplyComposePortsError {
  service: string;
  hostPort: number;
  targetPort: number;
  message: string;
}

export interface SpotlightApplyComposePortsResult {
  forwards: SpotlightForward[];
  errors: SpotlightApplyComposePortsError[];
}

export interface WorkspaceInfo {
  workspace_id: string;
  workspace_path: string;
  workspaces?: WorkspaceRecord[];
  spotlight?: SpotlightForward[];
}

export interface WorkspaceReadyCheck {
  name: string;
  command: string;
  args?: string[];
}

export interface WorkspaceReadyResult {
  ready: boolean;
  workspaceId: string;
  profile?: string;
  elapsedMs: number;
  attempts: number;
  lastResults: Record<string, number>;
}
