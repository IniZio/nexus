# Workspace Daemon

The workspace daemon (`workspace-daemon`) is a Go-based server that provides remote file system and execution capabilities to the Nexus Workspace SDK.

## Overview

```
┌─────────────┐     WebSocket      ┌─────────────────┐
│ OpenCode    │ ◄────────────────► │  Workspace      │
│ + Plugin    │                    │  Daemon         │
└─────────────┘                    └────────┬────────┘
                                           │
                                    ┌──────▼──────┐
                                    │ File System │
                                    │ & Execution │
                                    └─────────────┘
```

## Components

### Daemon (`packages/workspace-daemon/`)

- **server.go**: WebSocket server handling RPC calls
- **handlers/**: File system and execution handlers
- **lifecycle/**: Lifecycle script management

### Plugin (`packages/opencode-plugin/`)

OpenCode plugin that:
- Loads workspace configuration from `opencode.json`
- Provides `nexus-connect` and `nexus-status` tools
- Hooks into tool execution for monitoring

### SDK (`packages/workspace-sdk/`)

TypeScript SDK for interacting with the daemon:
- WebSocket client
- File operations (read, write, mkdir, etc.)
- Command execution

## Configuration

### Daemon

```bash
workspace-daemon \
  --port 8080 \
  --token <jwt-secret> \
  --workspace-dir /workspace
```

### OpenCode Plugin

```json
{
  "plugin": ["@nexus/opencode-plugin"],
  "nexus": {
    "workspace": {
      "endpoint": "ws://localhost:8080",
      "workspaceId": "my-workspace",
      "token": "${NEXUS_TOKEN}"
    }
  }
}
```

## Lifecycle Scripts

The daemon supports lifecycle hooks via `.nexus/lifecycle.json`:

```json
{
  "version": "1.0",
  "hooks": {
    "pre-start": [{ "name": "check-deps", "command": "npm", "args": ["install"] }],
    "post-start": [{ "name": "start-services", "command": "docker", "args": ["compose", "up", "-d"] }],
    "pre-stop": [{ "name": "save-state", "command": "./scripts/save.sh" }],
    "post-stop": [{ "name": "cleanup", "command": "docker", "args": ["compose", "down"] }]
  }
}
```

## RPC Methods

| Method | Description |
|--------|-------------|
| `fs.readFile` | Read file contents |
| `fs.writeFile` | Write file contents |
| `fs.mkdir` | Create directory |
| `fs.readdir` | List directory |
| `fs.exists` | Check path exists |
| `fs.stat` | Get file stats |
| `fs.rm` | Remove file/directory |
| `exec` | Execute command |
| `workspace.info` | Get workspace info |

## Docker

```dockerfile
FROM golang:1.21-alpine AS builder
WORKDIR /app
COPY . .
RUN go build -o workspace-daemon ./cmd/daemon

FROM alpine:latest
RUN apk --no-cache add openssh-client
COPY --from=builder /app/workspace-daemon /usr/local/bin/
WORKDIR /workspace
CMD ["workspace-daemon", "--port", "8080", "--token", "secret"]
```
