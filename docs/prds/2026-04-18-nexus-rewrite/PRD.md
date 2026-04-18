---
type: master
feature_area: nexus-rewrite
date: 2026-04-18
status: active
child_prds: []
---

# Nexus Daemon — Clean Rewrite

## Overview

The existing `packages/nexus/` daemon has accrued structural debt: a flat `pkg/` layout with 25+ sibling directories, mixed domain and infrastructure concerns in the same packages, no enforced module boundary, and 451 tests spread across unit/integration with no single canonical behavioral spec.

This initiative moves the existing code to `packages/nexus_backup/`, then reimplements the daemon from scratch using a clean layered architecture with well-defined concept boundaries. The new codebase uses Go's `internal/` to enforce the module boundary, organizes packages into domain hubs (not technical layers), and defines system behavior via a comprehensive BDD integration test suite that serves as the executable specification.

The new implementation achieves full feature parity with the current daemon. The backup copy is the canonical reference for business logic during the rewrite.

## Architecture

### Module Layout

```
packages/nexus/           ← same Go module path: github.com/inizio/nexus/packages/nexus
├── go.mod
├── cmd/
│   ├── nexusd/           ← daemon binary (was cmd/daemon/)
│   ├── nexus/            ← CLI binary (stays)
│   ├── nexus-agent/      ← Firecracker guest agent (was cmd/nexus-firecracker-agent/)
│   └── nexus-tap-helper/ ← TAP device helper (stays)
├── internal/             ← ALL daemon internals; unexportable from module
│   ├── domain/           ← pure types, interfaces, errors; no internal deps
│   ├── app/              ← use-cases; imports domain only
│   ├── infra/            ← implements domain interfaces; imports domain + stdlib/external
│   ├── transport/        ← wire protocol adapters
│   ├── rpc/              ← RPC handlers + dispatch; imports app + domain
│   ├── identity/           ← daemon auth: Identity type, LocalToken, Provider interface, auth errors
│   ├── creds/              ← credential delivery to workspace (separate from daemon auth)
│   │   ├── bundle/         ← host credential bundle (CLI→daemon wire format, was credsbundle/)
│   │   ├── relay/          ← auth relay broker: mints exec-time grants for workspace→daemon calls (was infra/relay/)
│   │   ├── inject/         ← injects credentials into workspace env at exec time (was credentials/)
│   │   └── agentprofile/   ← AI agent credential profile registry (was agentprofile/)
│   └── daemon/           ← daemon bootstrap, config, DI container, update
└── test/
    └── bdd/              ← BDD integration tests (executable specification)
        ├── harness/
        └── <feature>/
```

### Dependency Direction

```
domain ←── app ←── rpc ←── transport
  ↑           ↑
infra     daemon (wires everything)
  ↑
(external: firecracker, sqlite, os)
```

Rules:
- `domain/` imports nothing from `internal/`
- `app/` imports `domain/` only
- `infra/` imports `domain/` only
- `rpc/` imports `app/` and `domain/`
- `transport/` imports `rpc/`
- `daemon/` imports all layers (it is the composition root)
- `identity/` and `creds/` import `domain/` only
- `test/bdd/` imports everything via the daemon harness

### Package Map

#### `internal/domain/`
Pure entities, value objects, and repository/driver interfaces. No I/O, no SQL, no HTTP.

| Package | Contents |
|---|---|
| `domain/workspace` | `Workspace` rich entity (id, repo, ref, state, policy, auth bindings, tunnel ports, lineage), `State` enum + transition rules, `Policy` + validation, `CreateSpec`, errors, `WorkspaceRepository` interface |
| `domain/project` | `Project` type, `ProjectRepository` interface |
| `domain/runtime` | `Driver` interface (Backend, Create, Start, Stop, Restore, Pause, Resume, Fork, Destroy), `CreateRequest`, errors |
| `domain/spotlight` | `Forward` entity (references workspace by ID string), `ForwardSource` enum, `ExposeSpec`, `ForwardRepository` interface |

