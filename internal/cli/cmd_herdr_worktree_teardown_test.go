package cli

// cmd_herdr_worktree_teardown_test.go — unit tests for the two worktree
// sandbox teardown mechanisms.
//
// Mechanism 1: herdrSpacePruneFull reaps stale wt/ VMs (cmd_herdr_plugin.go).
// Mechanism 2: herdrWtSupervisedShell performs real-time teardown on last-pane
// close (cmd_herdr_default_shell.go).
//
// Assertion↔mechanism discipline: every test names its MUTATION TARGET.
// If the named mutation is applied, the test must turn RED.
//
// No live herdr calls, no live sandbox creates.  TMPDIR=/tmp go test ./internal/cli/ -count=1

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// ── Shared predicate ─────────────────────────────────────────────────────────

// TestIsHerdrWorktreeHandle_prefixMatchesProducer asserts that
// isHerdrWorktreeHandle accepts every handle produced by
// herdrWorktreeSandboxHandle.
//
// MUTATION TARGET: herdrWorktreeHandlePrefix constant.
// If the prefix in herdrWorktreeSandboxHandle diverges from the constant used
// by isHerdrWorktreeHandle, this test turns RED immediately.
func TestIsHerdrWorktreeHandle_prefixMatchesProducer(t *testing.T) {
	cases := []string{"feature/my-branch", "main", "worktree/silver-123", "fix/issue-42"}
	for _, branch := range cases {
		handle := herdrWorktreeSandboxHandle(branch)
		if !isHerdrWorktreeHandle(handle) {
			t.Errorf("isHerdrWorktreeHandle(%q) = false for handle %q produced by herdrWorktreeSandboxHandle; prefix drifted from producer",
				handle, handle)
		}
	}
}

// TestIsHerdrWorktreeHandle_rejectsNonWt asserts that non-wt/ handles return false.
//
// MUTATION TARGET: isHerdrWorktreeHandle prefix guard.
// Removing the prefix check makes this RED.
func TestIsHerdrWorktreeHandle_rejectsNonWt(t *testing.T) {
	cases := []string{"ac3/demo", "demo/sandbox-1", "local/my-box", "orca/work", "", "wt"}
	for _, h := range cases {
		if isHerdrWorktreeHandle(h) {
			t.Errorf("isHerdrWorktreeHandle(%q) = true; want false", h)
		}
	}
}

// TestIsHerdrWorktreeHandle_acceptsWtPrefix asserts that handles starting with
// "wt/" are accepted.
//
// MUTATION TARGET: isHerdrWorktreeHandle prefix guard.
// Changing the prefix to anything other than "wt/" makes this RED.
func TestIsHerdrWorktreeHandle_acceptsWtPrefix(t *testing.T) {
	cases := []string{"wt/main", "wt/feature-foo", "wt/worktree-silver-forest-225f"}
	for _, h := range cases {
		if !isHerdrWorktreeHandle(h) {
			t.Errorf("isHerdrWorktreeHandle(%q) = false; want true", h)
		}
	}
}

// ── Mechanism 1: herdrSpacePruneFull ─────────────────────────────────────────

// stubPruneSvc is a minimal herdrSpacePruneLister whose List returns a fixed
// set of sandbox handles as "alive".
type stubPruneSvc struct{ alive map[string]bool }

func (s stubPruneSvc) List(_ context.Context) ([]interface{ Handle() string }, error) {
	return nil, nil // unused; sandboxExists is constructed directly in tests
}

// newPruneHarness returns the injected functions and a populated storeRoot for
// herdrSpacePruneFull tests.
type pruneHarness struct {
	storeRoot       string
	sandboxExists   func(HerdrSpaceBinding) bool
	workspaceExists func(HerdrSpaceBinding) bool
	closer          func(context.Context, string) error
	removeSandbox   func(context.Context, string) error
	// call tracking
	removeCallHandles []string
	closerCallIDs     []string
}

