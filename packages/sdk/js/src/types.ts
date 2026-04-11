export interface WorkspaceClientConfig {
  endpoint: string;
  workspaceId?: string;
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
  authRelayToken?: string;
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
  method?: string;
  params?: unknown;
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

export interface ExecResultData {
  stdout: string;
  stderr: string;
  exit_code: number;
}

export type RequestHandler = (params?: Record<string, unknown>) => Promise<unknown>;

export type WorkspaceState = 'created' | 'running' | 'paused' | 'stopped' | 'restored' | 'removed';

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
  /** Preferred backend (e.g. "local", "lxc", "firecracker"). Daemon resolves best available if omitted. */
  backend?: string;
  /** Auth binding map (binding name → token value). */
  authBinding?: Record<string, string>;
  /** Base64-encoded gzipped tar of agent credential and config files from the user's home directory. Build with `credsbundle.Build()` on the client machine before creating the workspace. Required for remote daemon setups where the daemon cannot read the user's local filesystem. Max 4 MiB decoded. */
  configBundle?: string;
}

export interface WorkspaceRecord {
  id: string;
  repo: string;
  repoKind?: string;
  ref: string;
  workspaceName: string;
  agentProfile: string;
  backend: string;
  parentWorkspaceId?: string;
  authBinding?: Record<string, string>;
  policy?: WorkspacePolicy;
  state: WorkspaceState;
  rootPath: string;
  localWorktreePath?: string;
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

export interface Capability {
  name: string;
  available: boolean;
  metadata?: Record<string, unknown>;
}

export interface CapabilitiesListResult {
  capabilities: Capability[];
}

export interface WorkspaceStopResult {
  stopped: boolean;
}

export interface WorkspaceStartResult {
  started: boolean;
}

export interface PTYOpenParams {
  workspaceId: string;
  shell?: string;
  workdir?: string;
  cols?: number;
  rows?: number;
  authRelayToken?: string;
}

export interface PTYOpenResult {
  sessionId: string;
}

export interface PTYWriteResult {
  ok: boolean;
}

export interface PTYResizeResult {
  ok: boolean;
}

export interface PTYCloseResult {
  closed: boolean;
}

export interface PTYDataEvent {
  sessionId: string;
  data: string;
}

export interface PTYExitEvent {
  sessionId: string;
  exitCode: number;
}

export interface WorkspaceRestoreResult {
  restored: boolean;
  workspace: WorkspaceRecord;
}

export interface WorkspacePauseResult {
  paused: boolean;
}

export interface WorkspaceResumeResult {
  resumed: boolean;
}

export interface WorkspaceForkResult {
  forked: boolean;
  workspace: WorkspaceRecord;
}

export interface WorkspaceRelationNode {
  workspaceId: string;
  parentWorkspaceId?: string;
  lineageRootId?: string;
  derivedFromRef?: string;
  worktreeRef?: string;
  state: WorkspaceState;
  backend?: string;
  workspaceName: string;
  rootPath: string;
  localWorktreePath?: string;
  createdAt: string;
  updatedAt: string;
}

export interface WorkspaceRelationsGroup {
  repoId: string;
  repoKind?: string;
  repo: string;
  displayName: string;
  remoteUrl?: string;
  nodes: WorkspaceRelationNode[];
  lineageRoots: string[];
}

export interface WorkspaceRelationsListResult {
  relations: WorkspaceRelationsGroup[];
}

export interface AuthRelayMintParams {
  workspaceId: string;
  binding: string;
  ttlSeconds?: number;
}

export interface AuthRelayMintResult {
  token: string;
}

export interface AuthRelayRevokeResult {
  revoked: boolean;
}
