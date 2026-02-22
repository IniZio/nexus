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
    // Creates: .worktree/<name>/
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

### 2.2.6 File Sync Manager (Mutagen)

```go
// internal/sync/manager.go
type SyncManager struct {
    provider      SyncProvider       // Mutagen implementation
    sessions      map[string]*Session // workspace -> sync session
    config        *SyncConfig
}

// Core operations
func (m *SyncManager) CreateSession(workspaceID string, hostPath, containerPath string) (*Session, error)
func (m *SyncManager) PauseSession(workspaceID string) error
func (m *SyncManager) ResumeSession(workspaceID string) error
func (m *SyncManager) TerminateSession(workspaceID string) error
func (m *SyncManager) GetStatus(workspaceID string) (*SyncStatus, error)
```

**Mutagen Provider Implementation:**

```go
// internal/sync/mutagen.go
type MutagenProvider struct {
    daemonPath    string             // Path to mutagen daemon
    mode          SyncMode           // two-way-safe, two-way-resolved, one-way-replica
}

type Session struct {
    ID        string                 // Mutagen session identifier
    Alpha     string                 // Host worktree path
    Beta      string                 // Container path (via Docker volume)
    Config    MutagenConfig
    Status    SyncStatus
}

func (p *MutagenProvider) CreateSession(alpha, beta string, config SyncConfig) (*Session, error)
func (p *MutagenProvider) Pause(sessionID string) error
func (p *MutagenProvider) Resume(sessionID string) error
func (p *MutagenProvider) Terminate(sessionID string) error
func (p *MutagenProvider) Flush(sessionID string) error
func (p *MutagenProvider) Monitor(sessionID string) (*SyncStatus, error)
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
│   - Branch: nexus/feat  │     │  .worktree/  │
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
    
    // File Sync
    SyncSessionID string                 `json:"sync_session_id,omitempty"`
    SyncStatus    SyncState              `json:"sync_status,omitempty"`
    
    // Configuration
    Image         string                 `json:"image"`
    EnvVars       map[string]string      `json:"env_vars"`
    Volumes       []VolumeMount          `json:"volumes"`
    
    // Runtime
    LastActive    time.Time              `json:"last_active"`
    ProcessState  *ProcessState          `json:"process_state,omitempty"`
}

// SyncState represents file sync status
type SyncState struct {
    SessionID       string    `json:"session_id"`
    Provider        string    `json:"provider"`        // mutagen
    Status          string    `json:"status"`          // syncing | paused | error
    LastSyncAt      time.Time `json:"last_sync_at"`
    FilesTotal      int       `json:"files_total"`
    FilesSynced     int       `json:"files_synced"`
    Conflicts       int       `json:"conflicts"`
    Error           string    `json:"error,omitempty"`
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

## 2.8 File Sync Architecture

### 2.8.1 Overview

Nexus uses **Mutagen** for bidirectional file synchronization between host git worktrees and remote containers. This provides real-time sync with conflict resolution, enabling seamless development where edits on either side are automatically propagated.

**Why Mutagen:**
- **Real-time sync**: File system watching with sub-second propagation
- **Bidirectional**: Changes flow both ways (host ↔ container)
- **Conflict resolution**: Automatic conflict handling with configurable winners
- **Docker Desktop**: Powers Docker Desktop's file sync (battle-tested)
- **Cross-platform**: Works on macOS, Linux, Windows

### 2.8.2 Sync Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           File Sync Layer                                    │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────────┐         ┌─────────────────────┐                   │
│  │   Host Worktree     │  ←────  │   Mutagen Session   │                   │
│  │  (.worktree)        │   Sync  │   (two-way-safe)    │                   │
│  │                     │  ────→  │                     │                   │
│  │  • Source files     │         │  • Watch both sides │                   │
│  │  • Git repository   │         │  • Detect changes   │                   │
│  │  • User edits       │         │  • Resolve conflicts│                   │
│  └──────────┬──────────┘         └──────────┬──────────┘                   │
│             │                               │                               │
│             │      ┌─────────────────┐      │                               │
│             └──────┤   Mutagen Daemon├──────┘                               │
│                    │   (mutagen-io)  │                                      │
│                    └─────────────────┘                                      │
│                               │                                             │
│                               │ Unix socket / TCP                           │
│                               ▼                                             │
│  ┌─────────────────────┐         ┌─────────────────────┐                   │
│  │   Docker Volume     │  ←────  │  Container Agent    │                   │
│  │  (nexus-sync-<id>)  │   Sync  │  (mutagen-agent)    │                   │
│  │                     │  ────→  │                     │                   │
│  │  • Staging area     │         │  • Receives changes │                   │
│  │  • Persistent       │         │  • Applies to /work │                   │
│  └──────────┬──────────┘         └──────────┬──────────┘                   │
│             │                               │                               │
│             │                               │ Bind mount                    │
│             │                               ▼                               │
│             │                    ┌─────────────────────┐                    │
│             │                    │  Workspace          │                    │
│             │                    │  Container          │                    │
│             │                    │                     │                    │
│             │                    │  /workspace         │                    │
│             │                    │  (project files)    │                    │
│             │                    └─────────────────────┘                    │
│             │                                                               │
│             └───────────────────────────────────────────────────────────────┘
│                              Git Operations                                  │
│                              (host-side only)                                │
└─────────────────────────────────────────────────────────────────────────────┘
```