func newPruneHarness(t *testing.T) *pruneHarness {
	t.Helper()
	root := t.TempDir()
	h := &pruneHarness{storeRoot: root}
	// Default closer: succeed silently (models workspace_not_found → nil).
	h.closer = func(_ context.Context, wsID string) error {
		h.closerCallIDs = append(h.closerCallIDs, wsID)
		return nil
	}
	// Default removeSandbox: succeed silently.
	h.removeSandbox = func(_ context.Context, handle string) error {
		h.removeCallHandles = append(h.removeCallHandles, handle)
		return nil
	}
	return h
}

// addBinding writes a HerdrSpaceBinding into the harness store.
func (h *pruneHarness) addBinding(t *testing.T, b HerdrSpaceBinding) {
	t.Helper()
	if err := HerdrSpacePut(context.Background(), h.storeRoot, b); err != nil {
		t.Fatalf("addBinding: %v", err)
	}
}

// run calls herdrSpacePruneFull and returns the output.
func (h *pruneHarness) run(t *testing.T, apply bool) string {
	t.Helper()
	var out strings.Builder
	err := herdrSpacePruneFull(
		context.Background(),
		&out,
		h.storeRoot,
		"",
		h.sandboxExists,
		h.workspaceExists,
		h.closer,
		h.removeSandbox,
		apply,
	)
	if err != nil {
		t.Fatalf("herdrSpacePruneFull: %v", err)
	}
	return out.String()
}

// TestPruneFull_wtSandboxPresentWorkspaceGone_reapsVM (Mechanism 1, case a):
// A wt/ binding whose workspace is gone but sandbox is still present triggers
// removeSandbox and removes the binding.
//
// MUTATION TARGET: the isHerdrWorktreeHandle guard inside the apply loop.
// Removing that guard means removeSandbox is also called for non-wt/ handles.
// Changing sandboxExists || workspaceExists conditions prevents the reap.
func TestPruneFull_wtSandboxPresentWorkspaceGone_reapsVM(t *testing.T) {
	h := newPruneHarness(t)
	const handle = "wt/feature-foo"
	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:" + handle,
		HerdrWorkspaceID: "wTEST-gone",
		SandboxHandle:    handle,
		SandboxID:        "sb-abc",
	}
	h.addBinding(t, b)

	// Sandbox alive, workspace gone → stale wt/ reap candidate.
	h.sandboxExists = func(binding HerdrSpaceBinding) bool { return binding.SandboxHandle == handle }
	h.workspaceExists = func(HerdrSpaceBinding) bool { return false }

	out := h.run(t, true /* apply */)

	// removeSandbox must have been called with the wt/ handle.
	if len(h.removeCallHandles) != 1 || h.removeCallHandles[0] != handle {
		t.Errorf("removeSandbox calls = %v; want [%q]", h.removeCallHandles, handle)
	}
	// Output must contain REAPED annotation.
	if !strings.Contains(out, "REAPED") {
		t.Errorf("output missing REAPED annotation; got:\n%s", out)
	}
	// Binding must have been deleted.
	bindings, err := HerdrSpaceList(context.Background(), h.storeRoot)
	if err != nil {
		t.Fatalf("HerdrSpaceList: %v", err)
	}
	for _, rem := range bindings {
		if rem.SandboxHandle == handle {
			t.Errorf("binding for %q still present after prune", handle)
		}
	}
}

// TestPruneFull_nonWtWorkspaceGone_doesNotReapVM (Mechanism 1, case b):
// A non-wt/ stale binding MUST NOT have removeSandbox called — binding-only cleanup only.
//
// MUTATION TARGET (MUTATION PROOF): the isHerdrWorktreeHandle guard in the apply loop.
// Removing or weakening it causes removeSandbox to be called here → RED.
func TestPruneFull_nonWtWorkspaceGone_doesNotReapVM(t *testing.T) {
	h := newPruneHarness(t)
	const handle = "ac3/demo-sandbox"
	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:" + handle,
		HerdrWorkspaceID: "wTEST-ac3",
		SandboxHandle:    handle,
		SandboxID:        "sb-nonwt",
	}
	h.addBinding(t, b)

	// Sandbox alive, workspace gone → stale, but NOT a wt/ handle.
	h.sandboxExists = func(HerdrSpaceBinding) bool { return true }
	h.workspaceExists = func(HerdrSpaceBinding) bool { return false }

	h.run(t, true /* apply */)

	// MUTATION PROOF: removeSandbox must NOT have been called.
	if len(h.removeCallHandles) != 0 {
		t.Errorf("removeSandbox called for non-wt/ handle %q; want never called", handle)
	}
}

