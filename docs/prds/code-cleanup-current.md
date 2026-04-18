---
type: consolidated
feature_area: code-cleanup
date: 2026-04-17
status: active
sources:
  - docs/prds/2026-04-17-code-cleanup/PRD.md
  - docs/prds/2026-04-17-code-cleanup/2026-04-17-workspace-manager-redesign/PRD.md
child_prds:
  - docs/prds/code-cleanup-current/2026-04-18-architecture-redesign/PRD.md
---

# Code Cleanup — Current State

## Overview

Nexus codebase cleanup targeting dead runtime driver code, unused config options, stale auth provider references, and oversized files that hinder readability. The codebase is firecracker-only; Lima and seatbelt runtimes have been removed.

## Scope

### Phase 1 — Runtime Driver Cleanup

The only supported VM backend is **Firecracker**. The `process` sandbox driver provides process-isolation fallback for environments where VMs are unavailable.

Deleted components:
- `packages/nexus/pkg/runtime/seatbelt/` — seatbelt driver package
- `packages/nexus/pkg/runtime/selection/service.go` + `service_test.go` — backend selection service
- `packages/nexus/pkg/runtime/drivers/shared/` — shared helpers for lima/seatbelt
- `packages/nexus/pkg/runtime/lima/` — entire Lima driver package
- `cmd/nexus/templates/lima/` — Lima VM templates
- `packages/nexus/pkg/localws/manager.go` + `manager_test.go` — Lima-based local workspace management

`packages/nexus/pkg/runtime/factory.go` registers only `firecracker` and `process` drivers.

### Phase 2 — Config/Option Cleanup

The VM mode is always `dedicated`. The `vm.mode` config field and validation have been removed.

- `config/types.go`: `WorkspaceVMSettings.Mode` field and `isolation.vm.mode` validation removed
- `workspace_manager.go`: `vmModeForRepo()` function removed; `options["vm.mode"] = "dedicated"` assignment removed
- `runtimeLabel()`: No longer emits `vm.mode=dedicated`
- `config/loader_test.go`: VM mode tests removed
- `NexusAppTests.swift`: Test fixtures updated (no `vm.mode` reference)
- Integration harness: `dedicated` mode deduplicated; pool mode entry removed

### Phase 3 — Auth Provider Cleanup

The auth system is local-only. No OIDC or SAML flows are implemented.

- `pkg/auth/provider.go`: `ProviderType()` and `ProviderName()` document local-only operation
- `pkg/auth/identity.go`: `AuthProvider` json tag documents `always "local"`
- `pkg/config/daemon.go`: Future pool/OIDC/SAML comments removed
- `pkg/daemonclient/tokens.go`: Future OIDC comments removed
- `pkg/daemonclient/secretstore.go`: OIDC separation comment removed
- `ROADMAP.md`: Multi-User Architecture (Pool Mode, OIDC, Federation) marked `deferred`

### Phase 4 — Domain-Based Restructuring

The focus has explicitly shifted from LOC-based splitting to proper architectural design. Restructuring `handlers/` into logical domain-based sub-directories, renaming vague file names, and removing unused SDK surfaces.

#### Scope of changes

**File renames:**

| Old file | Problem | New location/name |
|---|---|---|
| `handlers/fs.go` | Generic "fs" — these are workspace file ops | `handlers/workspace/files.go` |
| `handlers/auth_relay.go` | "Auth relay" sounds like Nexus auth; actually injects creds into workspace execs | `handlers/credentials/inject.go` |
| `handlers/runtime_helpers.go` | "runtime" is vague; Firecracker-specific VM lifecycle helpers | `handlers/workspace/vm.go` |

**Files kept as-is (not renamed):**
- `handlers/spotlight.go` — "spotlight" maps to recognized user-facing port forwarding concept

**Removed:**
- `handlers/os_picker.go` — Swift app uses native `NSOpenPanel`; daemon RPC was unused

**Subdirectory structure for `handlers/`:**
```
handlers/
├── workspace/
│   ├── create.go           # HandleWorkspaceCreate
│   ├── checkout.go         # HandleWorkspaceCheckout + PTY tunnel
│   ├── lifecycle.go        # list/open/remove/stop/start/restore
│   ├── fork.go             # HandleWorkspaceFork
│   ├── relations.go        # HandleWorkspaceRelations
│   ├── info.go             # HandleWorkspaceInfo
│   ├── ready.go            # HandleWorkspaceReady
│   ├── local.go            # HandleWorkspaceSetLocalWorktree
│   ├── files.go            # HandleReadFile/WriteFile/Stat/etc. (was fs.go)
│   ├── git.go              # HandleGitCommand
│   ├── vm.go               # Firecracker VM lifecycle helpers (was runtime_helpers.go)
│   └── resource_policy.go  # sandbox CPU/mem limits
├── project/
│   └── manager.go
├── daemon/
│   ├── settings.go          # sandbox resource limits (CPU/mem)
│   ├── node.go             # node info + capabilities
│   └── service.go          # workspace service lifecycle (start/stop/restart/logs)
├── credentials/
│   └── inject.go           # token mint/revoke for injecting creds into workspace execs (was auth_relay.go)
└── spotlight.go            # port forwarding management (kept; user-facing concept)
```

