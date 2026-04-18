---
type: child
feature_area: code-cleanup
date: 2026-04-17
topic: workspace-manager-redesign
status: draft
parent_prd: 2026-04-17-code-cleanup
---

# Child PRD: Workspace Manager & Handlers Redesign

## Parent Context

Parent PRD: `2026-04-17-code-cleanup`
Affected section: Phase 4 — Large File Decomposition

## What Changed

Phase 4 decomposition attempted mechanical file splitting of `workspacemgr/manager.go` (1263 lines) and `handlers/workspace_manager.go` (1224 lines) but hit two walls:

1. **Advisor rejected mechanical split** — files would still exceed limits and helpers would be duplicated. Advisor insisted on "split by layer-owned behavior, not arbitrary chunks"
2. **User rejected shallow decomposition** — "construct PRD with well-defined design, don't hesitate to do full rewrite if needed, all should be well designed by advisor with several passes"

The real problem is not file size per se — it's that both files contain **multiple responsibility layers mixed together**:
- `manager.go`: workspace lifecycle + git/worktree operations + storage persistence + repository accessors
- `workspace_manager.go`: RPC DTOs + create flow + list/stop/start/restore/fork flows + PTY + runtime helpers + host path resolution

Simply cutting these at arbitrary line boundaries preserves the tangled dependencies and produces files that still violate size policy.

## Why This Cannot Wait

Continuing mechanical splits will produce poorly-designed output. The user's explicit instruction is to redesign properly.

## Proposed Resolution

**Approach: Responsibility-layered redesign with advisor review passes**

### Phase A — `workspacemgr` package redesign (target: ~400 lines total for manager.go)

**Current problems in `manager.go` (1263 lines):**
1. `Manager` struct + constructor (lines 26-54)
2. Storage/persistence helpers: `persistWorkspace`, `deleteRecord`, `loadAll` (lines 84-150)
3. Create flow: `Create` method (lines 159-273)
4. Read operations: `Get`, `List` (lines 275-298)
5. Mutating operations: `Remove`, `Stop`, `Restore`, `Start`, `Checkout`, `Fork`, `SetBackend`, `SetLineageSnapshot`, `SetLocalWorktree`, `SetTunnelPorts`, `UpdateProjectID`, `SetParentWorkspace`, `CopyDirtyStateFromWorkspace`, `SetCurrentCommit`, `SetDerivedFromRef`, `CanCheckout` (lines 300-655)
6. Repository accessors: `SpotlightRepository`, `ProjectRepository`, `SandboxResourceSettingsRepository` (lines 784-803)
7. Git/worktree operations: `resolveHostWorkspaceRoot`, `setupLocalWorkspaceCheckout`, `setupForkLocalWorkspaceCheckout`, `copyDirtyStateFromParent`, `copyUntrackedFiles`, `copyPath`, `cleanupLocalWorkspaceCheckout`, `runGit`, `runGitRaw`, `runGitWithInput`, `looksLikeGitRepo`, `localBranchExists`, `isDirEmpty`, `resolveHostWorkspacePath`, `normalizeLegacyWorkspacePath` (lines 881-1243)
8. Repo helpers: `deriveRepoKind`, `deriveRepoID`, `isLikelyLocalPath`, `isLikelyRemoteRepo`, `workspaceScopeKey`, `branchConflictWorkspaceID` (lines 849-966)
9. Clone helper: `cloneWorkspace` (lines 805-825)
10. Path normalization: `HostWorkspaceDirName` (lines 1245-1263)

**Proposed split:**

```
workspacemgr/
  types.go          — Workspace, CreateSpec, WorkspaceState, Policy, RemoveOptions (EXISTING, ~76L)
  manager.go        — Manager struct + NewManager + storage (persist/delete/loadAll) + repository accessors (~180L)
  workspace.go      — Create + Get/List + cloneWorkspace + normalizeWorkspaceRef + Get/Set wrappers (~200L)
  operations.go    — All mutating Set* methods + CanCheckout + branchConflictWorkspaceID + Remove/Stop/Restore/Start/Checkout/Fork (~300L)
  git.go            — All git/worktree ops + repo helpers + HostWorkspaceDirName + resolveHostWorkspaceRoot/Path + normalizeLegacyWorkspacePath (~350L)
```