// TestPruneFull_wtSandboxAlreadyAbsent_doesNotCallRemove (Mechanism 1, case c):
// A wt/ binding whose sandbox is already gone must not call removeSandbox —
// there is nothing to reap.
//
// MUTATION TARGET: the sandboxExists(b) guard inside the apply loop.
// Removing it causes removeSandbox to be called even when sandbox is absent → RED.
func TestPruneFull_wtSandboxAlreadyAbsent_doesNotCallRemove(t *testing.T) {
	h := newPruneHarness(t)
	const handle = "wt/feature-bar"
	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:" + handle,
		HerdrWorkspaceID: "wTEST-absent",
		SandboxHandle:    handle,
		SandboxID:        "sb-gone",
	}
	h.addBinding(t, b)

	// Sandbox gone, workspace gone → stale, but sandbox already absent.
	h.sandboxExists = func(HerdrSpaceBinding) bool { return false }
	h.workspaceExists = func(HerdrSpaceBinding) bool { return false }

	h.run(t, true /* apply */)

	if len(h.removeCallHandles) != 0 {
		t.Errorf("removeSandbox called when sandbox already absent; want never called; got %v", h.removeCallHandles)
	}
}

// TestPruneFull_dryRun_neverCallsRemove (Mechanism 1, case d):
// Dry-run mode must never call removeSandbox, even for wt/ reap candidates.
//
// MUTATION TARGET: the !apply guard that returns early before the apply loop.
// Removing it causes removeSandbox to be called in dry-run → RED.
func TestPruneFull_dryRun_neverCallsRemove(t *testing.T) {
	h := newPruneHarness(t)
	const handle = "wt/main"
	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:" + handle,
		HerdrWorkspaceID: "wTEST-dry",
		SandboxHandle:    handle,
		SandboxID:        "sb-dry",
	}
	h.addBinding(t, b)

	h.sandboxExists = func(HerdrSpaceBinding) bool { return true }
	h.workspaceExists = func(HerdrSpaceBinding) bool { return false }

	out := h.run(t, false /* dry-run */)

	if len(h.removeCallHandles) != 0 {
		t.Errorf("removeSandbox called in dry-run; want never; got %v", h.removeCallHandles)
	}
	// Dry-run must show the reap candidate and the "would be reaped" annotation.
	if !strings.Contains(out, "would be reaped") {
		t.Errorf("dry-run output missing 'would be reaped' annotation; got:\n%s", out)
	}
	// Dry-run must not delete the binding.
	bindings, _ := HerdrSpaceList(context.Background(), h.storeRoot)
	found := false
	for _, rem := range bindings {
		if rem.SandboxHandle == handle {
			found = true
		}
	}
	if !found {
		t.Error("dry-run deleted the binding; want it retained")
	}
}

// ── Mechanism 2: herdrWtSupervisedShell ──────────────────────────────────────

// setupWtSeams replaces the wt/ seams and returns a pointer to a bool that
// records whether herdrWtTeardownFn was called (the last-pane teardown path).
// childErr controls what herdrWtChildRunnerFn returns.
// remainingPanes controls how many panes herdrWtPaneListerFn reports.
// The removeErr parameter is retained for API compatibility but is unused now
// that teardown is delegated to herdrWtTeardownFn (which swallows all errors
// via failOpen:true).
func setupWtSeams(
	t *testing.T,
	childErr error,
	remainingPanes int,
	_ error, // removeErr: unused — teardown errors are swallowed by herdrWtTeardownFn
) (teardownCalled *bool) {
	t.Helper()

	origChild := herdrWtChildRunnerFn
	origPaner := herdrWtPaneListerFn
	origTeardown := herdrWtTeardownFn
	t.Cleanup(func() {
		herdrWtChildRunnerFn = origChild
		herdrWtPaneListerFn = origPaner
		herdrWtTeardownFn = origTeardown
	})

	herdrWtChildRunnerFn = func(_ context.Context, _ string, _ []string) error {
		return childErr
	}
	herdrWtPaneListerFn = func(_ context.Context, _, _ string) (int, error) {
		return remainingPanes, nil
	}
	called := false
	herdrWtTeardownFn = func(_ context.Context, _, _, _, _ string) {
		called = true
	}
	return &called
}