**Drop SDK/JS:** `packages/sdk/js/` formally removed. References removed from AGENTS.md, CONTRIBUTING.md, Taskfile.yml, tsconfig.json, scripts/check-file-sizes.sh, docs/guides/testing.md, docs/prds/2026-04-17-dev-workflow-overhaul/PRD.md, docs/reference/sdk.md (deleted).

**Drop nexus-ui:** `packages/nexus-ui/` removed. References removed from Taskfile.yml, pnpm-workspace.yaml, scripts/ci/nexus-core.sh, CONTRIBUTING.md, docs/prds/2026-04-17-dev-workflow-overhaul/PRD.md, docs/reference/project-structure.md.

**Drop e2e/flows:** `packages/e2e/` removed. Full test suite redesign to BDD-style integration tests TBD (see PRD update).

**Roadmap consolidation:** `ROADMAP.md` and `docs/roadmap.md` deleted. Roadmap content to be consolidated in a future session.

**ApplyComposePorts RPC:** Coordinated removal across server RPC registration, handlers/spotlight.go (handler + types), e2e test (spotlight-compose.e2e.test.ts), SDK schema. Port discovery is internal-only and handled automatically on workspace start — no user-facing RPC required.

**Design rules:**
- Host-filesystem validation and path policy belong in `workspacemgr` (domain), not `handlers` (transport)
- `Manager.Create` accepts a `CreateSpec`; no separate `CreatorInfo` struct needed (identity/auth removed from workspace structs)
- `deriveProjectRepoID` is consolidated into `workspacemgr/git.go` as `DeriveRepoID`
- `spotlight` package name is retained as it maps to a recognized user-facing concept (port forwarding)

#### `workspacemgr` Package

| File | Lines | Role |
|---|---|---|
| `manager.go` | 341 | `Manager` struct, `NewManager`, storage persistence, repository accessors, `Create` |
| `git.go` | 313 | Git/worktree helpers (`setupLocalWorkspaceCheckout`, `copyDirtyStateFromParent`, `runGit`, etc.) |
| `operations.go` | 533 | Mutating workspace methods (`Remove`, `Stop`, `Restore`, `Start`, `Checkout`, `Fork`, etc.) |
| `paths.go` | 152 | Host workspace path helpers (`resolveHostWorkspaceRoot`, `CanonicalExistingDir`, `InferredWorktreePath`, `CanonicalWorkspaceCandidate`, etc.) |
| `types.go` | ~76 | `Workspace`, `CreateSpec`, `WorkspaceState`, `Policy`, `RemoveOptions` |

#### `handlers` Package

| File | Lines | Role |
|---|---|---|
| `workspace_manager.go` | 308 | DTO structs, `HandleWorkspaceCheckout` + checkout helpers |
| `workspace_create.go` | 315 | `HandleWorkspaceCreate` + spec resolution + `createSourceHint` |
| `workspace_lifecycle.go` | 190 | `HandleWorkspaceList`, `HandleWorkspaceOpen`, `HandleWorkspaceRemove`, `HandleWorkspaceStop`, `HandleWorkspaceStopWithRuntime`, `HandleWorkspaceStart`, `HandleWorkspaceRestore` |
| `workspace_fork.go` | 99 | `HandleWorkspaceFork` + `resolveProjectRootForkSource` |
| `runtime_helpers.go` | 291 | Runtime driver helpers (`selectDriverForWorkspaceBackend`, `ensureLocalRuntimeWorkspace`, `suspendRuntimeWorkspace`, `resumeRuntimeWorkspace`, checkpoint helpers, `enrichWorkspaceRuntimeLabel`, etc.) |

**Accepted deviation**: DTO types remain inline in `workspace_manager.go` rather than a separate `workspace_types.go` file.

#### Derived Design Rules

- Host-filesystem validation and path policy belong in `workspacemgr` (domain), not `handlers` (transport)
- PTY reverse tunnel logic stays inline in `HandleWorkspaceCheckout` (checkout-specific orchestration)
- `Manager.Create` accepts a `CreateSpec`; no separate `CreatorInfo` struct needed (identity/auth removed from workspace structs)
- `deriveProjectRepoID` is consolidated into `workspacemgr/git.go` as `DeriveRepoID`

### Phase 5 — Handlers Subpackage Mapping

