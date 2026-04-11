# Agent Auth Forwarding & Workspace Mount Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace scattered, hardcoded, stale agent credential forwarding with a single agent profile registry that drives all auth and mount behavior across both drivers, making new agent support a one-line registry entry.

**Architecture:** A new `pkg/agentprofile` package declares every known agent's env vars, credential file paths, binary name, and npm package. Both `authrelay/env.go` and the runtime drivers (`seatbelt`, `firecracker`) read from this registry instead of maintaining parallel hardcoded lists. The seatbelt driver replaces its stale tar-bundle approach with live symlinks from the Lima guest home to Lima-automounted host paths — credentials are always fresh without re-bootstrapping. The firecracker driver keeps its tar-bundle (no Lima automount available) but generates it from the registry. A `runtime.ErrWorkspaceMountFailed` sentinel replaces the fragile string-matching error swallow.

**Tech Stack:** Go, Lima (seatbelt), Firecracker/vsock, existing `authrelay.Broker` + `runtime.Driver` interfaces.

---

## File Structure

**Create:**

- `packages/nexus/pkg/agentprofile/registry.go` — profile structs, registry slice, `Lookup`, `AllBinaries`, `AllCredFiles`, `AllInstallPkgs`
- `packages/nexus/pkg/agentprofile/registry_test.go` — lookup, aliases, dedup helpers

**Modify:**

- `packages/nexus/pkg/runtime/driver.go` — add `ErrWorkspaceMountFailed` sentinel
- `packages/nexus/pkg/authrelay/env.go` — replace switch with registry lookup
- `packages/nexus/pkg/authrelay/env_test.go` — add registry-driven case
- `packages/nexus/pkg/runtime/seatbelt/driver.go` — symlink-based cred forwarding, registry-driven bootstrap/install/detection, proper mount error propagation
- `packages/nexus/pkg/runtime/seatbelt/driver_test.go` — update mount error test, add symlink script test
- `packages/nexus/pkg/runtime/firecracker/driver.go` — registry-driven `buildHostAuthBundle` and `buildGuestCLIBootstrapCommand`
- `packages/nexus/pkg/runtime/firecracker/driver_test.go` — update auth bundle test

---

## Task 1: Agent Profile Registry

**Files:**

- Create: `packages/nexus/pkg/agentprofile/registry.go`
- Create: `packages/nexus/pkg/agentprofile/registry_test.go`
- **Step 1: Write the failing tests**

```go
// packages/nexus/pkg/agentprofile/registry_test.go
package agentprofile

import (
	"testing"
)

func TestLookupByCanonicalName(t *testing.T) {
	p := Lookup("claude")
	if p == nil {
		t.Fatal("expected claude profile, got nil")
	}
	if p.Name != "claude" {
		t.Fatalf("expected name claude, got %q", p.Name)
	}
}

func TestLookupByAlias(t *testing.T) {
	p := Lookup("anthropic")
	if p == nil {
		t.Fatal("expected claude profile via alias anthropic, got nil")
	}
	if p.Name != "claude" {
		t.Fatalf("expected canonical name claude, got %q", p.Name)
	}
}

func TestLookupCaseInsensitive(t *testing.T) {
	p := Lookup("CLAUDE")
	if p == nil {
		t.Fatal("expected claude profile for uppercase CLAUDE, got nil")
	}
}

func TestLookupUnknownReturnsNil(t *testing.T) {
	if Lookup("nope-does-not-exist") != nil {
		t.Fatal("expected nil for unknown binding")
	}
}

func TestLookupEmptyReturnsNil(t *testing.T) {
	if Lookup("") != nil {
		t.Fatal("expected nil for empty binding")
	}
}

func TestAllBinariesNonEmpty(t *testing.T) {
	bins := AllBinaries()
	if len(bins) == 0 {
		t.Fatal("expected at least one binary")
	}
	for _, b := range bins {
		if b == "" {
			t.Fatal("AllBinaries must not return empty strings")
		}
	}
}

func TestAllCredFilesNoDuplicates(t *testing.T) {
	files := AllCredFiles()
	seen := make(map[string]struct{})
	for _, f := range files {
		if f == "" {
			t.Fatal("AllCredFiles must not return empty strings")
		}
		if _, ok := seen[f]; ok {
			t.Fatalf("duplicate cred file: %q", f)
		}
		seen[f] = struct{}{}
	}
}

func TestAllInstallPkgsNonEmpty(t *testing.T) {
	pkgs := AllInstallPkgs()
	if len(pkgs) == 0 {
		t.Fatal("expected at least one install package")
	}
	seen := make(map[string]struct{})
	for _, p := range pkgs {
		if p == "" {
			t.Fatal("AllInstallPkgs must not return empty strings")
		}
		if _, ok := seen[p]; ok {
			t.Fatalf("duplicate install pkg: %q", p)
		}
		seen[p] = struct{}{}
	}
}

func TestCodexHasAPIKeyPrefix(t *testing.T) {
	p := Lookup("codex")
	if p == nil {
		t.Fatal("codex profile missing")
	}
	if p.APIKeyPrefix == "" {
		t.Fatal("codex profile must have APIKeyPrefix (distinguishes OAuth tokens from API keys)")
	}
}

func TestProfilesWithEnvVarsHaveAtLeastOneVar(t *testing.T) {
	for _, p := range registry {
		if len(p.EnvVars) == 0 {
			t.Fatalf("profile %q has no EnvVars — every profile must map to at least one env var", p.Name)
		}
	}
}
```

