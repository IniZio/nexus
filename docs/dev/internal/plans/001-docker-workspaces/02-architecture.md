# 2. Architecture

## 2.1 System Overview

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Nexus Workspace System                            │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────────┐     ┌─────────────────────┐     ┌───────────────┐ │
│  │     CLI (boulder)   │     │    IDE Plugins      │     │    SDK        │ │
│  │  • boulder ws up    │     │  • OpenCode         │     │  • TypeScript │ │
│  │  • boulder ws down  │     │  • Claude Code      │     │  • Go         │ │
│  │  • boulder ws list  │     │  • Cursor           │     │  • Python     │ │
│  └──────────┬──────────┘     └──────────┬──────────┘     └───────┬───────┘ │
│             │                           │                        │         │
│             └───────────────────────────┼────────────────────────┘         │
│                                         │                                  │
│                                         ▼                                  │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                    Workspace Manager (Go)                          │   │
│  │  ┌──────────────┐  ┌──────────────┐  ┌──────────────────────────┐  │   │
│  │  │   Provider   │  │   Worktree   │  │   Port Allocator         │  │   │
│  │  │   Registry   │  │   Manager    │  │   (Dynamic)              │  │   │
│  │  └──────────────┘  └──────────────┘  └──────────────────────────┘  │   │
│  └────────────────────────────────────────┬──────────────────────────┘   │
│                                           │                                │
│                    ┌──────────────────────┼──────────────────────┐         │
│                    │                      │                      │         │
│                    ▼                      ▼                      ▼         │
│  ┌─────────────────────────┐  ┌─────────────────────┐  ┌───────────────┐   │
│  │    Docker Backend       │  │   Sprite Backend    │  │   Mock        │   │
│  │  ┌───────────────────┐  │  │  ┌───────────────┐  │  │  (Testing)    │   │
│  │  │  Docker Engine    │  │  │  │  Sprite API   │  │  │               │   │
│  │  │  • Containers     │  │  │  │  • Firecracker│  │  │               │   │
│  │  │  • Volumes        │  │  │  │  • Checkpoints│  │  │               │   │
│  │  │  • Networks       │  │  │  │  • Billing    │  │  │               │   │
│  │  └───────────────────┘  │  │  └───────────────┘  │  │               │   │
│  └─────────────────────────┘  └─────────────────────┘  └───────────────┘   │
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                      Supporting Services                           │   │
│  │  ┌─────────────┐  ┌───────────────┐  ┌──────────────────────────┐  │   │
│  │  │   Daemon    │  │   Telemetry   │  │   Friction Collection    │  │   │
│  │  │  (WebSocket)│  │  (Agent Trace)│  │   (Usage Analytics)      │  │   │
│  │  └─────────────┘  └───────────────┘  └──────────────────────────┘  │   │
│  └─────────────────────────────────────────────────────────────────────┘   │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

## 2.2 Component Architecture

### 2.2.1 Configuration Hierarchy

Nexus uses a **3-level configuration hierarchy** with clear precedence:

```
┌─────────────────────────────────────────────────────────────────┐
│                    Configuration Precedence                      │
│                     (low to high priority)                       │
├─────────────────────────────────────────────────────────────────┤
│  1. Node/System    /etc/nexus/config.yaml                       │
│  2. User           ~/.nexus/config.yaml                         │
│  3. Project        ~/projects/myapp/.nexus/config.yaml          │
│  4. CLI Flags     --backend docker --port 3000                  │
│  5. Environment   NEXUS_BACKEND=docker                          │
└─────────────────────────────────────────────────────────────────┘
```

#### Level 1: Node/System Configuration

**Location:** `/etc/nexus/config.yaml` or `/opt/nexus/etc/config.yaml`

System-wide settings for multi-user nodes:

