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

#### Switch Workspace
```http
POST /api/v1/workspaces/{id}/switch
```

Response: `200 OK`

#### Execute Command
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

### Snapshots

#### Create Snapshot
```http
POST /api/v1/workspaces/{id}/snapshots
Content-Type: application/json

{
  "name": "before-refactor",
  "description": "Clean state before major changes"
}
```

#### List Snapshots
```http
GET /api/v1/workspaces/{id}/snapshots
```

#### Restore Snapshot
```http
POST /api/v1/snapshots/{snapshot_id}/restore
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
  rpc StartWorkspace(StartWorkspaceRequest) returns (Operation);
  rpc StopWorkspace(StopWorkspaceRequest) returns (Operation);
  rpc SwitchWorkspace(SwitchWorkspaceRequest) returns (SwitchWorkspaceResponse);
  
  // File operations (streaming)
  rpc StreamFile(StreamFileRequest) returns (stream FileChunk);
  rpc WriteFile(stream WriteFileRequest) returns (WriteFileResponse);
  
  // Execution
  rpc ExecStream(ExecRequest) returns (stream ExecOutput);
  
  // Snapshots
  rpc CreateSnapshot(CreateSnapshotRequest) returns (Snapshot);
  rpc RestoreSnapshot(RestoreSnapshotRequest) returns (Operation);
  
  // Monitoring
  rpc GetStats(GetStatsRequest) returns (ResourceStats);
  rpc StreamStats(StreamStatsRequest) returns (stream ResourceStats);
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

### Workspace Management

```bash
# Create workspace
boulder workspace create <name> [options]
  --template=<name>        # Use predefined template
  --image=<image>          # Custom Docker image
  --backend=<backend>      # docker (default) | sprite
  --resources=<class>      # small | medium | large | xlarge
  --from=<snapshot>        # Restore from snapshot

# Start/stop workspace
boulder workspace up <name>       # Start/create workspace
boulder workspace down <name>     # Stop workspace

# Switch workspace (<2s)
boulder workspace switch <name>   # Fast context switch

# List and info
boulder workspace list             # List all workspaces
boulder workspace show <name>     # Show workspace details

# Delete
boulder workspace destroy <name>  # Delete workspace
```

### Workspace Operations

```bash
# Execute command
boulder workspace exec <name> <command> [args...]
  --interactive, -i        # Interactive mode
  --tty, -t                # Allocate TTY

# Open shell
boulder workspace shell <name>   # Open shell in workspace

# View logs
boulder workspace logs <name>
  --follow, -f             # Stream logs
  --tail=<n>               # Last N lines
```

### Snapshots

```bash
boulder workspace snapshot create <name> <snapshot-name>
  --description=<desc>
  
boulder workspace snapshot list <name>
boulder workspace snapshot restore <name> <snapshot-name>
boulder workspace snapshot delete <name> <snapshot-name>
```

### Port Forwarding

```bash
boulder workspace port add <name> <container-port>
  --visibility=<vis>       # private | public | org
  
boulder workspace port list <name>
boulder workspace port remove <name> <port-id>
```

### Global Flags

```bash
--backend=<backend>      # Default backend
--debug                  # Enable debug logging
--json                   # JSON output format
--config=<path>         # Config file path (default: ~/.nexus/config.yaml)
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

# Telemetry
telemetry:
  enabled: true
  endpoint: https://telemetry.nexus.dev
  
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
};
```

### Error Response Format

```json
{
  "error": {
    "code": "WORKSPACE_NOT_FOUND",
    "message": "Workspace 'xyz' doesn't exist",
    "suggestion": "Run 'boulder workspace list' to see available workspaces",
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
boulder workspace repair <name>    # Repair broken workspace
boulder workspace cleanup           # Free up disk space
```
