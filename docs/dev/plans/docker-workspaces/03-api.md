# 3. API Specification

## 3.1 REST API

### Base URL
```
/api/v1
```

### Workspaces

#### List Workspaces
```http
GET /api/v1/workspaces
```

Query Parameters:
- `status` - Filter by status (running, stopped, etc.)
- `backend` - Filter by backend type
- `label_selector` - Filter by labels

Response:
```json
{
  "workspaces": [
    {
      "id": "ws-123",
      "name": "hanlun",
      "status": "running",
      "backend": "docker",
      "ports": [
        {"name": "web", "hostPort": 32801, "containerPort": 3000}
      ]
    }
  ]
}
```

#### Create Workspace
```http
POST /api/v1/workspaces
Content-Type: application/json

{
  "name": "feature-auth",
  "backend": "docker",
  "resources": "medium",
  "ports": [3000, 5173],
  "env": {
    "NODE_ENV": "development"
  }
}
```

Response: `201 Created`

#### Get Workspace
```http
GET /api/v1/workspaces/{id}
```

#### Update Workspace
```http
PATCH /api/v1/workspaces/{id}
Content-Type: application/json

{
  "resources": "large",
  "idleTimeout": 60
}
```

#### Delete Workspace
```http
DELETE /api/v1/workspaces/{id}?force=true
```

Response: `204 No Content`

#### Start Workspace
```http
POST /api/v1/workspaces/{id}/start
```

Response: `202 Accepted`

#### Stop Workspace
```http
POST /api/v1/workspaces/{id}/stop
Content-Type: application/json

{
  "timeout": 30
}
```

#### Pause Workspace
```http
POST /api/v1/workspaces/{id}/pause
Content-Type: application/json

{
  "timeout": 60
}
```

Response: `202 Accepted`

Pauses the workspace, checkpointing process state for fast resume.

#### Resume Workspace
```http
POST /api/v1/workspaces/{id}/resume
Content-Type: application/json

{
  "blocking": true,
  "timeout": 120
}
```

Response: `200 OK`

```json
{
  "workspaceId": "ws-123",
  "status": "running",
  "durationMs": 1800,
  "services": [
    {"name": "web", "status": "healthy", "url": "http://localhost:32901"},
    {"name": "api", "status": "healthy", "url": "http://localhost:32902"}
  ]
}
```

#### Switch Workspace
```http
POST /api/v1/workspaces/{id}/switch
```

Response: `200 OK`

#### Execute Command (via SSH)
```http
POST /api/v1/workspaces/{id}/exec
Content-Type: application/json

{
  "command": "npm",
  "args": ["test"],
  "cwd": "/workspace",
  "env": {"CI": "true"}
}
```

**Implementation Note:** Commands are executed via SSH connection to the workspace container, not `docker exec`. This ensures SSH agent forwarding is available for git operations.

#### SSH Connection Info
```http
GET /api/v1/workspaces/{id}/ssh
```

Response:
```json
{
  "workspaceId": "ws-123",
  "enabled": true,
  "host": "localhost",
  "port": 32801,
  "user": "nexus",
  "forwardAgent": true,
  "hostKeyFingerprint": "SHA256:abc123...",
  "connectionCommand": "ssh -A nexus@localhost -p 32801",
  "configured": true
}
```

#### Generate SSH Config
```http
POST /api/v1/workspaces/{id}/ssh/config
Content-Type: application/json

{
  "includeHostKey": true
}
```

Response:
```text
Host nexus-feature-auth
  HostName localhost
  Port 32801
  User nexus
  ForwardAgent yes
  StrictHostKeyChecking accept-new
  UserKnownHostsFile ~/.nexus/known_hosts
  IdentityFile ~/.ssh/id_ed25519
```

#### Regenerate SSH Host Keys
```http
POST /api/v1/workspaces/{id}/ssh/rotate-keys
```

Response: `202 Accepted`

Rotates the SSH host keys for the workspace. Requires workspace restart to take effect.

### Checkpoints

#### Create Checkpoint
```http
POST /api/v1/workspaces/{id}/checkpoints
Content-Type: application/json

{
  "name": "before-refactor",
  "description": "Clean state before major changes"
}
```

#### List Checkpoints
```http
GET /api/v1/workspaces/{id}/checkpoints
```

#### Restore Checkpoint
```http
POST /api/v1/checkpoints/{checkpoint_id}/restore
```

### Sessions

