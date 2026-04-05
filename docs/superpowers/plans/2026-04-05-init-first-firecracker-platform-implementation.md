# Init-First Firecracker Platform Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `nexus init` the single idempotent entrypoint that scaffolds `.nexus` metadata and fully validates/prepares Firecracker platform readiness (Linux native, macOS via Lima), while keeping `action-nexus` thin and self-contained.

**Architecture:** Move runtime setup responsibility into `init` by introducing an init bootstrap dispatch path. Keep Linux setup logic in Nexus, add Darwin Lima readiness flow driven by checked-in template, and make doctor use ephemeral Lima while normal workspace start uses persistent Lima. Preserve Linux runtime behavior and make non-Linux builds compile by adding non-Linux TAP stubs.

**Tech Stack:** Go 1.24, Bash, GitHub Actions composite action, Lima (`limactl`), Firecracker.

---

## File Structure and Responsibilities

- Create: `packages/nexus/pkg/runtime/firecracker/tap_nonlinux.go` - non-Linux symbols/stubs so darwin builds compile without Linux TAP implementation.
- Create: `packages/nexus/pkg/runtime/firecracker/tap_nonlinux_test.go` - non-Linux guard tests for deterministic error behavior.
- Modify: `packages/nexus/pkg/runtime/firecracker/tap_linux.go` - keep Linux-specific implementation only.
- Modify: `packages/nexus/pkg/runtime/firecracker/manager.go` - continue using shared TAP symbols while compatible with non-Linux stubs.
- Modify: `packages/nexus/pkg/runtime/firecracker/manager_test.go` - keep tests platform-safe.
- Create: `packages/nexus/templates/lima/firecracker.yaml` - checked-in versioned Lima template for Darwin Firecracker path.
- Create: `packages/nexus/cmd/nexus/init_runtime_setup.go` - runtime bootstrap dispatch called by `runInit`.
- Create: `packages/nexus/cmd/nexus/init_runtime_setup_darwin.go` - Darwin implementation for limactl install/readiness and instance policies.
- Create: `packages/nexus/cmd/nexus/init_runtime_setup_linux.go` - Linux implementation wrapping existing setup verification/install flow.
- Create: `packages/nexus/cmd/nexus/init_runtime_setup_other.go` - non-linux/non-darwin explicit unsupported return.
- Modify: `packages/nexus/cmd/nexus/main.go` - remove `setup` subcommand surface, call runtime setup from `runInit`, route doctor behavior by platform.
- Modify: `packages/nexus/cmd/nexus/main_test.go` - tests for init hard-fail, non-interactive/manual instructions, and bootstrap dispatch.
- Modify: `.case-studies/action-nexus/action.yml` - add `go-version` input and `actions/setup-go` in action internals.
- Modify: `.case-studies/action-nexus/scripts/prepare-backend.sh` - remove action-owned Darwin Firecracker hard-fail and setup ownership.
- Modify: `.case-studies/action-nexus/scripts/run-doctor.sh` - run `nexus init` before doctor and keep no-hang sudo behavior.
- Modify: `.case-studies/action-nexus/README.md` - document that action runs init and Nexus owns platform setup.

### Task 1: Make Firecracker Package Compile on non-Linux (TAP stubs)

**Files:**
- Create: `packages/nexus/pkg/runtime/firecracker/tap_nonlinux.go`
- Create: `packages/nexus/pkg/runtime/firecracker/tap_nonlinux_test.go`
- Modify: `packages/nexus/pkg/runtime/firecracker/tap_linux.go`

- [ ] **Step 1: Write failing non-Linux test and compile check**

```go
//go:build !linux

package firecracker

import "testing"

func TestNonLinuxTapSetupReturnsUnsupported(t *testing.T) {
	if _, err := realSetupTAP("nx-test", bridgeGatewayIP, guestSubnetCIDR); err == nil {
		t.Fatal("expected unsupported error on non-linux")
	}
}
```

- [ ] **Step 2: Run test/compile to verify current failure**

