package cli

// Tests for stderr capture in the worktree-sandbox createFn.

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestWorktreeSandboxCreate_StderrInError verifies that when the sandbox create
// subprocess writes "error: ..." to stderr and exits 1, the error returned by
// the production createFn closure contains that text.
//
// MUTATION PROOF: remove the stderrBuf capture in the production createFn →
// the error no longer contains "mke2fs not found" → this test goes RED.
func TestWorktreeSandboxCreate_StderrInError(t *testing.T) {
	storeBase := t.TempDir()
	t.Setenv("XDG_STATE_HOME", storeBase)
	t.Setenv("HERDR_BIN_PATH", "/bin/true")

	worktreePath := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("w-stderr", "src-wid", "test-branch", worktreePath),
	}.fn())

	// Return a fake child that writes an error line to stderr and exits 1.
	origSeam := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		if len(arg) >= 2 && arg[0] == "sandbox" && arg[1] == "create" {
			return exec.CommandContext(ctx, "sh", "-c",
				`echo "error: volumestore: mke2fs not found on PATH (install e2fsprogs)" >&2; exit 1`)
		}
		return exec.CommandContext(ctx, "/bin/true")
	}
	t.Cleanup(func() { herdrExecCommandContext = origSeam })

	var buf strings.Builder
	out := NewOutput(&buf, &buf, false)
	err := runHerdrPlugin(context.Background(), []string{"worktree-sandbox", "w-stderr"}, out)
	if err == nil {
		t.Fatal("expected error from failing sandbox create, got nil")
	}
	if !strings.Contains(err.Error(), "mke2fs not found") {
		t.Errorf("error does not contain stderr text: %v", err)
	}
}

// TestSandboxCreateLastErrors_ErrorLines verifies that lines starting with
// "error:" are selected over the general last-N-lines fallback.
func TestSandboxCreateLastErrors_ErrorLines(t *testing.T) {
	stderr := "info: starting mke2fs\ninfo: formatting disk\nerror: volumestore: mke2fs not found on PATH (install e2fsprogs)\ninfo: done"
	got := sandboxCreateLastErrors(stderr)
	if !strings.Contains(got, "mke2fs not found") {
		t.Errorf("got %q; want it to contain mke2fs not found", got)
	}
	if strings.Contains(got, "info:") {
		t.Errorf("got %q; should not contain info: lines", got)
	}
}

// TestSandboxCreateLastErrors_FallbackLast20 verifies that when no "error:"
// lines exist the last 20 non-empty lines are returned.
func TestSandboxCreateLastErrors_FallbackLast20(t *testing.T) {
	var lines []string
	for i := 0; i < 25; i++ {
		lines = append(lines, "line "+string(rune('A'+i)))
	}
	stderr := strings.Join(lines, "\n")
	got := sandboxCreateLastErrors(stderr)
	gotLines := strings.Split(got, "\n")
	if len(gotLines) != 20 {
		t.Errorf("got %d lines; want 20", len(gotLines))
	}
	if !strings.Contains(got, "line Y") || strings.Contains(got, "line A") {
		t.Errorf("expected last 20 lines; got: %q", got)
	}
}