- **Step 2: Run tests to verify they fail**

```bash
cd packages/nexus && go test ./pkg/agentprofile/... -v
```

Expected: FAIL — package does not exist yet.

- **Step 3: Create the registry**

```go
// packages/nexus/pkg/agentprofile/registry.go
package agentprofile

import "strings"

type Profile struct {
	Name         string
	Aliases      []string
	Binary       string
	EnvVars      []string
	APIKeyPrefix string
	CredFiles    []string
	InstallPkg   string
}

var registry = []Profile{
	{
		Name:      "claude",
		Aliases:   []string{"anthropic", "claude-code"},
		Binary:    "claude",
		EnvVars:   []string{"ANTHROPIC_API_KEY", "CLAUDE_API_KEY"},
		CredFiles: []string{".claude/.credentials.json", ".claude.json"},
		InstallPkg: "@anthropic-ai/claude-code",
	},
	{
		Name:         "codex",
		Binary:       "codex",
		EnvVars:      []string{"OPENAI_API_KEY"},
		APIKeyPrefix: "sk-",
		CredFiles: []string{
			".codex/auth.json",
			".codex/version.json",
			".codex/.codex-global-state.json",
			".config/openai/auth.json",
		},
		InstallPkg: "@openai/codex",
	},
	{
		Name:    "openai",
		Aliases: []string{"openai_api_key"},
		EnvVars: []string{"OPENAI_API_KEY"},
	},
	{
		Name:    "opencode",
		Binary:  "opencode",
		EnvVars: []string{"OPENCODE_API_KEY"},
		CredFiles: []string{
			".local/share/opencode/auth.json",
			".config/opencode/opencode.json",
			".config/opencode/ocx.jsonc",
			".config/opencode/dcp.jsonc",
		},
		InstallPkg: "opencode-ai",
	},
	{
		Name:    "github",
		Aliases: []string{"gh", "copilot", "github-copilot"},
		Binary:  "gh",
		EnvVars: []string{"GITHUB_TOKEN", "GH_TOKEN"},
		CredFiles: []string{
			".config/github-copilot/hosts.json",
			".config/github-copilot/apps.json",
		},
	},
	{
		Name:    "openrouter",
		EnvVars: []string{"OPENROUTER_API_KEY"},
	},
	{
		Name:    "minimax",
		EnvVars: []string{"MINIMAX_API_KEY"},
	},
}

func Lookup(name string) *Profile {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "" {
		return nil
	}
	for i := range registry {
		p := &registry[i]
		if strings.ToLower(p.Name) == normalized {
			return p
		}
		for _, a := range p.Aliases {
			if strings.ToLower(a) == normalized {
				return p
			}
		}
	}
	return nil
}

func AllBinaries() []string {
	out := make([]string, 0, len(registry))
	for _, p := range registry {
		if p.Binary != "" {
			out = append(out, p.Binary)
		}
	}
	return out
}

func AllCredFiles() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, p := range registry {
		for _, f := range p.CredFiles {
			if _, ok := seen[f]; !ok {
				seen[f] = struct{}{}
				out = append(out, f)
			}
		}
	}
	return out
}

func AllInstallPkgs() []string {
	seen := make(map[string]struct{})
	var out []string
	for _, p := range registry {
		if p.InstallPkg != "" {
			if _, ok := seen[p.InstallPkg]; !ok {
				seen[p.InstallPkg] = struct{}{}
				out = append(out, p.InstallPkg)
			}
		}
	}
	return out
}
```

- **Step 4: Run tests to verify they pass**

```bash
cd packages/nexus && go test ./pkg/agentprofile/... -v
```

Expected: PASS — all 9 tests pass.

- **Step 5: Commit**

```bash
git add packages/nexus/pkg/agentprofile/
git commit -m "feat(agentprofile): add agent profile registry"
```

---

## Task 2: Drive `authrelay/env.go` from Registry

**Files:**

- Modify: `packages/nexus/pkg/authrelay/env.go`
- Modify: `packages/nexus/pkg/authrelay/env_test.go`
- **Step 1: Add a new test case for an agent added to the registry**

Add this case to the `cases` slice in `packages/nexus/pkg/authrelay/env_test.go`:

```go
{
    name:    "amp_binding_via_registry",
    binding: "amp",
    value:   "sk-ant-xxx",
    want: map[string]string{
        "NEXUS_AUTH_BINDING": "amp",
        "NEXUS_AUTH_VALUE":   "sk-ant-xxx",
    },
},
```