Run: `GOOS=darwin GOARCH=arm64 go test ./pkg/runtime/firecracker/...`
Expected: FAIL with undefined symbols (`realSetupTAP`, `tapNameForWorkspace`, `bridgeGatewayIP`, etc.).

- [ ] **Step 3: Implement non-Linux stub symbols**

```go
//go:build !linux

package firecracker

import "fmt"

const bridgeName = "nexusbr0"
const bridgeGatewayIP = "172.26.0.1"
const guestSubnetCIDR = "172.26.0.0/16"

func tapNameForWorkspace(workspaceID string) string {
	suffix := workspaceID
	if len(suffix) > 12 {
		suffix = suffix[:12]
	}
	return "nx-" + suffix
}

func realSetupTAP(tapName, hostIP, subnetCIDR string) (any, error) {
	return nil, fmt.Errorf("firecracker tap setup is only supported on linux hosts")
}

func realTeardownTAP(tapName, subnetCIDR string) {}
```

- [ ] **Step 4: Re-run Linux and Darwin checks**

Run: `go test ./pkg/runtime/firecracker/... && GOOS=darwin GOARCH=arm64 go test ./pkg/runtime/firecracker/...`
Expected: PASS on Linux tests and Darwin compile/test path.

- [ ] **Step 5: Commit**

```bash
git add packages/nexus/pkg/runtime/firecracker/tap_nonlinux.go packages/nexus/pkg/runtime/firecracker/tap_nonlinux_test.go packages/nexus/pkg/runtime/firecracker/tap_linux.go
git commit -m "fix(firecracker): add non-linux tap stubs for darwin builds"
```

### Task 2: Move Runtime Setup into `nexus init`

**Files:**
- Create: `packages/nexus/cmd/nexus/init_runtime_setup.go`
- Modify: `packages/nexus/cmd/nexus/main.go`
- Modify: `packages/nexus/cmd/nexus/main_test.go`

- [ ] **Step 1: Write failing tests for init runtime bootstrap call**

```go
func TestRunInitCallsRuntimeBootstrapForFirecracker(t *testing.T) {
	root := t.TempDir()
	called := false
	orig := initRuntimeBootstrapRunner
	t.Cleanup(func() { initRuntimeBootstrapRunner = orig })
	initRuntimeBootstrapRunner = func(projectRoot, runtimeName string) error {
		called = true
		if runtimeName != "firecracker" {
			t.Fatalf("expected firecracker runtime, got %q", runtimeName)
		}
		return nil
	}
	if err := runInit(initOptions{projectRoot: root, runtime: "firecracker"}); err != nil {
		t.Fatalf("unexpected init error: %v", err)
	}
	if !called {
		t.Fatal("expected init runtime bootstrap runner to be called")
	}
}
```

- [ ] **Step 2: Run tests to verify failure before wiring**

Run: `go test ./cmd/nexus -run TestRunInitCallsRuntimeBootstrapForFirecracker -v`
Expected: FAIL because `runInit` does not call runtime bootstrap yet.

- [ ] **Step 3: Implement init runtime bootstrap dispatch and remove setup command surface**

```go
var initRuntimeBootstrapRunner = runInitRuntimeBootstrap

func runInit(opts initOptions) error {
	// existing scaffolding
	if err := initRuntimeBootstrapRunner(opts.projectRoot, runtimeName); err != nil {
		return err
	}
	fmt.Printf("initialized nexus workspace metadata at %s\n", nexusDir)
	return nil
}
```

```go
switch command {
case "init":
	runInitCommand(args)
	return
case "doctor":
	// handled below
default:
	printUsage()
	os.Exit(2)
}
```

- [ ] **Step 4: Re-run command package tests**

Run: `go test ./cmd/nexus -run 'TestRunInitCallsRuntimeBootstrapForFirecracker|TestRunInit' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/nexus/cmd/nexus/init_runtime_setup.go packages/nexus/cmd/nexus/main.go packages/nexus/cmd/nexus/main_test.go
git commit -m "feat(init): bootstrap runtime readiness during init"
```

### Task 3: Enforce Hard-Fail + Non-Interactive Manual Guidance in `init`