### 2.8.3 Sync Configuration

**Default Configuration:**

```yaml
# ~/.nexus/config.yaml
sync:
  provider: mutagen
  mode: two-way-safe
  
  # Paths to exclude from sync
  exclude:
    - node_modules
    - .git
    - build
    - dist
    - "*.log"
    - ".DS_Store"
    - ".nexus/"
  
  # Conflict resolution
  conflict:
    strategy: host-wins          # host-wins | container-wins | manual
    default-winner: host
  
  # Watch settings
  watch:
    mode: auto                   # auto | force-poll | no-watch
    pollingInterval: 500ms       # For force-poll mode
  
  # Performance tuning
  performance:
    maxEntryCount: 50000         # Max files to sync
    maxStagingSize: 10GB         # Max staging space
    scanMode: accelerated        # accelerated | full
  
  # Deployment mode
  deployment: hybrid             # embedded | external | hybrid
```

**Sync Modes:**

| Mode | Direction | Conflict Handling | Use Case |
|------|-----------|-------------------|----------|
| `two-way-safe` | Bidirectional | Safe (divergent files paused) | Default, safest |
| `two-way-resolved` | Bidirectional | Host wins | Known-good workflows |
| `one-way-replica` | Host → Container | N/A (read-only container) | Build containers |

### 2.8.4 Lifecycle Integration

**Workspace Creation Flow:**

```
1. Create git worktree on host
        ↓
2. Create Docker volume (nexus-sync-<id>)
        ↓
3. Start Mutagen session (host ↔ volume)
        ↓
4. Wait for initial sync (blocks until complete)
        ↓
5. Create container with volume bind mount
        ↓
6. Start container
```

**Workspace State Transitions:**

```
┌─────────────┐     pause      ┌─────────────┐
│   RUNNING   │───────────────▶│   PAUSED    │
│  (sync on)  │◀───────────────│  (sync off) │
└──────┬──────┘    resume      └─────────────┘
       │
       │ delete
       ▼
┌─────────────┐
│ TERMINATED  │
│ (sync ended)│
└─────────────┘
```

**Lifecycle Actions:**

| Workspace Event | Sync Action | Rationale |
|-----------------|-------------|-----------|
| `create` | Create + initial sync | Establish sync before container starts |
| `start` | Resume sync | Begin propagating changes |
| `stop` | Pause sync | Reduce resource usage while stopped |
| `switch-from` | Pause current | Stop syncing from old workspace |
| `switch-to` | Resume target | Begin syncing to new workspace |
| `destroy` | Terminate + cleanup | Remove sync session and volume |

### 2.8.5 Conflict Resolution

**Conflict Scenarios:**

1. **Simultaneous edit** (host and container): Host wins by default
2. **File deleted on one side, modified on other**: Container wins (preserves work)
3. **Directory structure conflict**: Manual resolution required

**Conflict Detection:**

```go
type Conflict struct {
    Path        string
    AlphaState  FileState      // Host state
    BetaState   FileState      // Container state
    Type        ConflictType   // edit-edit | delete-edit | permission
    DetectedAt  time.Time
}

func (m *SyncManager) HandleConflicts(conflicts []Conflict) error {
    for _, c := range conflicts {
        switch m.config.Conflict.Strategy {
        case "host-wins":
            return m.ResolveWithHostWins(c)
        case "container-wins":
            return m.ResolveWithContainerWins(c)
        case "manual":
            return m.QueueForManualResolution(c)
        }
    }
}
```

