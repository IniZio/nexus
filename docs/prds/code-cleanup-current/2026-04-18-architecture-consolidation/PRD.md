---
type: child
feature_area: code-cleanup
date: 2026-04-18
topic: architecture-consolidation
status: draft
parent_prd: docs/prds/code-cleanup-current.md
---

# Child PRD: Architecture Consolidation — Full Rework

## Parent Context

Parent PRD: `docs/prds/code-cleanup-current.md`
This child PRD covers post-Path A cleanup. The user has requested a thorough, no-compromise refactoring — fine with full rework to achieve the most elegant codebase.

## Overview

Path A (architecture redesign) established the new package structure: `transport/`, `infra/`, `deps/`, and topic-based handler packages. After direct code analysis of the 6 largest files, three packages exceed their size limits and need decomposition. Additionally, the `server.go` (581 lines) mixes concerns that should be separated.

## Concrete Findings (Measured)

| File | Lines | Layer | Limit | Status |
|---|---|---|---|---|
| `server/server.go` | 581 | Orchestration | 400 | **Over (+181)** |
| `workspacemgr/operations.go` | 532 | Orchestration | 400 | **Over (+132)** |
| `workspacemgr/manager.go` | 341 | Domain | 300 | **Over (+41)** |
| `server/rpc_handlers.go` | 305 | Transport | 500 | OK — grows linearly |
| `services/manager.go` | 290 | Domain | 300 | OK |
| `runtime/factory.go` | 50 | Domain | 300 | OK |

## Target Decomposition

### 1. `workspacemgr/manager.go` (341 lines → target ≤300)

**Current role:** Core workspace registry. Handles persistence to SQLite and high-level CRUD.

**Split:**
- `repository.go` (NEW, domain) — `loadAll`, `persistWorkspace`, `nodeStorePathForRoot`, `WorkspaceRepository()`, `ProjectRepository()` accessors
- `manager.go` (REMAINING, domain) — `Manager` struct, `NewManager`, `Create`, `Get`, `List`, `cloneWorkspace`, `SetProjectManager`, `SetSandboxSettings`
- `util.go` (NEW, domain) — `deriveRepoID`, `cloneWorkspace` (if not in manager.go)

**Note:** The 41-line overage is small; this split is modest. Primary driver is separating persistence from domain logic.

### 2. `workspacemgr/operations.go` (532 lines → target ≤400)

**Current role:** All mutating workspace operations: `Remove`, `Stop`, `Restore`, `Start`, `Checkout`, `Fork`, plus state setters and git helpers.

**Split into focused files (all orchestration layer):**

| File | Contents | Est. Lines |
|---|---|---|
| `start.go` | `Start`, `Stop`, `StopWithRuntime` | ~90 |
| `remove.go` | `Remove`, `RemoveWithOptions`, `RemoveWithID` | ~80 |
| `checkout.go` | `Checkout` | ~70 |
| `fork.go` | `Fork`, `resolveParentWorkspace`, `resolveForkSource` | ~100 |
| `restore.go` | `Restore` | ~70 |
| `setters.go` | `SetBackend`, `SetTunnelPorts`, `SetLocalWorktreePath`, `SetProjectID` | ~50 |
| `operations.go` | DELETED — re-exports removed; callers updated | — |

**Key seam:** Low-level git worktree helpers (`setupLocalWorkspaceCheckout`, `copyDirtyStateFromWorkspace`, `runGit`) belong in `pkg/git/` (domain) rather than here. This is a deeper cleanup — defer to a future session.

### 3. `server/server.go` (581 lines → target ≤400)

**Current role:** Central server coordinator: HTTP server, WebSocket upgrade, PTY routing, RPC registry, tunnel management, port state computation, daemon lifecycle.

**Split into focused files:**

| File | Contents | Est. Lines |
|---|---|---|
| `server.go` (REMAINING) | `Server` struct, `NewServer`, `Start`, `Close`, signal handling | ~200 |
| `websocket_handler.go` (NEW) | WebSocket upgrade, `serveWebSocket`, `servePTY` | ~150 |
| `workspace_tunnel.go` (NEW) | `WorkspacePortStates`, `StartWorkspaceTunnels`, `StopWorkspaceTunnels`, `SetWorkspaceTunnelPreference` | ~100 |
| `compose_hints.go` (NEW) | `ensureComposeHints`, `composeTargetPort` | ~50 |
| `workspace_resolver.go` (NEW) | `resolveWorkspace`, `extractWorkspaceID` | ~50 |