**Domain purity rule**: `domain/` packages compile with zero OS/db/network imports. No `os.Stat`, no `sql`, no `net`. The thin path-wrapper `pkg/workspace.Workspace` (with `os.Stat`, `MkdirAll`) from the old codebase is NOT the domain type — it was an infrastructure helper. The real domain type is the rich `workspacemgr.Workspace` struct.

**State machine** in `domain/workspace/state.go`:
- Valid transitions: `created→running`, `running→{paused,stopped}`, `paused→{running,stopped}`, `stopped→{running,removed,restored}`, `restored→running`, `any→removed`
- `State.CanTransitionTo(next State) bool` enforced in domain

#### `internal/app/`
Use-case services. Each method = one user-visible operation. No transport coupling.

| Package | Contents |
|---|---|
| `app/workspace` | CreateWorkspace, ForkWorkspace, CheckoutWorkspace, StartWorkspace, StopWorkspace, RemoveWorkspace, RestoreWorkspace, SetLocalWorktree, WorkspaceInfo, ListWorkspaces, WorkspaceRelations |
| `app/project` | CreateProject, GetProject, ListProjects, RemoveProject |
| `app/spotlight` | StartSpotlight, ListForwards, CloseForward; port monitor |
| `app/pty` | OpenPTY, WritePTY, ResizePTY, ClosePTY, AttachPTY, ListPTY, GetPTY, RenamePTY, TmuxSession |
| `app/auth` | MintAuthRelay, RevokeAuthRelay, ValidateToken |

#### `internal/infra/`
Infrastructure implementations — the only layer that touches external systems.

| Package | Contents |
|---|---|
| `infra/runtime/firecracker` | Firecracker Driver implementation |
| `infra/runtime/sandbox` | Process sandbox Driver implementation |
| `infra/store` | SQLite WorkspaceRepository, ProjectRepository, ForwardRepository |
| `infra/store/migrations` | Goose SQL migrations |
| `infra/secrets/discovery` | Host secret discovery |
| `infra/secrets/interceptor` | Exec interceptor for credential injection |
| `infra/secrets/server` | Host-side secret server |
| `infra/secrets/vending` | Credential vending to guest |
| `infra/secrets/vsock` | vsock transport for secret vending |
| `infra/git` | Git shell-out helpers: clone, fetch, checkout, DeriveRepoID, tree SHA |
| `infra/git/worktree` | Git worktree operations |

#### `internal/rpc/`
Thin handlers: validate input → call app service → map to response DTO. No business logic.

| Package | Contents |
|---|---|
| `rpc/workspace` | Handlers for all workspace.* methods |
| `rpc/project` | Handlers for project.* methods |
| `rpc/fs` | Handlers for fs.* methods |
| `rpc/pty` | Handlers for pty.* methods |
| `rpc/spotlight` | Handlers for spotlight.* methods + workspace.ports.* + workspace.tunnels.* |
| `rpc/daemon` | Handlers for node.info, daemon.settings.* |
| `rpc/auth` | Handlers for authrelay.mint, authrelay.revoke |
| `rpc/errors` | RPCError type and error mapping helpers |
| `rpc/registry` | Method registry, dispatch, TypedRegister helper |

#### `internal/transport/`
Adapters that connect the RPC registry to a wire protocol.

| Package | Contents |
|---|---|
| `transport` | Transport interface |
| `transport/websocket` | WebSocket transport implementation |
| `transport/stdio` | Stdio transport (stub/future) |

#### `internal/identity/`
Daemon authentication — who the RPC caller is.

| File | Contents |
|---|---|
| `identity.go` | `Identity` type, `LocalToken` |
| `provider.go` | `Provider` interface |
| `errors.go` | Auth errors |

#### `internal/creds/`
Credential delivery — how user secrets reach the workspace. Completely separate from daemon auth.