**Advisory questions for Pass 1:**
- Is `copyDirtyStateFromParent` correctly in `git.go` or should it be in `operations.go`?
- Should `cloneWorkspace` live in `types.go` or `workspace.go`?
- The `Manager` struct itself — should it stay in `manager.go` or move to a dedicated `manager_init.go`?

### Phase B — `handlers` package redesign (target: ~400 lines total per file)

**Current problems in `workspace_manager.go` (1224 lines):**
1. All DTO types (lines 24-121) — 98 lines of struct definitions that should be separate
2. Create flow: `HandleWorkspaceCreate/WithProjects` + resolveCreateSpec + resolveCreateSourceHint + createSourceHint + shouldUseProjectRootPathForBase + shouldCopyDirtyStateForCreate + isVMIsolationBackend (lines 123-415)
3. List/Open handlers: `HandleWorkspaceList`, `HandleWorkspaceOpen` (lines 415-422)
4. Remove/Stop/Start/Restore handlers: `HandleWorkspaceRemove`, `HandleWorkspaceStop`, `HandleWorkspaceStopWithRuntime`, `HandleWorkspaceStart`, `HandleWorkspaceRestore` (lines 442-601)
5. Fork handler: `HandleWorkspaceFork` + resolveProjectRootForkSource + resolveProjectRootWorkspace (lines 601-717)
6. Checkout handler: `HandleWorkspaceCheckout` + all helpers including PTY allocation logic (lines 717-1224)

**Also** this file contains PTY and runtime helpers that belong in separate packages:
- `HandlePTYOpen`, `HandlePTYOpenWithWorkspace` — PTY sessions (handler, NOT in this file)
- `selectDriverForWorkspaceBackend` — runtime driver selection (helper, NOT in this file)
- `ensureLocalRuntimeWorkspace`, `suspendRuntimeWorkspace`, `resumeRuntimeWorkspace` — runtime lifecycle (helper, NOT in this file)
- `checkpointLatestFirecrackerSnapshotForCreate`, `checkpointBaselineLineageSnapshot`, `preferredLineageSnapshotForCreate` — snapshot helpers (helper, NOT in this file)
- `RuntimeLabelForBackend`, `RuntimeLabelWithConfig` — runtime label helpers (helper, NOT in this file)
- `resolveCheckoutSpec`, `checkoutGitBranch`, `checkoutRefOnHost`, `runGitAt`, `normalizeCheckoutConflictMode`, `checkoutConflictPromptError` — checkout helpers (helper, NOT in this file)
- `normalizeBranchForHint`, `hasExplicitPolicy` — misc helpers (utility, NOT in this file)
- `deriveProjectRepoID` — duplicate helper (utility, NOT in this file)
- `preferredProjectRootForRuntime`, `workspaceWorktreePathForBackend`, `canonicalExistingDir`, `canonicalWorkspaceCandidate`, `resolvePtyDirectory` — host path helpers (duplicated in pty/handler.go)
- `applySandboxResourcePolicy` — policy helper

**Proposed split:**

```
handlers/
  workspace_types.go          — All 14 DTO structs (params + results) (~100L)
  workspace_create.go         — HandleWorkspaceCreate + WithProjects + spec resolution (~200L)
  workspace_lifecycle.go      — HandleWorkspaceList/Open/Remove/Stop/StopWithRuntime/Start/Restore (~200L)
  workspace_fork.go           — HandleWorkspaceFork + project root resolution helpers (~150L)
  workspace_checkout.go       — HandleWorkspaceCheckout + checkout helpers (~250L)
  pty_handlers.go             — HandlePTYOpen + HandlePTYOpenWithWorkspace (NEW, ~150L)
  runtime_helpers.go          — selectDriverForWorkspaceBackend + ensure/suspend/resume + checkpoint helpers + enrichWorkspaceRuntimeLabel + runtimeLabelForWorkspace (~200L)
```

**Advisory questions for Pass 1:**
- Should `workspace_types.go` live in `handlers/` or should DTOs be moved to `workspacemgr/types.go`?
- The PTY handlers currently live in `server/pty/handler.go` — should they stay there or move to `handlers/`?
- `deriveProjectRepoID` is duplicated in both `project_manager.go` and `handlers/workspace_checkout.go` — consolidate or keep separate?
- `resolveHostWorkspaceRoot` logic is duplicated in both `workspacemgr/manager.go` and `handlers/runtime_helpers.go` — consolidate into `workspacemgr/git.go`?