**Design note:** `WorkspacePortStates` and tunnel management are inherently server-level concerns — they coordinate across `workspacemgr` and `spotlight`. Moving them out of `server.go` into `server/` keeps the dependency flow correct without creating cross-domain耦合.

### 4. `server/rpc_handlers.go` (305 lines → split by domain)

**Current role:** Single function `newRPCRegistry` registering all RPC methods as closures.

**Split by domain topic:**

| File | Contents |
|---|---|
| `rpc_workspace.go` | workspace.* RPC registrations |
| `rpc_fs.go` | fs.* RPC registrations |
| `rpc_project.go` | project.* RPC registrations |
| `rpc_daemon.go` | daemon.* RPC registrations |
| `rpc_spotlight.go` | spotlight.* RPC registrations |
| `rpc_pty.go` | pty.* RPC registrations (already has `ptyDeps`) |
| `rpc_handlers.go` | `ptyDeps` struct, `newRPCRegistry` that calls all the others |

**Benefit:** Each file is independently reviewable. New RPC methods go in the appropriate domain file. No logic change — only file movement.

### 5. `spotlight/spotlight_rpc.go` — rename and absorb

**Current:** `spotlight_rpc.go` (the renamed old `spotlight.go`) contains only the RPC handler. `spotlight.go` contains the `Manager`.

**Proposed:** Rename `spotlight_rpc.go` → `rpc.go`. The handler `HandleSpotlightPortForward` stays in `spotlight/rpc.go`. No structural change needed — this is a naming cleanup.

### 6. Optional: `pkg/tunnel/` subpackage

**Consider:** The tunnel state computation (`WorkspacePortStates`) and tunnel lifecycle could become a dedicated `pkg/tunnel/` package that wraps `spotlight.Manager`. This would make `spotlight/` purely about port discovery and `tunnel/` about active tunnel orchestration.

**Defer this** to a future session — requires more design work to get right.

## Full Target Package Structure

```
pkg/
├── deps/                     ✅ — dependency injection container
├── infra/                    ✅ — shared infrastructure
│   └── relay/               ✅ — auth relay broker
├── transport/                ✅ — transport abstraction
│   ├── transport.go         ✅ — Transport, Registry
│   ├── websocket.go          ✅ — WebSocket stub
│   └── stdio.go             ✅ — Stdio stub
├── server/                   — daemon server
│   ├── server.go             — ~200 lines (Server, lifecycle, signal)
│   ├── websocket_handler.go  — ~150 lines (WS upgrade + PTY)
│   ├── workspace_tunnel.go   — ~100 lines (tunnel state + lifecycle)
│   ├── compose_hints.go      — ~50 lines (compose port hints)
│   ├── workspace_resolver.go — ~50 lines (workspace resolution)
│   ├── rpc_handlers.go       — ~50 lines (composes all rpc_* files)
│   ├── rpc_workspace.go      — workspace.* registrations
│   ├── rpc_fs.go            — fs.* registrations
│   ├── rpc_project.go       — project.* registrations
│   ├── rpc_daemon.go        — daemon.* registrations
│   ├── rpc_spotlight.go     — spotlight.* registrations
│   ├── rpc_pty.go           — pty.* registrations
│   └── pty/handler.go       ✅
├── workspacemgr/            — workspace domain manager
│   ├── manager.go           — ~300 lines (Manager + CRUD)
│   ├── repository.go        — ~41 lines (persistence helpers)
│   ├── git.go               ✅ — git/worktree helpers
│   ├── operations.go        — DELETED
│   ├── start.go             — ~90 lines (Start/Stop)
│   ├── remove.go            — ~80 lines (Remove variants)
│   ├── checkout.go          — ~70 lines (Checkout)
│   ├── fork.go              — ~100 lines (Fork)
│   ├── restore.go           — ~70 lines (Restore)
│   ├── setters.go           — ~50 lines (state setters)
│   ├── paths.go             ✅
│   └── types.go             ✅
├── workspace/                ✅
├── project/                  ✅
├── daemon/                   ✅
├── credentials/              ✅
├── spotlight/                ✅
│   ├── manager.go           ✅
│   └── rpc.go               ✅ (was spotlight_rpc.go)
├── services/                ✅ (manager.go 290 lines, OK)
├── runtime/factory.go       ✅ (50 lines, OK)
├── store/                   ✅
├── authrelay/               ✅ (moved to infra/relay)
└── ...
```