#### List Sessions
```http
GET /api/v1/sessions
```

Response:
```json
{
  "sessions": [
    {
      "id": "sess-123",
      "workspace_id": "ws-123",
      "status": "active",
      "created_at": "2026-02-22T10:00:00Z"
    }
  ]
}
```

#### Attach to Session
```http
POST /api/v1/sessions/{id}/attach
```

#### Kill Session
```http
DELETE /api/v1/sessions/{id}
```

### Lifecycle Management

#### Get Workspace Status
```http
GET /api/v1/workspaces/{id}/status
```

Response:
```json
{
  "workspaceId": "ws-123",
  "name": "hanlun-dev",
  "status": "running",
  "backend": "docker",
  "containerId": "abc123",
  "ports": {
    "ssh": 32801,
    "services": [
      {"name": "web", "containerPort": 3000, "hostPort": 32901, "status": "healthy"},
      {"name": "api", "containerPort": 3001, "hostPort": 32902, "status": "healthy"}
    ]
  },
  "sync": {
    "status": "syncing",
    "lastSyncAt": "2026-02-22T10:30:00Z"
  },
  "createdAt": "2026-02-20T08:00:00Z",
  "lastActiveAt": "2026-02-22T10:30:00Z"
}
```

#### Run Hook
```http
POST /api/v1/workspaces/{id}/hooks/{hook}
Content-Type: application/json

{
  "timeout": 300
}
```

Hooks: `pre-start`, `post-start`, `pre-stop`, `post-stop`, `health-check`

### Services

#### List Services
```http
GET /api/v1/workspaces/{id}/services
```

Response:
```json
{
  "services": [
    {
      "name": "web",
      "containerPort": 3000,
      "hostPort": 32901,
      "status": "healthCheck": "/api/health",
      "url": "healthy",
      "http://localhost:32901"
    }
  ]
}
```

#### Get Service Logs
```http
GET /api/v1/workspaces/{id}/services/{service}/logs?follow=true&tail=100
```

#### Restart Service
```http
POST /api/v1/workspaces/{id}/services/{service}/restart
```

### Port Forwarding

#### Add Port Forward
```http
POST /api/v1/workspaces/{id}/ports
Content-Type: application/json

{
  "containerPort": 3000,
  "visibility": "public"
}
```

#### Make Port Public
```http
POST /api/v1/workspaces/{id}/ports/{port_id}/public
```

### File Sync

#### Get Sync Status
```http
GET /api/v1/workspaces/{id}/sync
```

Response:
```json
{
  "workspaceId": "ws-123",
  "provider": "mutagen",
  "status": "syncing",
  "sessionId": "mutagen-abc123",
  "stats": {
    "filesTotal": 15234,
    "filesSynced": 15234,
    "conflicts": 0,
    "bytesTransferred": 104857600
  },
  "lastSyncAt": "2026-02-22T10:30:00Z",
  "error": null
}
```

#### Pause Sync
```http
POST /api/v1/workspaces/{id}/sync/pause
```

Response: `200 OK`

#### Resume Sync
```http
POST /api/v1/workspaces/{id}/sync/resume
```

Response: `200 OK`

#### Force Sync (Flush)
```http
POST /api/v1/workspaces/{id}/sync/flush
```

Response: `200 OK`

#### List Conflicts
```http
GET /api/v1/workspaces/{id}/sync/conflicts
```

Response:
```json
{
  "conflicts": [
    {
      "path": "src/config.ts",
      "type": "edit-edit",
      "alphaState": "modified",
      "betaState": "modified",
      "detectedAt": "2026-02-22T10:25:00Z"
    }
  ]
}
```

#### Resolve Conflict
```http
POST /api/v1/workspaces/{id}/sync/conflicts/resolve
Content-Type: application/json

{
  "path": "src/config.ts",
  "winner": "host"
}
```

---

## 3.2 gRPC API (Internal)

