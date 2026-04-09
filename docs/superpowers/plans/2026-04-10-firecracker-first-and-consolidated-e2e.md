# Firecracker-First Runtime and Consolidated E2E Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enforce firecracker-first workspace creation with structured preflight/setup branching and replace ad-hoc runtime smoke scripts with a dedicated SDK-driven TypeScript e2e package that validates UI/CLI-equivalent user journeys.

**Architecture:** Keep backend selection in `pkg/runtime` and workspace create orchestration in `pkg/handlers`, but add a preflight classifier and setup-attempt contract that is test-overridable and observable. Build a new monorepo package `packages/e2e/sdk-runtime` that imports `@nexus/sdk`, runs live daemon scenarios with isolated fixtures, and becomes the CI end-to-end gate. Remove scattered shell/JS smoke scripts only after scenario parity is achieved and mapped.

**Tech Stack:** Go (daemon/runtime/handlers), TypeScript + Node + pnpm workspaces (`@nexus/sdk`, new `packages/e2e/sdk-runtime`), existing Nexus CLI (`cmd/nexus`), websocket RPC.

---

### File Structure Lock-In

**Runtime policy and preflight path (Go)**
- Create: `packages/nexus/pkg/runtime/preflight.go`
- Create: `packages/nexus/pkg/runtime/preflight_test.go`
- Modify: `packages/nexus/pkg/runtime/factory.go`
- Modify: `packages/nexus/pkg/runtime/factory_test.go`
- Modify: `packages/nexus/pkg/handlers/workspace_manager.go`
- Modify: `packages/nexus/pkg/handlers/workspace_manager_test.go`
- Modify: `packages/nexus/cmd/nexus/workspace.go` (structured error display for create failures)

**Consolidated e2e package (TypeScript)**
- Create: `packages/e2e/sdk-runtime/package.json`
- Create: `packages/e2e/sdk-runtime/tsconfig.json`
- Create: `packages/e2e/sdk-runtime/jest.config.js`
- Create: `packages/e2e/sdk-runtime/src/harness/daemon.ts`
- Create: `packages/e2e/sdk-runtime/src/harness/fixtures.ts`
- Create: `packages/e2e/sdk-runtime/src/harness/rpc.ts`
- Create: `packages/e2e/sdk-runtime/src/harness/assertions.ts`
- Create: `packages/e2e/sdk-runtime/src/cases/runtime-selection.e2e.test.ts`
- Create: `packages/e2e/sdk-runtime/src/cases/worktree-sync.e2e.test.ts`
- Create: `packages/e2e/sdk-runtime/src/cases/lifecycle-hooks.e2e.test.ts`
- Create: `packages/e2e/sdk-runtime/src/cases/spotlight-compose.e2e.test.ts`
- Create: `packages/e2e/sdk-runtime/src/cases/tools-auth-forwarding.e2e.test.ts`
- Create: `packages/e2e/sdk-runtime/src/cases/ui-cli-parity-map.test.ts`
- Create: `packages/e2e/sdk-runtime/src/parity/matrix.json`

**CI and migration cleanup**
- Modify: `pnpm-workspace.yaml`
- Modify: root `package.json` (if needed to expose workspace test command)
- Modify: `Taskfile.yml` (add consolidated e2e task)
- Modify: `.github/workflows/ci.yml` (replace ad-hoc smoke invocations with package command)
- Create: `docs/dev/internal/testing/e2e-migration-manifest.md`
- Delete (after parity is proven):
  - `scripts/ci/pty-runtime-e2e.sh`
  - `scripts/ci/pty-lxc-managed-e2e.sh`
  - `scripts/ci/nexus-subcommand-e2e-init-exec.sh`
  - `scripts/ci/nexus-subcommand-e2e-doctor-backends.sh`
  - `packages/nexus/scripts/pty-remote-smoke.js`
  - any temporary ad-hoc SDK runtime smoke file added outside the new package

---

### Task 1: Add Firecracker Preflight Classifier and Structured Outcome Model

**Files:**
- Create: `packages/nexus/pkg/runtime/preflight.go`
- Test: `packages/nexus/pkg/runtime/preflight_test.go`

- [ ] **Step 1: Write failing test for preflight status classification contract**

```go
func TestClassifyFirecrackerPreflight_Statuses(t *testing.T) {
    checks := []PreflightCheck{
        {Name: "nested_virt", OK: true},
        {Name: "lima", OK: true},
        {Name: "tap", OK: true},
    }
    got := ClassifyFirecrackerPreflight(checks, false)
    if got.Status != PreflightPass {
        t.Fatalf("expected pass, got %s", got.Status)
    }
}
```

