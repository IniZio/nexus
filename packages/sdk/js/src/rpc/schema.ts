import type { ExecParams, ExecResultData } from '../types/exec';
import type {
  NodeInfo,
  WorkspaceCreateResult,
  WorkspaceForkResult,
  WorkspaceInfo,
  WorkspaceListResult,
  WorkspacePauseResult,
  WorkspaceRecord,
  WorkspaceRelationsListResult,
  WorkspaceRemoveResult,
  WorkspaceRestoreResult,
  WorkspaceResumeResult,
  WorkspaceReadyResult,
  WorkspaceStartResult,
  WorkspaceStopResult,
} from '../types/workspace';
import type {
  SpotlightApplyComposePortsResult,
  SpotlightForward,
} from '../types/spotlight';
import type {
  ProjectListResult,
  ProjectGetResult,
  ProjectRemoveResult,
} from '../types/project';

type WorkspaceCreateRPCParams = {
  spec: import('../types/workspace').WorkspaceCreateSpec & { configBundle?: string };
};

type FsDirEntry = {
  name: string;
  path: string;
  is_dir: boolean;
  size: number;
  mode: string;
};

type GitCommandRPCResult = {
  stdout: string;
  stderr: string;
  exit_code: number;
  action: string;
};

export interface RPCSchema {
  'workspace.create': [WorkspaceCreateRPCParams, WorkspaceCreateResult];
  'workspace.list': [Record<string, never>, WorkspaceListResult];
  'workspace.info': [{ workspaceId?: string; id?: string }, WorkspaceInfo];
  'workspace.start': [{ id: string }, WorkspaceStartResult];
  'workspace.stop': [{ id: string }, WorkspaceStopResult];
  'workspace.remove': [{ id: string }, WorkspaceRemoveResult];
  'workspace.pause': [{ id: string }, WorkspacePauseResult];
  'workspace.resume': [{ id: string }, WorkspaceResumeResult];
  'workspace.restore': [{ id: string }, WorkspaceRestoreResult];
  'workspace.fork': [
    { id: string; childWorkspaceName?: string; childRef?: string },
    WorkspaceForkResult,
  ];
  'workspace.relations.list': [{ repoId?: string }, WorkspaceRelationsListResult];
  'project.list': [Record<string, never>, ProjectListResult];
  'project.get': [{ id: string }, ProjectGetResult];
  'project.remove': [{ id: string }, ProjectRemoveResult];
  'workspace.setLocalWorktree': [
    { id: string; localWorktreePath: string; mutagenSessionId?: string },
    { ok: boolean; workspace?: WorkspaceRecord },
  ];
  'workspace.ready': [
    {
      workspaceId?: string;
      profile?: string;
      checks?: Array<{
        name: string;
        type?: string;
        command?: string;
        args?: string[];
        serviceName?: string;
        expectRunning?: boolean;
      }>;
      timeoutMs?: number;
      intervalMs?: number;
    },
    WorkspaceReadyResult,
  ];
  'authrelay.mint': [
    { workspaceId: string; binding: string; ttlSeconds?: number },
    { token: string },
  ];
  'authrelay.revoke': [{ token: string }, { revoked: boolean }];
  'exec': [ExecParams, ExecResultData];
  'fs.readFile': [
    { workspaceId?: string; path: string; encoding?: string },
    { content: string; encoding: string; size: number },
  ];
  'fs.writeFile': [
    { workspaceId?: string; path: string; content: string; encoding?: string },
    { ok: boolean; path: string; size: number },
  ];
  'fs.exists': [{ workspaceId?: string; path: string }, { exists: boolean; path: string }];
  'fs.readdir': [{ workspaceId?: string; path?: string }, { entries: FsDirEntry[]; path: string }];
  'fs.mkdir': [
    { workspaceId?: string; path: string; recursive?: boolean },
    { ok: boolean; path: string; size?: number },
  ];
  'fs.rm': [
    { workspaceId?: string; path: string; recursive?: boolean },
    { ok: boolean; path: string; size?: number },
  ];
  'fs.stat': [
    { workspaceId?: string; path: string },
    {
      name: string;
      path: string;
      isDir: boolean;
      size: number;
      mode: string;
      modTime: string;
      stats: {
        isFile: boolean;
        isDirectory: boolean;
        size: number;
        mtime: string;
        ctime: string;
        mode: number;
      };
    },
  ];
  'node.info': [Record<string, never>, NodeInfo];
  'spotlight.expose': [
    {
      spec: {
        workspaceId: string;
        service: string;
        remotePort: number;
        localPort: number;
        host?: string;
      };
    },
    { forward: SpotlightForward },
  ];
  'spotlight.list': [{ workspaceId?: string }, { forwards: SpotlightForward[] }];
  'spotlight.close': [{ id: string }, { closed: boolean }];
  'spotlight.applyComposePorts': [{ workspaceId: string }, SpotlightApplyComposePortsResult];
  'git.command': [
    { workspaceId?: string; action: string; params?: Record<string, unknown> },
    GitCommandRPCResult,
  ];
  'service.command': [
    { workspaceId?: string; action: string; params?: Record<string, unknown> },
    Record<string, unknown>,
  ];
  'os.pickDirectory': [{ prompt?: string }, { path?: string; cancelled: boolean }];
}
