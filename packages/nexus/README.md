# Workspace Daemon
# Nexus Daemon (`packages/nexus`)

Core Go runtime for Nexus workspace orchestration.

## What this package provides

- `nexus` CLI subcommands used in CI and project setup:
  - `nexus init`
  - `nexus doctor`
  - `nexus exec`
- Firecracker-first isolated runtime support.
- Workspace lifecycle helpers (`.nexus` probes/checks/e2e scaffolding).

## Build

```bash
cd packages/nexus
go build ./cmd/nexus/...
```

## Test

```bash
cd packages/nexus
go test ./cmd/nexus -count=1
go test ./pkg/runtime/firecracker -count=1
```

## Runtime notes

- Firecracker requires Linux/KVM.
- On macOS, use a Linux VM path (for example Lima) for firecracker-backed checks.

## Docs

- `docs/reference/workspace-daemon.md`
- `docs/reference/workspace-config.md`
    "path": "relative/path",
    "is_dir": false,
    "size": 1234,
    "mode": "-rw-r--r--",
    "mod_time": "2026-02-20T10:00:00Z"
  }
}
```

### Execution Methods

#### exec

Execute a command.

**Params:**
```json
{
  "command": "ls",
  "args": ["-la", "/workspace"],
  "options": {
    "timeout": 30,  // seconds, default: 30, max: 300
    "work_dir": "/workspace",  // optional
    "env": ["VAR=value"]  // optional
  }
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": "req-123",
  "result": {
    "stdout": "total 16\ndrwxr-xr-x    2 root root     4096 Feb 20 10:00 .\n...",
    "stderr": "",
    "exit_code": 0,
    "command": "ls -la /workspace"
  }
}
```

### Workspace Methods

#### workspace.info

Get workspace information.

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": "req-123",
  "result": {
    "workspace_id": "ws-1234567890",
    "workspace_path": "/workspace"
  }
}
```

## Error Codes

| Code | Message | Description |
|------|---------|-------------|
| -32000 | Server Error | Generic server error |
| -32001 | Invalid Token | JWT token validation failed |
| -32002 | Unauthorized | Not authenticated |
| -32003 | File Not Found | Requested file/directory doesn't exist |
| -32004 | Permission Denied | Insufficient permissions |
| -32005 | Command Timeout | Command execution timed out |
| -32600 | Invalid Request | Malformed JSON-RPC request |
| -32601 | Method Not Found | Unknown method |
| -32602 | Invalid Params | Invalid method parameters |
| -32603 | Internal Error | Internal server error |

## Security

- All paths are validated to prevent directory traversal attacks
- JWT tokens are required for all connections
- Commands have configurable timeouts (default 30s, max 5min)
- File operations are restricted to the workspace directory