```yaml
# /etc/nexus/config.yaml
# System-level configuration - affects all users on this node

daemon:
  port: 8080                      # Daemon WebSocket port
  host: 0.0.0.0                   # Bind address (0.0.0.0 for multi-user)
  tls:
    cert: /etc/nexus/certs/server.crt
    key: /etc/nexus/certs/server.key

# Global resource limits
limits:
  max_workspaces_per_user: 10     # Prevent resource exhaustion
  max_workspaces_total: 100       # Node-wide limit
  default_resources:
    cpu: 2
    memory: 4G
    storage: 50G

# Backend configuration
backends:
  docker:
    socket: /var/run/docker.sock
    network: nexus-workspace
    storage_driver: overlay2
  
  sprite:
    api_endpoint: https://api.sprites.dev
    # API key from user config or env var

# Multi-user node settings
multi_user:
  enabled: true
  workspace_root: /var/lib/nexus/workspaces  # Per-user subdirs created
  # Or use user home directories:
  # workspace_root: "~/.local/share/nexus/workspaces"
  
# System-wide defaults (can be overridden by user/project)
defaults:
  backend: docker
  idle_timeout: 30m
  image: nexus-workspace:latest
```

#### Level 2: User Configuration

**Location:** `~/.nexus/config.yaml`

Personal preferences and user-specific settings:

```yaml
# ~/.nexus/config.yaml
# User-level configuration - personal preferences and defaults

# User preferences
preferences:
  default_editor: cursor          # cursor | vscode | vim
  theme: dark
  telemetry:
    enabled: true
    share_friction_data: true

# Personal defaults (override node defaults)
defaults:
  backend: sprite                 # Prefer Sprite over Docker
  idle_timeout: 1h                # Longer idle timeout
  resources: large                # Default to larger instances
  
  # Port range for this user's workspaces
  port_range:
    start: 34000
    end: 36000

# User-specific backends
backends:
  sprite:
    api_key: env:SPRITE_API_KEY   # Reference env var
    org: my-org
    region: us-east-1

# Global workspace list (references to project configs)
# Workspaces auto-discovered from these paths
workspaces:
  hanlun:
    path: ~/projects/hanlun-lms
    # Project config loaded from ~/projects/hanlun-lms/.nexus/config.yaml
  
  nexus:
    path: ~/code/nexus
    # Uses defaults since no project config exists

# User secrets (encrypted at rest)
secrets:
  # SSH configuration
  ssh:
    mode: agent
    keys:
      - ~/.ssh/id_ed25519
      - ~/.ssh/id_rsa
  
  # Environment files to load into all workspaces
  env_files:
    - ~/.env
    - ~/.nexus/secrets.env
  
  # Named secrets for workspace use
  named:
    NPM_TOKEN:
      source: keychain
      service: npm
    GITHUB_TOKEN:
      source: env
      var: GITHUB_TOKEN

# Personal overrides for specific projects
project_overrides:
  hanlun:
    defaults:
      idle_timeout: 2h            # Keep this project alive longer
```

#### Level 3: Project/Workspace Configuration

**Location:** `<project-root>/.nexus/config.yaml`

Workspace-specific settings for the project:

```yaml
# ~/projects/hanlun-lms/.nexus/config.yaml
# Project-level configuration - workspace-specific settings

# Workspace identity
workspace:
  name: hanlun-lms                # Defaults to directory name
  display_name: "Hanlun Learning Platform"
  description: "Learning management system for schools"

# Backend override for this project
backend:
  type: docker
  # Project-specific Docker settings
  docker:
    image: node:20-alpine
    dockerfile: ./.nexus/Dockerfile  # Custom image build

# Port mappings for this project
ports:
  web:
    container: 3000
    host: 3000                    # Fixed port (or auto-allocate if omitted)
    visibility: public            # public | private | org
  
  api:
    container: 5000
    # Host port auto-allocated from user's range
    visibility: private
  
  database:
    container: 5432
    host: 15432                   # Fixed host port
    visibility: private

# Service definitions - what runs when workspace wakes
services:
  postgres:
    image: postgres:16-alpine
    ports:
      - "5432:5432"
    env:
      POSTGRES_DB: hanlun_dev
    volumes:
      - postgres-data:/var/lib/postgresql/data
  
  redis:
    image: redis:7-alpine
    ports:
      - "6379:6379"

# Environment variables (merged with user secrets)
env:
  NODE_ENV: development
  DATABASE_URL: postgres://localhost:5432/hanlun_dev
  REDIS_URL: redis://localhost:6379

# Pre/post start scripts
scripts:
  pre-start: |
    #!/bin/bash
    echo "Setting up workspace..."
    npm install
    
  post-start: |
    #!/bin/bash
    echo "Workspace ready!"
    npm run dev &

# Lifecycle hooks
hooks:
  on_wake: npm run db:migrate
  on_sleep: echo "Workspace sleeping..."

# Project-specific resource needs
resources:
  cpu: 4
  memory: 8G
  storage: 100G

# Project-specific secrets (encrypted)
secrets:
  DATABASE_PASSWORD:
    source: file
    path: ./.nexus/secrets/db-password.txt
  STRIPE_SECRET_KEY:
    source: env
    var: HANLUN_STRIPE_KEY
```