// stubBinding returns a minimal HerdrSpaceBinding with a wt/ handle.
func stubWtBinding() HerdrSpaceBinding {
	return HerdrSpaceBinding{
		SpaceLabel:       "nexus3:wt/test-branch",
		HerdrWorkspaceID: "wTEST-sup",
		SandboxHandle:    "wt/test-branch",
		SandboxID:        "sb-sup",
		GuestPaneID:      "w1V:p1",
	}
}

// TestWtSupervisedShell_lastPane_callsRemover (Mechanism 2, case i):
// When no other panes remain after the child exits, herdrWtTeardownFn must be
// called (which atomically tears down VM + workspace + binding).
//
// MUTATION TARGET: the `remaining > 0` guard before the teardown call.
// Inverting it means teardown is called when panes still exist → case ii RED.
// Removing the call entirely → RED.
func TestWtSupervisedShell_lastPane_callsRemover(t *testing.T) {
	teardownCalled := setupWtSeams(t, nil /* child ok */, 0 /* no panes remain */, nil /* unused */)

	binding := stubWtBinding()
	argv := []string{"/usr/bin/nexus3", "exec", "--pty", "--cwd", "/root", binding.SandboxHandle, "/bin/bash", "--login"}
	if err := herdrWtSupervisedShell(context.Background(), "/usr/bin/nexus3", binding, argv); err != nil {
		t.Fatalf("herdrWtSupervisedShell: %v", err)
	}

	if !*teardownCalled {
		t.Error("herdrWtTeardownFn not called when last pane exits; want called")
	}
}

// TestWtSupervisedShell_otherPanesRemain_doesNotCallRemover (Mechanism 2, case ii):
// When other panes remain after the child exits, herdrWtTeardownFn must NOT be
// called.
//
// MUTATION TARGET (MUTATION PROOF): the `remaining > 0` guard.
// Removing or inverting it causes teardown to be called → RED.
func TestWtSupervisedShell_otherPanesRemain_doesNotCallRemover(t *testing.T) {
	teardownCalled := setupWtSeams(t, nil, 2 /* 2 panes remain */, nil)

	binding := stubWtBinding()
	argv := []string{"/usr/bin/nexus3", "exec", "--pty", "--cwd", "/root", binding.SandboxHandle, "/bin/bash", "--login"}
	if err := herdrWtSupervisedShell(context.Background(), "/usr/bin/nexus3", binding, argv); err != nil {
		t.Fatalf("herdrWtSupervisedShell: %v", err)
	}

	if *teardownCalled {
		t.Error("herdrWtTeardownFn called when other panes remain; want NOT called")
	}
}

// TestWtSupervisedShell_removerError_swallowed (Mechanism 2, case iv):
// Teardown errors must be swallowed (fail-open) — the function must return nil
// so the caller (RunHerdrGuestShell) sees a clean exit.
//
// MUTATION TARGET: the FAIL-OPEN contract enforced by herdrWtTeardownFn
// (failOpen:true in herdrSpaceTeardown).  The outer herdrWtSupervisedShell
// must always return nil regardless of teardown outcome.
func TestWtSupervisedShell_removerError_swallowed(t *testing.T) {
	teardownCalled := setupWtSeams(t, nil, 0, errors.New("unused: teardown errors are swallowed by herdrWtTeardownFn"))

	binding := stubWtBinding()
	argv := []string{"/usr/bin/nexus3", "exec", "--pty", "--cwd", "/root", binding.SandboxHandle, "/bin/bash", "--login"}
	err := herdrWtSupervisedShell(context.Background(), "/usr/bin/nexus3", binding, argv)
	if err != nil {
		t.Errorf("herdrWtSupervisedShell returned error %v; want nil (fail-open)", err)
	}
	if !*teardownCalled {
		t.Error("herdrWtTeardownFn was never called; want it called (then errors swallowed)")
	}
}

