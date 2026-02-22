export type WorkspaceStatus =
  | 'pending'
  | 'stopped'
  | 'running'
  | 'paused'
  | 'error'
  | 'destroying'
  | 'destroyed';

export type BackendType = 'docker' | 'sprite' | 'kubernetes' | 'mock';

export type ISO8601Timestamp = string;

export interface Workspace {
  id: string;
  name: string;
  displayName?: string;
  status: WorkspaceStatus;
  statusMessage?: string;
  backend: BackendType;
  backendConfig: BackendConfig;
  repository: Repository;
  branch: string;
  worktreePath: string;
  resources: ResourceAllocation;
  ports: PortMapping[];
  createdAt: ISO8601Timestamp;
  updatedAt: ISO8601Timestamp;
  lastActiveAt: ISO8601Timestamp;
  expiresAt?: ISO8601Timestamp;
  config: WorkspaceConfig;
  labels: Record<string, string>;
  annotations: Record<string, string>;
}

export interface BackendConfig {
  containerId?: string;
  networkId?: string;
  imageDigest?: string;
  spriteInstanceId?: string;
  metadata: Record<string, unknown>;
}

export interface Repository {
  url: string;
  provider: 'github' | 'gitlab' | 'bitbucket' | 'other';
  localPath: string;
  auth?: RepositoryAuth;
  defaultBranch: string;
  currentCommit: string;
}

export interface RepositoryAuth {
  type: 'ssh' | 'https' | 'token';
  keychainRef: string;
}

export interface ResourceAllocation {
  cpu: {
    cores: number;
    limit?: number;
  };
  memory: {
    bytes: number;
    limit?: number;
    swap?: number;
  };
  storage: {
    bytes: number;
    ephemeral?: number;
  };
  gpu?: {
    count: number;
    type: 'nvidia' | 'amd';
    memory: number;
  };
}

export interface PortMapping {
  name: string;
  protocol: 'tcp' | 'udp';
  containerPort: number;
  hostPort: number;
  visibility: 'private' | 'public' | 'org';
  url?: string;
}

export interface WorkspaceConfig {
  image: string;
  devcontainerPath?: string;
  env: Record<string, string>;
  envFiles: string[];
  volumes: VolumeConfig[];
  services: ServiceConfig[];
  hooks: WorkspaceHooks;
  ide: IDEConfig;
  idleTimeout: number;
  shutdownBehavior: 'stop' | 'pause' | 'destroy';
}

export interface VolumeConfig {
  type: 'bind' | 'volume' | 'tmpfs';
  source: string;
  target: string;
  readOnly?: boolean;
}

export interface ServiceConfig {
  name: string;
  image: string;
  ports: PortMapping[];
  env: Record<string, string>;
  volumes: VolumeConfig[];
  dependsOn: string[];
  healthCheck?: HealthCheckConfig;
}

export interface HealthCheckConfig {
  command: string[];
  interval: number;
  timeout: number;
  retries: number;
  startPeriod: number;
}

export interface WorkspaceHooks {
  preCreate?: string[];
  postCreate?: string[];
  preStart?: string[];
  postStart?: string[];
  preStop?: string[];
  postStop?: string[];
}

export interface IDEConfig {
  default: 'vscode' | 'vim' | 'none';
  extensions: string[];
  settings: Record<string, unknown>;
}

export type ResourceClass = 'small' | 'medium' | 'large' | 'xlarge';

const GB = 1024 * 1024 * 1024;

export const RESOURCE_CLASSES: Record<ResourceClass, { cpu: number; memory: number; storage: number }> = {
  small: { cpu: 1, memory: 2 * GB, storage: 20 * GB },
  medium: { cpu: 2, memory: 4 * GB, storage: 50 * GB },
  large: { cpu: 4, memory: 8 * GB, storage: 100 * GB },
  xlarge: { cpu: 8, memory: 16 * GB, storage: 200 * GB },
} as const;

export interface WorkspaceState {
  id: string;
  name: string;
  status: WorkspaceStatus;
  backend: BackendType;
  createdAt: ISO8601Timestamp;
  updatedAt: ISO8601Timestamp;
  branch: string;
  worktreePath: string;
  ports: Record<string, number>;
  containerId: string;
  image: string;
  envVars: Record<string, string>;
  volumes: VolumeConfig[];
  lastActive: ISO8601Timestamp;
  processState?: ProcessState;
  config: WorkspaceConfig;
  labels: Record<string, string>;
  annotations: Record<string, string>;
}

export interface ProcessState {
  pid: number;
  command: string;
  running: boolean;
}

export interface PortAllocation {
  workspaceId: string;
  service: string;
  hostPort: number;
  containerPort: number;
  protocol: 'tcp' | 'udp';
  state: 'available' | 'allocated' | 'bound';
}

export const WORKSPACE_STATE_TRANSITIONS: Record<WorkspaceStatus, WorkspaceStatus[]> = {
  pending: ['stopped', 'error', 'destroying'],
  stopped: ['running', 'destroying', 'paused'],
  running: ['stopped', 'paused', 'error', 'destroying'],
  paused: ['running', 'destroying'],
  error: ['pending', 'stopped', 'destroying'],
  destroying: ['destroyed', 'error'],
  destroyed: [],
};

export function isValidTransition(from: WorkspaceStatus, to: WorkspaceStatus): boolean {
  return WORKSPACE_STATE_TRANSITIONS[from].includes(to);
}

export const WORKSPACE_NAME_REGEX = /^[a-z0-9][a-z0-9-]*[a-z0-9]$/;
export const RESERVED_NAMES = ['current', 'default', 'main', 'master', 'all'] as const;

export function isValidWorkspaceName(name: string): boolean {
  if (name.length < 2 || name.length > 64) return false;
  if (!WORKSPACE_NAME_REGEX.test(name)) return false;
  if ((RESERVED_NAMES as readonly string[]).includes(name)) return false;
  return true;
}

export const PORT_RANGES = {
  reserved: { start: 32768, end: 32799 },
  docker: { start: 32800, end: 34999, portsPerWorkspace: 10 },
  sprite: { start: 35000, end: 39999 },
  dynamic: { start: 40000, end: 65535 },
} as const;