> This verifies that an unknown binding gracefully returns only the NEXUS_AUTH_* vars. It also serves as the scaffold for Task 1 self-check: if someone adds `amp` to the registry, this test updates to include `ANTHROPIC_API_KEY` automatically.

- **Step 2: Run tests to confirm current state**

```bash
cd packages/nexus && go test ./pkg/authrelay/... -v
```

Expected: PASS — new case passes because `amp` is unknown and falls through to the base map.

- **Step 3: Replace `env.go` with registry-driven implementation**

```go
// packages/nexus/pkg/authrelay/env.go
package authrelay

import (
	"strings"

	"github.com/inizio/nexus/packages/nexus/pkg/agentprofile"
)

func RelayEnv(binding, value string) map[string]string {
	out := map[string]string{
		"NEXUS_AUTH_BINDING": binding,
		"NEXUS_AUTH_VALUE":   value,
	}
	p := agentprofile.Lookup(binding)
	if p == nil {
		return out
	}
	if p.APIKeyPrefix != "" && !strings.HasPrefix(strings.TrimSpace(value), p.APIKeyPrefix) {
		return out
	}
	for _, k := range p.EnvVars {
		out[k] = value
	}
	return out
}
```

- **Step 4: Run all authrelay tests**

```bash
cd packages/nexus && go test ./pkg/authrelay/... -v
```

Expected: PASS — all existing cases pass (behavior preserved), plus the new `amp_binding_via_registry` case.

- **Step 5: Verify the whole module still compiles**

```bash
cd packages/nexus && go build ./...
```

Expected: no errors.

- **Step 6: Commit**

```bash
git add packages/nexus/pkg/authrelay/
git commit -m "feat(authrelay): drive RelayEnv from agentprofile registry"
```

---

## Task 3: Seatbelt — Live Symlink Credential Forwarding

**Problem:** `buildSeatbeltHostAuthBundle` tarballs host credential files at bootstrap time. These credentials go stale (OAuth tokens expire; keys rotate). Lima automatically mounts the host home directory at the same absolute path inside the VM, so we can symlink from `$GUEST_HOME/<path>` → `<HOST_HOME>/<path>` instead, making credentials always live.

**Files:**

- Modify: `packages/nexus/pkg/runtime/seatbelt/driver.go`
- Modify: `packages/nexus/pkg/runtime/seatbelt/driver_test.go`
- **Step 1: Write failing test for symlink-based bootstrap script**

Add to `packages/nexus/pkg/runtime/seatbelt/driver_test.go`:

```go
func TestBuildSeatbeltBootstrapScriptContainsSymlinks(t *testing.T) {
	script := buildSeatbeltBootstrapScript("/Users/testhost")

	// Must not contain tar extraction (old bundle approach)
	if strings.Contains(script, "nexus-auth.tar.gz") {
		t.Fatal("bootstrap script must not use tar bundle — use symlinks instead")
	}

	// Must create a symlink for a known credential file
	if !strings.Contains(script, "ln -sfn") {
		t.Fatal("bootstrap script must create symlinks for credential files")
	}

	// Must reference the host home path in symlink targets
	if !strings.Contains(script, "/Users/testhost") {
		t.Fatal("bootstrap script must use host home as symlink target")
	}
}

func TestBuildSeatbeltBootstrapScriptInstallsRegistryPackages(t *testing.T) {
	script := buildSeatbeltBootstrapScript("/Users/testhost")

	for _, pkg := range agentprofile.AllInstallPkgs() {
		if !strings.Contains(script, pkg) {
			t.Fatalf("bootstrap script missing install package %q", pkg)
		}
	}
}

func TestBuildSeatbeltBootstrapScriptChecksRegistryBinaries(t *testing.T) {
	script := buildSeatbeltBootstrapScript("/Users/testhost")

	for _, bin := range agentprofile.AllBinaries() {
		if !strings.Contains(script, bin) {
			t.Fatalf("bootstrap script missing binary check for %q", bin)
		}
	}
}
```

- **Step 2: Run failing tests**

```bash
cd packages/nexus && go test ./pkg/runtime/seatbelt/... -run "TestBuildSeatbeltBootstrapScript" -v
```

Expected: FAIL — function signature mismatch or old tar logic.

- **Step 3: Replace seatbelt bootstrap with registry-driven symlink approach**

In `packages/nexus/pkg/runtime/seatbelt/driver.go`, replace `buildSeatbeltHostAuthBundle`, `buildSeatbeltBootstrapScript`, `detectHostCLIAvailability`, and the `hostCLIAvailability` struct with the following:

```go
func buildSeatbeltBootstrapScript(hostHome string) string {
	parts := []string{
		"set -e",
		buildCredentialSymlinkCleanup(),
		"unset DOCKER_HOST DOCKER_CONTEXT",
		"if ! (command -v docker >/dev/null 2>&1 && (docker info >/dev/null 2>&1 || sudo -n docker info >/dev/null 2>&1) && (docker compose version >/dev/null 2>&1 || docker-compose version >/dev/null 2>&1) && command -v make >/dev/null 2>&1); then sudo -n apt-get update; sudo -n DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose-v2 make curl ca-certificates gnupg nodejs npm || sudo -n DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io docker-compose make curl ca-certificates gnupg nodejs npm; sudo -n systemctl enable docker >/dev/null 2>&1 || true; sudo -n systemctl start docker >/dev/null 2>&1 || sudo -n service docker start >/dev/null 2>&1 || true; sudo -n usermod -aG docker $USER >/dev/null 2>&1 || true; fi",
		"(docker info >/dev/null 2>&1 || sudo -n docker info >/dev/null 2>&1)",
		"(docker compose version >/dev/null 2>&1 || docker-compose version >/dev/null 2>&1)",
		"command -v make >/dev/null 2>&1",
	}

	pkgs := agentprofile.AllInstallPkgs()
	if len(pkgs) > 0 {
		joined := strings.Join(pkgs, " ")
		parts = append(parts,
			"if command -v npm >/dev/null 2>&1; then cd /tmp >/dev/null 2>&1 || true; npm i -g "+joined+" >/dev/null 2>&1 || sudo -n npm i -g "+joined+" >/dev/null 2>&1 || true; fi",
		)
	}

	for _, bin := range agentprofile.AllBinaries() {
		parts = append(parts, "if command -v "+bin+" >/dev/null 2>&1; then "+bin+" --version >/dev/null 2>&1 || true; fi")
	}

	hostHome = strings.TrimSpace(hostHome)
	if hostHome != "" {
		parts = append(parts,
			"mkdir -p ~/.config ~/.local/share",
			"if command -v npm >/dev/null 2>&1; then cd /tmp >/dev/null 2>&1 || true; NPM_BIN=$(npm bin -g 2>/dev/null || true); if [ -n \"$NPM_BIN\" ] && [ -d \"$NPM_BIN\" ]; then export PATH=\"$NPM_BIN:$PATH\"; fi; fi",
		)
		parts = append(parts, buildCredentialSymlinks(hostHome))
	}

	return strings.Join(parts, "; ")
}

func buildCredentialSymlinkCleanup() string {
	dirs := make(map[string]struct{})
	for _, cf := range agentprofile.AllCredFiles() {
		dir := filepath.Dir(cf)
		dirs[dir] = struct{}{}
	}
	var checks []string
	for dir := range dirs {
		checks = append(checks, `if [ -L "$HOME/`+dir+`" ]; then rm -f "$HOME/`+dir+`"; fi`)
	}
	return strings.Join(checks, "; ")
}

func buildCredentialSymlinks(hostHome string) string {
	var parts []string
	for _, cf := range agentprofile.AllCredFiles() {
		dir := filepath.Dir(cf)
		hostPath := shellQuote(filepath.Join(hostHome, cf))
		parts = append(parts,
			`mkdir -p "$HOME/`+dir+`"`,
			`if [ -e `+hostPath+` ]; then ln -sfn `+hostPath+` "$HOME/`+cf+`"; fi`,
		)
	}
	return strings.Join(parts, "; ")
}
```

Also update `bootstrapSeatbeltTooling` to call the new signature (remove `hostCLI` and `authBundle` arguments):

```go
func bootstrapSeatbeltTooling(ctx context.Context, instance, hostHome string) error {
	instance = strings.TrimSpace(instance)
	if instance == "" {
		instance = "nexus-firecracker"
	}

	candidates := instanceCandidates(instance)
	if discovered, err := listLimaInstancesFn(ctx); err == nil && len(discovered) > 0 {
		candidates = filterCandidatesByAvailability(candidates, discovered)
	}

	script := buildSeatbeltBootstrapScript(hostHome)

	var lastErr error
	for _, candidate := range candidates {
		if err := ensureLimaInstanceRunningFn(ctx, candidate); err != nil {
			lastErr = err
			continue
		}

		const maxAttempts = 3
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			cmd := exec.CommandContext(ctx, "limactl", "shell", candidate, "--", "sh", "-lc", script)
			out, err := cmd.CombinedOutput()
			if err == nil {
				return nil
			}

			trimmed := strings.TrimSpace(string(out))
			lastErr = fmt.Errorf("bootstrap seatbelt tooling in %s failed: %s", candidate, trimmed)
			if !isTransientLimaShellError(trimmed) || attempt == maxAttempts {
				break
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("bootstrap seatbelt tooling failed: no lima instance candidates")
}
```

Remove: `buildSeatbeltHostAuthBundle`, `addSeatbeltPathToTar`, `detectHostCLIAvailability`, and the `hostCLIAvailability` struct from the seatbelt driver.