### 2.8.6 Embedded Mutagen Daemon

Nexus embeds the Mutagen daemon directly within the workspace daemon process, eliminating the need for users to install Mutagen CLI separately.

#### Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                        Embedded Mutagen Architecture                         │
├─────────────────────────────────────────────────────────────────────────────┤
│                                                                             │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                      Nexus Daemon (Go)                               │   │
│  │                                                                      │   │
│  │  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐   │   │
│  │  │   Workspace      │  │  Embedded        │  │   gRPC API       │   │   │
│  │  │   Manager        │──│  Mutagen Daemon  │──│   Clients        │   │   │
│  │  │                  │  │  (in-process)    │  │                  │   │   │
│  │  └──────────────────┘  └────────┬─────────┘  └──────────────────┘   │   │
│  │                                 │                                    │   │
│  └─────────────────────────────────┼────────────────────────────────────┘   │
│                                    │                                        │
│                                    │ Unix Socket                            │
│                                    ▼                                        │
│  ┌─────────────────────────────────────────────────────────────────────┐   │
│  │                   Mutagen gRPC Services                              │   │
│  │                                                                      │   │
│  │  ┌──────────────────┐  ┌──────────────────┐  ┌──────────────────┐   │   │
│  │  │  Synchronization │  │   Forwarding     │  │   Daemon         │   │   │
│  │  │   Service        │  │   Service        │  │   Service        │   │   │
│  │  └──────────────────┘  └──────────────────┘  └──────────────────┘   │   │
│  └─────────────────────────────────┬────────────────────────────────────┘   │
│                                    │                                        │
│                                    │ sync sessions                          │
│                    ┌───────────────┼───────────────┐                        │
│                    │               │               │                        │
│                    ▼               ▼               ▼                        │
│  ┌─────────────────────┐   ┌─────────────────────┐   ┌─────────────────┐   │
│  │  Host Worktree      │   │  Docker Volume      │   │  mutagen-agent  │   │
│  │  (.worktrees/<name>)│──▶│  (nexus-sync-<id>)  │──▶│  (in container) │   │
│  │                     │   │                     │   │                 │   │
│  │  • Source files     │   │  • Staging area     │   │  • Applies to   │   │
│  │  • Git repository   │   │  • Persistent       │   │    /workspace   │   │
│  └─────────────────────┘   └─────────────────────┘   └─────────────────┘   │
│                                                                             │
│  Data Directory: ~/.nexus/mutagen/                                          │
│  Socket Path:    ~/.nexus/mutagen/daemon/daemon.sock                        │
│                                                                             │
└─────────────────────────────────────────────────────────────────────────────┘
```

#### Key Design Decisions

| Aspect | Approach | Rationale |
|--------|----------|-----------|
| **Isolation** | Separate data directory (`~/.nexus/mutagen/`) | Avoids conflicts with standalone Mutagen installations |
| **Socket** | Unix domain socket in data directory | Fast, secure, no network exposure |
| **Lifecycle** | Managed by Nexus daemon | Auto-start on first sync, graceful shutdown on exit |
| **Agents** | Bundled `mutagen-agents.tar.gz` | Required for container-side sync endpoints |
| **Version** | Pinned Mutagen version | Ensures compatibility, tested combination |

#### Implementation

**1. Starting the Embedded Daemon**

```go
// internal/sync/mutagen/daemon.go
package mutagen

import (
    "context"
    "fmt"
    "os"
    "os/exec"
    "path/filepath"
    "runtime"
    "sync"
    "time"

    "google.golang.org/grpc"
    "github.com/mutagen-io/mutagen/pkg/daemon"
    "github.com/mutagen-io/mutagen/pkg/filesystem"
    "github.com/mutagen-io/mutagen/pkg/grpcutil"
    "github.com/mutagen-io/mutagen/pkg/ipc"
    daemonsvc "github.com/mutagen-io/mutagen/pkg/service/daemon"
)