```protobuf
// proto/nexus/workspace/v1/workspace.proto
syntax = "proto3";
package nexus.workspace.v1;

service WorkspaceService {
  // Workspace lifecycle
  rpc CreateWorkspace(CreateWorkspaceRequest) returns (Workspace);
  rpc GetWorkspace(GetWorkspaceRequest) returns (Workspace);
  rpc ListWorkspaces(ListWorkspacesRequest) returns (ListWorkspacesResponse);
  rpc UpdateWorkspace(UpdateWorkspaceRequest) returns (Workspace);
  rpc DeleteWorkspace(DeleteWorkspaceRequest) returns (DeleteWorkspaceResponse);
  
  // Lifecycle operations
  rpc StartWorkspace(StartWorkspaceRequest) returns (StartWorkspaceResponse);
  rpc StopWorkspace(StopWorkspaceRequest) returns (Operation);
  rpc SwitchWorkspace(SwitchWorkspaceRequest) returns (SwitchWorkspaceResponse);
  rpc PauseWorkspace(PauseWorkspaceRequest) returns (Operation);
  rpc ResumeWorkspace(ResumeWorkspaceRequest) returns (ResumeWorkspaceResponse);
  rpc RestartWorkspace(RestartWorkspaceRequest) returns (Operation);
  rpc GetWorkspaceStatus(GetWorkspaceStatusRequest) returns (WorkspaceStatus);
  rpc WaitForWorkspace(WaitForWorkspaceRequest) returns (WaitForWorkspaceResponse);
  
  // Services
  rpc ListServices(ListServicesRequest) returns (ListServicesResponse);
  rpc GetServiceLogs(GetServiceLogsRequest) returns (stream LogEntry);
  rpc RestartService(RestartServiceRequest) returns (Operation);
  
  // File operations (streaming)
  rpc StreamFile(StreamFileRequest) returns (stream FileChunk);
  rpc WriteFile(stream WriteFileRequest) returns (WriteFileResponse);
  
  // Execution
  rpc ExecStream(ExecRequest) returns (stream ExecOutput);
  
  // Checkpoints
  rpc CreateCheckpoint(CreateCheckpointRequest) returns (Checkpoint);
  rpc RestoreCheckpoint(RestoreCheckpointRequest) returns (Operation);
  
  // Sessions
  rpc ListSessions(ListSessionsRequest) returns (ListSessionsResponse);
  rpc AttachSession(AttachSessionRequest) returns (stream SessionOutput);
  rpc KillSession(KillSessionRequest) returns (Operation);
  
  // Monitoring
  rpc GetStats(GetStatsRequest) returns (ResourceStats);
  rpc StreamStats(StreamStatsRequest) returns (stream ResourceStats);
  
  // File Sync
  rpc GetSyncStatus(GetSyncStatusRequest) returns (SyncStatus);
  rpc PauseSync(PauseSyncRequest) returns (SyncStatus);
  rpc ResumeSync(ResumeSyncRequest) returns (SyncStatus);
  rpc FlushSync(FlushSyncRequest) returns (FlushSyncResponse);
  rpc ListConflicts(ListConflictsRequest) returns (ListConflictsResponse);
  rpc ResolveConflict(ResolveConflictRequest) returns (ResolveConflictResponse);
  
  // SSH Access
  rpc GetSSHInfo(GetSSHInfoRequest) returns (SSHInfo);
  rpc GenerateSSHConfig(GenerateSSHConfigRequest) returns (SSHConfigResponse);
  rpc RotateSSHKeys(RotateSSHKeysRequest) returns (Operation);
}

message GetSSHInfoRequest {
  string workspace_id = 1;
}

message SSHInfo {
  string workspace_id = 1;
  bool enabled = 2;
  string host = 3;
  int32 port = 4;
  string user = 5;
  bool forward_agent = 6;
  string host_key_fingerprint = 7;
  string connection_command = 8;
  bool configured = 9;
}

message GenerateSSHConfigRequest {
  string workspace_id = 1;
  bool include_host_key = 2;
}

message SSHConfigResponse {
  string config_content = 1;
  string host_entry_name = 2;
}

message RotateSSHKeysRequest {
  string workspace_id = 1;
}

message SyncStatus {
  string session_id = 1;
  string provider = 2;
  SyncState state = 3;
  SyncStats stats = 4;
  google.protobuf.Timestamp last_sync_at = 5;
  string error_message = 6;
}

enum SyncState {
  SYNC_STATE_UNSPECIFIED = 0;
  SYNC_STATE_SYNCING = 1;
  SYNC_STATE_PAUSED = 2;
  SYNC_STATE_ERROR = 3;
}

message SyncStats {
  int32 files_total = 1;
  int32 files_synced = 2;
  int32 conflicts = 3;
  int64 bytes_transferred = 4;
}

message Workspace {
  string id = 1;
  string name = 2;
  string display_name = 3;
  WorkspaceStatus status = 4;
  BackendType backend = 5;
  Repository repository = 6;
  string branch = 7;
  ResourceAllocation resources = 8;
  repeated PortMapping ports = 9;
  WorkspaceConfig config = 10;
  
  google.protobuf.Timestamp created_at = 20;
  google.protobuf.Timestamp updated_at = 21;
  google.protobuf.Timestamp last_active_at = 22;
}

enum WorkspaceStatus {
  WORKSPACE_STATUS_UNSPECIFIED = 0;
  WORKSPACE_STATUS_PENDING = 1;
  WORKSPACE_STATUS_STOPPED = 2;
  WORKSPACE_STATUS_RUNNING = 3;
  WORKSPACE_STATUS_PAUSED = 4;
  WORKSPACE_STATUS_ERROR = 5;
  WORKSPACE_STATUS_DESTROYING = 6;
  WORKSPACE_STATUS_DESTROYED = 7;
}

enum BackendType {
  BACKEND_TYPE_UNSPECIFIED = 0;
  BACKEND_TYPE_DOCKER = 1;
  BACKEND_TYPE_SPRITE = 2;
}

// Lifecycle Management Messages

message StartWorkspaceRequest {
  string workspace_id = 1;
  bool skip_hooks = 2;
  bool skip_services = 3;
  bool blocking = 4;
  int32 timeout_seconds = 5;
}

message StartWorkspaceResponse {
  string workspace_id = 1;
  WorkspaceStatus status = 2;
  repeated ServiceHealth services = 3;
  int32 duration_ms = 4;
}

message ServiceHealth {
  string service_name = 1;
  string status = 2;  // healthy | unhealthy | starting
  int32 port = 3;
  string url = 4;
}

message PauseWorkspaceRequest {
  string workspace_id = 1;
  int32 timeout_seconds = 2;
}

message ResumeWorkspaceRequest {
  string workspace_id = 1;
  bool blocking = 2;
  int32 timeout_seconds = 3;
}

message ResumeWorkspaceResponse {
  string workspace_id = 1;
  WorkspaceStatus status = 2;
  repeated ServiceHealth services = 3;
  int32 duration_ms = 4;
}

message RestartWorkspaceRequest {
  string workspace_id = 1;
  bool force = 2;
}

message GetWorkspaceStatusRequest {
  string workspace_id = 1;
}

message WaitForWorkspaceRequest {
  string workspace_id = 1;
  string condition = 2;  // running | healthy | stopped
  int32 timeout_seconds = 3;
}

message WaitForWorkspaceResponse {
  string workspace_id = 1;
  WorkspaceStatus status = 2;
  bool condition_met = 3;
  int32 duration_ms = 4;
}

// Checkpoint Messages

message Checkpoint {
  string id = 1;
  string name = 2;
  string description = 3;
  google.protobuf.Timestamp created_at = 4;
}

message CreateCheckpointRequest {
  string workspace_id = 1;
  string name = 2;
  string description = 3;
}

message RestoreCheckpointRequest {
  string checkpoint_id = 1;
  string workspace_id = 2;
}

// Session Messages

message Session {
  string id = 1;
  string workspace_id = 2;
  string status = 3;
  google.protobuf.Timestamp created_at = 4;
}

message ListSessionsRequest {
  string workspace_id = 1;
}

message ListSessionsResponse {
  repeated Session sessions = 1;
}

message AttachSessionRequest {
  string session_id = 1;
}

message SessionOutput {
  oneof content {
    string stdout = 1;
    string stderr = 2;
    int32 exit_code = 3;
  }
}

message KillSessionRequest {
  string session_id = 1;
}

// Service Management Messages

message ListServicesRequest {
  string workspace_id = 1;
}

message ListServicesResponse {
  repeated Service services = 1;
}

message Service {
  string name = 1;
  int32 container_port = 2;
  int32 host_port = 3;
  string status = 4;  // running | stopped | healthy | unhealthy
  string health_check_path = 5;
  string url = 6;
}

message GetServiceLogsRequest {
  string workspace_id = 1;
  string service_name = 2;
  bool follow = 3;
  int32 tail = 4;
  string since = 5;
}

message LogEntry {
  string service_name = 1;
  string timestamp = 2;
  string level = 3;  // info | warn | error | debug
  string message = 4;
}

message RestartServiceRequest {
  string workspace_id = 1;
  string service_name = 2;
}
```