#### Configuration Precedence Rules

When loading configuration, values are merged with the following precedence:

```
1. Node defaults       → Base system settings
2. User defaults       → Personal preferences override
3. Project config      → Workspace-specific overrides
4. CLI flags           → Command-line explicit values
5. Environment vars    → NEXUS_* variables (highest priority)
```

**Example merge for `idle_timeout`:**
- Node config: `30m`
- User config: `1h` (overrides node)
- Project config: `2h` (overrides user)
- CLI flag: `--idle-timeout 4h` (overrides project)
- Env var: `NEXUS_IDLE_TIMEOUT=5h` (highest priority)

**Final value:** `5h`

#### Multi-User Node Support

For shared development servers or CI runners:

```
/var/lib/nexus/
├── etc/
│   └── config.yaml              # Node configuration
├── workspaces/
│   ├── alice/                   # User alice's workspaces
│   │   ├── hanlun-lms/
│   │   └── nexus/
│   ├── bob/                     # User bob's workspaces
│   │   └── project-x/
│   └── shared/                  # Shared workspaces (optional)
├── state/
│   ├── daemon.pid
│   └── workspaces.db            # SQLite metadata
└── logs/
    └── daemon.log
```

User isolation:
- Unix permissions: `700` on user directories
- User IDs validated via system auth
- No cross-user workspace visibility (by default)

```yaml
# In /etc/nexus/config.yaml
multi_user:
  enabled: true
  auth:
    method: unix                  # unix | ldap | sso
    sudo_access: false            # Require sudo for admin operations
  isolation:
    workspaces: true              # Separate workspace dirs per user
    networks: true                # Separate Docker networks per user
    volumes: true                 # Prevent volume sharing between users
```

#### Configuration Validation

Each config level is validated independently:

1. **Node config:** Validated on daemon start
2. **User config:** Validated on CLI invocation
3. **Project config:** Validated on workspace operations

Validation rules:
- Port ranges must not overlap system ranges
- Resource limits cannot exceed node maximums
- Backend configurations must be valid for the type
- Secret references must resolve (or warn if missing)

```go
// Config loader merges all levels
type ConfigLoader struct {
    NodeConfig    *NodeConfig
    UserConfig    *UserConfig
    ProjectConfig *ProjectConfig
}

func (cl *ConfigLoader) Load(workspacePath string) (*MergedConfig, error) {
    // 1. Load node config
    // 2. Load user config (merge over node)
    // 3. Load project config (merge over user)
    // 4. Apply CLI flags
    // 5. Apply environment variables
    // 6. Validate final config
}
```

### 2.2.2 Workspace Manager

```go
// internal/workspace/manager.go
type Manager struct {
    config        *Config               // ~/.nexus/config.yaml
    provider      Provider              // Backend (Docker/Sprite)
    gitManager    *git.Manager          // Worktree operations
    portAllocator *ports.Allocator      // Dynamic port allocation
    stateStore    *state.Store          // Workspace metadata
    telemetry     *telemetry.Collector  // Agent Trace integration
}

// Core operations
func (m *Manager) Create(name string, opts CreateOptions) error
func (m *Manager) Start(name string) error
func (m *Manager) Stop(name string) error
func (m *Manager) Switch(name string) error        // Sub-2s context switch
func (m *Manager) Destroy(name string) error
func (m *Manager) List() ([]Workspace, error)
```

### 2.2.3 Provider Interface