**Files:**
- Modify: `packages/nexus/cmd/nexus/init_runtime_setup.go`
- Modify: `packages/nexus/cmd/nexus/main_test.go`

- [ ] **Step 1: Write failing tests for non-interactive fail-fast messaging**

```go
func TestRunInitFirecrackerReturnsManualStepsInNonInteractiveMode(t *testing.T) {
	orig := initRuntimeBootstrapRunner
	t.Cleanup(func() { initRuntimeBootstrapRunner = orig })
	initRuntimeBootstrapRunner = func(projectRoot, runtimeName string) error {
		return fmt.Errorf("manual privileged step required")
	}
	err := runInit(initOptions{projectRoot: t.TempDir(), runtime: "firecracker"})
	if err == nil {
		t.Fatal("expected init failure")
	}
	if !strings.Contains(err.Error(), "manual next steps") {
		t.Fatalf("expected manual next steps in error, got: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test ./cmd/nexus -run TestRunInitFirecrackerReturnsManualStepsInNonInteractiveMode -v`
Expected: FAIL until error wrapping and instructions are implemented.

- [ ] **Step 3: Implement explicit hard-fail wrapper with manual commands**

```go
func wrapInitRuntimeSetupError(projectRoot, runtimeName string, err error) error {
	if runtimeName != "firecracker" {
		return err
	}
	return fmt.Errorf("nexus init runtime setup failed: %w\n\nmanual next steps:\n  sudo -E nexus init --project-root %s --runtime firecracker\n", err, projectRoot)
}
```

- [ ] **Step 4: Re-run tests**

Run: `go test ./cmd/nexus -run 'TestRunInitFirecrackerReturnsManualStepsInNonInteractiveMode|TestRunInit' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/nexus/cmd/nexus/init_runtime_setup.go packages/nexus/cmd/nexus/main_test.go
git commit -m "fix(init): fail fast with manual steps in non-interactive sessions"
```

### Task 4: Add Darwin Lima Bootstrap with Checked-In Template

**Files:**
- Create: `packages/nexus/templates/lima/firecracker.yaml`
- Create: `packages/nexus/cmd/nexus/init_runtime_setup_darwin.go`
- Modify: `packages/nexus/cmd/nexus/init_runtime_setup.go`
- Modify: `packages/nexus/cmd/nexus/main_test.go`

- [ ] **Step 1: Write failing tests for Darwin limactl detection/install guidance**

```go
func TestDarwinBootstrapReturnsInstallInstructionsWhenLimactlMissing(t *testing.T) {
	err := runInitRuntimeBootstrapDarwin(t.TempDir(), "firecracker")
	if err == nil {
		t.Fatal("expected missing limactl error")
	}
	if !strings.Contains(err.Error(), "brew install lima") {
		t.Fatalf("expected brew instruction, got: %v", err)
	}
}
```

- [ ] **Step 2: Run darwin-targeted test build to verify failure**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/nexus -run DarwinBootstrap -v`
Expected: FAIL until darwin bootstrap implementation exists.

- [ ] **Step 3: Implement darwin bootstrap and template usage**

```go
func runInitRuntimeBootstrapDarwin(projectRoot, runtimeName string) error {
	if runtimeName != "firecracker" {
		return nil
	}
	if _, err := exec.LookPath("limactl"); err != nil {
		if _, brewErr := exec.LookPath("brew"); brewErr == nil {
			_ = exec.Command("brew", "install", "lima").Run()
		}
	}
	if _, err := exec.LookPath("limactl"); err != nil {
		return fmt.Errorf("limactl not found; run: brew install lima")
	}
	templatePath := filepath.Join(moduleRoot(), "templates", "lima", "firecracker.yaml")
	return ensurePersistentLimaInstance("nexus-firecracker", templatePath)
}
```

```yaml
# packages/nexus/templates/lima/firecracker.yaml
vmType: "vz"
rosetta:
  enabled: false
nestedVirtualization: true
mounts:
  - location: "{{.Dir}}"
    writable: true
