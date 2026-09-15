package cli

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// herdrColdBuildFloor is the absolute regression guard for
// herdrWorktreeCreateTimeout, grounded in measurements rather than a round
// number picked in isolation.
//
// Measured live on this host on 2026-08-31, through the UNBOUNDED
// `nexus3 create --file` path (no herdrWorktreeCreateTimeout in effect), with
// a cold buildkit LAYER cache (caches/buildkit.ext4 wiped first — a cold
// fingerprint over a warm layer cache is fast and does not reproduce this):
//   - a mid-size Next.js monorepo: 120s
//   - nexus3:                      152s
//
// herdrWorktreeCreateTimeout must stay comfortably above the worse of the
// two (152s) or the exact self-sustaining cache-poison loop this slice fixes
// (build overruns bound -> SIGKILL -> unclean death -> dirty-marker wipe ->
// next attempt starts cold -> repeat) reopens. This is an ABSOLUTE floor: it
// exists because TestHerdrWorktreeCreateLockTimeout_ExceedsWorstCaseCreate
// only checks a RELATIVE invariant (lock > create) that a shrunk
// herdrWorktreeCreateTimeout still satisfies, so that test alone cannot catch
// a regression back toward the original, too-short 90s value.
const herdrColdBuildFloor = 152 * time.Second

func TestHerdrWorktreeCreateTimeout_AboveMeasuredColdBuildFloor(t *testing.T) {
	if herdrWorktreeCreateTimeout <= herdrColdBuildFloor {
		t.Fatalf("herdrWorktreeCreateTimeout (%v) must exceed the measured cold-build floor (%v; see herdrColdBuildFloor doc comment for the 2026-08-31 Next.js-monorepo/nexus3 measurements) — "+
			"a value at or below this reopens the self-sustaining buildkit cache-poison loop (build overruns bound -> SIGKILL -> unclean death -> dirty-marker wipe -> next attempt starts cold)",
			herdrWorktreeCreateTimeout, herdrColdBuildFloor)
	}
}

// TestHerdrWorktreeCreateLockTimeout_ExceedsWorstCaseCreate guards the
// invariant documented on herdrWorktreeCreateLockTimeout: a second concurrent
// caller waiting for the create-intent lock must never give up before the
// first caller's worst-case total time (herdrWorktreeCreateTimeout).
func TestHerdrWorktreeCreateLockTimeout_ExceedsWorstCaseCreate(t *testing.T) {
	if herdrWorktreeCreateLockTimeout <= herdrWorktreeCreateTimeout {
		t.Fatalf("herdrWorktreeCreateLockTimeout (%v) must exceed herdrWorktreeCreateTimeout (%v)",
			herdrWorktreeCreateLockTimeout, herdrWorktreeCreateTimeout)
	}
}

