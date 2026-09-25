package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

// Pane-first provisioning: ORDERING (pane opens first, build inside) +
// PERSISTENCE (pane holds on failure). Tests wire the script stubs.

type worktreePaneEnv struct {
	binDir   string
	shimLog  string
	herdrLog string
	herdrBin string
}

func newWorktreePaneEnv(t *testing.T, shimExit int) *worktreePaneEnv {
	t.Helper()
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pane.sh", "open-pane.sh", "on-worktree-created.sh"} {
		src, err := os.ReadFile(filepath.Join("..", "..", "plugins", "herdr", "bin", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(binDir, name), src, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	e := &worktreePaneEnv{
		binDir:   binDir,
		shimLog:  filepath.Join(root, "shim.argv"),
		herdrLog: filepath.Join(root, "herdr.argv"),
		herdrBin: filepath.Join(root, "herdr"),
	}

	shim := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + e.shimLog + "\nexit " +
		itoa(shimExit) + "\n"
	if err := os.WriteFile(filepath.Join(root, "nexus-shim.sh"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	herdr := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + e.herdrLog + "\nexit 0\n"
	if err := os.WriteFile(e.herdrBin, []byte(herdr), 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

func itoa(n int) string { return strconv.Itoa(n) }

func (e *worktreePaneEnv) runScript(t *testing.T, name string, args []string, env []string) (string, int) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{filepath.Join(e.binDir, name)}, args...)...)
	cmd.Env = append(os.Environ(), append([]string{"HERDR_BIN_PATH=" + e.herdrBin}, env...)...)
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("run %s: %v", name, err)
		}
		code = ee.ExitCode()
	}
	return string(out), code
}

func (e *worktreePaneEnv) shimArgv(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.shimLog)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (e *worktreePaneEnv) herdrArgv(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.herdrLog)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ORDERING half: pane opens, build inside, shim NOT called.
func TestOpenPaneScript_WorktreeSandboxOpensPaneFirst(t *testing.T) {
	e := newWorktreePaneEnv(t, 0)
	_, code := e.runScript(t, "open-pane.sh", []string{"worktree-sandbox"},
		[]string{"HERDR_WORKSPACE_ID=w42"})
	if code != 0 {
		t.Errorf("open-pane.sh worktree-sandbox exited %d, want 0", code)
	}

	herdr := e.herdrArgv(t)
	if !strings.Contains(herdr, "plugin pane open") {
		t.Errorf("action did not open a pane; herdr argv:\n%s", herdr)
	}
	if !strings.Contains(herdr, "--entrypoint worktree-sandbox") {
		t.Errorf("action opened the wrong entrypoint; herdr argv:\n%s", herdr)
	}
	if !strings.Contains(herdr, "--workspace w42") {
		t.Errorf("action did not target the focused workspace; herdr argv:\n%s", herdr)
	}

	if shim := e.shimArgv(t); strings.Contains(shim, "worktree-sandbox") {
		t.Errorf("action ran the provisioning itself instead of delegating to the pane — "+
			"the build would happen with no visible surface. shim argv:\n%s", shim)
	}
}

// Routing: pane invokes `nexus herdr worktree-sandbox` verb.
func TestPaneScript_WorktreeSandboxRoutesToNexusVerb(t *testing.T) {
	e := newWorktreePaneEnv(t, 0)
	_, code := e.runScript(t, "pane.sh", []string{"worktree-sandbox"},
		[]string{"HERDR_WORKSPACE_ID=w42"})
	if code != 0 {
		t.Errorf("pane.sh worktree-sandbox exited %d, want 0", code)
	}
	shim := strings.TrimSpace(e.shimArgv(t))
	if shim != "herdr worktree-sandbox w42" {
		t.Errorf("pane.sh invoked %q; want %q", shim, "herdr worktree-sandbox w42")
	}
	// The verb must be one the CLI actually knows, or the pane fails at runtime.
	if _, known := herdrGroupVerbToPluginSub("worktree-sandbox"); !known {
		t.Error("worktree-sandbox is not a known verb in herdrGroupVerbToPluginSub")
	}
}

// --auto predicate: worktree.created event must keep it.
func TestPaneScript_WorktreeSandboxAutoFlag(t *testing.T) {
	e := newWorktreePaneEnv(t, 0)
	_, _ = e.runScript(t, "pane.sh", []string{"worktree-sandbox"},
		[]string{"HERDR_WORKSPACE_ID=w42", "NEXUS_WORKTREE_AUTO=1"})
	shim := strings.TrimSpace(e.shimArgv(t))
	if shim != "herdr worktree-sandbox --auto w42" {
		t.Errorf("pane.sh invoked %q; want %q", shim, "herdr worktree-sandbox --auto w42")
	}
}

// PERSISTENCE half: pane holds on failure via "Press Enter to close" prompt.
func TestPaneScript_WorktreeSandboxHoldsPaneOpenOnFailure(t *testing.T) {
	e := newWorktreePaneEnv(t, 1)
	out, code := e.runScript(t, "pane.sh", []string{"worktree-sandbox"},
		[]string{"HERDR_WORKSPACE_ID=w42"})

	if code != 1 {
		t.Errorf("pane.sh exited %d; want 1 (the provisioning failure must propagate)", code)
	}
	if !strings.Contains(out, "Press Enter to close") {
		t.Errorf("pane did not hold open on failure — the error closes with the pane "+
			"and survives only in `herdr plugin log list`. output:\n%s", out)
	}
	if !strings.Contains(out, "FAILED") {
		t.Errorf("pane did not say the provisioning failed. output:\n%s", out)
	}
}

// Success: pane closes without holding (no dead pane).
func TestPaneScript_WorktreeSandboxSucceedsWithoutHolding(t *testing.T) {
	e := newWorktreePaneEnv(t, 0)
	out, code := e.runScript(t, "pane.sh", []string{"worktree-sandbox"},
		[]string{"HERDR_WORKSPACE_ID=w42"})
	if code != 0 {
		t.Errorf("exited %d, want 0", code)
	}
	if strings.Contains(out, "Press Enter to close") {
		t.Errorf("pane held open on SUCCESS; every provisioned worktree would leave a "+
			"pane waiting on a keypress. output:\n%s", out)
	}
}

// Fail-closed: refuse without HERDR_WORKSPACE_ID.
func TestPaneScript_WorktreeSandboxRefusesWithoutWorkspaceID(t *testing.T) {
	e := newWorktreePaneEnv(t, 0)
	out, code := e.runScript(t, "pane.sh", []string{"worktree-sandbox"},
		[]string{"HERDR_WORKSPACE_ID="})
	if code == 0 {
		t.Errorf("exited 0 with no workspace ID; want a refusal. output:\n%s", out)
	}
	if shim := e.shimArgv(t); shim != "" {
		t.Errorf("ran provisioning without knowing which worktree: %q", shim)
	}
	if !strings.Contains(out, "Press Enter to close") {
		t.Errorf("refusal closed the pane, so the operator never sees it. output:\n%s", out)
	}
}

func TestOnWorktreeCreated_OpensProvisioningPane(t *testing.T) {
	e := newWorktreePaneEnv(t, 0)
	_, code := e.runScript(t, "on-worktree-created.sh", nil,
		[]string{"HERDR_WORKSPACE_ID=w42"})
	if code != 0 {
		t.Errorf("hook exited %d, want 0", code)
	}
	herdr := e.herdrArgv(t)
	if !strings.Contains(herdr, "--entrypoint worktree-sandbox") {
		t.Errorf("hook did not open the provisioning pane; herdr argv:\n%s", herdr)
	}
	if !strings.Contains(herdr, "NEXUS_WORKTREE_AUTO=1") {
		t.Errorf("hook did not carry the --auto predicate into the pane — it would bind "+
			"a sandbox for every new worktree in every repo. herdr argv:\n%s", herdr)
	}
	if !strings.Contains(herdr, "--no-focus") {
		t.Errorf("hook stole focus; herdr argv:\n%s", herdr)
	}
	if shim := e.shimArgv(t); strings.Contains(shim, "worktree-sandbox") {
		t.Errorf("hook provisioned inline despite the pane opening cleanly:\n%s", shim)
	}
}

// Fail-OPEN: missing pane → fallback to inline (operator loses visibility, not sandbox).
func TestOnWorktreeCreated_FallsBackWhenPaneCannotOpen(t *testing.T) {
	e := newWorktreePaneEnv(t, 0)
	failing := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + e.herdrLog + "\nexit 1\n"
	if err := os.WriteFile(e.herdrBin, []byte(failing), 0o755); err != nil {
		t.Fatal(err)
	}

	out, _ := e.runScript(t, "on-worktree-created.sh", nil,
		[]string{"HERDR_WORKSPACE_ID=w42"})

	shimLines := strings.Split(strings.TrimSpace(e.shimArgv(t)), "\n")
	if len(shimLines) < 1 || shimLines[0] != "herdr worktree-sandbox --auto w42" {
		t.Errorf("did not fall back to inline provisioning when the pane could not open; "+
			"first shim argv = %q. A missing pane must cost visibility, not the sandbox.", shimLines)
	}
	if len(shimLines) < 2 || shimLines[1] != "herdr focus-changed --workspace w42 --only-if-focused" {
		t.Errorf("focus-changed must trail provisioning in fallback; shim lines = %q", shimLines)
	}
	if !strings.Contains(out, "no progress will be visible") {
		t.Errorf("fell back silently — the operator has no way to know why nothing appeared:\n%s", out)
	}
}

// Auto-mode pane failure surfaces (was buried in herdr plugin log list).
func TestHerdrWorktreeSandbox_paneFailureSurfacesInAutoMode(t *testing.T) {
	for _, tc := range []struct {
		name              string
		conditional, auto bool
	}{
		{"auto mode (worktree.created hook)", false, true},
		{"explicit mode", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			swapListFn(t, stubWorktreeList{
				info: linkedWorktreeInfoAuto("w-pane", "feature/pane", "/work/pane", "/repo/.git"),
			}.fn())
			swapRenameFn(t, func(_ context.Context, _, _, _ string) error { return nil })

			t.Setenv("HERDR_BIN_PATH", "/nonexistent-herdr-for-testing")
			old := herdrExecCommandContext
			herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				if len(args) >= 3 && args[0] == "plugin" && args[1] == "pane" && args[2] == "open" {
					return exec.CommandContext(ctx, "sh", "-c", "exit 1")
				}
				return exec.CommandContext(ctx, "sh", "-c", "exit 0")
			}
			t.Cleanup(func() { herdrExecCommandContext = old })

			// Needs sibling binding for auto mode, else short-circuits (vacuous).
			if tc.auto {
				seedBindingWithRepoRoot(t, root, "w-src", "repo/sibling", "/repo")
			}

			var w strings.Builder
			err := herdrWorktreeSandbox(
				context.Background(), "w-pane", &w, root,
				true /*openPane*/, tc.conditional, tc.auto, false,
				func(_ context.Context, _, _, _, _ string, _ []string, _ []string, _ string, _ domain.EgressPathPolicies, _ domain.EgressMCPPolicies, _ bool) error {
					return nil
				},
				func(_ context.Context, _ string) (domain.Sandbox, error) {
					return domain.Sandbox{}, nil
				},
			)

			// Vacuity guard: if step 5 skipped, step 9 never ran.
			if strings.Contains(w.String(), "skipping") {
				t.Fatalf("short-circuited before step 9; this test proves nothing:\n%s", w.String())
			}
			if err == nil {
				t.Fatalf("guest pane failed but worktree-sandbox returned nil — the error goes "+
					"only to the plugin log and the provisioning pane closes on it.\noutput:\n%s", w.String())
			}
			if !strings.Contains(err.Error(), "open guest pane") {
				t.Errorf("error does not name the pane failure: %v", err)
			}
			// The message must tell the operator the VM is NOT lost, or they
			// will tear down a perfectly good sandbox.
			if !strings.Contains(w.String(), "committed and reusable") {
				t.Errorf("output does not say the sandbox survived the pane failure:\n%s", w.String())
			}
		})
	}
}