// EmbeddedDaemon manages an embedded Mutagen daemon instance.
type EmbeddedDaemon struct {
    dataDir      string
    socketPath   string
    cmd          *exec.Cmd
    conn         *grpc.ClientConn
    mu           sync.RWMutex
    started      bool
    stopCh       chan struct{}
}

// NewEmbeddedDaemon creates a new embedded daemon configuration.
// The daemon is not started until Start() is called.
func NewEmbeddedDaemon(dataDir string) *EmbeddedDaemon {
    return &EmbeddedDaemon{
        dataDir:    dataDir,
        socketPath: filepath.Join(dataDir, "daemon", "daemon.sock"),
        stopCh:     make(chan struct{}),
    }
}

// Start launches the embedded Mutagen daemon.
// It sets up the custom data directory and starts the daemon process.
func (d *EmbeddedDaemon) Start(ctx context.Context) error {
    d.mu.Lock()
    defer d.mu.Unlock()

    if d.started {
        return nil
    }

    // Ensure data directory exists
    if err := os.MkdirAll(d.dataDir, 0700); err != nil {
        return fmt.Errorf("failed to create mutagen data directory: %w", err)
    }

    // Set MUTAGEN_DATA_DIRECTORY to isolate from system Mutagen
    env := os.Environ()
    env = append(env, fmt.Sprintf("MUTAGEN_DATA_DIRECTORY=%s", d.dataDir))

    // Find the mutagen binary (bundled or external)
    mutagenPath, err := d.findMutagenBinary()
    if err != nil {
        return fmt.Errorf("mutagen binary not found: %w", err)
    }

    // Start daemon: mutagen daemon run
    d.cmd = &exec.Cmd{
        Path:   mutagenPath,
        Args:   []string{"mutagen", "daemon", "run"},
        Env:    env,
        Stdout: os.Stdout, // Optional: redirect to structured logging
        Stderr: os.Stderr,
    }

    if err := d.cmd.Start(); err != nil {
        return fmt.Errorf("failed to start mutagen daemon: %w", err)
    }

    // Wait for daemon to be ready
    if err := d.waitForReady(ctx); err != nil {
        d.cmd.Process.Kill()
        return fmt.Errorf("daemon failed to become ready: %w", err)
    }

    // Connect to daemon
    conn, err := d.connect()
    if err != nil {
        d.cmd.Process.Kill()
        return fmt.Errorf("failed to connect to daemon: %w", err)
    }
    d.conn = conn

    d.started = true

    // Start monitoring goroutine
    go d.monitor()

    return nil
}

// findMutagenBinary locates the mutagen binary, preferring bundled version.
func (d *EmbeddedDaemon) findMutagenBinary() (string, error) {
    // 1. Check for bundled binary next to nexus daemon
    if exe, err := os.Executable(); err == nil {
        exeDir := filepath.Dir(exe)
        bundled := filepath.Join(exeDir, "mutagen")
        if runtime.GOOS == "windows" {
            bundled += ".exe"
        }
        if _, err := os.Stat(bundled); err == nil {
            return bundled, nil
        }
        
        // Check libexec directory (FHS layout)
        libexec := filepath.Join(exeDir, "..", "libexec", "mutagen")
        if runtime.GOOS == "windows" {
            libexec += ".exe"
        }
        if _, err := os.Stat(libexec); err == nil {
            return libexec, nil
        }
    }

    // 2. Fallback to PATH
    return exec.LookPath("mutagen")
}

// waitForReady waits for the daemon socket to become available.
func (d *EmbeddedDaemon) waitForReady(ctx context.Context) error {
    ticker := time.NewTicker(100 * time.Millisecond)
    defer ticker.Stop()

    timeout := time.AfterFunc(10*time.Second, func() {})
    defer timeout.Stop()

    for {
        select {
        case <-ctx.Done():
            return ctx.Err()
        case <-timeout.C:
            return fmt.Errorf("timeout waiting for daemon")
        case <-ticker.C:
            if _, err := os.Stat(d.socketPath); err == nil {
                return nil
            }
        }
    }
}

// connect establishes a gRPC connection to the daemon.
func (d *EmbeddedDaemon) connect() (*grpc.ClientConn, error) {
    ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
    defer cancel()

    return grpc.DialContext(
        ctx,
        d.socketPath,
        grpc.WithInsecure(),
        grpc.WithContextDialer(ipc.DialContext),
        grpc.WithBlock(),
        grpc.WithDefaultCallOptions(
            grpc.MaxCallSendMsgSize(grpcutil.MaximumMessageSize),
            grpc.MaxCallRecvMsgSize(grpcutil.MaximumMessageSize),
        ),
    )
}