### Phase C — Architectural constraints (remote-first)

**Constraint from AGENTS.md:** "The daemon may run on a different machine than the user. Design and verify under that assumption."

This means:
- Handlers should NOT contain daemon-host filesystem logic
- Host path resolution (`resolveHostWorkspaceRoot`, `resolveHostWorkspacePath`) belongs in `workspacemgr` or a transport layer
- Handlers should only manipulate workspace state via the `Manager` interface
- The `Manager` should not import `auth` (boundary: identity flows in from handlers, not stored in workspace)

**Advisory question for Pass 2:**
- Should `Manager.Create` take an identity parameter or should the handler layer extract identity and pass it as a `CreatorInfo` struct?

## Impact on Master Plan

Phase 4 in the master PRD (`2026-04-17-code-cleanup`) currently says "decompose manager.go and workspace_manager.go". This child PRD replaces that with a proper redesign.

## Recommendation

Proceed with Phase A + B + C as designed above, with two advisor review passes:
1. Pass 1: Review the proposed file boundaries and responsibility assignments
2. Pass 2: Review the final file contents before commit

After approval, implement Phase A first, verify build/test, then Phase B, verify build/test.

## Advisor Pass 1 Decisions

### `workspacemgr` Split — APPROVED
- `types.go` (EXISTING, ~76L): APPROVED
- `manager.go` (Manager struct + NewManager + storage + repo accessors, ~180L): APPROVED
- `workspace.go` (Create + Get/List + cloneWorkspace + normalizeWorkspaceRef, ~200L): APPROVED
- `operations.go` (All mutating Set* methods + CanCheckout + Remove/Stop/Restore/Start/Checkout/Fork, ~300L): APPROVED
- `git.go` (All git/worktree ops + repo helpers + HostWorkspaceDirName + resolveHostWorkspaceRoot/Path + normalizeLegacyWorkspacePath, ~350L): APPROVED

**Advisory Q1 (copyDirtyStateFromParent location):** → `git.go` (correct as proposed)
**Advisory Q2 (cloneWorkspace location):** → `types.go`
**Advisory Q3 (Manager struct):** → `manager.go` (no separate `manager_init.go` needed)

### `handlers` Split — PROCEED with corrections

**Correction:** Split `workspace_checkout.go` into two files:
- `workspace_checkout.go` (~200L): HandleWorkspaceCheckout + checkout helpers only (NO PTY or runtime helpers)
- `pty_handlers.go` (~150L): HandlePTYOpen + HandlePTYOpenWithWorkspace + isPTYAllocationSupported + getPtyBackend + resolvePtyDirectory (NEW file, already in `server/pty/handler.go` — do NOT duplicate, consolidate there)
- `runtime_helpers.go` (~200L): selectDriverForWorkspaceBackend + ensure/suspend/resume + checkpoint helpers + enrichWorkspaceRuntimeLabel + runtimeLabelForWorkspace — APPROVED

**Correction:** `resolveHostWorkspaceRoot` and host path helpers should be in `workspacemgr/git.go`, NOT in handlers. Remove from `runtime_helpers.go`.

**Advisory Q4 (DTOs in handlers vs workspacemgr):** → Keep in `handlers/workspace_types.go` (transport DTOs)
**Advisory Q5 (deriveProjectRepoID):** → Consolidate into `workspacemgr/git.go` (repo helper, not handler concern)
**Advisory Q6 (resolveHostWorkspaceRoot consolidation):** → YES — consolidate into `workspacemgr/git.go`
**Advisory Q7 (CreatorInfo vs identity param):** → `CreatorInfo` struct passed to `Manager.Create`

## Implementation Plan

### Phase A — `workspacemgr` package (implement first)

1. Verify `types.go` (existing, ~76L) — no changes needed
2. Create `git.go` — move all git/worktree/repo helpers from `manager.go`
3. Create `workspace.go` — move Create/Get/List/cloneWorkspace from `manager.go`
4. Create `operations.go` — move all mutating methods from `manager.go`
5. Rewrite `manager.go` — Manager struct + NewManager + storage + repo accessors (~180L)
6. Verify build + tests pass