- [ ] **Step 2: Run targeted test to verify failure**

Run: `go test ./pkg/runtime -run TestClassifyFirecrackerPreflight_Statuses -count=1`
Expected: FAIL with undefined preflight types/functions.

- [ ] **Step 3: Implement preflight result/check structs and classifier**

```go
type PreflightStatus string

const (
    PreflightPass                PreflightStatus = "pass"
    PreflightInstallableMissing  PreflightStatus = "installable_missing"
    PreflightUnsupportedNested   PreflightStatus = "unsupported_nested_virt"
    PreflightHardFail            PreflightStatus = "hard_fail"
)

type PreflightCheck struct {
    Name        string `json:"name"`
    OK          bool   `json:"ok"`
    Message     string `json:"message"`
    Remediation string `json:"remediation"`
    Installable bool   `json:"installable,omitempty"`
}

type FirecrackerPreflightResult struct {
    Status         PreflightStatus `json:"status"`
    Checks         []PreflightCheck `json:"checks"`
    SetupAttempted bool            `json:"setupAttempted"`
    SetupOutcome   string          `json:"setupOutcome"`
}
```

- [ ] **Step 4: Add tests for all statuses including nested virtualization exception**

```go
func TestClassifyFirecrackerPreflight_UnsupportedNestedVirt(t *testing.T) {
    checks := []PreflightCheck{{Name: "nested_virt", OK: false, Message: "nested virtualization unsupported"}}
    got := ClassifyFirecrackerPreflight(checks, true)
    if got.Status != PreflightUnsupportedNested {
        t.Fatalf("expected unsupported_nested_virt, got %s", got.Status)
    }
}
```

- [ ] **Step 5: Run runtime package tests**

Run: `go test ./pkg/runtime -count=1`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add packages/nexus/pkg/runtime/preflight.go packages/nexus/pkg/runtime/preflight_test.go
git commit -m "feat(runtime): add firecracker preflight classifier"
```

### Task 2: Enforce Firecracker-First Create Flow with One Setup Retry and Structured Errors

**Files:**
- Modify: `packages/nexus/pkg/runtime/factory.go`
- Modify: `packages/nexus/pkg/runtime/factory_test.go`
- Modify: `packages/nexus/pkg/handlers/workspace_manager.go`
- Modify: `packages/nexus/pkg/handlers/workspace_manager_test.go`
- Modify: `packages/nexus/cmd/nexus/workspace.go`

- [ ] **Step 1: Write failing runtime test for darwin firecracker-first expansion**

```go
func TestSelectDriverDarwinPrefersFirecrackerWhenPreflightPasses(t *testing.T) {
    f := NewFactory([]Capability{
        {Name: "runtime.darwin", Available: true},
        {Name: "runtime.firecracker", Available: true},
        {Name: "runtime.seatbelt", Available: true},
    }, map[string]Driver{
        "firecracker": &stubDriver{backend: "firecracker"},
        "seatbelt":    &stubDriver{backend: "seatbelt"},
    })

    d, err := f.SelectDriver([]string{"darwin"}, nil)
    if err != nil { t.Fatalf("select driver: %v", err) }
    if d.Backend() != "firecracker" {
        t.Fatalf("expected firecracker, got %s", d.Backend())
    }
}
```

- [ ] **Step 2: Run targeted test to verify failure**

Run: `go test ./pkg/runtime -run TestSelectDriverDarwinPrefersFirecrackerWhenPreflightPasses -count=1`
Expected: FAIL under current seatbelt-first behavior.

- [ ] **Step 3: Implement create-flow preflight decision gate in handler before backend set**

```go
preflight := runtime.RunFirecrackerPreflight(spec.Repo, runtime.PreflightOptions{UseOverrides: internalPreflightOverrideEnabled()})
switch preflight.Status {
case runtime.PreflightPass:
    spec.Backend = "firecracker"
case runtime.PreflightInstallableMissing:
    setupErr := attemptRuntimeSetupOnce(spec.Repo, "firecracker")
    preflight.SetupAttempted = true
    if setupErr == nil {
        preflight = runtime.RunFirecrackerPreflight(spec.Repo, runtime.PreflightOptions{UseOverrides: internalPreflightOverrideEnabled()})
    } else {
        preflight.SetupOutcome = "failed"
    }
    if preflight.Status != runtime.PreflightPass {
        return nil, rpcPreflightFailure(preflight)
    }
    spec.Backend = "firecracker"
case runtime.PreflightUnsupportedNested:
    spec.Backend = "seatbelt"
default:
    return nil, rpcPreflightFailure(preflight)
}
```

- [ ] **Step 4: Add failing handler tests for branch behavior and one-time setup retry**

```go
func TestHandleWorkspaceCreate_InstallableMissingRetriesSetupOnce(t *testing.T) {
	mgr := workspacemgr.NewManager(t.TempDir())
	repo := setupRepoWithWorkspaceConfig(t, `{"version":1}`)

	setPreflightSequenceForTest([]runtime.FirecrackerPreflightResult{
		{Status: runtime.PreflightInstallableMissing},
		{Status: runtime.PreflightPass},
	})
	setupCalls := 0
	setRuntimeSetupRunnerForTest(func(_ string, _ string) error {
		setupCalls++
		return nil
	})

	factory := runtime.NewFactory(
		[]runtime.Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]runtime.Driver{"firecracker": &mockDriver{backend: "firecracker"}},
	)

	params, _ := json.Marshal(WorkspaceCreateParams{Spec: workspacemgr.CreateSpec{Repo: repo, WorkspaceName: "alpha", AgentProfile: "default"}})
	result, rpcErr := HandleWorkspaceCreate(context.Background(), params, mgr, factory)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	if result.Workspace.Backend != "firecracker" {
		t.Fatalf("expected firecracker backend, got %q", result.Workspace.Backend)
	}
	if setupCalls != 1 {
		t.Fatalf("expected one setup attempt, got %d", setupCalls)
	}
}
```

- [ ] **Step 5: Add CLI create error rendering for structured preflight diagnostics**

```go
if strings.Contains(err.Error(), "backend selection failed") && strings.Contains(err.Error(), "status") {
    fmt.Fprintf(os.Stderr, "nexus workspace create: runtime preflight failed\n")
}
```

- [ ] **Step 6: Run focused suites**

Run: `go test ./pkg/runtime ./pkg/handlers ./cmd/nexus -count=1`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add packages/nexus/pkg/runtime/factory.go packages/nexus/pkg/runtime/factory_test.go packages/nexus/pkg/handlers/workspace_manager.go packages/nexus/pkg/handlers/workspace_manager_test.go packages/nexus/cmd/nexus/workspace.go
git commit -m "feat(workspace): enforce firecracker-first preflight flow"
```

