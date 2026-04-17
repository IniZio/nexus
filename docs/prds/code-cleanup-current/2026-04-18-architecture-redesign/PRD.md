---
type: child
feature_area: code-cleanup
date: 2026-04-18
topic: architecture-redesign
status: draft
parent_prd: docs/prds/code-cleanup-current.md
---

# Child PRD: Architecture Redesign — Nested Modular Structure with Transport Abstraction

## Parent Context

Parent PRD: `docs/prds/code-cleanup-current.md`
Affected section: Phase 4 — Domain-Based Restructuring (completed); "Architecture" section (package map)
Scope expansion: This child PRD covers structural changes not addressed in Phase 4.

## What Changed

Phase 4 restructured `handlers/` into subdirectories — a discrete, completed win. But the broader `pkg/` layout was left flat. Meanwhile:

1. **Transport layer is tightly coupled to the server.** `server/server.go` (581 lines) mixes HTTP server setup, WebSocket upgrade, and PTY session routing. There is no abstraction for plugging in alternative transports (stdio, Unix domain socket) without rewriting the server entry point.

2. **`pkg/` has 20+ top-level packages with no logical grouping.** Packages like `auth`, `authrelay`, `daemonclient`, `credsbundle`, `agentprofile` all relate to credentials and identity but live at the same level as `runtime`, `spotlight`, and `workspace`. The authgear-server uses a `lib/infra/` + `lib/<domain>/` nesting pattern that makes boundaries explicit.

3. **Terminology inconsistencies remain.** `spotlight.expose` should be `spotlight.start`. Other RPC method names may need review.

4. **No `ARCHITECTURE.md`** exists in the repository. The user requested one referencing `https://architecture.md/` as the template — filled in with Nexus-specific content.

5. **Cross-package helper leakage.** `workspace.SandboxResourcePolicyFromRepository` was exported from `handlers/workspace/` to serve `daemon/settings.go`. This is a symptom of a missing shared domain package.

## Why This Cannot Wait

The current structure makes every subsequent cleanup harder:
- New packages get added flat (`pkg/internal/`, `pkg/types/`) rather than nested
- `server/` is the highest-risk file for introducing bugs during cleanup (e.g., removing Lima/Firecracker references)
- No clear place for shared domain types — they end up in handlers or workspacemgr, creating import cycles
- Without `ARCHITECTURE.md`, new contributors and agents have no canonical reference

## Proposed Resolution

Two-path approach:

### Path A: Full Redesign (Recommended)

Restructure `packages/nexus/pkg/` into a nested modular layout inspired by authgear-server's `lib/<domain>/` + `lib/infra/` pattern, and extract a `transport` abstraction.

#### 1. Target Package Structure

```
pkg/
├── auth/                         # ← existing (rename/move into nested group)
│   ├── identity.go               # Identity type, provider interfaces
│   └── token.go                 # Local token generation
├── credentials/                  # ← existing handlers/credentials/ inject.go
│   └── inject.go                # Mint/revoke tokens for workspace exec
├── daemon/                       # ← existing handlers/daemon/
│   ├── settings.go              # Sandbox resource limits (CPU/mem)
│   ├── node.go                  # Node info + capabilities
│   └── service.go              # Workspace service lifecycle
├── deps/                         # NEW: dependency injection container
│   ├── deps.go                  # Container struct with all managers
│   └── wire.go                  # Construction/factory functions
├── infra/                        # NEW: shared infrastructure
│   ├── store/                   # SQLite persistence
│   ├── relay/                   # Authrelay broker (moved from authrelay/)
│   └── http/                    # HTTP server setup + TLS config
├── project/                      # ← existing pkg/project/
│   └── manager.go               # Project CRUD
├── runtime/                      # ← existing (firecracker + process drivers)
│   └── factory.go
├── spotlight/                     # ← existing pkg/spotlight/
│   ├── manager.go               # Port forwarding management
│   └── portmonitor.go           # Port discovery
├── transport/                    # NEW: transport abstraction layer
│   ├── transport.go            # Transport interface + registry
│   ├── websocket.go             # WebSocket transport (current)
│   ├── stdio.go                 # Stdio transport (future)
│   └── rpc.go                   # RPC method registration
├── workspace/                     # ← existing handlers/workspace/
│   ├── create.go
│   ├── checkout.go
│   ├── lifecycle.go
│   ├── fork.go
│   ├── relations.go
│   ├── info.go
│   ├── ready.go
│   ├── local.go
│   ├── files.go
│   ├── git.go
│   ├── exec.go
│   ├── vm.go
│   └── resource_policy.go
├── agentprofile/                  # ← existing
│   └── registry.go
├── buildinfo/                     # ← existing
│   └── buildinfo.go
├── compose/                        # ← existing
│   └── discovery.go
├── config/                         # ← existing (daemon config, node config, workspace config)
│   ├── loader.go
│   ├── node.go
│   └── workspace.go
├── credsbundle/                    # ← existing
│   └── bundle.go
├── daemonclient/                   # ← existing (client-side daemon interaction)
│   ├── autostart.go
│   ├── tokens.go
│   └── secretstore.go
├── lifecycle/                      # ← existing
│   └── manager.go
├── services/                       # ← existing (workspace-internal services)
│   └── manager.go
└── workspace/                      # ← existing pkg/workspace/ (domain, not handlers)
    └── workspace.go               # Low-level workspace FS ops, path sanitization
```

