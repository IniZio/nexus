package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// Pane-first provisioning. Building a worktree sandbox pulls an image, writes an
// ext4 disk, and boots a VM — minutes on a cold cache — and until this change
// none of that had a surface. The action ran the build inline in its own
// process, so the operator's new worktree workspace showed a host-path shell and
// nothing else, and a failure went to `herdr plugin log list` and nowhere an
// operator looks.
//
// The fix has two halves and both are load-bearing:
//
//	ORDERING     — the pane opens FIRST and the build runs inside it.
//	PERSISTENCE  — the pane stays open on a non-zero exit, so the error is still
//	               on screen when someone comes to read it.
//
// These tests run the REAL plugin scripts against a stub shim, the same
// mechanism herdr_scripts_test.go uses. They cannot boot a VM; what they pin is
// the wiring, which is where both halves of the defect actually lived.

// worktreePaneEnv copies the plugin scripts next to stub binaries that record
// their argv.
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

	// The shim sits one level above bin/, exactly as in the real plugin.
	shim := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + e.shimLog + "\nexit " +
		itoa(shimExit) + "\n"
	if err := os.WriteFile(filepath.Join(root, "nexus3-shim.sh"), []byte(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	herdr := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + e.herdrLog + "\nexit 0\n"
	if err := os.WriteFile(e.herdrBin, []byte(herdr), 0o755); err != nil {
		t.Fatal(err)
	}
	return e
}

func itoa(n int) string { return strconv.Itoa(n) }

// runScript executes one of the copied scripts with the given args and env,
// feeding it an immediate EOF on stdin so a `read -r _` hold returns instead of
// blocking the test forever. Returns combined output and exit code.
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

// TestOpenPaneScript_WorktreeSandboxOpensPaneFirst is the ORDERING half.
//
// The action must open the worktree-sandbox pane and must NOT run the build
// itself. If it calls the nexus3 shim directly, the build is happening in a
// process with no surface — which is the defect.
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

// TestPaneScript_WorktreeSandboxRoutesToNexus3Verb is the routing invariant that
// moved out of TestHerdrManifestDispatch when the direct call left open-pane.sh.
// The pane is now where `nexus3 herdr worktree-sandbox` is actually invoked, and
// if it stops being invoked there the whole flow silently does nothing.
func TestPaneScript_WorktreeSandboxRoutesToNexus3Verb(t *testing.T) {
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

// TestPaneScript_WorktreeSandboxAutoFlag pins that the worktree.created event
// path keeps its --auto predicate. Losing it would make the hook bind a sandbox
// for EVERY new worktree workspace, in every repo, rather than only where a
// sibling workspace is already nexus3-bound.
func TestPaneScript_WorktreeSandboxAutoFlag(t *testing.T) {
	e := newWorktreePaneEnv(t, 0)
	_, _ = e.runScript(t, "pane.sh", []string{"worktree-sandbox"},
		[]string{"HERDR_WORKSPACE_ID=w42", "NEXUS3_WORKTREE_AUTO=1"})
	shim := strings.TrimSpace(e.shimArgv(t))
	if shim != "herdr worktree-sandbox --auto w42" {
		t.Errorf("pane.sh invoked %q; want %q", shim, "herdr worktree-sandbox --auto w42")
	}
}

// TestPaneScript_WorktreeSandboxHoldsPaneOpenOnFailure is the PERSISTENCE half.
//
// The stub shim exits 1. The pane must block on Enter before closing, so the
// error is still on screen. The test supplies an immediate EOF, so a script that
// holds correctly returns promptly here while still holding for a real operator
// whose stdin is a live terminal.
//
// The discriminator is the prompt itself: no prompt means no hold means the pane
// closed on the error and took it with it.
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

// TestPaneScript_WorktreeSandboxSucceedsWithoutHolding is the mirror: a
// successful build must NOT leave a pane demanding Enter. Without this, "always
// hold" passes the persistence test above while making every successful
// provisioning leave a dead pane behind.
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

// TestPaneScript_WorktreeSandboxRefusesWithoutWorkspaceID is the fail-closed
// rail for this script. An absent HERDR_WORKSPACE_ID is not a reason to guess or
// to silently do nothing: `nexus3 herdr worktree-sandbox ""` would bind the
// wrong thing or fail obscurely.
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

// TestOnWorktreeCreated_OpensProvisioningPane covers the event path, which is
// the one the defect was actually reported on: herdr fires worktree.created and
// the hook used to build a VM for minutes with no surface at all.
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
	if !strings.Contains(herdr, "NEXUS3_WORKTREE_AUTO=1") {
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

// TestOnWorktreeCreated_FallsBackWhenPaneCannotOpen pins the deliberate
// fail-OPEN here, which is the one place in this slice that is not a refusal
// and is worth naming as such.
//
// A pane is a SIGNAL. If herdr cannot give us one, the right answer is still to
// provision the worktree — the operator loses visibility, not their sandbox.
// This is the opposite call from the submission-confirmation guard, where the
// missing input IS the thing being checked and refusing is the only safe answer.
func TestOnWorktreeCreated_FallsBackWhenPaneCannotOpen(t *testing.T) {
	e := newWorktreePaneEnv(t, 0)
	// Make `herdr plugin pane open` fail.
	failing := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + e.herdrLog + "\nexit 1\n"
	if err := os.WriteFile(e.herdrBin, []byte(failing), 0o755); err != nil {
		t.Fatal(err)
	}

	out, _ := e.runScript(t, "on-worktree-created.sh", nil,
		[]string{"HERDR_WORKSPACE_ID=w42"})

	shim := strings.TrimSpace(e.shimArgv(t))
	if shim != "herdr worktree-sandbox --auto w42" {
		t.Errorf("did not fall back to inline provisioning when the pane could not open; "+
			"shim argv = %q. A missing pane must cost visibility, not the sandbox.", shim)
	}
	if !strings.Contains(out, "no progress will be visible") {
		t.Errorf("fell back silently — the operator has no way to know why nothing appeared:\n%s", out)
	}
}

// TestHerdrWorktreeSandbox_paneFailureSurfacesInAutoMode closes the gap that
// buried the reported failure.
//
// Step 9 used to return nil under failSafe (auto/conditional mode) when the
// guest pane could not be opened. The reasoning was that the sandbox and binding
// are committed and recoverable — which is TRUE, and beside the point. The
// consequence was a live VM, no pane, and the only record of it a line in
// `herdr plugin log list`:
//
//	exit 1  workspace_not_found
//	worktree-sandbox: open guest pane: space-create: open shell pane: exit status 1
//
// Now that provisioning runs inside a pane that holds itself open on a non-zero
// exit, returning the error is what puts that message in front of the operator.
// auto mode is the mode the worktree.created hook uses, so it is exactly the
// mode that must not swallow it.
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
			// Fail ONLY `plugin pane open`. Everything else succeeds, so the
			// sandbox and the binding are genuinely committed and the pane is
			// the only thing that went wrong — the exact reported shape.
			old := herdrExecCommandContext
			herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
				if len(args) >= 3 && args[0] == "plugin" && args[1] == "pane" && args[2] == "open" {
					return exec.CommandContext(ctx, "sh", "-c", "exit 1")
				}
				return exec.CommandContext(ctx, "sh", "-c", "exit 0")
			}
			t.Cleanup(func() { herdrExecCommandContext = old })

			// auto mode's repo predicate needs a sibling binding whose RepoRoot
			// matches this worktree's repo, or it short-circuits at step 5 and
			// never reaches step 9 — which would make this test vacuous.
			if tc.auto {
				seedBindingWithRepoRoot(t, root, "w-src", "repo/sibling", "/repo")
			}

			var w strings.Builder
			err := herdrWorktreeSandbox(
				context.Background(), "w-pane", &w, root,
				true /*openPane*/, tc.conditional, tc.auto, false,
				func(_ context.Context, _, _, _, _ string, _ []string, _ []string, _ string, _ domain.EgressPathPolicies, _ bool) error {
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