### Phase B — `handlers` package (implement second)

1. Create `workspace_types.go` — all 14 DTO structs (~100L)
2. Create `workspace_create.go` — HandleWorkspaceCreate flow (~200L)
3. Create `workspace_lifecycle.go` — List/Open/Remove/Stop/Start/Restore handlers (~200L)
4. Create `workspace_fork.go` — HandleWorkspaceFork + project root helpers (~150L)
5. Create `workspace_checkout.go` — HandleWorkspaceCheckout + checkout helpers (~200L)
6. Move PTY handlers from `server/pty/handler.go` to `handlers/pty_handlers.go` (consolidate, don't duplicate)
7. Create `runtime_helpers.go` — runtime driver helpers (~200L, WITHOUT host path helpers)
8. Rewrite `workspace_manager.go` — import delegation stubs for backward compat during transition
9. Verify build + tests pass

## Advisor Pass 2 Decisions — Phase B Adjustment

### Key Corrections to Original Phase B Plan

**Correction 1 — PTY Reverse Tunnel in Checkout:** `HandleWorkspaceCheckout` has PTY reverse tunnel setup/teardown inline (lines 717-764). This is workspace-checkout-specific orchestration, not general PTY. Per advisor Pass 2, this should stay inline in `workspace_checkout.go` alongside checkout helpers. `server/pty/handler.go` (lines 1-355) already handles general PTY sessions (HandlePTYOpen/HandlePTYOpenWithWorkspace) — leave there.

**Correction 2 — Host Path Helpers Location:** `resolveHostWorkspaceRoot`, `resolveHostWorkspacePath`, `HostWorkspaceDirName` are currently in `workspacemgr/git.go` after Phase A. Per advisor, these belong in a dedicated `workspacemgr/paths.go` (not mixed with git helpers). Handlers access host paths via `workspacemgr` package, not directly.

**Correction 3 — config.NodeDBPath in Handler:** Handler should NOT call `config.NodeDBPath()`. Pass fallback log path as server-layer dependency.

### Phase B Final File Layout

```
handlers/
  workspace_types.go      — 14 canonical DTO structs (WorkspaceCreateParams, WorkspaceOpenParams, …, WorkspaceCheckoutResult) (~100L)
  workspace_create.go      — HandleWorkspaceCreate + HandleWorkspaceCreateWithProjects + spec resolution + createSourceHint (~200L)
  workspace_lifecycle.go   — HandleWorkspaceList + HandleWorkspaceOpen + HandleWorkspaceRemove + HandleWorkspaceStop + HandleWorkspaceStopWithRuntime + HandleWorkspaceStart + HandleWorkspaceRestore (~200L)
  workspace_fork.go        — HandleWorkspaceFork + resolveProjectRootForkSource + resolveProjectRootWorkspace (~150L)
  workspace_checkout.go    — HandleWorkspaceCheckout + normalizeCheckoutConflictMode + checkoutRefOnHost + runGitAt + checkoutConflictPromptError + parseChangedFiles (~250L)
  runtime_helpers.go       — selectDriverForWorkspaceBackend + normalizeWorkspaceBackend + ensureLocalRuntimeWorkspace + suspendRuntimeWorkspace + resumeRuntimeWorkspace + checkpointLatestFirecrackerSnapshotForCreate + checkpointBaselineLineageSnapshot + preferredLineageSnapshotForCreate + enrichWorkspaceRuntimeLabel + runtimeLabelForWorkspace + isVMIsolationBackend + shouldCopyDirtyStateForCreate + hasExplicitPolicy + preferredProjectRootForRuntime + inferredWorktreePath + canonicalExistingDir + canonicalWorkspaceCandidate (~300L)
  workspace_manager.go     — thin import-redirection stubs for backward compat (~50L)

workspacemgr/
  paths.go                 — resolveHostWorkspaceRoot + resolveHostWorkspacePath + HostWorkspaceDirName + normalizeLegacyWorkspacePath (MOVED from git.go) (~100L)
  git.go                  — git/worktree helpers only (UPDATED: paths.go functions removed) (~330L)
```

**Note:** `deriveProjectRepoID` and `normalizeBranchForHint` — these are `projectmgr` concerns (used in both `workspace_create.go` and `project_manager.go`). Per advisor Q5, consolidate into `workspacemgr/git.go` as canonical location.

### What does NOT change

- `server/pty/handler.go` — handles general PTY session lifecycle; stays in place
- `handlers/project_manager.go` — unchanged (118 lines)
- `handlers/workspace_ready.go`, `workspace_info.go`, `workspace_local.go`, `workspace_relations.go`, `workspace_resource_policy.go`, `workspace_resource_runtime.go` — all within size limits, unchanged

## Accepted Deviations from Original Plan

- **DTOs inline in workspace_manager.go**: Original plan proposed `handlers/workspace_types.go` as a separate file. After review, keeping DTO types inline in `workspace_manager.go` (308L total with checkout handler) was accepted — DTOs are transport types scoped to the handlers package and do not benefit from additional file separation at this scale.

## Steer Log

### 2026-04-17 — Child PRD created

- **Trigger**: Mechanical decomposition attempted; advisor rejected (still over limits, duplicated helpers); user explicitly said "don't hesitate to do full rewrite, well designed by advisor with several passes"
- **From**: Attempting to split `manager.go` and `workspace_manager.go` at arbitrary line boundaries
- **To**: Responsibility-layered redesign documented in this child PRD, with advisor review passes before implementation
- **Rationale**: The files mix multiple responsibility layers. Simply cutting them produces poorly-designed output that still violates size policy.
- **Affected sections**: Phase 4 of master PRD

### 2026-04-17 Phase A Complete → Phase B PRD Adjustment

- **Trigger**: Phase A committed (5759abfeb934cdbb2ebc176de7d2cd8759eae8af). advisor Pass 2 reviewed Phase B patterns.
- **From**: Original Phase B plan had PTY extraction to handlers/pty_handlers.go and host path helpers in runtime_helpers.go
- **To**: PTY reverse tunnel stays inline in workspace_checkout.go; host path helpers moved to workspacemgr/paths.go; config.NodeDBPath not called from handler
- **Rationale**: PTY reverse tunnel is checkout-specific orchestration; generic PTY session lifecycle stays in server/pty/handler.go. Host path helpers are workspacemgr domain, not handler domain. Handlers receive dependencies from server layer.
- **Affected sections**: Phase 4 — Phase B file layout

### 2026-04-17 Phase B Complete → Commit Pending

- **Trigger**: Phase B decomposition implemented. Advisor completion gate APPROVED with condition: document DTOs-inline exception.
- **Status**: APPROVED — all files ≤400 lines, single responsibility, 454 tests pass, full build success
- **From**: workspace_manager.go 1224L (all handlers + DTOs + helpers + checkout in one file)
- **To**:
  - workspace_manager.go 308L: DTOs + HandleWorkspaceCheckout + checkout helpers
  - workspace_create.go 315L: create flow + spec resolution
  - workspace_lifecycle.go 190L: list/open/remove/stop/start/restore
  - workspace_fork.go 99L: fork handler
  - runtime_helpers.go 341L: runtime driver helpers
  - workspacemgr/paths.go 94L: host path helpers (new)
  - workspacemgr/git.go 302L: git helpers (updated, paths removed)
  - workspacemgr/operations.go 533L: mutating methods (new)
  - workspacemgr/manager.go 341L: Manager + storage (rewritten)
- **Action**: Commit Phase B to branch fix/daemon-config-bundle-fallback

### 2026-04-17 Phase C Complete → Commit Pending

- **Trigger**: Phase C advisor decision: relocate three host-path helpers to workspacemgr/paths.go; skip CreatorInfo (superseded by Phase 4 auth removal)
- **Status**: IMPLEMENTED — build passes, 454 tests pass
- **Changes**:
  - `workspacemgr/paths.go`: +3 functions (CanonicalExistingDir, InferredWorktreePath, CanonicalWorkspaceCandidate) — all host-filesystem validation
  - `handlers/runtime_helpers.go`: -3 duplicate functions (replaced with calls to workspacemgr package)
- **Rationale**: Host-filesystem policy belongs in domain layer (workspacemgr), not handler layer
- **Action**: Commit Phase C to branch fix/daemon-config-bundle-fallback