```go
// pkg/workspace/provider.go
type Provider interface {
    // Lifecycle
    Create(ctx context.Context, spec WorkspaceSpec) (*Workspace, error)
    Start(ctx context.Context, id string) error
    Stop(ctx context.Context, id string) error
    Destroy(ctx context.Context, id string) error
    
    // State
    Get(ctx context.Context, id string) (*Workspace, error)
    List(ctx context.Context, filter ListFilter) ([]Workspace, error)
    
    // Health
    Health(ctx context.Context) error
    
    // Resources
    Stats(ctx context.Context, id string) (*ResourceStats, error)
    
    // Cleanup
    Close() error
}

// Backend implementations
type DockerProvider struct { /* ... */ }
type SpriteProvider struct { /* ... */ }
type MockProvider struct { /* ... */ }  // For testing
```

### 2.2.4 Worktree Manager

```go
// pkg/git/manager.go
type Manager struct {
    repoRoot string
}

func (m *Manager) CreateWorktree(name string) (string, error) {
    // Creates: .nexus/worktrees/<name>/
    // Branch: nexus/<name>
}

func (m *Manager) RemoveWorktree(name string) error
func (m *Manager) ListWorktrees() ([]Worktree, error)
func (m *Manager) SyncWorktree(name string) error  // git pull, etc.
```

### 2.2.5 Port Allocator

```go
// pkg/ports/allocator.go
type Allocator struct {
    basePort    int      // Starting port range
    allocations map[string]int  // workspace -> ssh port
}

func (a *Allocator) Allocate(workspace string, service string) (int, error) {
    // Algorithm:
    // 1. Hash workspace name for deterministic base
    // 2. Assign sequential ports for services
    // 3. Check availability, increment if conflict
}

// Port mapping example:
// Workspace: feature-auth (base: 32800)
//   SSH:      32768 (for exec access)
//   Web:      32801 (container:3000)
//   API:      32802 (container:5000)
//   Postgres: 32803 (container:5432)
```

## 2.3 Data Flow Diagrams

### 2.3.1 Workspace Creation Flow

```
User: boulder workspace create feature-auth
            │
            ▼
┌─────────────────────────┐
│   CLI: Parse arguments  │
│   - name: feature-auth  │
│   - flags: --backend    │
└───────────┬─────────────┘
            │
            ▼
┌──────────────────────────────────────────┐
│   Config Loader: Merge Hierarchy         │
│   1. Load /etc/nexus/config.yaml         │
│   2. Load ~/.nexus/config.yaml           │
│   3. Load ./.nexus/config.yaml (project) │
│   4. Apply CLI flags                     │
│   5. Apply NEXUS_* env vars              │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Manager: Validate     │
│   - Check name format   │
│   - Check not exists    │
│   - Validate resources  │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐     ┌─────────────────────┐
│   Git: Create Worktree  │────▶│  git worktree add   │
│   - Branch: nexus/feat  │     │  .nexus/worktrees/  │
└───────────┬─────────────┘     └─────────────────────┘
            │
            ▼
┌─────────────────────────┐
│   Provider: Create      │
│   - Allocate ports      │
│   - Create container    │
│   - Mount worktree      │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Initialize Workspace  │
│   - Load project env    │
│   - Run pre-start hook  │
│   - Start services      │
│   - Run post-start hook │
└───────────┬─────────────┘
            │
            ▼
         Success!
```

### 2.3.2 Workspace Switch Flow (Sub-2s Target)

```
User: boulder workspace switch feature-auth
            │
            ▼
┌─────────────────────────┐
│   Current: feature-ui   │
│   Target: feature-auth  │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Checkpoint Current    │
│   - Save running state  │
│   - Persist terminals   │
│   - Pause processes     │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Stop Current          │
│   - docker stop (fast)  │
│   - Keep volumes        │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Start Target          │
│   - docker start        │
│   - Restore state       │
│   - Resume processes    │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Update .nexus/current │
│   - Set active workspace│
└───────────┬─────────────┘
            │
            ▼
         Success! (<2s)
```

### 2.3.3 File Operation Flow (via Daemon)

```
IDE Plugin (OpenCode)
         │
         │ fs.readFile("/workspace/src/app.ts")
         ▼
┌─────────────────────────┐
│   SDK: TypeScript       │
│   - Build RPC request   │
│   - Send over WebSocket │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Daemon: Go WebSocket  │
│   - JWT auth            │
│   - Route to handler    │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Handler: FS Operation │
│   - Validate path       │
│   - Check permissions   │
│   - Read file           │
└───────────┬─────────────┘
            │
            ▼
┌─────────────────────────┐
│   Response: File Data   │
│   - Return content      │
│   - Record telemetry    │
└───────────┬─────────────┘
            │
            ▼
         IDE Plugin
```