// Connection returns the gRPC connection to the daemon.
func (d *EmbeddedDaemon) Connection() *grpc.ClientConn {
    d.mu.RLock()
    defer d.mu.RUnlock()
    return d.conn
}

// IsRunning returns true if the daemon is running.
func (d *EmbeddedDaemon) IsRunning() bool {
    d.mu.RLock()
    defer d.mu.RUnlock()
    return d.started && d.conn != nil
}

// Stop gracefully shuts down the daemon.
func (d *EmbeddedDaemon) Stop(ctx context.Context) error {
    d.mu.Lock()
    defer d.mu.Unlock()

    if !d.started {
        return nil
    }

    // Signal stop
    close(d.stopCh)

    // Close gRPC connection
    if d.conn != nil {
        d.conn.Close()
        d.conn = nil
    }

    // Request daemon shutdown via API
    if d.cmd != nil && d.cmd.Process != nil {
        // Send terminate signal to daemon
        daemonClient := daemonsvc.NewDaemonClient(d.conn)
        shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
        defer cancel()
        
        _, _ = daemonClient.Terminate(shutdownCtx, &daemonsvc.TerminateRequest{})
        
        // Wait for process to exit
        done := make(chan error, 1)
        go func() {
            done <- d.cmd.Wait()
        }()

        select {
        case <-ctx.Done():
            d.cmd.Process.Kill()
            return ctx.Err()
        case <-done:
            // Process exited
        }
    }

    d.started = false
    return nil
}

// monitor watches the daemon process and restarts if necessary.
func (d *EmbeddedDaemon) monitor() {
    if d.cmd == nil {
        return
    }

    err := d.cmd.Wait()
    
    d.mu.Lock()
    defer d.mu.Unlock()

    if !d.started {
        // Intentionally stopped
        return
    }

    // Daemon exited unexpectedly - could trigger restart or alert
    // For now, just mark as stopped
    d.started = false
    d.conn = nil
    
    // TODO: Implement restart policy with backoff
    _ = err
}
```

**2. Creating Sync Sessions**

```go
// internal/sync/mutagen/session.go
package mutagen

import (
    "context"
    "fmt"

    "github.com/mutagen-io/mutagen/pkg/service/synchronization"
    "github.com/mutagen-io/mutagen/pkg/synchronization"
    "github.com/mutagen-io/mutagen/pkg/url"
)

// SessionManager handles Mutagen synchronization sessions.
type SessionManager struct {
    daemon      *EmbeddedDaemon
    syncClient  synchronization.SynchronizationClient
}

// NewSessionManager creates a session manager for the given daemon.
func NewSessionManager(daemon *EmbeddedDaemon) *SessionManager {
    return &SessionManager{
        daemon:     daemon,
        syncClient: synchronization.NewSynchronizationClient(daemon.Connection()),
    }
}

// CreateSession creates a new synchronization session between host and container.
func (sm *SessionManager) CreateSession(
    ctx context.Context,
    name string,
    hostPath string,
    containerID string,
    containerPath string,
) (*SessionInfo, error) {
    // Parse alpha URL (host worktree)
    alpha, err := url.Parse(hostPath, url.Kind_Synchronization, true)
    if err != nil {
        return nil, fmt.Errorf("invalid host path: %w", err)
    }

    // Parse beta URL (Docker container)
    // Format: docker://<container_id>/<path>
    betaURL := fmt.Sprintf("docker://%s%s", containerID, containerPath)
    beta, err := url.Parse(betaURL, url.Kind_Synchronization, false)
    if err != nil {
        return nil, fmt.Errorf("invalid container path: %w", err)
    }

    // Create session specification
    spec := &synchronization.CreationSpecification{
        Alpha: alpha,
        Beta:  beta,
        Configuration: &synchronization.Configuration{
            SynchronizationMode: synchronization.SynchronizationMode_TwoWaySafe,
            IgnoreVCS:           true,
            DefaultFileMode:     0644,
            DefaultDirectoryMode: 0755,
        },
        ConfigurationAlpha: &synchronization.Configuration{
            // Host-specific settings
            WatchMode: synchronization.WatchMode_WatchModePortable,
        },
        ConfigurationBeta: &synchronization.Configuration{
            // Container-specific settings
            WatchMode: synchronization.WatchMode_WatchModePortable,
        },
        Name: name,
        Labels: map[string]string{
            "nexus/workspace": name,
            "nexus/managed":   "true",
        },
    }

    // Create the session
    resp, err := sm.syncClient.Create(ctx, &synchronization.CreateRequest{
        Specification: spec,
    })
    if err != nil {
        return nil, fmt.Errorf("failed to create sync session: %w", err)
    }

    return &SessionInfo{
        ID:   resp.Session,
        Name: name,
    }, nil
}