```

- [ ] **Step 4: Re-run darwin compile + command tests**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/nexus -run 'DarwinBootstrap|RunInit' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/nexus/templates/lima/firecracker.yaml packages/nexus/cmd/nexus/init_runtime_setup_darwin.go packages/nexus/cmd/nexus/init_runtime_setup.go packages/nexus/cmd/nexus/main_test.go
git commit -m "feat(init): add darwin lima bootstrap with checked-in template"
```

### Task 5: Enforce Doctor Ephemeral Lima vs Workspace Persistent Lima Policy

**Files:**
- Modify: `packages/nexus/cmd/nexus/main.go`
- Create: `packages/nexus/cmd/nexus/doctor_lima_darwin.go`
- Modify: `packages/nexus/cmd/nexus/main_test.go`

- [ ] **Step 1: Write failing test for doctor ephemeral instance naming/cleanup**

```go
func TestDoctorDarwinUsesEphemeralLimaInstance(t *testing.T) {
	name := doctorLimaInstanceName(1712300000)
	if !strings.HasPrefix(name, "nexus-doctor-") {
		t.Fatalf("unexpected doctor instance name: %s", name)
	}
}
```

- [ ] **Step 2: Run targeted tests for failure**

Run: `go test ./cmd/nexus -run TestDoctorDarwinUsesEphemeralLimaInstance -v`
Expected: FAIL until helper/flow exists.

- [ ] **Step 3: Implement darwin doctor wrapper flow with guaranteed teardown**

```go
func runDoctorViaLimaDarwin(opts options) error {
	instance := doctorLimaInstanceName(time.Now().Unix())
	if err := startEphemeralLima(instance); err != nil {
		return err
	}
	defer func() { _ = deleteLimaInstance(instance) }()
	return runDoctorInsideLima(instance, opts)
}
```

```go
func bootstrapFirecrackerExecContext(projectRoot string, execCtx doctorExecContext) error {
	if runtime.GOOS == "darwin" {
		return bootstrapFirecrackerExecContextDarwin(projectRoot, execCtx)
	}
	return firecrackerBootstrapRunner(projectRoot, execCtx)
}
```

- [ ] **Step 4: Re-run command tests and Darwin compile**

Run: `go test ./cmd/nexus -run 'DoctorDarwin|BootstrapDoctorExecContext' -v && GOOS=darwin GOARCH=arm64 go test ./cmd/nexus -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/nexus/cmd/nexus/main.go packages/nexus/cmd/nexus/doctor_lima_darwin.go packages/nexus/cmd/nexus/main_test.go
git commit -m "feat(doctor): use ephemeral lima on darwin firecracker"
```

### Task 6: Keep `action-nexus` Thin and Self-Contained

**Files:**
- Modify: `.case-studies/action-nexus/action.yml`
- Modify: `.case-studies/action-nexus/scripts/prepare-backend.sh`
- Modify: `.case-studies/action-nexus/scripts/run-doctor.sh`
- Modify: `.case-studies/action-nexus/README.md`

- [ ] **Step 1: Write failing action behavior checks (shell-level)**

```bash
grep -q "actions/setup-go" .case-studies/action-nexus/action.yml
grep -q "nexus init" .case-studies/action-nexus/scripts/run-doctor.sh
```

- [ ] **Step 2: Run checks to verify failure before changes**

Run: `bash -lc 'grep -q "actions/setup-go" .case-studies/action-nexus/action.yml && grep -q "nexus init" .case-studies/action-nexus/scripts/run-doctor.sh'`
Expected: FAIL before updates.

- [ ] **Step 3: Implement action updates**

```yaml
inputs:
  go-version:
    description: "Go version used to build nexus doctor"
    required: false
    default: "1.24.x"

steps:
  - uses: actions/setup-go@v5
    with:
      go-version: ${{ inputs.go-version }}
```

```bash
# run-doctor.sh
"$doctor_bin" init --project-root "$project_root" --runtime "$runtime_backend"
"$doctor_bin" doctor --project-root "$project_root" --suite "$suite" --report-json "$report_path"
```

- [ ] **Step 4: Re-run action script static checks**