## 2.4 State Management

### 2.4.1 Workspace States

```
                    ┌─────────────┐
         ┌─────────▶│   PENDING   │◀────────┐
         │          │  (creating) │         │
         │          └──────┬──────┘         │
         │                 │                │
         │                 ▼                │
         │          ┌─────────────┐         │
         │    ┌────│    STOPPED  │────┐    │
         │    │    │   (ready)   │    │    │
         │    │    └──────┬──────┘    │    │
         │    │           │           │    │
    destroy  start      switch      stop  create
         │    │           │           │    │
         │    │           ▼           │    │
         │    │    ┌─────────────┐    │    │
         │    └────│   RUNNING   │────┘    │
         │         │   (active)  │         │
         │         └──────┬──────┘         │
         │                │                │
         │                ▼                │
         │         ┌─────────────┐         │
         └─────────│    ERROR    │─────────┘
                   │  (failed)   │
                   └─────────────┘
```

### 2.4.2 State Persistence

```go
// State stored in: .nexus/state/workspaces/<name>.json
type WorkspaceState struct {
    ID            string                 `json:"id"`
    Name          string                 `json:"name"`
    Status        WorkspaceStatus        `json:"status"`
    Backend       BackendType            `json:"backend"`
    CreatedAt     time.Time              `json:"created_at"`
    UpdatedAt     time.Time              `json:"updated_at"`
    
    // Git
    Branch        string                 `json:"branch"`
    WorktreePath  string                 `json:"worktree_path"`
    
    // Resources
    Ports         map[string]int         `json:"ports"`  // service -> host port
    ContainerID   string                 `json:"container_id"`
    
    // Configuration
    Image         string                 `json:"image"`
    EnvVars       map[string]string      `json:"env_vars"`
    Volumes       []VolumeMount          `json:"volumes"`
    
    // Runtime
    LastActive    time.Time              `json:"last_active"`
    ProcessState  *ProcessState          `json:"process_state,omitempty"`
}
```

## 2.5 Network Architecture

### 2.5.1 Port Allocation Strategy

```
Port Range Allocation:

┌─────────────────────────────────────────────────────────────┐
│  32768 - 32799  │  Reserved (system)                         │
├─────────────────────────────────────────────────────────────┤
│  32800 - 34999  │  Docker backend workspaces                 │
│                 │  - Base: 32800                             │
│                 │  - Per-workspace: 10 ports                 │
│                 │  - Max workspaces: 220                     │
├─────────────────────────────────────────────────────────────┤
│  35000 - 39999  │  Sprite backend workspaces                 │
│                 │  - Remote port forwarding                  │
├─────────────────────────────────────────────────────────────┤
│  40000 - 65535  │  Dynamic allocation (fallback)             │
└─────────────────────────────────────────────────────────────┘

Per-Workspace Port Assignment:
  Offset 0: SSH access (if enabled)
  Offset 1: Web/dashboard
  Offset 2: API server
  Offset 3: Database
  Offset 4: Cache (Redis)
  Offset 5-9: Additional services
```

### 2.5.2 Container Networking

```
Docker Network Topology:

┌─────────────────────────────────────────────────────────────┐
│                    nexus-workspace-network                   │
│  (Bridge network, isolated per workspace)                   │
│                                                              │
│  ┌─────────────────┐      ┌─────────────────┐               │
│  │  Main Container │      │  DB Container   │               │
│  │  (app server)   │◀────▶│  (Postgres)     │               │
│  │  Port: 3000     │      │  Port: 5432     │               │
│  │  IP: 172.20.0.2 │      │  IP: 172.20.0.3 │               │
│  └────────┬────────┘      └─────────────────┘               │
│           │                                                  │
│           │ Port mapping: 32801:3000                         │
│           ▼                                                  │
│  ┌─────────────────────────────────────────────────────────┐ │
│  │                     Host Machine                         │ │
│  │  localhost:32801 ──────▶ container:3000                 │ │
│  └─────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

## 2.6 Data Models

### 2.6.1 Core Types

```typescript
// packages/core/src/workspace/types.ts