---

## 3.3 WebSocket API (Real-time)

### Connection
```typescript
const ws = new WebSocket('ws://localhost:8080/v1/ws');

// Authentication (first message)
ws.send(JSON.stringify({
  type: 'auth',
  token: 'jwt-token-here'
}));
```

### Request/Response Pattern
```typescript
interface WSRequest {
  id: string;           // Client-generated request ID
  type: string;         // Method name
  payload: unknown;     // Method-specific payload
}

interface WSResponse {
  id: string;           // Matches request ID
  success: boolean;
  result?: unknown;     // Success response
  error?: WSError;      // Error details
}
```

### File Operations
```typescript
// Read file
interface FSReadFileRequest {
  type: 'fs.readFile';
  payload: {
    path: string;
    encoding?: 'utf8' | 'base64';
  };
}

// Write file
interface FSWriteFileRequest {
  type: 'fs.writeFile';
  payload: {
    path: string;
    content: string;
    encoding?: 'utf8' | 'base64';
  };
}

// List directory
interface FSReadDirRequest {
  type: 'fs.readdir';
  payload: {
    path: string;
    recursive?: boolean;
  };
}
```

### Execution
```typescript
interface ExecRequest {
  type: 'exec';
  payload: {
    command: string;
    args?: string[];
    cwd?: string;
    env?: Record<string, string>;
    timeout?: number;
  };
}

// Streaming response
interface ExecStreamMessage {
  type: 'exec.stdout' | 'exec.stderr' | 'exec.exit';
  payload: {
    data?: string;
    exitCode?: number;
  };
}
```

