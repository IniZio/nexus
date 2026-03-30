# Remote Workspace Core Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Deliver a remote-only workspace core with minimal API, explicit lifecycle, credential policies, and Spotlight port forwarding that feels local for developer workflows.

**Architecture:** Keep client thin and move orchestration into `workspace-daemon` as a workspace manager with explicit lifecycle states. Expose only a small typed SDK surface (`workspace.*`, `handle.exec/fs/git/service/spotlight/info`) while keeping transport details private. Validate behavior by repeatedly provisioning and exercising isolated remote workspaces against `internal-repo`.

**Tech Stack:** TypeScript (SDK + tests via Jest), Go (daemon + handlers + tests), WebSocket JSON-RPC, Git, Docker runtime backend, Taskfile CI tasks.

---

### Task 1: Add remote workspace lifecycle contract in daemon

**Files:**
- Create: `packages/workspace-daemon/pkg/workspacemgr/types.go`
- Create: `packages/workspace-daemon/pkg/workspacemgr/manager.go`
- Create: `packages/workspace-daemon/pkg/workspacemgr/manager_test.go`
- Modify: `packages/workspace-daemon/pkg/server/server.go`
- Test: `packages/workspace-daemon/pkg/workspacemgr/manager_test.go`

**Step 1: Write the failing lifecycle state tests**

```go
func TestManager_CreateWorkspace_InitialState(t *testing.T) {
    m := newTestManager(t)
    ws, err := m.Create(context.Background(), CreateSpec{Repo: "git@example/repo.git", Ref: "main", WorkspaceName: "alpha", AgentProfile: "default"})
    require.NoError(t, err)
    require.Equal(t, StateSetup, ws.State)
}
```

**Step 2: Run test to verify it fails**

Run: `cd packages/workspace-daemon && go test ./pkg/workspacemgr -run TestManager_CreateWorkspace_InitialState -v`
Expected: FAIL with missing package/types/manager implementation.

**Step 3: Implement minimal manager/types**

```go
type WorkspaceState string

const (
    StateSetup WorkspaceState = "setup"
    StateStart WorkspaceState = "start"
    StateReady WorkspaceState = "ready"
    StateActive WorkspaceState = "active"
    StateTeardown WorkspaceState = "teardown"
)
```

**Step 4: Wire server to hold manager instance**

Add manager field and instantiate in `NewServer`, keeping old workspace path behavior for compatibility.

**Step 5: Run tests to verify pass**

Run: `cd packages/workspace-daemon && go test ./pkg/workspacemgr -v`
Expected: PASS.

**Step 6: Commit**

```bash
git add packages/workspace-daemon/pkg/workspacemgr packages/workspace-daemon/pkg/server/server.go
git commit -m "feat(workspace-daemon): add remote workspace lifecycle manager"
```

### Task 2: Add create/open/list/remove RPC methods

**Files:**
- Create: `packages/workspace-daemon/pkg/handlers/workspace_manager.go`
- Create: `packages/workspace-daemon/pkg/handlers/workspace_manager_test.go`
- Modify: `packages/workspace-daemon/pkg/server/server.go`
- Modify: `packages/workspace-daemon/pkg/rpcerrors/errors.go`
- Test: `packages/workspace-daemon/pkg/handlers/workspace_manager_test.go`

**Step 1: Write failing RPC handler tests for create/open/list/remove**

```go
func TestHandleWorkspaceCreate(t *testing.T) {
    result, rpcErr := HandleWorkspaceCreate(ctx, params, mgr)
    require.Nil(t, rpcErr)
    require.NotEmpty(t, result.ID)
}
```

**Step 2: Run targeted tests to confirm failure**

Run: `cd packages/workspace-daemon && go test ./pkg/handlers -run Workspace -v`
Expected: FAIL with undefined handlers/method routing.

**Step 3: Implement handlers and JSON params structs**

Add methods:
- `workspace.create`
- `workspace.open`
- `workspace.list`
- `workspace.remove`

**Step 4: Route methods in `processRPC` switch**

Return JSON-RPC method-not-found for unknown methods, keep existing methods untouched.

**Step 5: Run tests**

Run: `cd packages/workspace-daemon && go test ./pkg/handlers -run Workspace -v`
Expected: PASS.

**Step 6: Commit**

```bash
git add packages/workspace-daemon/pkg/handlers packages/workspace-daemon/pkg/server/server.go packages/workspace-daemon/pkg/rpcerrors/errors.go
git commit -m "feat(workspace-daemon): add workspace lifecycle rpc methods"
```

### Task 3: Add credential policy model and info surface