Add to imports: `"github.com/inizio/nexus/packages/nexus/pkg/agentprofile"`, `"path/filepath"`.
Remove from imports: `"archive/tar"`, `"bytes"`, `"compress/gzip"`, `"encoding/base64"`, `"io"`.

- **Step 4: Run seatbelt tests**

```bash
cd packages/nexus && go test ./pkg/runtime/seatbelt/... -v
```

Expected: PASS — existing bootstrap/mount tests pass, new symlink tests pass.

- **Step 5: Verify build**

```bash
cd packages/nexus && go build ./...
```

Expected: no errors.

- **Step 6: Commit**

```bash
git add packages/nexus/pkg/runtime/seatbelt/
git commit -m "feat(seatbelt): replace stale auth bundle with live host-symlink credential forwarding"
```

---

## Task 4: Workspace Mount Sentinel Error

**Problem:** `seatbelt/driver.go` swallows mount failures by string-matching `"prepare /workspace mount failed"` and returning `nil`. This makes the workspace appear healthy when `/workspace` is not available. The `runtime` package should define a sentinel so handlers can distinguish a degraded-mount workspace from a hard failure.

**Files:**

- Modify: `packages/nexus/pkg/runtime/driver.go`
- Modify: `packages/nexus/pkg/runtime/seatbelt/driver.go`
- Modify: `packages/nexus/pkg/runtime/seatbelt/driver_test.go`
- Modify: `packages/nexus/pkg/handlers/workspace_manager.go`
- **Step 1: Add failing test for mount error propagation**

Add to `packages/nexus/pkg/runtime/seatbelt/driver_test.go`:

```go
func TestCreateReturnsErrWorkspaceMountFailedWhenAllMountsFail(t *testing.T) {
	d := NewDriver()
	oldLookPath := seatbeltLookPath
	t.Cleanup(func() { seatbeltLookPath = oldLookPath })
	seatbeltLookPath = func(file string) (string, error) { return "/usr/local/bin/limactl", nil }

	d.bootstrapInstance = func(ctx context.Context, instance, hostHome string) error { return nil }
	d.prepareWorkspaceFS = func(ctx context.Context, instance, localPath string) error {
		return errors.New("prepare /workspace mount failed: instance unreachable")
	}

	err := d.Create(context.Background(), runtime.CreateRequest{
		WorkspaceID: "ws-mount-fail",
		ProjectRoot: t.TempDir(),
	})
	if err == nil {
		t.Fatal("expected error when all mount candidates fail")
	}
	if !errors.Is(err, runtime.ErrWorkspaceMountFailed) {
		t.Fatalf("expected ErrWorkspaceMountFailed sentinel, got: %v", err)
	}
}
```

Also add a test that `ensureLocalRuntimeWorkspace` in handlers treats `ErrWorkspaceMountFailed` as non-fatal. Add this to `packages/nexus/pkg/handlers/exec_authrelay_test.go` or a new test file `packages/nexus/pkg/handlers/workspace_manager_test.go`:

```go
func TestEnsureLocalRuntimeWorkspaceToleratesmountFailed(t *testing.T) {
	fakeMountFailDriver := &fakeMountFailDriver{}
	factory := buildFakeFactory(fakeMountFailDriver)
	ws := &workspacemgr.Workspace{
		ID:       "ws-1",
		Backend:  "seatbelt",
		RootPath: t.TempDir(),
	}
	rpcErr := ensureLocalRuntimeWorkspace(context.Background(), ws, factory, nil)
	if rpcErr != nil {
		t.Fatalf("expected no RPC error for mount failure (degraded workspace), got: %v", rpcErr.Message)
	}
}

type fakeMountFailDriver struct{}

func (f *fakeMountFailDriver) Backend() string { return "seatbelt" }
func (f *fakeMountFailDriver) Create(_ context.Context, _ runtime.CreateRequest) error {
	return fmt.Errorf("%w: lima instance unreachable", runtime.ErrWorkspaceMountFailed)
}
func (f *fakeMountFailDriver) Start(_ context.Context, _ string) error  { return nil }
func (f *fakeMountFailDriver) Stop(_ context.Context, _ string) error   { return nil }
func (f *fakeMountFailDriver) Restore(_ context.Context, _ string) error { return nil }
func (f *fakeMountFailDriver) Pause(_ context.Context, _ string) error  { return nil }
func (f *fakeMountFailDriver) Resume(_ context.Context, _ string) error { return nil }
func (f *fakeMountFailDriver) Fork(_ context.Context, _, _ string) error { return nil }
func (f *fakeMountFailDriver) Destroy(_ context.Context, _ string) error { return nil }

func buildFakeFactory(d runtime.Driver) *runtime.Factory {
	f := runtime.NewFactory()
	f.Register(d)
	return f
}
```

> Note: Check the `runtime.Factory` API to adjust `buildFakeFactory` if needed.

- **Step 2: Run failing tests**