interface Workspace {
  // Identity
  id: string;                    // UUID v4
  name: string;                  // User-defined, URL-safe
  displayName?: string;          // Human-readable
  
  // Status
  status: WorkspaceStatus;       // pending | stopped | running | error
  statusMessage?: string;        // Human-readable status
  
  // Backend
  backend: BackendType;          // docker | sprite | mock
  backendConfig: BackendConfig;
  
  // Git
  repository: Repository;
  branch: string;                // nexus/<name>
  worktreePath: string;          // Absolute path
  
  // Resources
  resources: ResourceAllocation;
  ports: PortMapping[];
  
  // Lifecycle
  createdAt: ISO8601Timestamp;
  updatedAt: ISO8601Timestamp;
  lastActiveAt: ISO8601Timestamp;
  expiresAt?: ISO8601Timestamp;  // For temporary workspaces
  
  // Configuration
  config: WorkspaceConfig;
  
  // Metadata
  labels: Record<string, string>;
  annotations: Record<string, string>;
}

type WorkspaceStatus = 
  | 'pending'      // Creating/initializing
  | 'stopped'      // Created but not running
  | 'running'      // Active and accessible
  | 'paused'       // Suspended (checkpointed)
  | 'error'        // Failed state
  | 'destroying'   // Being deleted
  | 'destroyed';   // Deleted (soft delete)

type BackendType = 'docker' | 'sprite' | 'kubernetes' | 'mock';
```

### 2.6.2 Resource Allocation

```typescript
interface ResourceAllocation {
  // Compute
  cpu: {
    cores: number;               // 0.5, 1, 2, 4, 8
    limit?: number;              // Hard limit (cores)
  };
  memory: {
    bytes: number;               // In bytes (e.g., 8589934592 = 8GB)
    limit?: number;              // Hard limit
    swap?: number;               // Swap allocation
  };
  
  // Storage
  storage: {
    bytes: number;               // Primary storage
    ephemeral?: number;          // Temp/scratch space
  };
}

// Predefined resource classes
const RESOURCE_CLASSES = {
  'small': { cpu: 1, memory: 2 * GB, storage: 20 * GB },
  'medium': { cpu: 2, memory: 4 * GB, storage: 50 * GB },
  'large': { cpu: 4, memory: 8 * GB, storage: 100 * GB },
  'xlarge': { cpu: 8, memory: 16 * GB, storage: 200 * GB },
} as const;
```

### 2.6.3 Port Mapping

```typescript
interface PortMapping {
  name: string;                  // Service name (web, api, db)
  protocol: 'tcp' | 'udp';
  
  // Container side
  containerPort: number;
  
  // Host side
  hostPort: number;
  
  // Accessibility
  visibility: 'private' | 'public' | 'org';
  
  // URL (if publicly accessible)
  url?: string;
}
```

## 2.7 Reference Research

### Comparative Analysis

| Feature | Sprites | Codespaces | DevPod | Gitpod | Nexus Target |
|---------|---------|------------|--------|--------|--------------|
| **Cold Start** | <2s | 30-60s | 30s | 45s | **<30s** |
| **Warm Start** | <100ms | <5s | <5s | <5s | **<2s** |
| **Local Option** | No | No | Yes | No | **Yes (Docker)** |
| **Hybrid** | No | No | Limited | No | **Yes (Docker+Sprite)** |
| **Cost** | Pay-per-use | $0.18/hr | Free | $9/mo | **Free (local)** |
| **Offline** | No | No | Yes | No | **Yes (Docker)** |

### Key Insights from Research

**Sprites.dev (fly.io):**
- Firecracker MicroVMs for fast checkpoints
- Copy-on-write filesystem snapshots
- HTTP request triggers VM allocation

**GitHub Codespaces:**
- Dev Container standard (`.devcontainer/devcontainer.json`)
- Automatic HTTPS URLs for port forwarding
- Prebuilds via GitHub Actions

**DevPod (loft.sh):**
- Provider interface pattern
- IDE agnostic (VS Code, JetBrains, SSH)
- Same UX for local and remote
