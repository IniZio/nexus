# Workspace Project Config Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a single canonical `.nexus/workspace.json` config with `$schema`, and integrate it across workspace lifecycle, readiness, services, and spotlight defaults with backward compatibility.

**Architecture:** Introduce a daemon-side config loader/validator that reads `.nexus/workspace.json` from each workspace root and caches parsed results. Integrate config resolution with strict precedence (request params > workspace.json > built-ins) and keep one-file fast-break behavior (no legacy fallback path).

**Tech Stack:** Go (daemon + tests), TypeScript (SDK types/tests), JSON Schema, Jest, Go test.

---

### Task 1: Add workspace config schema and Go model types

**Files:**
- Create: `schemas/workspace.v1.schema.json`
- Create: `packages/workspace-daemon/pkg/config/types.go`
- Create: `packages/workspace-daemon/pkg/config/types_test.go`

**Step 1: Write the failing schema/type tests**

```go
func TestWorkspaceConfig_VersionRequired(t *testing.T) {
    var cfg WorkspaceConfig
    err := cfg.ValidateBasic()
    require.Error(t, err)
}
```

**Step 2: Run test to verify it fails**

Run: `cd packages/workspace-daemon && go test ./pkg/config -run WorkspaceConfig -v`
Expected: FAIL (package/types not implemented).

**Step 3: Add schema + type structs**

Include fields:
- `$schema`
- `version`
- `readiness.profiles`
- `services.defaults`
- `spotlight.defaults`
- `auth.defaults`
- `lifecycle.onSetup/onStart/onTeardown`

**Step 4: Add basic validators (`ValidateBasic`)**

Validate `version >= 1`, non-empty profile/check names, valid service defaults ranges.

**Step 5: Run tests**

Run: `cd packages/workspace-daemon && go test ./pkg/config -run WorkspaceConfig -v`
Expected: PASS.

**Step 6: Commit**

```bash
git add schemas/workspace.v1.schema.json packages/workspace-daemon/pkg/config
git commit -m "feat(config): add workspace.json schema and config types"
```

### Task 2: Implement daemon config loader with one-file behavior

**Files:**
- Create: `packages/workspace-daemon/pkg/config/loader.go`
- Create: `packages/workspace-daemon/pkg/config/loader_test.go`

**Step 1: Write failing loader tests**

Cover:
- loads `.nexus/workspace.json` when present
- returns defaults when missing
- ignores legacy split config path
- invalid workspace.json returns structured error

**Step 2: Run test to verify failure**

Run: `cd packages/workspace-daemon && go test ./pkg/config -run Loader -v`
Expected: FAIL.

**Step 3: Implement loader API**

```go
func LoadWorkspaceConfig(root string) (WorkspaceConfig, []string, error)
```

Where warnings include legacy deprecation notes.

**Step 4: Keep strict one-file behavior**

Do not add legacy migration mapping in loader path.

**Step 5: Run tests**

Run: `cd packages/workspace-daemon && go test ./pkg/config -run Loader -v`
Expected: PASS.

**Step 6: Commit**

```bash
git add packages/workspace-daemon/pkg/config
git commit -m "feat(config): add workspace config loader"
```

### Task 3: Integrate config into readiness profile resolution

**Files:**
- Modify: `packages/workspace-daemon/pkg/handlers/workspace_ready.go`
- Modify: `packages/workspace-daemon/pkg/handlers/workspace_ready_test.go`
- Modify: `packages/workspace-daemon/pkg/server/server.go`

**Step 1: Write failing tests for config-driven profile resolution**

Add test where `.nexus/workspace.json` defines custom profile and `workspace.ready` resolves it.

**Step 2: Run tests to verify failure**

Run: `cd packages/workspace-daemon && go test ./pkg/handlers -run WorkspaceReady -v`
Expected: FAIL (profile not loaded from config).

**Step 3: Implement config-backed profile lookup**

Priority:
1. explicit checks
2. profile from workspace.json
3. built-in profile map

**Step 4: Run tests**

Run: `cd packages/workspace-daemon && go test ./pkg/handlers -run WorkspaceReady -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add packages/workspace-daemon/pkg/handlers/workspace_ready.go packages/workspace-daemon/pkg/handlers/workspace_ready_test.go packages/workspace-daemon/pkg/server/server.go
git commit -m "feat(ready): resolve profiles from workspace.json"
```

### Task 4: Integrate service defaults from config

**Files:**
- Modify: `packages/workspace-daemon/pkg/handlers/service.go`
- Modify: `packages/workspace-daemon/pkg/services/manager.go`
- Modify: `packages/workspace-daemon/pkg/handlers/service_test.go`

**Step 1: Write failing service default tests**

Cover start/restart/stop with omitted options using config defaults.

**Step 2: Run tests to fail first**

Run: `cd packages/workspace-daemon && go test ./pkg/handlers -run ServiceCommand -v`
Expected: FAIL.

**Step 3: Merge defaults with request overrides**

Behavior:
- request values override config defaults
- config defaults override manager hardcoded defaults

**Step 4: Run tests**

Run: `cd packages/workspace-daemon && go test ./pkg/handlers -run ServiceCommand -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add packages/workspace-daemon/pkg/handlers/service.go packages/workspace-daemon/pkg/services/manager.go packages/workspace-daemon/pkg/handlers/service_test.go
git commit -m "feat(service): apply workspace.json service defaults"
```

### Task 5: Integrate auth defaults into workspace.create