### Task 3: Add Internal Preflight Override Controls for Deterministic E2E Branch Coverage

**Files:**
- Modify: `packages/nexus/pkg/runtime/preflight.go`
- Modify: `packages/nexus/pkg/runtime/preflight_test.go`
- Modify: `packages/nexus/pkg/handlers/workspace_manager_test.go`

- [ ] **Step 1: Write failing tests for override statuses**

```go
func TestRunFirecrackerPreflight_OverrideHardFail(t *testing.T) {
    t.Setenv("NEXUS_INTERNAL_PREFLIGHT_OVERRIDE", "hard_fail")
    res := RunFirecrackerPreflight(t.TempDir(), PreflightOptions{UseOverrides: true})
    if res.Status != PreflightHardFail {
        t.Fatalf("expected hard_fail, got %s", res.Status)
    }
}
```

- [ ] **Step 2: Run targeted override test to verify failure**

Run: `go test ./pkg/runtime -run TestRunFirecrackerPreflight_OverrideHardFail -count=1`
Expected: FAIL before override implementation.

- [ ] **Step 3: Implement env-gated overrides + diagnostic marker field**

```go
if opts.UseOverrides {
    switch strings.TrimSpace(os.Getenv("NEXUS_INTERNAL_PREFLIGHT_OVERRIDE")) {
    case "pass":
        return forced(PreflightPass)
    case "installable_missing":
        return forced(PreflightInstallableMissing)
    case "unsupported_nested_virt":
        return forced(PreflightUnsupportedNested)
    case "hard_fail":
        return forced(PreflightHardFail)
    }
}
```

- [ ] **Step 4: Verify tests and branch wiring assertions**

Run: `go test ./pkg/runtime ./pkg/handlers -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/nexus/pkg/runtime/preflight.go packages/nexus/pkg/runtime/preflight_test.go packages/nexus/pkg/handlers/workspace_manager_test.go
git commit -m "test(runtime): add internal preflight override controls"
```

### Task 4: Create Dedicated SDK Runtime E2E Package

