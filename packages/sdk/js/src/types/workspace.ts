import type { SpotlightForward } from './spotlight';

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
  backend?: string;
  authBinding?: Record<string, string>;
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