```bash
cd packages/nexus && go test ./pkg/runtime/seatbelt/... ./pkg/handlers/... -run "TestCreateReturnsErrWorkspaceMountFailed|TestEnsureLocalRuntimeWorkspaceTolerates" -v
```

Expected: FAIL — `runtime.ErrWorkspaceMountFailed` not defined yet.

- **Step 3: Add sentinel to `runtime/driver.go`**

```go
// packages/nexus/pkg/runtime/driver.go
package runtime

import (
	"context"
	"errors"
)

var ErrWorkspaceMountFailed = errors.New("workspace mount not available")

type Driver interface {
	Backend() string
	Create(ctx context.Context, req CreateRequest) error
	Start(ctx context.Context, workspaceID string) error
	Stop(ctx context.Context, workspaceID string) error
	Restore(ctx context.Context, workspaceID string) error
	Pause(ctx context.Context, workspaceID string) error
	Resume(ctx context.Context, workspaceID string) error
	Fork(ctx context.Context, workspaceID, childWorkspaceID string) error
	Destroy(ctx context.Context, workspaceID string) error
}

type CreateRequest struct {
	WorkspaceID   string
	WorkspaceName string
	ProjectRoot   string
	Options       map[string]string
}

type WorkspaceMetadata struct {
	Backend     string
	WorkspaceID string
	State       string
}
```

- **Step 4: Update seatbelt `Create` to wrap mount failures with sentinel**

In `packages/nexus/pkg/runtime/seatbelt/driver.go`, replace the mount error handling block inside `Create`:

**Before (current code to replace):**

```go
if d.prepareWorkspaceFS != nil {
    if err := d.prepareWorkspaceFS(ctx, instance, req.ProjectRoot); err != nil {
        if strings.TrimSpace(instance) == "nexus-seatbelt" {
            fallbackCandidates := []string{"nexus-firecracker", "mvm", "default"}
            for _, fallback := range fallbackCandidates {
                if fallbackErr := d.prepareWorkspaceFS(ctx, fallback, req.ProjectRoot); fallbackErr != nil {
                    continue
                }
                if ws, ok := d.workspaces[req.WorkspaceID]; ok {
                    ws.instance = fallback
                }
                return nil
            }
        }
        if strings.Contains(strings.ToLower(err.Error()), "prepare /workspace mount failed") {
            return nil
        }
        d.mu.Lock()
        delete(d.workspaces, req.WorkspaceID)
        d.mu.Unlock()
        return err
    }
}
```

**After (new code):**

```go
if d.prepareWorkspaceFS != nil {
    if err := d.prepareWorkspaceFS(ctx, instance, req.ProjectRoot); err != nil {
        if strings.TrimSpace(instance) == "nexus-seatbelt" {
            fallbackCandidates := []string{"nexus-firecracker", "mvm", "default"}
            for _, fallback := range fallbackCandidates {
                if fallbackErr := d.prepareWorkspaceFS(ctx, fallback, req.ProjectRoot); fallbackErr == nil {
                    if ws, ok := d.workspaces[req.WorkspaceID]; ok {
                        ws.instance = fallback
                    }
                    return nil
                }
            }
        }
        d.mu.Lock()
        delete(d.workspaces, req.WorkspaceID)
        d.mu.Unlock()
        return fmt.Errorf("%w: %v", runtime.ErrWorkspaceMountFailed, err)
    }
}
```

- **Step 5: Update `ensureLocalRuntimeWorkspace` in handlers to tolerate mount failure**

In `packages/nexus/pkg/handlers/workspace_manager.go`, replace:

```go
err = driver.Create(ctx, req)
if err != nil && !strings.Contains(err.Error(), "already exists") {
    return &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime create failed: %v", err)}
}
```

With:

```go
err = driver.Create(ctx, req)
if err != nil {
    if strings.Contains(err.Error(), "already exists") {
        return nil
    }
    if errors.Is(err, runtime.ErrWorkspaceMountFailed) {
        return nil
    }
    return &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: fmt.Sprintf("runtime create failed: %v", err)}
}
```

Add `"errors"` to the imports of `workspace_manager.go`. Add `"github.com/inizio/nexus/packages/nexus/pkg/runtime"` if not already imported.

- **Step 6: Run all affected tests**

```bash
cd packages/nexus && go test ./pkg/runtime/seatbelt/... ./pkg/handlers/... -v
```

Expected: PASS — all existing tests pass, new sentinel tests pass. The `TestCreateFallsBackToDefaultInstanceWhenSeatbeltMountPrepareFails` test should still pass since fallbacks are still attempted before the sentinel error is returned.

- **Step 7: Build the whole module**

```bash
cd packages/nexus && go build ./...
```

Expected: no errors.

- **Step 8: Commit**

```bash
git add packages/nexus/pkg/runtime/driver.go packages/nexus/pkg/runtime/seatbelt/ packages/nexus/pkg/handlers/
git commit -m "feat(runtime): add ErrWorkspaceMountFailed sentinel, remove string-match error swallow"
```