Run: `bash -n .case-studies/action-nexus/scripts/prepare-backend.sh && bash -n .case-studies/action-nexus/scripts/run-doctor.sh && grep -q "actions/setup-go" .case-studies/action-nexus/action.yml`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add .case-studies/action-nexus/action.yml .case-studies/action-nexus/scripts/prepare-backend.sh .case-studies/action-nexus/scripts/run-doctor.sh .case-studies/action-nexus/README.md
git commit -m "feat(action): setup go and delegate platform setup to nexus init"
```

### Task 7: Full Verification and Regression Checks

**Files:**
- Modify: `packages/nexus/cmd/nexus/main_test.go`
- Modify: `packages/nexus/pkg/runtime/firecracker/manager_test.go`
- Modify: `.case-studies/hanlun-lms/.github/workflows/nexus-doctor.yml` (if needed to remove redundant setup-go)

- [ ] **Step 1: Add/adjust regression tests for removed setup command messaging and init-first behavior**

```go
func TestPrintUsageDoesNotAdvertiseSetupSubcommand(t *testing.T) {
	// capture printUsage output and assert no "setup firecracker"
}
```

- [ ] **Step 2: Run complete Go test suite for affected package roots**

Run: `go test ./cmd/nexus ./pkg/runtime/firecracker`
Expected: PASS.

- [ ] **Step 3: Run cross-platform compile checks**

Run: `GOOS=darwin GOARCH=arm64 go test ./cmd/nexus ./pkg/runtime/firecracker/... && GOOS=linux GOARCH=amd64 go test ./cmd/nexus ./pkg/runtime/firecracker/...`
Expected: PASS.

- [ ] **Step 4: Run local action-level smoke commands**

Run: `bash -n .case-studies/action-nexus/scripts/*.sh && grep -q "go-version" .case-studies/action-nexus/action.yml && grep -q "nexus init" .case-studies/action-nexus/scripts/run-doctor.sh`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add packages/nexus/cmd/nexus/main_test.go packages/nexus/pkg/runtime/firecracker/manager_test.go .case-studies/hanlun-lms/.github/workflows/nexus-doctor.yml
git commit -m "test: cover init-first firecracker setup and cross-platform regressions"
```

## Spec Coverage Check

- `nexus init` as single idempotent entrypoint: covered by Tasks 2 and 3.
- Hard-fail + manual next-step instructions: covered by Task 3.
- Non-interactive/read-only terminal no-hang behavior: covered by Tasks 3 and 6.
- macOS path uses checked-in Lima template with Nexus ownership: covered by Task 4.
- Doctor ephemeral vs workspace persistent policy: covered by Task 5.
- Action remains thin + includes internal setup-go: covered by Task 6.
- Darwin compile reliability for Firecracker package: covered by Task 1 and Task 7.

## Placeholder/Consistency Check

- No `TODO`/`TBD` placeholders remain.
- Function names used consistently across tasks (`runInitRuntimeBootstrap`, `runDoctorViaLimaDarwin`, `doctorLimaInstanceName`).
- Runtime ownership boundaries remain consistent (Nexus owns setup, action only prepares tooling + invokes Nexus commands).

## Strict Acceptance Addendum (2026-04-05)

1. `nexus doctor` startup selection must prefer Makefile discovery first:
   - when `Makefile` has `start:`, selected command is exactly `make start`.
2. Doctor logs must include explicit startup selection evidence:
   - `doctor lifecycle start selected command: make start`
3. When Makefile `start` is selected, legacy lifecycle script invocations are disallowed:
   - no `bash .nexus/lifecycles/setup.sh`
   - no `bash .nexus/lifecycles/start.sh`
4. Compose discovery implementation must parse stdout-only JSON from compose config output and never parse combined stderr/stdout.
5. Compose discovery failure behavior must be warning-only and actionable, without misleading parser noise from mixed streams.
6. Unit coverage must include:
   - startup precedence (`make start` wins over lifecycle script and compose fallback)
   - lifecycle setup skip when `make start` exists
   - non-JSON compose output handling path
7. CI validation must capture visible evidence lines for startup command selection and startup command output streaming.