### Events (Server → Client)
```typescript
interface WorkspaceEvent {
  type: 'workspace.status' | 'workspace.stats' | 'port.forward';
  payload: {
    workspaceId: string;
    // Event-specific data
  };
}
```

---

## 3.4 CLI Interface

The Nexus CLI is designed to match Sprite's interface for a familiar user experience.

### Workspace Management

```bash
# Create workspace
nexus create [name] [flags]
  -d, --display-name string   Display name
  -r, --repo string          Repository URL
  -b, --branch string        Branch name
  --from-branch string       Base branch for worktree
  --backend string           Backend (docker, sprite) [default: docker]
  --no-worktree              Skip git worktree creation

# List all workspaces
nexus list (or nexus ls)

# Show workspace status
nexus status <workspace>

# Start a stopped workspace
nexus start <workspace>

# Stop a running workspace
nexus stop <workspace> [flags]
  --timeout int   Timeout in seconds [default: 30]

# Delete a workspace
nexus destroy <workspace> [flags]
  -f, --force   Force delete without confirmation

# Get workspace URL
nexus url <workspace>

# Set active workspace
nexus use <workspace>
```

### Lifecycle Management

```bash
# Pause workspace (checkpoint state)
nexus pause <workspace>

# Resume paused workspace
nexus resume <workspace>
```

### Interactive Shell

```bash
# Interactive SSH shell
nexus console <workspace>
```

### Execute Commands

```bash
# Execute command in workspace
nexus exec <workspace> -- <command> [args...]

# Example
nexus exec myworkspace -- npm test
```

### Sessions

```bash
# List active sessions
nexus sessions list

# Attach to a session
nexus sessions attach <id>

# Kill a session
nexus sessions kill <id>
```

### Checkpoints

```bash
# Create checkpoint
nexus checkpoint create <workspace> [flags]
  -n, --name string   Checkpoint name

# List checkpoints
nexus checkpoint list <workspace>

# Restore from checkpoint
nexus restore <workspace> <checkpoint-id>
```

### Services

```bash
# List services in workspace
nexus services <workspace>

# Get service logs
nexus services logs <workspace> <service> [flags]
  --tail int   Number of lines [default: 100]
```

### Port Forwarding

```bash
# Port forwarding
nexus proxy <workspace> <port>
```

### File Sync

```bash
# Show sync status
nexus sync status <workspace>

# Pause sync
nexus sync pause <workspace>

# Resume sync
nexus sync resume <workspace>

# Force sync
nexus sync flush <workspace>
```

### Configuration & Diagnostics

```bash
# Show configuration
nexus config

# Check setup
nexus doctor

# Print version
nexus version
```

### Daemon

```bash
# Start daemon
nexus daemon [flags]
  -p, --port int          Port to listen on [default: 8080]
  -w, --workspace-dir     Workspace directory path
```

