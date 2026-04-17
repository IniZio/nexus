# Nexus Daemon Architecture

> Reference: https://architecture.md/

## Overview

The Nexus daemon (`packages/nexus/`) is a Go service that manages remote workspaces using Firecracker VMs. It exposes RPC handlers over a WebSocket transport and is designed to run on a remote host separate from the CLI client.

## Package Layers

Packages are organized into four layers. Dependencies must point downward (outer → inner).

### Transport Layer (`pkg/transport/`)
Wire protocol, transport abstraction, and RPC registration.

| File | Role |
|---|---|
| `transport.go` | `Transport` interface, `Registry` interface, `Deps` struct |
| `websocket.go` | `WebSocketTransport` implementation |
| `stdio.go` | `StdioTransport` stub (not yet implemented) |
| `rpc.go` | `TypedRegister` helper for compile-time-safe RPC registration |

Transports are pluggable via the `Transport` interface. The daemon currently uses WebSocket; stdio is reserved for future local connections.

### Domain Layer (`pkg/`)
Pure business logic with no internal dependencies on other nexus packages.

- `workspace/` — workspace lifecycle, readiness, git, files, exec
- `project/` — project management, repo绑定
- `store/` — SQLite persistence interfaces and implementations
- `config/` — node configuration
- `agentprofile/` — agent profile lookup
- `keyring/` — keyring integration
- `secrets/` — secret management
- `buildinfo/` — build information
- `git/` — git operations
- `runtime/` — VM runtime selection (Firecracker)
- `safeenv/` — safe environment execution
- `lifecycle/` — lifecycle management

### Orchestration Layer (`pkg/`)
Application logic that composes domain packages. Depends on domain.

- `workspacemgr/` — workspace manager, orchestrates all workspace operations
- `services/` — service manager (start/stop services per workspace)
- `daemon/` — node info, settings, daemon status
- `spotlight/` — spotlight server management
- `credentials/` — credential injection
- `credsbundle/` — credential bundle handling
- `update/` — update management
- `daemonclient/` — client for connecting to the daemon

### Infrastructure Layer (`pkg/infra/`)
Shared infrastructure primitives used by orchestration and transport.

- `infra/relay/` — auth relay broker (`package authrelay`); mint and consume auth grants for workspace env vars

### Dependency Injection (`pkg/deps/`)
The `deps.Deps` struct is the single injection point for all managers and factories.

### Server/RPC (`pkg/server/`)
HTTP/WebSocket server setup, PTY handler, and RPC method registration via `rpc.TypedRegister`.

## Transport Abstraction

```go
// Transport is the interface for a communication transport.
type Transport interface {
    Name() string
    Serve(reg Registry, deps *Deps) error
    Close() error
}

// Registry maps RPC method names to handler functions.
type Registry interface {
    Register(method string, h Handler)
    Dispatch(ctx context.Context, method, msgID string, params json.RawMessage, conn any) (interface{}, *rpckit.RPCError)
    RegisteredMethods() []string
}
```

Concrete transports (`WebSocketTransport`) implement `Transport`. Handlers are registered using `TypedRegister` for type-safe parameter marshaling.

## Handler Organization

RPC handlers were previously scattered under `handlers/` and have been reorganized into topic-based packages under `pkg/`:

| Handler package | Topic |
|---|---|
| `pkg/workspace/` | workspace lifecycle, git, files, exec, readiness |
| `pkg/project/` | project CRUD and workspace associations |
| `pkg/daemon/` | node info, settings, daemon status |
| `pkg/credentials/` | credential injection |
| `pkg/spotlight/` | spotlight server management |

Handler functions follow the signature:
```go
func Handle<T>(ctx context.Context, params T, mgr *Manager) (*Result, *rpckit.RPCError)
```

## Key Interfaces

### WorkspaceManager (project package)
Used by project handlers to list and remove workspaces without importing `workspacemgr` (avoids import cycles).

```go
type WorkspaceManager interface {
    ListEntries() []WorkspaceEntry
    RemoveWithID(id string) error
}
```

### Deps
Centralized dependency container passed to transports.

```go
type Deps struct {
    WorkspaceMgr    *workspacemgr.Manager
    ProjectMgr      *project.Manager
    RuntimeFactory  *runtime.Factory
    SpotlightMgr    *spotlight.Manager
    ServiceMgr      *services.Manager
    AuthRelay       *authrelay.Broker
    NodeCfg         *config.NodeConfig
    SandboxSettings store.SandboxResourceSettingsRepository
}
```

## Remote-First Notes

- The daemon may run on a different machine than the user.
- Daemon host paths are not user paths; user credentials must travel via RPC (`workspace.create` `configBundle`, auth relay at exec time, or explicit client-supplied payloads).
- `nexus create` calls `authbundle.BuildFromHome()` on the **client machine** and sends the result as `configBundle` in `workspace.create`.