| Current file | Proposed location | Notes |
|---|---|---|
| `workspace_manager.go` | `workspace/checkout.go` | DTOs + checkout handler |
| `workspace_create.go` | `workspace/create.go` | create flow |
| `workspace_lifecycle.go` | `workspace/lifecycle.go` | list/open/remove/stop/start/restore |
| `workspace_fork.go` | `workspace/fork.go` | fork handler |
| `workspace_relations.go` | `workspace/relations.go` | workspace relations |
| `workspace_info.go` | `workspace/info.go` | aggregated state + forwards |
| `workspace_ready.go` | `workspace/ready.go` | service readiness polling |
| `workspace_local.go` | `workspace/local.go` | local worktree setting |
| `workspace_resource_policy.go` | `workspace/resource_policy.go` | sandbox CPU/mem limits |
| `runtime_helpers.go` | `workspace/vm.go` | firecracker VM lifecycle helpers |
| `fs.go` | `workspace/files.go` | workspace file ops |
| `git.go` | `workspace/git.go` | git command proxy |
| `exec.go` | `workspace/exec.go` | workspace command execution |
| `spotlight.go` | `spotlight.go` (kept; user-facing concept) | port forwarding management |
| `auth_relay.go` | `credentials/inject.go` | token mint/revoke for workspace exec |
| `project_manager.go` | `project/manager.go` | project CRUD |
| `daemon_settings.go` | `daemon/settings.go` | sandbox resource limits |
| `node.go` | `daemon/node.go` | node info + capabilities |
| `service.go` | `daemon/service.go` | workspace service lifecycle |

### Phase 6 — Dead Code Scan

Cleanups from the dead code scan:
- `docs/guides/testing.md`: Pool mode example removed from `AllDrivers`
- `pty/handler.go`: Lima backend branch removed
- `factory_test.go`: Test renamed to generic backend rejection test

Remaining dead items (deferred):
- `HandleWorkspaceStop` — superseded by runtime-aware version; not actively deleted to preserve RPC compat
- `HostWorkspaceMarkerPath()` — exported but used only internally; marker path access still works correctly

## Architecture

### Package Map

```
pkg/
├── auth/                    — local auth token provider, identity
├── config/                  — workspace config loading, daemon config, loader, node config
├── daemonclient/            — token management, secret store
├── handlers/                — RPC handlers (transport layer)
│   ├── workspace/           — workspace lifecycle, create, fork, checkout, file ops, git, exec, VM, resource policy
│   │   ├── create.go        # HandleWorkspaceCreate
│   │   ├── checkout.go      # DTOs + HandleWorkspaceCheckout
│   │   ├── lifecycle.go     # list/open/remove/stop/start/restore
│   │   ├── fork.go         # HandleWorkspaceFork
│   │   ├── relations.go     # HandleWorkspaceRelations
│   │   ├── info.go         # HandleWorkspaceInfo
│   │   ├── ready.go         # HandleWorkspaceReady
│   │   ├── local.go         # HandleWorkspaceSetLocalWorktree
│   │   ├── files.go         # HandleReadFile/WriteFile/Stat/etc.
│   │   ├── git.go           # HandleGitCommand
│   │   ├── exec.go          # HandleExec
│   │   ├── vm.go            # Firecracker VM lifecycle helpers
│   │   └── resource_policy.go  # sandbox CPU/mem limits
│   ├── project/
│   │   └── manager.go       — project CRUD
│   ├── daemon/
│   │   ├── settings.go      — sandbox resource limits (CPU/mem)
│   │   ├── node.go          — node info + capabilities
│   │   └── service.go       — workspace service lifecycle (start/stop/restart/logs)
│   ├── credentials/
│   │   └── inject.go        — token mint/revoke for workspace exec auth
│   └── spotlight.go         — port forwarding management (user-facing concept)
├── project/                 — project management (renamed from projectmgr)
├── runtime/                 — firecracker + process drivers, factory
├── server/                  — daemon server, RPC registration
│   └── pty/handler.go       — PTY session lifecycle
├── spotlight/                — TCP port forwarding manager
├── authrelay/               — credential injection broker for workspace execs
├── services/                 — workspace service lifecycle manager
├── store/                   — sqlite workspace repository
├── workspacemgr/            — workspace lifecycle manager (domain layer)
│   ├── manager.go           — Manager + storage
│   ├── git.go               — git/worktree helpers
│   ├── operations.go        — mutating workspace operations
│   ├── paths.go             — host path resolution and validation
│   └── types.go            — workspace domain types
└── workspace/create/        — workspace creation preparation
```

### Remote-First Constraint

The daemon may run on a different machine than the user. Host path resolution and filesystem operations belong in the domain layer (`workspacemgr`), not the transport layer (`handlers`). Handlers manipulate workspace state via the `Manager` interface.

## Known Limitations

- `HandleWorkspaceStop` is present for RPC compatibility but delegates to `HandleWorkspaceStopWithRuntime` internally
- `HostWorkspaceMarkerPath()` remains exported due to cross-package usage across `handlers`, `workspacemgr`, `server`, and `pty`
