# Workspace Daemon

The workspace daemon is a Go-based server that provides remote file system and execution capabilities to the Nexus Workspace SDK via WebSocket.

## Overview

```
┌─────────────┐     WebSocket      ┌─────────────────┐
│ SDK Client  │ ◄────────────────► │  Workspace      │
│             │                    │  Daemon (Go)    │
└─────────────┘                    └────────┬────────┘
                                            │
                                     ┌──────▼──────┐
                                     │ File System │
                                     │ & Execution │
                                     └─────────────┘
```

## Installation

```bash
# Build from source
cd packages/workspace-daemon
go build -o workspace-daemon ./cmd/daemon
```

## Running the Daemon

```bash
workspace-daemon \
  --port 8080 \
  --token <jwt-secret> \
  --workspace-dir /workspace
```

## Configuration

| Flag | Description | Default |
|------|-------------|---------|
| `--port` | Server port | 8080 |
| `--token` | Authentication token | - |
| `--workspace-dir` | Workspace directory | /workspace |
| `--host` | Host to bind to | localhost |

## Components

### Server (`cmd/daemon/`)

- Main entry point for the daemon
- WebSocket server handling RPC calls

### Handlers (`pkg/`)

- File system handlers
- Command execution handlers

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
| `workspace.create` | Create isolated remote workspace |
| `workspace.open` | Open workspace by id |
| `workspace.list` | List workspace records |
| `workspace.remove` | Remove workspace by id |
| `workspace.ready` | Poll readiness checks until success/timeout |
| `spotlight.expose` | Expose remote service port locally |
| `spotlight.list` | List active Spotlight forwards |
| `spotlight.close` | Close Spotlight forward |
| `spotlight.applyDefaults` | Apply project spotlight defaults from `.nexus/workspace.json` |
| `spotlight.applyComposePorts` | Auto-forward all docker-compose published ports |
| `git.command` | Run scoped git action in workspace |
| `service.command` | Start/stop/restart/status/logs for workspace services |

### `service.command` options

Supported actions:

- `start`
- `stop`
- `restart`
- `status`
- `logs`

Optional params for `start`/`restart`:

- `stopTimeoutMs` - graceful stop timeout before forced kill
- `autoRestart` - automatically restart on unexpected exit
- `maxRestarts` - cap restarts when `autoRestart` is true
- `restartDelayMs` - delay between restart attempts

### `workspace.ready`

Poll one or more command checks inside the workspace until all return exit code 0 or timeout.

Params:

- `workspaceId`
- `checks`: `[{ name, command, args[] }]`
- `profile`: readiness profile name (for built-in check set)
- `timeoutMs` (optional)
- `intervalMs` (optional)

Built-in profiles:

- `default-services`
  - service `student-portal` running
  - service `api` running
  - service `opencode-acp` running (optional: skipped when `opencode` is unavailable)

Convention-over-configuration behavior:

- On `workspace.ready`, daemon attempts compose port auto-forward once per workspace session.
- If `docker-compose.yml` or `docker-compose.yaml` exists, all published `ports` mappings are forwarded.
- Forward collisions are tolerated per mapping; successful mappings continue.
- If no compose file is present, auto-forward is a no-op.
- Spotlight host defaults to loopback (`127.0.0.1`) when host binding is not explicit.

Project config source:

- `.nexus/workspace.json` (canonical)
- `workspace.ready` profile lookups resolve from project config first, then built-ins

See `docs/reference/workspace-config.md` for full schema and examples.

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

## SDK Integration

Use the Workspace SDK to connect:

```typescript
import { WorkspaceClient } from '@nexus/workspace-sdk';

const client = new WorkspaceClient({
  endpoint: 'ws://localhost:8080',
  workspaceId: 'my-workspace',
  token: 'secret',
});

const ws = await client.workspace.create({
  repo: '<internal-repo-url>',
  ref: 'main',
  workspaceName: 'workspace-student-portal',
  agentProfile: 'default',
  policy: {
    authProfiles: ['gitconfig'],
    sshAgentForward: true,
    gitCredentialMode: 'host-helper'
  }
});

await ws.spotlight.expose({ service: 'student-portal', remotePort: 5173, localPort: 5173 });
await ws.spotlight.expose({ service: 'api', remotePort: 8000, localPort: 8000 });
```
