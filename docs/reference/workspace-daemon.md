# Environment Daemon (`nexusd`)

The Nexus daemon behind environment operations is implemented in `packages/nexusd` and runs from `./cmd/daemon`.

## Binary and Startup

Build and run from source:

```bash
cd packages/nexusd
go run ./cmd/daemon --token <secret>
```

Supported daemon flags (from `packages/nexusd/cmd/daemon/main.go`):
- `--port` (default: `8080`)
- `--workspace-dir` (default: `$HOME/.nexus/workspaces`)
- `--token` (required unless `--jwt-secret-file` is provided)
- `--jwt-secret-file`

The daemon exits if neither `--token` nor `--jwt-secret-file` is provided.

## Implemented Server Surfaces

Core server implementation is in `packages/nexusd/pkg/server/server.go`.

### HTTP endpoints

Registered routes include:
- `GET /health`
- `GET|POST /api/v1/workspaces`
- `GET|POST|DELETE /api/v1/workspaces/{id-or-name}` and subpaths
- `GET|POST /api/v1/config`
- `GET /ws`
- `GET /ws/ssh-agent`

Environment-related subpaths implemented in server handlers include:
- `/start`, `/stop`, `/exec`, `/logs`, `/status`
- `/sync/status`, `/sync/pause`, `/sync/resume`, `/sync/flush`
- `/checkpoints`
- `/ports`

### WebSocket RPC methods

`packages/nexusd/pkg/server/server.go` dispatches:
- `fs.readFile`
- `fs.writeFile`
- `fs.exists`
- `fs.readdir`
- `fs.mkdir`
- `fs.rm`
- `fs.stat`
- `exec`
- `workspace.info`

Handlers live in `packages/nexusd/pkg/handlers/fs.go` and `packages/nexusd/pkg/handlers/exec.go`.

## Scope Notes

- This page documents the implemented daemon in `packages/nexusd` only.
- User-facing commands are organized around `project`, `branch`, `version`, and `environment`.
- Low-level daemon routes and RPC method names still use legacy `workspace` terminology in the current implementation.
- Internal implementation details may change; rely on CLI docs for supported user workflows.