### Global Flags

```bash
-u, --url string       API server URL [default: http://localhost:8080]
-t, --token string    Authentication token
--daemon-token string Daemon token for serve command
```

---

## 3.5 Configuration Schema

### Full Config Example

```yaml
# ~/.nexus/config.yaml

# Daemon settings
daemon:
  port: 8080
  host: localhost
  
# Default settings for new workspaces
defaults:
  backend: docker
  idle_timeout: 30m
  resources: medium
  image: nexus-workspace:latest
  
# Workspace definitions (optional - auto-discovery works too)
workspaces:
  hanlun:
    path: /Users/newman/code/hanlun
    backend: docker
    ports: [3000, 5173]
    env:
      NODE_ENV: development
      API_URL: http://localhost:8080
    resources:
      cpu: 2
      memory: 4GB
      
  docs-site:
    path: /Users/newman/code/docs
    backend: docker
    image: node:18-alpine
    ports: [3000]
    
# Secret handling
secrets:
  ssh:
    mode: agent                   # agent | mount | auto
    
  env_files:
    - ~/.env
    
  named:
    NPM_TOKEN:
      source: keychain
      service: npm
      account: auth-token
      
    DATABASE_PASSWORD:
      source: file
      path: ~/.secrets/db-password.txt

# File Sync Configuration
sync:
  provider: mutagen
  mode: two-way-safe
  exclude:
    - node_modules
    - .git
    - build
    - dist
    - "*.log"
    - ".DS_Store"
  conflict:
    strategy: host-wins
  watch:
    mode: auto
    pollingInterval: 500ms
  performance:
    maxEntryCount: 50000
    maxStagingSize: 10GB
  deployment: hybrid

# Telemetry
telemetry:
  enabled: true
  endpoint: https://telemetry.nexus.dev

# SSH Configuration
ssh:
  # Key injection
  injection:
    enabled: true
    sources:
      - ~/.ssh/id_ed25519.pub
      - ~/.ssh/id_rsa.pub
    include_agent_keys: true

  # Port allocation
  port_range:
    start: 32800
    end: 34999

  # Connection settings
  connection:
    user: nexus
    forward_agent: true
    server_alive_interval: 30
    strict_host_key_checking: accept-new
    user_known_hosts_file: ~/.nexus/known_hosts

  # SSH client options
  client_options:
    - "IdentitiesOnly=yes"
    - "AddKeysToAgent=yes"

# IDE integration
ide:
  default: vscode
  extensions:
    - dbaeumer.vscode-eslint
    - bradlc.vscode-tailwindcss
```

---

## 3.6 Error Handling

### Error Taxonomy

```typescript
// Error codes
const ERROR_CODES = {
  // Workspace errors
  WORKSPACE_NOT_FOUND: { status: 404, retryable: false },
  WORKSPACE_ALREADY_EXISTS: { status: 409, retryable: false },
  WORKSPACE_START_FAILED: { status: 500, retryable: true },
  
  // Resource errors
  RESOURCE_EXHAUSTED: { status: 503, retryable: true },
  PORT_CONFLICT: { status: 409, retryable: true },
  
  // Permission errors
  PERMISSION_DENIED: { status: 403, retryable: false },
  AUTHENTICATION_FAILED: { status: 401, retryable: false },
  
  // Backend errors
  BACKEND_UNAVAILABLE: { status: 503, retryable: true },
  CONTAINER_ERROR: { status: 500, retryable: true },
  
  // Sync errors
  SYNC_SESSION_FAILED: { status: 500, retryable: true },
  SYNC_CONFLICT: { status: 409, retryable: false },
  SYNC_PAUSED: { status: 503, retryable: true },
  SYNC_PROVIDER_NOT_FOUND: { status: 503, retryable: false },
};
```

### Error Response Format

```json
{
  "error": {
    "code": "WORKSPACE_NOT_FOUND",
    "message": "Workspace 'xyz' doesn't exist",
    "suggestion": "Run 'nexus list' to see available workspaces",
    "retryable": false,
    "details": {
      "workspaceName": "xyz"
    }
  }
}
```

### Recovery Procedures

```bash
# Automatic recovery happens for:
# - Port conflicts (auto-retry with new port)
# - Transient network errors (exponential backoff)
# - Backend unavailable (retry with timeout)

# Manual recovery:
nexus doctor    # Diagnose issues
nexus daemon    # Restart daemon
```