| Package | Contents |
|---|---|
| `creds/bundle` | `CredentialBundle` type; CLI→daemon serialization of host credentials (SSH keys, git config) |
| `creds/relay` | Auth relay broker: mints short-lived grants consumed by workspace processes calling back to daemon |
| `creds/inject` | Injects credentials into the workspace environment at exec time |
| `creds/agentprofile` | AI agent credential profile registry: maps agent names (Claude, Cursor, etc.) to their required credential configs |

#### `internal/daemon/`
Composition root: wires all layers together, owns the process lifecycle.

| Package | Contents |
|---|---|
| `daemon/config` | Daemon node config + workspace config loading |
| `daemon/node` | Node info, capabilities, node settings |
| `daemon/deps` | Deps struct — single DI container |
| `daemon/update` | Self-update check/apply, manifest, lock |
| `daemon/service` | Daemon service entry point (start, shutdown) |

#### `test/bdd/`
BDD integration tests — the executable spec. Each test file = one behavioral scenario.

| Package | Contents |
|---|---|
| `bdd/harness` | Daemon runner, client wrapper, fixture helpers |
| `bdd/workspace` | Workspace lifecycle scenarios |
| `bdd/project` | Project CRUD scenarios |
| `bdd/spotlight` | Port exposure scenarios |
| `bdd/pty` | Terminal session scenarios |
| `bdd/auth` | Auth relay + credential delivery scenarios |
| `bdd/fs` | Filesystem RPC scenarios |

## Data Model

No schema changes from the current implementation. Tables stay:
- `workspaces` — id, metadata JSON, project_id, state
- `projects` — id, name, repo_url, config JSON
- `spotlight_forwards` — id, workspace_id, spec JSON, state
- `sandbox_resource_settings` — global VM resource defaults

## API / Interface

### RPC Methods (full parity with current daemon)

**Workspace:** `workspace.info`, `workspace.create`, `workspace.list`, `workspace.relations.list`, `workspace.remove`, `workspace.stop`, `workspace.start`, `workspace.restore`, `workspace.fork`, `workspace.checkout`, `workspace.setLocalWorktree`, `workspace.ready`, `workspace.ports.list`, `workspace.ports.add`, `workspace.ports.remove`, `workspace.tunnels.start`, `workspace.tunnels.stop`

**Filesystem:** `fs.readFile`, `fs.writeFile`, `fs.exists`, `fs.readdir`, `fs.mkdir`, `fs.rm`, `fs.stat`

**PTY:** `pty.open`, `pty.write`, `pty.resize`, `pty.close`, `pty.attach`, `pty.list`, `pty.get`, `pty.rename`, `pty.tmux`

**Project:** `project.list`, `project.create`, `project.get`, `project.remove`

**Spotlight:** `spotlight.start`, `spotlight.list`, `spotlight.close`

**Daemon/Node:** `node.info`, `daemon.settings.get`, `daemon.settings.update`

**Auth:** `authrelay.mint`, `authrelay.revoke`

**Exec/Git/Service:** `exec`, `git.command`, `service.command`

### Key Interfaces

```go
// domain/runtime
type Driver interface {
    Start(ctx context.Context, ws *workspace.Workspace) error
    Stop(ctx context.Context, ws *workspace.Workspace) error
    Snapshot(ctx context.Context, ws *workspace.Workspace) (*Snapshot, error)
    Restore(ctx context.Context, ws *workspace.Workspace, snap *Snapshot) error
}

// domain/workspace
type Repository interface {
    Create(ctx context.Context, ws *Workspace) error
    Get(ctx context.Context, id string) (*Workspace, error)
    List(ctx context.Context) ([]*Workspace, error)
    Update(ctx context.Context, ws *Workspace) error
    Delete(ctx context.Context, id string) error
}

// transport
type Transport interface {
    Name() string
    Serve(reg rpc.Registry, deps *daemon.Deps) error
    Close() error
}
```

## Error Handling