---

## Task 5: Firecracker — Registry-Driven Auth Bundle and Bootstrap

**Problem:** `firecracker/driver.go` has the same hardcoded credential file paths and CLI availability struct as seatbelt. Firecracker cannot use live symlinks (no Lima automount), so the tar bundle is kept — but the paths and install packages are now derived from the registry.

**Files:**

- Modify: `packages/nexus/pkg/runtime/firecracker/driver.go`
- Modify: `packages/nexus/pkg/runtime/firecracker/driver_test.go`
- **Step 1: Add failing test**

Add to `packages/nexus/pkg/runtime/firecracker/driver_test.go`:

```go
func TestBuildHostAuthBundleIncludesRegistryPaths(t *testing.T) {
	home := t.TempDir()

	credFile := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(credFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credFile, []byte(`{"token":"test"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	encoded, err := buildHostAuthBundleFromHome(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if encoded == "" {
		t.Fatal("expected non-empty bundle when cred files exist")
	}

	raw, decErr := base64.StdEncoding.DecodeString(encoded)
	if decErr != nil {
		t.Fatalf("bundle is not valid base64: %v", decErr)
	}

	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("bundle is not gzip: %v", err)
	}
	tr := tar.NewReader(gr)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar read error: %v", err)
		}
		if strings.HasSuffix(hdr.Name, ".credentials.json") {
			found = true
		}
	}
	if !found {
		t.Fatal("bundle does not contain .credentials.json from registry")
	}
}

func TestBuildGuestCLIBootstrapCommandIncludesRegistryPackages(t *testing.T) {
	cmd := buildGuestCLIBootstrapCommand()
	for _, pkg := range agentprofile.AllInstallPkgs() {
		if !strings.Contains(cmd, pkg) {
			t.Fatalf("bootstrap command missing install package %q", pkg)
		}
	}
}
```

- **Step 2: Run failing tests**

```bash
cd packages/nexus && go test ./pkg/runtime/firecracker/... -run "TestBuildHostAuthBundle|TestBuildGuestCLIBootstrapCommand" -v
```

Expected: FAIL — `buildHostAuthBundleFromHome` does not exist yet; `buildGuestCLIBootstrapCommand` takes different args.

- **Step 3: Refactor firecracker bootstrap functions**

In `packages/nexus/pkg/runtime/firecracker/driver.go`:

**Replace** `buildHostAuthBundle()` (which hardcodes paths) with:

```go
func buildHostAuthBundle() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return "", nil
	}
	return buildHostAuthBundleFromHome(home)
}

func buildHostAuthBundleFromHome(home string) (string, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)

	added := 0
	for _, cf := range agentprofile.AllCredFiles() {
		src := filepath.Join(home, cf)
		if err := addPathToTar(tw, home, src); err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return "", err
		}
		if _, statErr := os.Stat(src); statErr == nil {
			added++
		}
	}

	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}

	if added == 0 || buf.Len() == 0 {
		return "", nil
	}

	const maxBundleBytes = 4 * 1024 * 1024
	if buf.Len() > maxBundleBytes {
		return "", nil
	}

	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}
