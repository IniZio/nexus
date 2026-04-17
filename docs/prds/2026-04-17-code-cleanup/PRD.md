---
type: master
feature_area: code-cleanup
date: 2026-04-17
status: active
child_prds: []
---

# Code Cleanup

## Overview

This PRD defines a systematic cleanup of the Nexus codebase to achieve a focused, unified architecture aligned with the firecracker-only runtime simplification. The goal is hyper-focused core flow code — nginx/redis-style organized, no compromises, well-structured patterns with no dead paths or unused features.

## Scope of Cleanup

The cleanup proceeds in five phases:

### Phase 1 — Runtime Driver Cleanup
Remove all dead runtime driver code that is unreachable after firecracker-only simplification.

**Status: ✅ COMPLETE**

- `packages/nexus/pkg/runtime/seatbelt/` — seatbelt driver package — **DELETED** (was already removed in prior simplification)
- `packages/nexus/pkg/runtime/selection/service.go` + `service_test.go` — **DELETED**
- `packages/nexus/pkg/runtime/drivers/shared/` — shared helpers for lima/seatbelt — **DELETED**
- `packages/nexus/pkg/runtime/lima/` — entire lima driver package — **DELETED** (prior simplification)
- Lima templates in `cmd/nexus/templates/lima/` — **DELETED**
- `packages/nexus/pkg/localws/manager.go` + `manager_test.go` — **DELETED**
- `packages/nexus/pkg/runtime/factory.go` — now registers only `firecracker` and `process`
- `packages/nexus/pkg/runtime/factory_test.go` — test renamed from Lima to generic backend

### Phase 2 — Config/Option Cleanup
Remove pool mode and VM mode options that are no longer used.

**Status: ✅ COMPLETE**

- `config/types.go`: Removed `WorkspaceVMSettings.Mode` field and `isolation.vm.mode` validation
- `workspace_manager.go`: Removed `vmModeForRepo()` function and `options["vm.mode"] = "dedicated"` assignment (mode is always dedicated)
- `workspace_manager.go:runtimeLabel()`: Removed `vm.mode=dedicated` from firecracker label
- `config/loader_test.go`: Removed `TestLoader_LoadsVMMode` and `TestLoader_InvalidVMMode_ReturnsError`
- `workspace_vm_mode_test.go`: Deleted — was testing the now-deleted `vm.mode` option injection
- `packages/nexus/test/integration/harness.go`: Removed `pool` from `Mode` comment, deduplicated firecracker entries to one `dedicated` entry
- `packages/nexus/test/integration/driver_test.go`: Renamed `TestPoolCoexistence` → `TestProcessCoexistence`, filter now only `process` mode
- `NexusAppTests.swift`: Updated test fixture `vm.mode=pool` → `vm.mode=dedicated`

**Audit findings:**
- `firecracker/driver.go` does **not** differentiate pool vs dedicated behavior
- `vm.mode` is **not** read anywhere in `runtime/firecracker/`
- `isolation.vm.mode` was used only for config validation and `vmModeForRepo()` — both now deleted

### Phase 3 — Auth Provider Cleanup
Remove OIDC/SAML auth provider references that are not actively used.

**Status: ✅ COMPLETE**

**Audit findings:**
- No OIDC flow implemented anywhere in daemon or SDK
- No CLI command or RPC method activates OIDC
- No OIDC tokens stored or processed — `RefreshToken`/`Expiry` fields exist but are unused
- No tests exercise OIDC paths

**Changes made:**
- `pkg/auth/provider.go`: Updated `ProviderType()` doc from `"local","oidc","saml"` to `"local"`, `ProviderName()` doc from `"local","oidc:authgear",etc.` to `"local"`
- `pkg/auth/identity.go`: Updated `AuthProvider` json tag comment from `"local","oidc","saml"` to `always "local"`
- `pkg/config/daemon.go`: Removed `"Future: pool mode"` and `"In future pool mode, this configures OIDC/SAML"` comments
- `pkg/daemonclient/tokens.go`: Removed `"For future OIDC"` comment and restructured TokenSet doc
- `pkg/daemonclient/secretstore.go`: Removed `"This is separate from TokenStore (OIDC tokens per endpoint)"` comment
- `ROADMAP.md`: Changed "Multi-User Architecture (Pool Mode, OIDC, Federation)" from `planned` to `deferred`

