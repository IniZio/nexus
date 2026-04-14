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
  projectId?: string;
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

export interface WorkspaceStopResult {
  stopped: boolean;
}

export interface WorkspaceStartResult {
  workspace: WorkspaceRecord;
}

export interface WorkspaceRestoreResult {
  restored: boolean;
  workspace: WorkspaceRecord;
}

export interface WorkspaceForkResult {
  forked: boolean;
  workspace: WorkspaceRecord;
}

export interface Capability {
  name: string;
  available: boolean;
}

export interface NodeInfo {
  node: Record<string, unknown>;
  capabilities: Capability[];
  compatibility: Record<string, unknown>;
}

export interface WorkspaceRelationNode {
  workspaceId: string;
  parentWorkspaceId?: string;
  lineageRootId?: string;
  derivedFromRef?: string;
  worktreeRef?: string;
  state: string;
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