**Files:**
- Create: `packages/e2e/sdk-runtime/package.json`
- Create: `packages/e2e/sdk-runtime/tsconfig.json`
- Create: `packages/e2e/sdk-runtime/jest.config.js`
- Create: `packages/e2e/sdk-runtime/src/harness/daemon.ts`
- Create: `packages/e2e/sdk-runtime/src/harness/fixtures.ts`
- Create: `packages/e2e/sdk-runtime/src/harness/rpc.ts`
- Create: `packages/e2e/sdk-runtime/src/harness/assertions.ts`
- Modify: `pnpm-workspace.yaml`

- [ ] **Step 1: Write failing package-level smoke test scaffold**

```ts
describe('sdk-runtime e2e harness', () => {
  it('connects to daemon using @nexus/sdk', async () => {
    const client = await connectSDKClient();
    const caps = await client.workspace.capabilities();
    expect(Array.isArray(caps)).toBe(true);
    await client.disconnect();
  });
});
```

- [ ] **Step 2: Run test to verify failure due missing package/harness**

Run: `pnpm --filter @nexus/e2e-sdk-runtime test`
Expected: FAIL (package not found or harness missing).

- [ ] **Step 3: Implement package and harness modules**

```json
{
  "name": "@nexus/e2e-sdk-runtime",
  "private": true,
  "type": "module",
  "scripts": {
    "build": "tsc -p tsconfig.json --noEmit",
    "test": "jest --runInBand",
    "test:ci": "jest --runInBand --ci"
  },
  "dependencies": {
    "@nexus/sdk": "workspace:*"
  }
}
```

- [ ] **Step 4: Wire workspace inclusion and run package tests**

Run: `pnpm install && pnpm --filter @nexus/e2e-sdk-runtime test`
Expected: PASS for harness smoke.

- [ ] **Step 5: Commit**

```bash
git add packages/e2e/sdk-runtime pnpm-workspace.yaml
git commit -m "feat(e2e): add sdk-runtime test package"
```

### Task 5: Implement Core User-Journey E2E Cases (Fixture, Sync, PTY, Lifecycle, Spotlight)

**Files:**
- Create: `packages/e2e/sdk-runtime/src/cases/runtime-selection.e2e.test.ts`
- Create: `packages/e2e/sdk-runtime/src/cases/worktree-sync.e2e.test.ts`
- Create: `packages/e2e/sdk-runtime/src/cases/lifecycle-hooks.e2e.test.ts`
- Create: `packages/e2e/sdk-runtime/src/cases/spotlight-compose.e2e.test.ts`
- Create: `packages/e2e/sdk-runtime/src/cases/tools-auth-forwarding.e2e.test.ts`
- Create: `packages/e2e/sdk-runtime/src/cases/ui-cli-parity-map.test.ts`
- Create: `packages/e2e/sdk-runtime/src/parity/matrix.json`

- [ ] **Step 1: Add failing runtime-selection branch tests (using overrides)**

```ts
it('uses firecracker on preflight pass', async () => {
  const run = await runCreateWithOverride('pass');
  expect(run.backend).toBe('firecracker');
});

it('uses seatbelt only for unsupported nested virt', async () => {
  const run = await runCreateWithOverride('unsupported_nested_virt');
  expect(run.backend).toBe('seatbelt');
});
```

- [ ] **Step 2: Run targeted tests to verify RED state**

Run: `pnpm --filter @nexus/e2e-sdk-runtime test -- runtime-selection`
Expected: FAIL with missing harness helpers or mismatched backend behavior.

- [ ] **Step 3: Implement fixture/sync test with per-run local git repo**

```ts
it('syncs host and workspace worktree both directions', async () => {
  const fx = await createLocalGitFixture();
  const ws = await createWorkspaceFromFixture(fx.path);
  await startWorkspace(ws.id);

  await writeHostFile(fx.path, 'host.txt', 'host-change');
  await expect(execInWorkspace(ws.id, 'cat /workspace/host.txt')).resolves.toContain('host-change');

  await execInWorkspace(ws.id, "printf 'guest-change' > /workspace/guest.txt");
  await expect(readHostFile(fx.path, 'guest.txt')).resolves.toContain('guest-change');
});
```

- [ ] **Step 4: Implement lifecycle, compose spotlight, and tool/auth tests**

