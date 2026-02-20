# Workspace Daemon

Server-side component that receives SDK calls from `@nexus/workspace-sdk`.

## Overview

The workspace daemon is a Go-based WebSocket server that provides secure file system operations and command execution for remote workspaces. It implements the JSON-RPC 2.0 protocol for communication with the SDK.

## Features

- WebSocket-based communication
- JWT token authentication
- Secure file operations (read, write, mkdir, rm, stat, readdir)
- Command execution with timeout support
- Directory traversal protection
- Docker container support

## Building

```bash
# Build the binary
go build -o daemon ./cmd/daemon

# Build Docker image
docker build -t nexus/workspace-daemon .
```

## Running

```bash
# Basic usage
./daemon --port 8080 --workspace-dir /path/to/workspace --token YOUR_JWT_TOKEN

# With custom timeout
./daemon --port 8080 --workspace-dir /workspace --token my-secret-token
```

## Docker

```bash
# Build and run
docker build -t nexus/workspace-daemon .
docker run -d \
  -p 8080:8080 \
  -v /path/to/workspace:/workspace \
  -e TOKEN=your-jwt-token \
  nexus/workspace-daemon
```

## Configuration

| Flag | Environment Variable | Default | Description |
|------|---------------------|---------|-------------|
| `--port` | `PORT` | 8080 | Port to listen on |
| `--workspace-dir` | `WORKSPACE_DIR` | /workspace | Workspace directory path |
| `--token` | `TOKEN` | - | JWT secret token (required) |

## API Reference

### JSON-RPC 2.0 Protocol

All requests follow the JSON-RPC 2.0 format:

```json
{
  "jsonrpc": "2.0",
  "id": "req-123",
  "method": "fs.readFile",
  "params": {
    "path": "/workspace/src/index.ts"
  }
}
```

### File System Methods

#### fs.readFile

Read file contents.

**Params:**
```json
{
  "path": "relative/path/to/file",
  "encoding": "utf8"  // optional, default: utf8
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": "req-123",
  "result": {
    "content": "file contents...",
    "encoding": "utf8",
    "size": 1234
  }
}
```

#### fs.writeFile

Write file contents.

**Params:**
```json
{
  "path": "relative/path/to/file",
  "content": "file contents...",
  "encoding": "utf8"  // optional
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": "req-123",
  "result": {
    "ok": true,
    "path": "relative/path/to/file",
    "size": 1234
  }
}
```

#### fs.exists

Check if a file or directory exists.

**Params:**
```json
{
  "path": "relative/path"
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": "req-123",
  "result": {
    "exists": true,
    "path": "relative/path"
  }
}
```

#### fs.readdir

List directory contents.

**Params:**
```json
{
  "path": "relative/path"  // optional, default: "."
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": "req-123",
  "result": {
    "entries": [
      {"name": "file.txt", "path": "file.txt", "is_dir": false, "size": 123},
      {"name": "subdir", "path": "subdir", "is_dir": true, "size": 0}
    ],
    "path": "."
  }
}
```

#### fs.mkdir

Create a directory.

**Params:**
```json
{
  "path": "relative/path/to/dir",
  "recursive": true  // optional, default: false
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": "req-123",
  "result": {
    "ok": true,
    "path": "relative/path/to/dir"
  }
}
```

#### fs.rm

Remove a file or directory.

**Params:**
```json
{
  "path": "relative/path",
  "recursive": true  // optional, default: false
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": "req-123",
  "result": {
    "ok": true,
    "path": "relative/path"
  }
}
```

#### fs.stat

Get file/directory metadata.

**Params:**
```json
{
  "path": "relative/path"
}
```

**Response:**
```json
{
  "jsonrpc": "2.0",
  "id": "req-123",
  "result": {
    "name": "filename",
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