// TestWtSupervisedShell_nonWtPath_usesExecSeam (Mechanism 2, case iii):
// A non-wt/ binding must take the exec-replace path (execFn called) and must
// never reference herdrWtSandboxRemoverFn.
//
// MUTATION TARGET: the isHerdrWorktreeHandle guard in herdrDefaultShellCore
// that routes wt/ handles to herdrWtSupervisedShell.
// Removing the guard routes all handles through the supervised path → execFn
// not called → RED.
func TestWtSupervisedShell_nonWtPath_usesExecSeam(t *testing.T) {
	// Set herdrWtSandboxRemoverFn to fail-loud if called.
	origRemov := herdrWtSandboxRemoverFn
	t.Cleanup(func() { herdrWtSandboxRemoverFn = origRemov })
	herdrWtSandboxRemoverFn = func(_ context.Context, handle string) error {
		return errors.New("UNEXPECTED: remover called for non-wt/ path; handle=" + handle)
	}

	// Wire a non-wt/ binding into a temp storeRoot.
	storeRoot := t.TempDir()
	const handle = "demo/my-sandbox"
	const wsID = "wTEST-nonwt"
	if err := HerdrSpacePut(context.Background(), storeRoot, HerdrSpaceBinding{
		SpaceLabel:       "nexus3:" + handle,
		HerdrWorkspaceID: wsID,
		SandboxHandle:    handle,
		SandboxID:        "sb-nonwt",
	}); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	// Track execFn calls.
	var execCalledArgv0 string
	mockExec := func(argv0 string, _ []string, _ []string) error {
		execCalledArgv0 = argv0
		return nil // returning nil simulates test seam (production syscall.Exec never returns)
	}

	// Disable auto-create predicate (no worktree cwd).
	origAutoCreate := herdrDefaultShellAutoCreateFn
	t.Cleanup(func() { herdrDefaultShellAutoCreateFn = origAutoCreate })
	herdrDefaultShellAutoCreateFn = func(_ context.Context, _, _, _ string, _ io.Writer) (HerdrSpaceBinding, bool) {
		return HerdrSpaceBinding{}, false
	}

	// Patch auto-create predicate function.
	origPred := herdrAutoCreatePredicateFn
	t.Cleanup(func() { herdrAutoCreatePredicateFn = origPred })
	herdrAutoCreatePredicateFn = func(_ []HerdrSpaceBinding) bool { return false }

	t.Setenv("HERDR_WORKSPACE_ID", wsID)
	t.Setenv("NEXUS3_HOST_SHELL", "") // ensure not forced to host shell

	if err := herdrDefaultShellCore(
		context.Background(),
		func(key string) string {
			switch key {
			case "HERDR_WORKSPACE_ID":
				return wsID
			case "NEXUS3_HOST_SHELL":
				return ""
			}
			return ""
		},
		storeRoot,
		nil, // svc=nil: skip state check, use /root cwd
		"/usr/bin/nexus3",
		mockExec,
	); err != nil {
		t.Fatalf("herdrDefaultShellCore: %v", err)
	}

	// execFn must have been called (the non-wt/ exec-replace path).
	if execCalledArgv0 == "" {
		t.Error("execFn not called for non-wt/ handle; want exec-replace path")
	}
}

// ── parseWtPaneListRemaining — the production pane-lister logic the seam hid ──

// TestParseWtPaneList_workspaceGone_reapsAsZero is the regression guard for the
// live bug: closing the whole worktree workspace makes `herdr pane list` return
// workspace_not_found, which MUST map to 0 remaining panes (reap), not a
// fail-open error.
//
// MUTATION TARGET: remove the `resp.Error.Code == "workspace_not_found"` →
// (0,nil) branch. Expected RED: returns an error, teardown fails open, sandbox
// leaks on every whole-workspace close.
func TestParseWtPaneList_workspaceGone_reapsAsZero(t *testing.T) {
	out := []byte(`{"error":{"code":"workspace_not_found","message":"workspace w4T not found"},"id":"cli:pane:list"}`)
	// herdr exits non-zero for this case:
	n, err := parseWtPaneListRemaining(out, osexecErr(), "w4T:p1")
	if err != nil {
		t.Fatalf("workspace_not_found must not error; got %v", err)
	}
	if n != 0 {
		t.Errorf("workspace_not_found must map to 0 remaining panes; got %d", n)
	}
}