- All user-visible errors surface as `RPCError{Code, Message}` via `rpc/errors`
- Infrastructure errors (DB, VM, git) are wrapped and translated at the `app/` boundary
- BDD tests assert on error codes, not internal error types

## Known Limitations

- `cmd/nexus/` (the CLI, 2322L) is not restructured in this pass — it remains as-is and continues to call the daemon via RPC. A separate CLI cleanup effort can follow.
- `cmd/nexus-agent/` guest agent is not restructured — only the daemon is reimplemented.
- `cmd/nexus-tap-helper/` is not restructured.
- The backup at `packages/nexus_backup/` is the reference for business logic; it is not deleted until parity is confirmed.
- Stdio transport stub carried forward but not implemented (same as current).

## Task Graph

### Implementation Phases

This is a large initiative. It is broken into phases, each of which is a coherent deliverable.

| Phase | Focus | Gate |
|---|---|---|
| P1 | BDD harness + empty scenarios (spec-first) | Harness boots daemon; scenarios compile and fail |
| P2 | Domain layer + infra/store | Domain types defined; store compiles |
| P3 | infra/runtime (Firecracker + sandbox) | VM can start/stop in tests |
| P4 | app/workspace + rpc/workspace | workspace.create/start/stop/remove pass BDD |
| P5 | app/spotlight + rpc/spotlight + infra/secrets | Port exposure + secrets pass BDD |
| P6 | app/pty + rpc/pty | PTY scenarios pass BDD |
| P7 | auth layer (relay, bundle, agentprofile) | Auth relay scenarios pass BDD |
| P8 | rpc/fs, rpc/daemon, service.command, git.command | Remaining RPC scenarios pass BDD |
| P9 | CLI wiring update (cmd/nexus points to new daemon) | Full e2e nexus CLI tests pass |
| P10 | Delete nexus_backup; update ARCHITECTURE.md | All BDD pass; no backup reference needed |

### Task List (P1 — BDD Harness)

| ID | Task | Depends On | Agent | Files | Est. |
|----|------|-----------|-------|-------|------|
| T1.1 | Backup: `mv packages/nexus packages/nexus_backup` | — | coder | repo root | Quick |
| T1.2 | Scaffold new `packages/nexus/` with go.mod, cmd/ stubs, internal/ tree | T1.1 | coder | new module scaffold | Short |
| T1.3 | Write `test/bdd/harness/` — daemon runner, RPC client, fixture helpers | T1.2 | coder | test/bdd/harness/ | Short |
| T1.4 | Write empty BDD scenario files for each feature area (compile-pass with t.Skip) | T1.3 | coder | test/bdd/{workspace,project,...}/ | Short |
| T1.5 | Verify: `go test ./test/bdd/...` compiles and all skip | T1.4 | coder | — | Quick |

### Dependency Graph (P1)

```
T1.1 → T1.2 → T1.3 → T1.4 → T1.5
```

Phases P2–P10 will be defined as child PRDs once P1 is complete and the harness structure is validated.

### Parallelization Rules

- Within P1: strictly sequential (each step builds on previous).
- P2 (domain) and P3 (infra/runtime) can run in parallel once P1 is complete.
- P4–P8 are sequential per feature but can be parallelized across independent features once domain layer is stable.
- Do not begin P9 until all BDD scenarios pass.

## Steer Log

### 2026-04-18 — Scope change: incremental refactor → clean rewrite

- **Trigger**: User identified incremental taxonomy refactor as half-baked; requested full rewrite with proper concept boundaries and BDD spec
- **From**: Incremental import-path moves of existing packages (docs/prds/code-cleanup-current/2026-04-18-package-taxonomy/PRD.md)
- **To**: Full rewrite with backup, clean `internal/` layout, BDD-first spec, full feature parity
- **Rationale**: Structural debt in existing codebase is too deep for incremental improvement; a clean slate with a BDD spec as the ground truth enables correct design and prevents regressing behavior
- **Affected sections**: All