**Key changes vs current:**
- `infra/` groups shared infrastructure: store, relay, http
- `transport/` isolates the transport interface from `server/`
- `deps/` provides explicit DI instead of ad-hoc wiring in `server.go`
- Auth packages (`auth`, `authrelay`, `agentprofile`, `credsbundle`, `daemonclient`) could further group under `pkg/identity/` or remain flat — decision deferred

#### 2. Transport Abstraction

```go
// transport/transport.go
type Transport interface {
    Name() string
    Serve(l Registry, deps *deps.Deps) error
    Close() error
}

// Registry maps RPC method names to handlers.
// Each transport calls registry.Register(method, handlerFunc).
// The handler func receives (ctx context.Context, req json.RawMessage, conn any) -> (interface{}, error).
```

Current WebSocket transport moves from `server/server.go` into `transport/websocket.go`. A future stdio transport would implement the same `Transport` interface.

`server/server.go` becomes a thin loader:
```go
func (s *Server) newRPCRegistry() *rpc.Registry { /* as today */ }
func (s *Server) serveTransport(tr Transport) error { return tr.Serve(s.newRPCRegistry(), s.deps) }
```

#### 3. Handlers → Domain Packages

Handlers in `handlers/workspace/`, `handlers/daemon/`, `handlers/project/`, `handlers/credentials/` are already domain-grouped from Phase 4. They should move one level up — from `handlers/` into `pkg/` directly:

| Current | Proposed |
|---|---|
| `handlers/workspace/` | `workspace/` (handler impl stays with domain) |
| `handlers/daemon/` | `daemon/` (handler impl stays with domain) |
| `handlers/project/` | `project/` (handler impl stays with domain) |
| `handlers/credentials/` | `credentials/` (handler impl stays with domain) |
| `handlers/spotlight.go` | `spotlight/` (absorbs spotlight handler) |

This eliminates the `handlers/` directory entirely, placing handler implementations alongside the domain types they operate on.

#### 4. Terminology Fixes

- `spotlight.expose` → `spotlight.start` (expose is a vague verb; start is consistent with workspace lifecycle)
- Audit remaining RPC method names for consistency (e.g., `workspace.remove` vs `workspace.delete`, `service.command` vs `service.start`)

### Path B: Incremental (No Structural Change)

- Keep `pkg/` flat
- Only extract `transport/` interface within `server/`
- Rename `spotlight.expose` → `spotlight.start`
- Create `ARCHITECTURE.md` documenting current structure
- Defer nesting until next cleanup sprint

## Impact on Master Plan

### Affects existing Phase 4 deliverables:
- `pkg/handlers/` structure is already built — moving to `pkg/` root is a rename + import path update
- `rpc_handlers.go` call prefixes would change from `workspace.X` → `workspace.X` (same) and `handlers.X` → `spotlight.X`
- Test files in `handlers/workspace/`, etc., would move with their parent packages

### Deferred items from current PRD:
- Full test suite redesign (BDD-style) — not affected by this restructure
- Integration test harness redesign — not affected

### New items this PRD adds:
- `pkg/transport/` extraction
- `pkg/infra/` grouping
- `pkg/deps/` DI container
- `ARCHITECTURE.md` creation
- `spotlight.expose` → `spotlight.start` rename
- `handlers/` → `pkg/` migration

## Recommendation

**Proceed with Path A** — full redesign. The cost of moving handlers now (while imports are fresh) is much lower than deferring. The transport abstraction enables future stdio support without touching domain logic. The `deps/` container makes `server.go` readable and testable.

Path B preserves technical debt and defers the hardest part (moving handlers out of flat `handlers/`).

## Effort Estimate

| Task | Effort |
|---|---|
| Migrate handlers/workspace → pkg/workspace/ | Short (2–3h) |
| Migrate handlers/daemon → pkg/daemon/ | Quick (1h) |
| Migrate handlers/project → pkg/project/ | Quick (1h) |
| Migrate handlers/credentials → pkg/credentials/ | Quick (1h) |
| Move spotlight handler into pkg/spotlight/ | Short (2h) |
| Extract transport/ interface | Short (3–4h) |
| Create infra/ grouping | Medium (2–3h) |
| Create deps/ DI container | Medium (2–3h) |
| Rename spotlight.expose → spotlight.start | Quick (1h) |
| Update rpc_handlers.go imports | Short (2h) |
| Update all test imports | Short (2h) |
| Write ARCHITECTURE.md | Medium (3–4h) |
| Verify build + tests | Short (1h) |

**Total: ~20–26h (3–4 days)**

This is a significant effort. The user has already committed the Phase 4 win. This child PRD should be reviewed and approved before proceeding.