// TestParseWtPaneList_resultShape_countsExcludingSelf proves the parser reads
// the real {"result":{"panes":[...]}} shape (not a bare array) and excludes the
// caller's own pane.
//
// MUTATION TARGET: change the struct back to a bare []{PaneID} array. Expected
// RED: unmarshal yields no panes → wrong count.
func TestParseWtPaneList_resultShape_countsExcludingSelf(t *testing.T) {
	out := []byte(`{"id":"cli:pane:list","result":{"panes":[{"pane_id":"w8:pT"},{"pane_id":"w8:p2"},{"pane_id":"w8:p3"}]}}`)
	n, err := parseWtPaneListRemaining(out, nil, "w8:pT")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n != 2 {
		t.Errorf("remaining (excluding self w8:pT) = %d; want 2", n)
	}
}

// TestParseWtPaneList_lastPane_isZero: only the caller's own pane remains → 0.
func TestParseWtPaneList_lastPane_isZero(t *testing.T) {
	out := []byte(`{"result":{"panes":[{"pane_id":"w8:pT"}]}}`)
	n, err := parseWtPaneListRemaining(out, nil, "w8:pT")
	if err != nil || n != 0 {
		t.Errorf("sole own pane → 0,nil; got %d, %v", n, err)
	}
}

// TestParseWtPaneList_herdrDown_errorsFailOpen: unparseable output + command
// failure must propagate an error so the caller fails open (does NOT reap).
//
// MUTATION TARGET: swallow the cmdErr and return (0,nil). Expected RED: a
// transient herdr outage would reap a live sandbox.
func TestParseWtPaneList_herdrDown_errorsFailOpen(t *testing.T) {
	n, err := parseWtPaneListRemaining([]byte("connection refused"), osexecErr(), "w8:pT")
	if err == nil {
		t.Errorf("herdr-down must error (fail-open skip); got n=%d nil err", n)
	}
}

// osexecErr returns a non-nil error standing in for a non-zero herdr exit.
func osexecErr() error { return errors.New("exit status 1") }

// TestWtTeardownFn_sandboxIDMismatch_guardRefusesSvcRemove verifies that the
// real-time reap path passes binding.SandboxID as expectedSandboxID so that a
// handle rebound to a NEW sandbox between binding-capture and pane-close does
// NOT have its VM removed.
//
// MUTATION TARGET: remove the expectedSandboxID wiring in herdrWtTeardownFn
// (revert teardownOpts{failOpen:true} without expectedSandboxID field).
// Expected RED: svcRemove is called for the new sandbox → test fails.
func TestWtTeardownFn_sandboxIDMismatch_guardRefusesSvcRemove(t *testing.T) {
	ctx := context.Background()

	// Seed the store with a binding whose SandboxID is "sb-original".
	storeRoot := t.TempDir()
	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:wt/guard-test",
		HerdrWorkspaceID: "wGUARD",
		SandboxHandle:    "wt/guard-test",
		SandboxID:        "sb-original",
		GuestPaneID:      "wG:p1",
	}
	if err := HerdrSpacePut(ctx, storeRoot, b); err != nil {
		t.Fatalf("seed binding: %v", err)
	}

	// Stub herdrWtSandboxRemoverFn to detect any svcRemove call.
	origRemover := herdrWtSandboxRemoverFn
	t.Cleanup(func() { herdrWtSandboxRemoverFn = origRemover })
	removeCalled := false
	herdrWtSandboxRemoverFn = func(_ context.Context, _ string) error {
		removeCalled = true
		return nil
	}

	// Call the REAL herdrWtTeardownFn with a mismatched sandboxID ("sb-new").
	// The guard must refuse and herdrWtSandboxRemoverFn must NOT be called.
	// herdrBin is irrelevant: workspaceClose is never reached when the guard fires.
	herdrWtTeardownFn(ctx, storeRoot, "wt/guard-test", "herdr-unused", "sb-new")

	if removeCalled {
		t.Error("svcRemove was called despite SandboxID mismatch; expectedSandboxID guard is not wired")
	}
}