// TestWorktreeSandboxCreateSubprocess_DefaultKillSemantics guards that the
// "sandbox create" subprocess at cmd_herdr_plugin.go:486 carries Go's default
// exec.CommandContext kill semantics: WaitDelay==0, no custom Cancel. A
// non-zero WaitDelay enables a SIGTERM grace period that let the guest sync
// run while buildkitd was still writing, stamping the cache clean while torn
// (the self-sustaining cache-poison loop). That mechanism was deleted; this
// test ensures it is not reintroduced.
//
// The test exercises the PRODUCTION createFn closure at line 484-490 of
// cmd_herdr_plugin.go by calling runHerdrPlugin("worktree-sandbox") so a
// mutation at that site (cmd.WaitDelay = X after line 486) makes this go RED.
//
// MUTATION PROOF:
//
//	Mutation:  in the production createFn (cmd_herdr_plugin.go:487), add:
//	           cmd.WaitDelay = time.Second
//	Expected:  this test goes RED (captured.WaitDelay != 0)
//	Restore:   remove the line → GREEN
func TestWorktreeSandboxCreateSubprocess_DefaultKillSemantics(t *testing.T) {
	// Fake XDG_STATE_HOME so newSandboxService() creates a real FileStore in a
	// temp dir. store.DefaultRoot() returns XDG_STATE_HOME+"/nexus3" and
	// store.NewFileStore creates the directory, so no pre-mkdir needed.
	storeBase := t.TempDir()
	t.Setenv("XDG_STATE_HOME", storeBase)

	// resolveHerdrBin checks HERDR_BIN_PATH first; /bin/true is a valid path
	// that won't be called as a pane manager.
	t.Setenv("HERDR_BIN_PATH", "/bin/true")

	// herdrListWorktreeForWorkspaceFn: return a linked worktree so
	// herdrWorktreeSandbox proceeds through steps 1–6 to reach step 7.
	// worktreePath is a fresh temp dir: no nexus3.yaml → herdrResolveWorktreeImage
	// falls back to --image herdrDefaultImage; no .git → worktreeCommonGitDir
	// returns "" and the git-config step is skipped.
	worktreePath := t.TempDir()
	swapListFn(t, stubWorktreeList{
		info: linkedWorktreeInfo("test-wid", "src-wid", "test-branch", worktreePath),
	}.fn())

	// Replace herdrExecCommandContext: capture the cmd built for "sandbox
	// create" and return a no-op (/bin/true) so Run() does not start a real
	// sandbox. Any other seam call (e.g. step 9 open-pane) also gets a no-op.
	var captured *exec.Cmd
	origSeam := herdrExecCommandContext
	herdrExecCommandContext = func(ctx context.Context, name string, arg ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "/bin/true")
		if len(arg) >= 2 && arg[0] == "sandbox" && arg[1] == "create" {
			captured = cmd
		}
		return cmd
	}
	t.Cleanup(func() { herdrExecCommandContext = origSeam })

	var buf strings.Builder
	out := NewOutput(&buf, &buf, false)
	// Error is expected: step 8 (getFn/svc.Get) fails because no sandbox was
	// actually created. Step 7 (createFn) has already been invoked at this
	// point and the cmd captured.
	_ = runHerdrPlugin(context.Background(), []string{"worktree-sandbox", "test-wid"}, out)

	if captured == nil {
		t.Fatal("sandbox create subprocess was not captured via herdrExecCommandContext: " +
			"step 7 was not reached — verify that the worktree-sandbox case still " +
			"calls createFn via herdrExecCommandContext before the getFn step")
	}
	if captured.WaitDelay != 0 {
		t.Errorf("cmd.WaitDelay = %v, want 0 — a non-zero WaitDelay enables "+
			"SIGTERM grace that allows dirty cache writes during context "+
			"cancellation, re-opening the buildkit cache-poison loop "+
			"(cmd_herdr_plugin.go:486-489)", captured.WaitDelay)
	}

	// cmd.Cancel must ALSO be the exec.CommandContext default (SIGKILL), and
	// this assertion is load-bearing rather than decorative.
	//
	// It is tempting to argue that WaitDelay==0 alone is sufficient because it
	// "disables the grace period". That is backwards. Per os/exec's WaitDelay
	// docs, WaitDelay is precisely what BOUNDS the wait for "a child process
	// that fails to exit — perhaps because it ignored or failed to receive a
	// shutdown signal from a Cancel function", after which the child "will be
	// terminated using os.Process.Kill". So WaitDelay==0 does not remove a
	// grace period; it removes the upper bound on one. A custom Cancel that
	// sends SIGTERM with WaitDelay==0 lets the create subprocess shut down
	// gracefully on its own timetable, unbounded — which is exactly the
	// deleted mechanism that let the guest sync run while buildkitd was still
	// writing and stamp a torn cache disk clean.
	//
	// func values are not comparable, so compare code pointers against a
	// reference Cmd built by exec.CommandContext: both closures originate from
	// the same func literal inside os/exec, so their code pointers match,
	// while any hand-written Cancel is a different literal.
	if captured.Cancel == nil {
		t.Errorf("cmd.Cancel is nil, want the exec.CommandContext default — " +
			"a nil Cancel means context cancellation never signals the child at all")
	} else {
		refCmd := exec.CommandContext(context.Background(), "/bin/true")
		wantPC := reflect.ValueOf(refCmd.Cancel).Pointer()
		gotPC := reflect.ValueOf(captured.Cancel).Pointer()
		if gotPC != wantPC {
			t.Errorf("cmd.Cancel is a custom func, want the exec.CommandContext "+
				"default (os.Process.Kill) — a custom Cancel (e.g. SIGTERM) with "+
				"WaitDelay==0 restores unbounded graceful shutdown of the create "+
				"subprocess, re-opening the buildkit cache-poison loop "+
				"(cmd_herdr_plugin.go:486-489); got pc=%#x want pc=%#x", gotPC, wantPC)
		}
	}

	// SCOPE LIMIT, stated honestly: this test captures the *exec.Cmd through
	// the herdrExecCommandContext seam, so it guards the fields production sets
	// on that Cmd AFTER the seam returns. It does NOT guard against production
	// abandoning the seam itself (e.g. switching to exec.Command with a
	// hand-rolled Cancel), because the test supplies the seam. The
	// `captured == nil` fatal above is what catches that shape: it fires if the
	// create call stops going through the seam.
}