// SessionInfo holds information about a sync session.
type SessionInfo struct {
    ID   string
    Name string
}

// PauseSession pauses a synchronization session.
func (sm *SessionManager) PauseSession(ctx context.Context, sessionID string) error {
    _, err := sm.syncClient.Pause(ctx, &synchronization.PauseRequest{
        Selection: &selection.Selection{
            Specifications: []string{sessionID},
        },
    })
    return err
}

// ResumeSession resumes a paused synchronization session.
func (sm *SessionManager) ResumeSession(ctx context.Context, sessionID string) error {
    _, err := sm.syncClient.Resume(ctx, &synchronization.ResumeRequest{
        Selection: &selection.Selection{
            Specifications: []string{sessionID},
        },
    })
    return err
}

// TerminateSession terminates a synchronization session.
func (sm *SessionManager) TerminateSession(ctx context.Context, sessionID string) error {
    _, err := sm.syncClient.Terminate(ctx, &synchronization.TerminateRequest{
        Selection: &selection.Selection{
            Specifications: []string{sessionID},
        },
        SkipWaitForDestinations: false,
    })
    return err
}

// GetSessionStatus retrieves the current status of a session.
func (sm *SessionManager) GetSessionStatus(ctx context.Context, sessionID string) (*SyncStatus, error) {
    resp, err := sm.syncClient.List(ctx, &synchronization.ListRequest{
        Selection: &selection.Selection{
            Specifications: []string{sessionID},
        },
    })
    if err != nil {
        return nil, err
    }

    if len(resp.SessionStates) == 0 {
        return nil, fmt.Errorf("session not found: %s", sessionID)
    }

    state := resp.SessionStates[0]
    return &SyncStatus{
        SessionID:       state.Session.Identifier,
        Name:            state.Session.Name,
        Status:          state.Status.String(),
        AlphaPath:       state.Session.Alpha.Path,
        BetaPath:        state.Session.Beta.Path,
        LastError:       state.LastError,
        Conflicts:       len(state.Conflicts),
    }, nil
}

// SyncStatus represents the status of a sync session.
type SyncStatus struct {
    SessionID  string
    Name       string
    Status     string
    AlphaPath  string
    BetaPath   string
    LastError  string
    Conflicts  int
}
```

**3. Workspace Integration**

```go
// internal/sync/manager.go
package sync

import (
    "context"
    "fmt"
    "os"
    "path/filepath"
)

// Manager coordinates file synchronization for all workspaces.
type Manager struct {
    dataDir        string
    daemon         *mutagen.EmbeddedDaemon
    sessionManager *mutagen.SessionManager
    sessions       map[string]*WorkspaceSync // workspace -> sync info
}

// Config holds sync manager configuration.
type Config struct {
    // DataDirectory is where Mutagen stores its data.
    // Default: ~/.nexus/mutagen/
    DataDirectory string
}

// NewManager creates a new sync manager with embedded Mutagen.
func NewManager(cfg *Config) (*Manager, error) {
    dataDir := cfg.DataDirectory
    if dataDir == "" {
        home, err := os.UserHomeDir()
        if err != nil {
            return nil, fmt.Errorf("failed to get home directory: %w", err)
        }
        dataDir = filepath.Join(home, ".nexus", "mutagen")
    }

    daemon := mutagen.NewEmbeddedDaemon(dataDir)

    return &Manager{
        dataDir:        dataDir,
        daemon:         daemon,
        sessionManager: mutagen.NewSessionManager(daemon),
        sessions:       make(map[string]*WorkspaceSync),
    }, nil
}