### Phase 4 — Large File Decomposition
Reduce file size violations that hinder readability and maintainability.

**Status: ⏳ PENDING**

| File | Lines | Limit | Status |
|---|---|---|---|
| `pkg/workspacemgr/manager.go` | 1268 | ≤400 | severely over |
| `pkg/handlers/workspace_manager.go` | 1225 | ≤400 | severely over |
| `pkg/handlers/workspace_manager_test.go` | ? | ≤500 | unknown |

**Decomposition strategy:**
- `workspacemgr/manager.go` → split by responsibility (workspace entity + orchestration + sub-packages)
- `handlers/workspace_manager.go` → split by method group (create handlers, list handlers, fork handlers, etc.)

### Phase 5 — Wildcard/Dead Code Scan
Broad scan for code that appears dead — unused functions, unreachable branches, commented-out logic.

**Status: ⏳ PENDING**

**Approach:**
1. Go compiler + static analysis: unused functions, unreachable code
2. Grep for TODO/FIXME/deprecated comments that are resolved
3. Check for config fields that are written but never read
4. Check for RPC fields that are never set or checked
5. Check for persisted state (store schema) fields that are written but never read

## Cleanup Principles

### Principle 1 — Three-tier classification

For each candidate cleanup item, classify as:

| Class | Definition | Action |
|---|---|---|
| **Trivial dead** | No entry point, no test path, no persisted state | Delete immediately |
| **Compatibility dead** | May be reached via config/RPC/persistence that external clients could send | Deprecate with error, delete in follow-up |
| **Uncertain** | Cannot determine usage without deeper investigation | Defer to audit |

### Principle 2 — Disable before delete

Where deletion risk is uncertain, prefer:
1. Make the feature emit a clear error ("pool mode not supported")
2. Add a comment marking it as deprecated
3. Delete in a follow-up after proving no breakage

### Principle 3 — No commented code in final state

All commented-out code should either be restored (if still needed) or deleted. The codebase should not ship with commented blocks of old implementations.

## Known Cleanup Candidates (by file)

### ✅ Trivially dead — deleted directly
- `pkg/runtime/seatbelt/` — entire seatbelt driver package — **DELETED**
- `pkg/runtime/selection/service.go` + `service_test.go` — **DELETED**
- `pkg/runtime/drivers/shared/` — shared helpers for lima/seatbelt — **DELETED**
- `pkg/runtime/lima/` — entire lima driver package — **DELETED**
- `cmd/nexus/templates/lima/` — Lima templates — **DELETED**
- `pkg/localws/manager.go` + `manager_test.go` — **DELETED**
- `workspace_vm_mode_test.go` — **DELETED**

### ✅ Phase 5 — Dead code scan cleanup (committed)
- `docs/guides/testing.md` — removed `{Backend: "firecracker", Mode: "pool"}` example from AllDrivers
- `packages/nexus/pkg/runtime/factory_test.go` — test `TestSelectDriverRejectsUnknownBackend` retained (tests unknown backend rejection), no longer references Lima in test name
- `packages/nexus/pkg/server/pty/handler.go:356` — removed `|| requestedBackend == "lima"` branch (lima unreachable since driver deleted)
- `ROADMAP.md` — OIDC/Federation updated to `deferred`

### Compatibility dead — deprecate with error
- `config/daemon.go` — future comments cleaned (Phase 3 done) — **DONE**
- `ROADMAP.md` — OIDC/Federation deferred — **DONE**

### Uncertain — deferred (Phase 4 decomposition context)
- `pkg/workspacemgr/manager.go` — 1268 lines — multi-tenant placeholders (`OwnerUserID`, `TenantID`, `CreatedBy`) confirmed "Compatibility dead" — populated but never read — candidate for removal during decomposition
- `pkg/handlers/workspace_manager.go` — 1225 lines — `HandleWorkspaceStop` superseded by `HandleWorkspaceStopWithRuntime` — dead after decomposition
- `pkg/auth/identity.go` — `Claims`, `TokenExpiry`, `OrgName` fields are "Compatibility dead" (future multi-tenancy placeholders, never read)
- `packages/nexus/pkg/config/types.go:30` — `DoctorConfig.RequiredHostPorts` — "Trivial dead" (in JSON but never read in doctor logic)
- `packages/nexus/pkg/config/node.go:17` — `NodeConfig.Schema` — "Trivial dead" (written for IDE, never read)
- `pkg/update/manager.go:114` — `Rollback()` — stub with no active workflow
- `pkg/workspacemgr/markers.go:17` — `HostWorkspaceMarkerPath()` — only used internally, should be unexported