**Files:**
- Modify: `packages/workspace-daemon/pkg/workspacemgr/types.go`
- Modify: `packages/workspace-daemon/pkg/handlers/workspace_manager.go`
- Modify: `packages/workspace-daemon/pkg/handlers/workspace_manager_test.go`

**Step 1: Write failing workspace.create default-merge tests**

Case: request omits policy fields but workspace.json provides auth defaults.

**Step 2: Run test to verify failure**

Run: `cd packages/workspace-daemon && go test ./pkg/handlers -run WorkspaceCreate -v`
Expected: FAIL.

**Step 3: Implement policy default merge**

Merge only missing request fields; never overwrite explicit request policy.

**Step 4: Run tests**

Run: `cd packages/workspace-daemon && go test ./pkg/handlers -run WorkspaceCreate -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add packages/workspace-daemon/pkg/workspacemgr/types.go packages/workspace-daemon/pkg/handlers/workspace_manager.go packages/workspace-daemon/pkg/handlers/workspace_manager_test.go
git commit -m "feat(workspace): apply auth defaults from workspace.json"
```

### Task 6: Add spotlight defaults helper action

**Files:**
- Modify: `packages/workspace-daemon/pkg/handlers/spotlight.go`
- Modify: `packages/workspace-daemon/pkg/handlers/spotlight_test.go`
- Modify: `packages/workspace-sdk/src/workspace-handle.ts`
- Modify: `packages/workspace-sdk/src/types.ts`
- Modify: `packages/workspace-sdk/src/__tests__/spotlight.test.ts`

**Step 1: Write failing tests for apply-defaults behavior**

Add daemon and SDK tests for applying spotlight defaults from workspace.json.

**Step 2: Run tests to fail**

Run:
- `cd packages/workspace-daemon && go test ./pkg/handlers -run Spotlight -v`
- `cd packages/workspace-sdk && pnpm exec jest src/__tests__/spotlight.test.ts --runInBand`

Expected: FAIL.

**Step 3: Implement helper action**

Add a narrow action:
- `spotlight.applyDefaults` (or `spotlight.exposeDefaults`)

Returns created mappings and per-mapping errors.

**Step 4: Run tests**

Run same commands as step 2.
Expected: PASS.

**Step 5: Commit**

```bash
git add packages/workspace-daemon/pkg/handlers/spotlight.go packages/workspace-daemon/pkg/handlers/spotlight_test.go packages/workspace-sdk/src/workspace-handle.ts packages/workspace-sdk/src/types.ts packages/workspace-sdk/src/__tests__/spotlight.test.ts
git commit -m "feat(spotlight): add workspace.json spotlight defaults helper"
```

### Task 7: Update lifecycle manager to require workspace.json lifecycle block

**Files:**
- Modify: `packages/workspace-daemon/pkg/lifecycle/manager.go`
- Modify: `packages/workspace-daemon/pkg/lifecycle/manager_test.go`

**Step 1: Write failing lifecycle source-priority tests**

Cover:
- uses `workspace.json` lifecycle when present
- no fallback to split lifecycle file

**Step 2: Run tests to fail**

Run: `cd packages/workspace-daemon && go test ./pkg/lifecycle -v`
Expected: FAIL.

**Step 3: Implement strict source behavior**

Use loader output only; no legacy fallback assumptions.

**Step 4: Run tests**

Run: `cd packages/workspace-daemon && go test ./pkg/lifecycle -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add packages/workspace-daemon/pkg/lifecycle/manager.go packages/workspace-daemon/pkg/lifecycle/manager_test.go
git commit -m "feat(lifecycle): require workspace.json lifecycle"
```

### Task 8: Documentation and examples cleanup

**Files:**
- Modify: `docs/reference/workspace-daemon.md`
- Modify: `docs/reference/workspace-sdk.md`
- Create: `docs/reference/workspace-config.md`

**Step 1: Add canonical config docs with schema URL**

Include examples using generic `<internal-repo-url>` placeholders and no project-specific names.

**Step 2: Document precedence + strict one-file behavior**

Explicitly describe request > workspace.json > built-ins and no split-config fallback path.

**Step 3: Add readiness profile + spotlight defaults examples**

Show both explicit checks and `readyProfile` flows.

**Step 4: Run markdown lint/check if available**

Run: `grep -RIn "hanlun" docs || true`
Expected: no external project mentions in active docs.

**Step 5: Commit**

```bash
git add docs/reference/workspace-daemon.md docs/reference/workspace-sdk.md docs/reference/workspace-config.md
git commit -m "docs: add workspace.json canonical config reference"
```

### Task 9: Final verification gate

**Files:**
- Modify: `docs/plans/2026-03-30-workspace-project-config-design.md` (if needed)

**Step 1: Run daemon tests**

Run: `cd packages/workspace-daemon && go test ./...`
Expected: PASS.

**Step 2: Run SDK checks/tests**

Run:
- `cd packages/workspace-sdk && pnpm exec tsc --noEmit`
- `cd packages/workspace-sdk && pnpm exec jest --runInBand`

Expected: PASS.

**Step 3: Run workspace battle dry-run**

Run: `bash e2e/workspace/internal-battle-test.sh --dry-run`
Expected: PASS.

**Step 4: Optional repo CI**

Run: `task ci`
Expected: PASS or document pre-existing environment blockers.

**Step 5: Final commit for any verification/docs adjustments**

```bash
git add -A
git commit -m "chore: finalize workspace.json integration verification"
```