// Start initializes the sync manager and starts the embedded daemon.
func (m *Manager) Start(ctx context.Context) error {
    if err := m.daemon.Start(ctx); err != nil {
        return fmt.Errorf("failed to start mutagen daemon: %w", err)
    }
    return nil
}

// Stop gracefully shuts down the sync manager.
func (m *Manager) Stop(ctx context.Context) error {
    // Terminate all active sessions
    for workspaceID, sync := range m.sessions {
        if err := m.sessionManager.TerminateSession(ctx, sync.SessionID); err != nil {
            // Log but continue
            fmt.Printf("Failed to terminate session for %s: %v\n", workspaceID, err)
        }
    }

    // Stop the daemon
    return m.daemon.Stop(ctx)
}

// CreateWorkspaceSync establishes file sync for a new workspace.
func (m *Manager) CreateWorkspaceSync(
    ctx context.Context,
    workspaceID string,
    worktreePath string,
    containerID string,
) error {
    sessionName := fmt.Sprintf("nexus-%s", workspaceID)
    
    session, err := m.sessionManager.CreateSession(
        ctx,
        sessionName,
        worktreePath,
        containerID,
        "/workspace",
    )
    if err != nil {
        return fmt.Errorf("failed to create sync session: %w", err)
    }

    m.sessions[workspaceID] = &WorkspaceSync{
        WorkspaceID: workspaceID,
        SessionID:   session.ID,
        SessionName: session.Name,
    }

    return nil
}

// PauseWorkspaceSync pauses sync for a workspace (e.g., when stopping).
func (m *Manager) PauseWorkspaceSync(ctx context.Context, workspaceID string) error {
    sync, ok := m.sessions[workspaceID]
    if !ok {
        return fmt.Errorf("no sync session for workspace: %s", workspaceID)
    }

    return m.sessionManager.PauseSession(ctx, sync.SessionID)
}

// ResumeWorkspaceSync resumes sync for a workspace (e.g., when starting).
func (m *Manager) ResumeWorkspaceSync(ctx context.Context, workspaceID string) error {
    sync, ok := m.sessions[workspaceID]
    if !ok {
        return fmt.Errorf("no sync session for workspace: %s", workspaceID)
    }

    return m.sessionManager.ResumeSession(ctx, sync.SessionID)
}

// DestroyWorkspaceSync terminates sync for a workspace.
func (m *Manager) DestroyWorkspaceSync(ctx context.Context, workspaceID string) error {
    sync, ok := m.sessions[workspaceID]
    if !ok {
        return nil // Already destroyed or never existed
    }

    if err := m.sessionManager.TerminateSession(ctx, sync.SessionID); err != nil {
        return fmt.Errorf("failed to terminate session: %w", err)
    }

    delete(m.sessions, workspaceID)
    return nil
}

// WorkspaceSync tracks sync state for a workspace.
type WorkspaceSync struct {
    WorkspaceID string
    SessionID   string
    SessionName string
}
```

#### Bundling Mutagen Binary

The Mutagen CLI binary and agent bundle must be distributed with Nexus:

```
nexus/
├── bin/
│   ├── nexus-daemon       # Main daemon executable
│   ├── mutagen            # Mutagen CLI (bundled)
│   └── mutagen-agents.tar.gz  # Agent binaries for various platforms
└── lib/
    └── ...
```

**Build Process:**

```makefile
# Makefile
MUTAGEN_VERSION := 0.18.0

# Download Mutagen release
download-mutagen:
    mkdir -p build/mutagen
    curl -L -o build/mutagen/mutagen.tar.gz \
        https://github.com/mutagen-io/mutagen/releases/download/v$(MUTAGEN_VERSION)/mutagen_linux_amd64_v$(MUTAGEN_VERSION).tar.gz
    tar -xzf build/mutagen/mutagen.tar.gz -C build/mutagen/
    
    # Download agent bundle
    curl -L -o build/mutagen/mutagen-agents.tar.gz \
        https://github.com/mutagen-io/mutagen/releases/download/v$(MUTAGEN_VERSION)/mutagen-agents.tar.gz