```

**Replace** `detectHostCLIAvailability()` + `hostCLIAvailability` struct with a simple boolean map:

```go
func detectHostCLIAvailability() map[string]bool {
	out := make(map[string]bool)
	for _, bin := range agentprofile.AllBinaries() {
		_, err := exec.LookPath(bin)
		out[bin] = err == nil
	}
	return out
}
```

**Replace** `buildGuestCLIBootstrapCommand(hostCLI hostCLIAvailability)` (which conditionally includes packages based on availability) with a version that always installs all registry packages:

```go
func buildGuestCLIBootstrapCommand() string {
	parts := []string{
		"set -e",
		"mkdir -p ~/.config ~/.local/share",
		`if [ -n "${NEXUS_HOST_AUTH_BUNDLE:-}" ]; then ` +
			`(printf '%s' "$NEXUS_HOST_AUTH_BUNDLE" | base64 -d 2>/dev/null || printf '%s' "$NEXUS_HOST_AUTH_BUNDLE" | base64 -D 2>/dev/null) >/tmp/nexus-auth.tar.gz && ` +
			`tar -xzf /tmp/nexus-auth.tar.gz -C "$HOME" >/dev/null 2>&1 || true; ` +
			`rm -f /tmp/nexus-auth.tar.gz >/dev/null 2>&1 || true; fi`,
		`if command -v npm >/dev/null 2>&1; then NPM_BIN=$(npm bin -g 2>/dev/null || true); if [ -n "$NPM_BIN" ] && [ -d "$NPM_BIN" ]; then export PATH="$NPM_BIN:$PATH"; fi; fi`,
	}

	pkgs := agentprofile.AllInstallPkgs()
	if len(pkgs) > 0 {
		joined := strings.Join(pkgs, " ")
		parts = append(parts, "if command -v npm >/dev/null 2>&1; then npm i -g "+joined+" >/dev/null 2>&1 || true; fi")
	}

	for _, bin := range agentprofile.AllBinaries() {
		parts = append(parts, "command -v "+bin+" >/dev/null 2>&1")
	}

	return strings.Join(parts, "; ")
}
```

Update the call site in `bootstrapGuestToolingAndAuth` to remove the `hostCLI` dependency:

```go
func (d *Driver) bootstrapGuestToolingAndAuth(ctx context.Context, workspaceID string, authBundle string) error {
	if strings.TrimSpace(authBundle) == "" {
		return nil
	}

	conn, err := d.waitForAgentConn(ctx, workspaceID, 30*time.Second)
	if err != nil {
		return fmt.Errorf("bootstrap firecracker guest agent connection failed: %w", err)
	}
	defer conn.Close()

	client := NewAgentClient(conn)
	env := []string{"NEXUS_HOST_AUTH_BUNDLE=" + authBundle}

	request := ExecRequest{
		ID:      fmt.Sprintf("bootstrap-%d", time.Now().UnixNano()),
		Command: "sh",
		Args:    []string{"-lc", buildGuestCLIBootstrapCommand()},
		WorkDir: "/workspace",
		Env:     env,
	}
	result, execErr := client.Exec(ctx, request)
	if execErr != nil {
		return fmt.Errorf("bootstrap firecracker guest tooling failed: %w", execErr)
	}
	if result.ExitCode != 0 {
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = strings.TrimSpace(result.Stdout)
		}
		if detail == "" {
			detail = fmt.Sprintf("exit code %d", result.ExitCode)
		}
		return fmt.Errorf("bootstrap firecracker guest tooling failed: %s", detail)
	}

	return nil
}
```

Update the `Create` call site:

```go
authBundle, err := buildHostAuthBundle()
if err != nil {
    return fmt.Errorf("prepare host auth bundle: %w", err)
}
if shouldBootstrapGuestTooling(req.Options) {
    if err := d.bootstrapGuestToolingAndAuth(ctx, req.WorkspaceID, authBundle); err != nil {
        return err
    }
}
```

Add to imports: `"github.com/inizio/nexus/packages/nexus/pkg/agentprofile"`.

- **Step 4: Run firecracker tests**

```bash
cd packages/nexus && go test ./pkg/runtime/firecracker/... -v
```

Expected: PASS — all existing tests pass, new bundle/bootstrap tests pass.

- **Step 5: Build and run all tests**

```bash
cd packages/nexus && go build ./... && go test ./...
```

Expected: PASS everywhere, no compilation errors.

- **Step 6: Commit**

```bash
git add packages/nexus/pkg/runtime/firecracker/
git commit -m "feat(firecracker): drive auth bundle and bootstrap from agentprofile registry"
```

---

## Adding a New Agent (Verification)

After these tasks, adding support for a new agent (e.g., `amp`) is a single registry entry:

```go
// packages/nexus/pkg/agentprofile/registry.go — add to registry slice:
{
    Name:    "amp",
    Binary:  "amp",
    EnvVars: []string{"ANTHROPIC_API_KEY", "CLAUDE_API_KEY"},
    CredFiles: []string{
        ".amp/config.json",
    },
    InstallPkg: "@sourcegraph/amp",
},
```

This automatically:

- Wires `RelayEnv("amp", value)` to inject `ANTHROPIC_API_KEY`
- Includes `~/.amp/config.json` in firecracker's tar bundle
- Creates a live symlink at `$GUEST_HOME/.amp/config.json` → `$HOST_HOME/.amp/config.json` in seatbelt
- Installs `@sourcegraph/amp` during Lima bootstrap
- Verifies `amp --version` after install

---

## Self-Review

**Spec coverage:**

- ✅ Workspace mount robust — `ErrWorkspaceMountFailed` sentinel replaces string-match swallow (Task 4)
- ✅ Agent auth forward robust — live symlinks replace stale tar bundle for seatbelt (Task 3); firecracker bundle paths from registry (Task 5)
- ✅ Adding new agent is trivial — one registry entry in `agentprofile/registry.go` drives all four consumers (Task 1)
- ✅ `RelayEnv` registry-driven — no more switch statement (Task 2)

**Placeholder scan:** None found.

**Type consistency:**

- `agentprofile.Profile` used consistently across all tasks
- `runtime.ErrWorkspaceMountFailed` defined in Task 4 Step 3, used in Task 4 Steps 4–5
- `buildHostAuthBundleFromHome(home string)` defined in Task 5 Step 3, tested in Task 5 Step 1
- `buildGuestCLIBootstrapCommand()` (no args) defined in Task 5 Step 3, tested in Task 5 Step 1
- `bootstrapGuestToolingAndAuth(ctx, workspaceID, authBundle)` (3 args) defined in Task 5 Step 3, call site updated same step