```ts
it('executes lifecycle hooks in expected order', async () => {
  const logs = await runWorkspaceLifecycleFixture();
  expect(logs).toEqual(['pre-start', 'post-start', 'pre-stop', 'post-stop']);
});

it('auto-detects compose ports and exposes spotlight forwards', async () => {
  const forwards = await applyComposePortsAndList();
  expect(forwards.length).toBeGreaterThan(0);
});

it('verifies codex/opencode/claude and auth relay flow', async () => {
  const tools = await checkGuestTools();
  expect(tools.opencode.available).toBe(true);
  const token = await mintAuthRelay();
  expect(token.length).toBeGreaterThan(10);
});
```

- [ ] **Step 5: Implement parity map assertions (UI/CLI -> RPC -> test IDs)**

```ts
it('covers every mapped UI/CLI action with an e2e case', () => {
  const map = loadParityMap();
  for (const row of map.actions) {
    expect(existingCaseIDs).toContain(row.testCaseId);
  }
});
```

- [ ] **Step 6: Run full e2e package tests**

Run: `pnpm --filter @nexus/e2e-sdk-runtime test:ci`
Expected: PASS with machine-readable artifacts written by harness.

- [ ] **Step 7: Commit**

```bash
git add packages/e2e/sdk-runtime
git commit -m "test(e2e): cover runtime lifecycle spotlight and auth"
```

### Task 6: Replace Legacy Ad-Hoc Scripts and Wire CI to New Package

**Files:**
- Modify: `.github/workflows/ci.yml`
- Modify: `Taskfile.yml`
- Create: `docs/dev/internal/testing/e2e-migration-manifest.md`
- Delete: `scripts/ci/pty-runtime-e2e.sh`
- Delete: `scripts/ci/pty-lxc-managed-e2e.sh`
- Delete: `scripts/ci/nexus-subcommand-e2e-init-exec.sh`
- Delete: `scripts/ci/nexus-subcommand-e2e-doctor-backends.sh`
- Delete: `packages/nexus/scripts/pty-remote-smoke.js`

- [ ] **Step 1: Write failing CI check by removing legacy invocation and adding new package command**

```yaml
- name: Runtime E2E
  run: pnpm --filter @nexus/e2e-sdk-runtime test:ci
```

- [ ] **Step 2: Run workflow-equivalent local commands to verify failure before migration complete**

Run: `pnpm --filter @nexus/e2e-sdk-runtime test:ci`
Expected: FAIL until all migrated tests are in place.

- [ ] **Step 3: Add migration manifest mapping old scripts to new case IDs**

```md
| Legacy Script | New Test Case ID | Status |
| --- | --- | --- |
| scripts/ci/pty-runtime-e2e.sh | E2E-PTY-001, E2E-PTY-002 | migrated |
```

- [ ] **Step 4: Remove migrated ad-hoc files and run CI-equivalent command**

Run:

```bash
pnpm --filter @nexus/e2e-sdk-runtime test:ci
go test ./pkg/runtime ./pkg/handlers ./pkg/server ./pkg/lifecycle -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add .github/workflows/ci.yml Taskfile.yml docs/dev/internal/testing/e2e-migration-manifest.md
git rm scripts/ci/pty-runtime-e2e.sh scripts/ci/pty-lxc-managed-e2e.sh scripts/ci/nexus-subcommand-e2e-init-exec.sh scripts/ci/nexus-subcommand-e2e-doctor-backends.sh packages/nexus/scripts/pty-remote-smoke.js
git commit -m "refactor(testing): consolidate runtime e2e into ts package"
```

### Task 7: Final Verification Sweep and Branch Hygiene

**Files:**
- Modify: none (verification only)

- [ ] **Step 1: Run full touched verification suite**

Run:

```bash
go test ./pkg/runtime ./pkg/handlers ./pkg/server ./pkg/lifecycle ./pkg/spotlight ./pkg/workspacemgr -count=1
pnpm --filter @nexus/sdk test
pnpm --filter @nexus/e2e-sdk-runtime test:ci
```

Expected: PASS.

- [ ] **Step 2: Run one live user-path proof against local daemon**

Run:

```bash
task dev
pnpm --filter @nexus/e2e-sdk-runtime test -- runtime-selection worktree-sync spotlight-compose
```

Expected: PASS with artifacts proving backend, sync, lifecycle, spotlight, and tool/auth checks.

- [ ] **Step 3: Validate git status and push**

Run: `git status --short --branch && git log --oneline -12 && git push origin feat/workspace-robustness-persistence-versioning`
Expected: clean tree (ignoring unrelated pre-existing files) and successful push.