### Large files — refactor target
- `pkg/workspacemgr/manager.go` — 1268 lines — **PENDING**
- `pkg/handlers/workspace_manager.go` — 1225 lines — **PENDING**

## Steer Log

### 2026-04-17 — PRD created

- **Trigger**: User requested thorough codebase cleanup focusing on core flow. Advisor recommended separate PRD for pool mode and OIDC audit. User compared target organization to nginx/redis.
- **From**: Unstructured codebase with Lima/seatbelt remnants, pool mode options, OIDC future comments, oversized files.
- **To**: Clean, focused codebase with clear deletion/deprecation decisions per item.
- **Rationale**: Post-firecracker-only cleanup requires systematic approach — cannot just delete Lima references blindly without understanding pool mode and OIDC usage.
- **Affected sections**: All above.

### 2026-04-17 — Phases 1 & 2 complete

- **Trigger**: User reviewed progress and said "since the mode is always dedicated, its literally useless, just delete them"
- **From**: Phase 2 partially done (pool config validation updated, `vmModeForRepo()` existed but unused). VM mode field still in `config/types.go`, `runtimeLabel()` still emitting `vm.mode=dedicated`
- **To**: VM mode field deleted from `WorkspaceVMSettings`, validation removed, `vmModeForRepo()` deleted, `options["vm.mode"]` assignment deleted, `runtimeLabel()` no longer emits `vm.mode`, `workspace_vm_mode_test.go` deleted, test fixtures updated to `dedicated`, integration harness deduplicated
- **Commits**: `875d926` (docs/flow), `9a477e9` (Lima/seatbelt removal), `a3071b3` (pool mode removal), latest commit (vm mode field removal)
- **Rationale**: `firecracker/driver.go` confirms no pool/dedicated differentiation exists. Mode field is purely vestigial — always `"dedicated"` with no behavioral effect. Safe to delete since no external config readers depend on it.
- **Remaining**: Phase 3 (OIDC audit), Phase 4 (large file decomposition), Phase 5 (wildcard scan)

### 2026-04-17 — Phase 3 complete

- **Trigger**: OIDC/SAML comments identified during Phase 5 dead code scan.
- **From**: `provider.go`, `identity.go`, `config/daemon.go`, `tokens.go`, `secretstore.go` all had future/OIDC comments; ROADMAP listed OIDC as planned.
- **To**: All OIDC/SAML comments removed, auth provider types restricted to local-only, ROADMAP updated to deferred.
- **Commits**: `7a79acf7` (OIDC/SAML removal)
- **Rationale**: Repo-wide scan confirmed no OIDC flows implemented, no CLI/RPC activates OIDC, no tokens stored/processed, no tests exercise OIDC paths.

### 2026-04-17 — Phase 5 complete (dead code scan)

- **Trigger**: User said dead code scan should precede Phase 4 decomposition.
- **From**: `testing.md` had pool mode example, `pty/handler.go` had unreachable Lima branch, `factory_test.go` was already renamed.
- **To**: `testing.md` pool example removed, `pty/handler.go` Lima branch removed, committed as `fix: remove remaining Lima/pool dead code references`.
- **Scan findings** (Phase 5 items deferred to Phase 4):
  - Multi-tenant placeholders (`OwnerUserID`, `TenantID`, `CreatedBy`) — Compatibility dead — populated but never read
  - `HandleWorkspaceStop` — superseded by runtime-aware version — dead
  - `Identity.Claims`, `TokenExpiry`, `OrgName` — Compatibility dead (future multi-tenancy placeholders)
  - `DoctorConfig.RequiredHostPorts` — Trivial dead (never read)
  - `NodeConfig.Schema` — Trivial dead (written for IDE, never read)
  - `Rollback()` — stub with no active workflow
  - `HostWorkspaceMarkerPath()` — should be unexported
- **Rationale**: Trivial/micro cleanups deferred to Phase 4 decomposition to avoid churn before large file work.