**Files:**
- Create: `packages/workspace-daemon/pkg/workspacemgr/policy.go`
- Modify: `packages/workspace-daemon/pkg/workspacemgr/types.go`
- Modify: `packages/workspace-daemon/pkg/handlers/workspace_manager.go`
- Modify: `packages/workspace-daemon/pkg/server/server.go`
- Create: `packages/workspace-daemon/pkg/workspacemgr/policy_test.go`
- Test: `packages/workspace-daemon/pkg/workspacemgr/policy_test.go`

**Step 1: Write failing tests for policy validation**

```go
func TestValidatePolicy_RejectsUnknownCredentialMode(t *testing.T) {
    err := ValidatePolicy(Policy{GitCredentialMode: "invalid"})
    require.Error(t, err)
}
```

**Step 2: Run test to fail first**

Run: `cd packages/workspace-daemon && go test ./pkg/workspacemgr -run Policy -v`
Expected: FAIL with missing policy validator.

**Step 3: Implement policy types + validator**

Accepted values:
- `gitCredentialMode`: `host-helper | ephemeral-helper | none`
- `sshAgentForward`: bool
- `authProfiles`: `claude-auth | codex-auth | gitconfig`

**Step 4: Include effective policy in `workspace.info`**

Return policy and active spotlight mappings from a single info endpoint.

**Step 5: Run tests**

Run: `cd packages/workspace-daemon && go test ./pkg/workspacemgr -run Policy -v`
Expected: PASS.

**Step 6: Commit**

```bash
git add packages/workspace-daemon/pkg/workspacemgr packages/workspace-daemon/pkg/handlers/workspace_manager.go packages/workspace-daemon/pkg/server/server.go
git commit -m "feat(workspace-daemon): add workspace credential policy model"
```

### Task 4: Implement Spotlight port forwarding API

**Files:**
- Create: `packages/workspace-daemon/pkg/spotlight/manager.go`
- Create: `packages/workspace-daemon/pkg/spotlight/manager_test.go`
- Create: `packages/workspace-daemon/pkg/handlers/spotlight.go`
- Modify: `packages/workspace-daemon/pkg/server/server.go`
- Test: `packages/workspace-daemon/pkg/spotlight/manager_test.go`

**Step 1: Write failing spotlight manager tests**

```go
func TestExpose_FailsOnLocalPortCollision(t *testing.T) {
    _, err := mgr.Expose(ctx, ExposeSpec{LocalPort: 5173, RemotePort: 5173})
    require.NoError(t, err)
    _, err = mgr.Expose(ctx, ExposeSpec{LocalPort: 5173, RemotePort: 8000})
    require.Error(t, err)
}
```

**Step 2: Run spotlight test and confirm failure**

Run: `cd packages/workspace-daemon && go test ./pkg/spotlight -v`
Expected: FAIL (missing manager).

**Step 3: Implement spotlight manager + handler methods**

RPC methods:
- `spotlight.expose`
- `spotlight.list`
- `spotlight.close`

**Step 4: Add internal-repo default profile mapping helper**

Default labels:
- `student-portal` => 5173
- `api` => 8000

**Step 5: Run tests**

Run: `cd packages/workspace-daemon && go test ./pkg/spotlight -v`
Expected: PASS.

**Step 6: Commit**

```bash
git add packages/workspace-daemon/pkg/spotlight packages/workspace-daemon/pkg/handlers/spotlight.go packages/workspace-daemon/pkg/server/server.go
git commit -m "feat(workspace-daemon): add spotlight port forwarding api"
```

### Task 5: Create SDK v2 minimal API wrapper over RPC

**Files:**
- Create: `packages/workspace-sdk/src/workspace-manager.ts`
- Create: `packages/workspace-sdk/src/workspace-handle.ts`
- Create: `packages/workspace-sdk/src/spotlight.ts`
- Modify: `packages/workspace-sdk/src/index.ts`
- Modify: `packages/workspace-sdk/src/types.ts`
- Create: `packages/workspace-sdk/src/__tests__/workspace-manager.test.ts`
- Test: `packages/workspace-sdk/src/__tests__/workspace-manager.test.ts`

**Step 1: Write failing SDK tests for create/open/list/remove**

```ts
it('creates workspace and returns handle', async () => {
  const ws = await client.workspace.create(spec)
  expect(ws.id).toBeDefined()
  expect(ws.exec).toBeDefined()
})
```

**Step 2: Run target test to verify failure**

Run: `cd packages/workspace-sdk && pnpm test -- workspace-manager.test.ts`
Expected: FAIL (missing API).