## Task Graph

### Task List

| ID | Task | Depends On | Est. |
|----|------|-----------|------|
| R1 | Decompose `workspacemgr/manager.go` → `repository.go` + `manager.go` | — | Short (2h) |
| R2 | Decompose `workspacemgr/operations.go` → `start.go`, `remove.go`, `checkout.go`, `fork.go`, `restore.go`, `setters.go`, DELETE `operations.go` | — | Short (3h) |
| R3 | Decompose `server/server.go` → `websocket_handler.go`, `workspace_tunnel.go`, `compose_hints.go`, `workspace_resolver.go`, `server.go` | — | Medium (4h) |
| R4 | Split `server/rpc_handlers.go` → `rpc_workspace.go`, `rpc_fs.go`, `rpc_project.go`, `rpc_daemon.go`, `rpc_spotlight.go`, `rpc_pty.go`, `rpc_handlers.go` | — | Short (2h) |
| R5 | Rename `spotlight_rpc.go` → `spotlight/rpc.go` | — | Quick (15 min) |
| R6 | Update all `workspacemgr/` callers to new file paths | R1, R2 | Short (1h) |
| R7 | Update all `server/` callers to new file paths | R3, R4 | Short (1h) |
| R8 | Verify build passes | R6, R7 | Quick (15 min) |
| R9 | Run full test suite | R8 | Quick (15 min) |
| R10 | Update `ARCHITECTURE.md` with new package structure | R9 | Quick (30 min) |
| R11 | Update child PRD status + master PRD package map | R9 | Quick (15 min) |

### Dependency Graph

```
R1 ──▶ R6 ──▶ R8 ──▶ R9 ──▶ R10, R11
R2 ──▶ R6 ─────────────────┘
R3 ──▶ R7 ─────────────────┘
R4 ──▶ R7 ─────────────────┘
R5 ──▶ R7 ─────────────────┘
```

R1 and R2 can run in parallel (disjoint files in workspacemgr/). R3 and R4 can run in parallel (disjoint files in server/). R5 is trivial. All converge at R6/R7.

### Parallelization

**Wave 1 (parallel):**
- R1: `workspacemgr/manager.go` → `repository.go`
- R2: `workspacemgr/operations.go` → split into operation files
- R3: `server/server.go` → split into handler/tunnel files
- R4: `server/rpc_handlers.go` → split by domain

**Wave 2 (sequential — caller updates):**
- R6: Update workspacemgr/ callers
- R7: Update server/ callers

**Wave 3 (verification):**
- R8 → R9 → R10 → R11

## Effort Estimate

| Task | Effort |
|---|---|
| R1: workspacemgr/manager.go split | Short (2h) |
| R2: workspacemgr/operations.go split | Short (3h) |
| R3: server/server.go split | Medium (4h) |
| R4: rpc_handlers.go split | Short (2h) |
| R5: spotlight_rpc.go rename | Quick (15 min) |
| R6–R7: Caller updates | Short (2h) |
| R8–R9: Verify | Quick (30 min) |
| R10–R11: Docs | Quick (45 min) |
| **Total** | **~14–15h (2 days)** |

## Risks

- `workspacemgr` → `server/` dependency: `workspace_tunnel.go` in server/ calls `workspacemgr` and `spotlight` — this is the correct direction (server orchestrates)
- Many callers of `workspacemgr` operations will need updated import paths — automated via `replaceAll` edits
- `operations.go` deletion must happen only after all callers are confirmed to use the new files

## Deferred (Future Sessions)

- `pkg/tunnel/` subpackage for tunnel orchestration (separates spotlight discovery from active tunnel lifecycle)
- Move low-level git helpers from `workspacemgr/git.go` to `pkg/git/` (domain)
- Auth package grouping (`auth`, `authrelay`, `agentprofile`, `credsbundle`, `daemonclient` → `pkg/identity/`)