# Bundle into nexus distribution
bundle: download-mutagen
    mkdir -p dist/bin dist/lib
    cp build/mutagen/mutagen dist/bin/
    cp build/mutagen/mutagen-agents.tar.gz dist/bin/
    cp build/nexus-daemon dist/bin/
    # Create platform-specific packages...
```

#### Configuration

```yaml
# ~/.nexus/config.yaml
sync:
  provider: mutagen
  
  # Mutagen-specific settings
  mutagen:
    # Data directory (default: ~/.nexus/mutagen/)
    data_directory: "~/.nexus/mutagen/"
    
    # Sync mode: two-way-safe, two-way-resolved, one-way-replica
    mode: two-way-safe
    
    # Conflict resolution strategy
    conflict:
      strategy: host-wins
    
    # Paths to exclude from sync
    exclude:
      - node_modules
      - .git
      - build
      - dist
      - "*.log"
      - ".DS_Store"
      - ".nexus/"
    
    # Watch settings
    watch:
      mode: auto
      polling_interval: 500ms
    
    # Performance limits
    performance:
      max_entry_count: 50000
      max_staging_size: 10GB
```

#### Error Handling

Common error scenarios and recovery:

| Error | Cause | Recovery |
|-------|-------|----------|
| `daemon.Start() fails` | Binary not found, permissions | Check bundled binary exists, check executable permissions |
| `waitForReady timeout` | Daemon crashed, socket issue | Check logs, retry with backoff, alert user |
| `CreateSession fails` | Container not running, path invalid | Verify container state, check path exists |
| `session paused` | Container stopped | Auto-resume when container starts |
| `conflicts detected` | Simultaneous edits | Use configured resolution strategy |

#### Comparison: Embedded vs External Mutagen

| Aspect | Embedded (Recommended) | External |
|--------|----------------------|----------|
| **User Setup** | Zero setup required | Must install Mutagen CLI |
| **Version Control** | Pinned, tested version | User-managed, may mismatch |
| **Binary Size** | +50MB (mutagen + agents) | No additional size |
| **Updates** | With Nexus releases | Independent updates |
| **Isolation** | Separate data directory | Shared with other Mutagen usage |
| **Reliability** | Full lifecycle control | Depends on external daemon state |

**Recommendation:** Use embedded Mutagen as the default for the best user experience. Consider adding a hybrid fallback to external Mutagen for advanced users who want independent updates.

### 2.8.7 Monitoring & Observability

**Sync Metrics:**

```go
type SyncMetrics struct {
    SessionID       string
    Status          string        // syncing | paused | error
    
    // Sync stats
    FilesTotal      int
    FilesSynced     int
    FilesConflicting int
    
    // Performance
    LastSyncLatency time.Duration
    BytesTransferred int64
    
    // Health
    LastError       error
    ErrorCount      int
}
```

**Health Checks:**

```bash
# Check sync status
boulder workspace sync-status <name>

# Force sync (flush pending changes)
boulder workspace sync-flush <name>

# Pause/resume sync
boulder workspace sync-pause <name>
boulder workspace sync-resume <name>
```

### 2.8.8 Failure Handling

**Common Failures:**

| Failure | Detection | Recovery |
|---------|-----------|----------|
| Mutagen daemon crash | Health check | Auto-restart, re-sync |
| Disk full | Sync error | Pause sync, alert user |
| Too many files | Entry count exceeded | Exclude patterns, warn |
| Network partition | Beta unreachable | Pause, retry with backoff |
| Conflict storm | High conflict rate | Pause, manual intervention |

### 2.8.9 Integration with Git Worktrees

**Key Principle:** Git operations happen on the host, not in the container.

```
Host:                                    Container:
┌─────────────────┐                      ┌─────────────────┐
│ Git repository  │                      │ Project files   │
│ (source of truth)│                     │ (synced copy)   │
└────────┬────────┘                      └────────┬────────┘
         │                                        │
         │ git checkout, commit, push            │ read-only git
         │ (native SSH keys)                     │ (via sync)
         ▼                                        ▼
┌─────────────────┐                      ┌─────────────────┐
│ Worktree dir    │ ═══════════════════▶ │ /workspace      │
│ (.worktree)     │   Mutagen sync     │                 │
└─────────────────┘                      └─────────────────┘
```

**Benefits:**
- SSH keys stay on host (security)
- Git UI/IDE integration works natively
- Container remains simple (no SSH agent)

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