**Step 3: Implement `client.workspace.*` and `WorkspaceHandle`**

Expose only minimal surface:
- `exec`
- `fs`
- `git`
- `service`
- `spotlight`
- `info`

**Step 4: Update exports and types**

Keep old client methods functional for compatibility while marking as legacy in docs later.

**Step 5: Run tests**

Run: `cd packages/workspace-sdk && pnpm test -- workspace-manager.test.ts`
Expected: PASS.

**Step 6: Commit**

```bash
git add packages/workspace-sdk/src
git commit -m "feat(workspace-sdk): add minimal workspace manager and handle api"
```

### Task 6: Add SDK Spotlight and lifecycle integration tests

**Files:**
- Create: `packages/workspace-sdk/src/__tests__/spotlight.test.ts`
- Modify: `packages/workspace-sdk/src/__tests__/client.test.ts`
- Modify: `packages/workspace-sdk/src/workspace-handle.ts`
- Test: `packages/workspace-sdk/src/__tests__/spotlight.test.ts`

**Step 1: Write failing spotlight tests**

```ts
it('exposes and lists spotlight mappings', async () => {
  const fwd = await handle.spotlight.expose({ service: 'student-portal', remotePort: 5173, localPort: 5173 })
  const all = await handle.spotlight.list()
  expect(all.some(x => x.id === fwd.id)).toBe(true)
})
```

**Step 2: Run tests and confirm fail**

Run: `cd packages/workspace-sdk && pnpm test -- spotlight.test.ts`
Expected: FAIL (no spotlight wrapper).

**Step 3: Implement spotlight SDK wrapper methods**

Wire to daemon RPC methods and return typed result.

**Step 4: Run tests**

Run: `cd packages/workspace-sdk && pnpm test -- spotlight.test.ts`
Expected: PASS.

**Step 5: Commit**

```bash
git add packages/workspace-sdk/src/__tests__ packages/workspace-sdk/src/workspace-handle.ts
git commit -m "test(workspace-sdk): add spotlight lifecycle coverage"
```

### Task 7: Add internal-repo dogfooding harness for setup/start/teardown

**Files:**
- Create: `e2e/workspace/internal-battle-test.sh`
- Create: `e2e/workspace/internal-battle-test.md`
- Modify: `Taskfile.yml`
- Modify: `docs/reference/workspace-daemon.md`
- Modify: `docs/reference/workspace-sdk.md`
- Test: `e2e/workspace/internal-battle-test.sh`

**Step 1: Write harness script with strict mode and cleanup trap**

```bash
#!/usr/bin/env bash
set -euo pipefail
trap cleanup EXIT
```

Include:
- clone `<internal-repo-url>`
- create N isolated workspaces
- run project setup
- start `opencode serve`/ACP
- verify Spotlight forwards (`5173`, `8000`)
- teardown all workspaces and validate no leaked forwards/processes

**Step 2: Run script in dry-run mode first**

Run: `bash e2e/workspace/internal-battle-test.sh --dry-run`
Expected: PASS with printed execution plan.

**Step 3: Add Taskfile entry**

Add task: `workspace:battle-test` to run harness with configurable parallel count.

**Step 4: Document command and expected outputs**

Update references with examples and failure modes.

**Step 5: Execute one real run**

Run: `task workspace:battle-test`
Expected: all workspaces complete setup/start/ready/teardown; report success/failure counts.

**Step 6: Commit**

```bash
git add e2e/workspace Taskfile.yml docs/reference/workspace-daemon.md docs/reference/workspace-sdk.md
git commit -m "feat(e2e): add internal-repo remote workspace battle test"
```

### Task 8: Verification gate before completion claim

**Files:**
- Modify: `docs/reference/workspace-sdk.md`
- Modify: `docs/reference/workspace-daemon.md`
- Modify: `docs/explanation/architecture.md`

**Step 1: Run SDK tests**

Run: `cd packages/workspace-sdk && pnpm test`
Expected: PASS.

**Step 2: Run daemon tests**

Run: `cd packages/workspace-daemon && go test ./...`
Expected: PASS.

**Step 3: Run workspace battle test**

Run: `task workspace:battle-test`
Expected: PASS with teardown cleanup validation.

**Step 4: Run repository CI task**

Run: `task ci`
Expected: PASS with no lint/type/test errors.

**Step 5: Final commit for docs consistency**

```bash
git add docs/reference/workspace-sdk.md docs/reference/workspace-daemon.md docs/explanation/architecture.md
git commit -m "docs: align workspace architecture with remote-only minimal api"
```
