package cli

// herdr_txn_test.go — unit tests for the herdr-space ↔ nexus3-sandbox
// transaction primitives in herdr_txn.go.
//
// AC mapping:
//   T-AC1:  TestHerdrSpaceCreateTxn_PutFail_LeavesNoWorkspace
//           TestHerdrSpaceCreateTxn_PutFail_PreExistingSandboxNotRemoved
//   T-AC2:  TestHerdrSpaceCreateTxn_PostCommit_PreExistingSandboxNeverRemoved
//   T-AC4:  TestHerdrSpaceTeardown_CloseFail_BindingRetained
//   T-AC11: TestHerdrSpaceTeardown_DoubleTeardown_IdempotentNoPanic
//           TestHerdrSpaceTeardown_SvcRemoveNotFound_ToleratedAndDeletes
//   TBD-SHL-7: TestHerdrSpaceEnsureWorkspaceTxn_PutFail_ClosesWorkspace
//
// Every test names its MUTATION TARGET in the doc comment.
// No live herdr binary; no live sandbox service.
// TMPDIR=/tmp go test ./internal/cli/ -count=1

import (
	"context"
	"errors"
	"testing"
)

// ── Test harness ──────────────────────────────────────────────────────────────

// txnHarness wires injectable fakes for txnDeps and tracks calls.
type txnHarness struct {
	workspaceClosed []string // workspace IDs passed to workspaceClose
	sandboxRemoved  []string // refs passed to svcRemove
	bindingPuts     int
	bindingDeletes  int

	// Control whether each step succeeds (nil = success).
	putErr    error
	closeErr  error
	removeErr error
	createErr error
	openErr   error
}

// deps returns a txnDeps wired to h's fakes and storeRoot for binding storage.
// Callers may override individual fields after calling deps().
func (h *txnHarness) deps(storeRoot string) txnDeps {
	return txnDeps{
		ensureSandbox: func(_ context.Context, ref string) (string, string, bool, error) {
			// Default: pre-existing sandbox (createdByUs=false).
			return ref, "sb-" + ref, false, nil
		},
		workspaceCreate: func(_ context.Context, _, label, _ string) (string, string, error) {
			if h.createErr != nil {
				return "", "", h.createErr
			}
			return "wID-" + label, "rpane-" + label, nil
		},
		workspaceClose: func(_ context.Context, id string) error {
			h.workspaceClosed = append(h.workspaceClosed, id)
			return h.closeErr
		},
		bindingPut: func(ctx context.Context, b HerdrSpaceBinding) error {
			h.bindingPuts++
			if h.putErr != nil {
				return h.putErr
			}
			return HerdrSpacePut(ctx, storeRoot, b)
		},
		bindingDelete: func(ctx context.Context, label string) error {
			h.bindingDeletes++
			return HerdrSpaceDelete(ctx, storeRoot, label)
		},
		svcRemove: func(_ context.Context, ref string) error {
			h.sandboxRemoved = append(h.sandboxRemoved, ref)
			return h.removeErr
		},
		openPane: func(_ context.Context, _, _, _, _ string, _ bool) (string, error) {
			if h.openErr != nil {
				return "", h.openErr
			}
			return "pane-1", nil
		},
	}
}

// ── T-AC1 ─────────────────────────────────────────────────────────────────────

// TestHerdrSpaceCreateTxn_PutFail_LeavesNoWorkspace asserts that when
// bindingPut fails (pre-commit), workspaceClose is called to compensate and
// the sandbox is removed when createdByUs=true.
//
// MUTATION TARGET: the workspaceClose call inside the bindingPut-error branch
// of herdrSpaceCreateTxn. Removing it leaves the workspace orphaned → RED.
// Secondary: the `if createdByUs` guard for svcRemove. Removing it either
// over-compensates or under-compensates depending on the test case.
func TestHerdrSpaceCreateTxn_PutFail_LeavesNoWorkspace(t *testing.T) {
	root := t.TempDir()
	h := &txnHarness{putErr: errors.New("disk full")}
	deps := h.deps(root)
	deps.ensureSandbox = func(_ context.Context, ref string) (string, string, bool, error) {
		return ref, "sb-" + ref, true, nil // sandbox was created by us
	}

	err := herdrSpaceCreateTxn(context.Background(),
		createSpec{ref: "orca/demo", label: "nexus3:orca/demo"},
		deps, "herdr-bin", root)

	if err == nil {
		t.Fatal("expected error from herdrSpaceCreateTxn when bindingPut fails")
	}
	// Workspace must have been closed (compensation).
	if len(h.workspaceClosed) == 0 {
		t.Error("workspaceClose not called on bindingPut failure; workspace would be orphaned")
	}
	// Sandbox must have been removed (createdByUs=true).
	if len(h.sandboxRemoved) == 0 {
		t.Error("svcRemove not called on bindingPut failure when createdByUs=true")
	}
	// No binding must remain.
	if _, gerr := HerdrSpaceGetByLabel(context.Background(), root, "nexus3:orca/demo"); !errors.Is(gerr, ErrHerdrSpaceNotFound) {
		t.Errorf("binding must not exist after Put-fail; got err=%v", gerr)
	}
}

// TestHerdrSpaceCreateTxn_PutFail_PreExistingSandboxNotRemoved asserts that
// when bindingPut fails and createdByUs=false, svcRemove is NOT called.
//
// MUTATION TARGET: the `if createdByUs` guard in the bindingPut compensation.
// Removing it causes h.sandboxRemoved to be non-empty → RED.
func TestHerdrSpaceCreateTxn_PutFail_PreExistingSandboxNotRemoved(t *testing.T) {
	root := t.TempDir()
	h := &txnHarness{putErr: errors.New("conflict")}
	deps := h.deps(root)
	// ensureSandbox returns createdByUs=false (pre-existing).
	deps.ensureSandbox = func(_ context.Context, ref string) (string, string, bool, error) {
		return ref, "sb-" + ref, false, nil
	}

	_ = herdrSpaceCreateTxn(context.Background(),
		createSpec{ref: "orca/old", label: "nexus3:orca/old"},
		deps, "herdr-bin", root)

	if len(h.sandboxRemoved) != 0 {
		t.Errorf("svcRemove called for pre-existing sandbox on Put-fail: %v", h.sandboxRemoved)
	}
}

// ── T-AC2 ─────────────────────────────────────────────────────────────────────

// TestHerdrSpaceCreateTxn_PostCommit_PreExistingSandboxNeverRemoved asserts
// that even when openPane fails post-commit, svcRemove is never called for a
// pre-existing sandbox (createdByUs=false) and the binding is retained.
//
// MUTATION TARGET: the `return nil` after the openPane-error log in
// herdrSpaceCreateTxn. Changing it to call svcRemove removes a running sandbox
// the user owns → RED.
func TestHerdrSpaceCreateTxn_PostCommit_PreExistingSandboxNeverRemoved(t *testing.T) {
	root := t.TempDir()
	h := &txnHarness{openErr: errors.New("pane create failed")}
	deps := h.deps(root)
	deps.ensureSandbox = func(_ context.Context, ref string) (string, string, bool, error) {
		return ref, "sb-" + ref, false, nil // pre-existing
	}

	err := herdrSpaceCreateTxn(context.Background(),
		createSpec{ref: "orca/pre", label: "nexus3:orca/pre"},
		deps, "herdr-bin", root)

	// Post-commit openPane failure must NOT propagate.
	if err != nil {
		t.Errorf("expected nil from post-commit openPane failure; got %v", err)
	}
	// svcRemove must never be called.
	if len(h.sandboxRemoved) != 0 {
		t.Errorf("svcRemove called post-commit for pre-existing sandbox: %v", h.sandboxRemoved)
	}
	// Binding must be retained.
	if _, gerr := HerdrSpaceGetByLabel(context.Background(), root, "nexus3:orca/pre"); gerr != nil {
		t.Errorf("binding must be retained after post-commit openPane failure; got %v", gerr)
	}
}

// ── T-AC4 ─────────────────────────────────────────────────────────────────────

// TestHerdrSpaceTeardown_CloseFail_BindingRetained asserts that when
// workspaceClose fails, herdrSpaceTeardown returns nil and the binding remains.
//
// MUTATION TARGET: the `return nil` in the workspaceClose-error branch of
// herdrSpaceTeardown. Removing it (falling through to bindingDelete) deletes
// the binding without closing the workspace → RED.
func TestHerdrSpaceTeardown_CloseFail_BindingRetained(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:demo",
		HerdrWorkspaceID: "wTEST",
		SandboxHandle:    "orca/demo",
		SandboxID:        "sb-123",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	h := &txnHarness{closeErr: errors.New("workspace unreachable")}
	deps := h.deps(root)

	err := herdrSpaceTeardown(ctx, root, "orca/demo", deps, teardownOpts{
		sandboxAlreadyRemoved: true,
	})
	if err != nil {
		t.Errorf("teardown must return nil on close failure; got %v", err)
	}
	// Binding must still be present.
	got, gerr := HerdrSpaceGetByLabel(ctx, root, "nexus3:demo")
	if gerr != nil || got.SpaceLabel != b.SpaceLabel {
		t.Errorf("binding must be retained after close failure; gerr=%v got=%+v", gerr, got)
	}
}

// ── svcRemove not-found tolerance ────────────────────────────────────────────

// TestHerdrSpaceTeardown_SvcRemoveNotFound_ToleratedAndDeletes asserts that
// when svcRemove returns a "not found" error (sandbox already gone via another
// route), herdrSpaceTeardown returns nil and deletes the binding.
//
// MUTATION TARGET: the herdrTxnIsNotFound(err) guard in the svcRemove branch
// of herdrSpaceTeardown. Removing it lets the not-found error propagate →
// teardown returns an error and the binding is retained → RED.
func TestHerdrSpaceTeardown_SvcRemoveNotFound_ToleratedAndDeletes(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:gone",
		HerdrWorkspaceID: "", // no workspace: close is a no-op success
		SandboxHandle:    "orca/gone",
		SandboxID:        "sb-g",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	// svcRemove returns a "not found" error — herdrTxnIsNotFound matches via
	// strings.Contains(err.Error(), "not found").
	h := &txnHarness{removeErr: errors.New(`resolve "orca/gone": not found`)}
	deps := h.deps(root)

	if err := herdrSpaceTeardown(ctx, root, "orca/gone", deps, teardownOpts{}); err != nil {
		t.Errorf("svcRemove not-found must be tolerated; got %v", err)
	}
	// Binding must be deleted: close succeeded (no-op), delete authorised.
	if _, gerr := HerdrSpaceGetByLabel(ctx, root, b.SpaceLabel); !errors.Is(gerr, ErrHerdrSpaceNotFound) {
		t.Errorf("binding must be deleted after tolerated svcRemove not-found; gerr=%v", gerr)
	}
}

// ── T-AC11 ────────────────────────────────────────────────────────────────────

// TestHerdrSpaceTeardown_DoubleTeardown_IdempotentNoPanic asserts that calling
// herdrSpaceTeardown twice for the same handle returns nil both times and
// deletes the binding at most once.
//
// MUTATION TARGET: the errors.Is(err, ErrHerdrSpaceNotFound) tolerance in
// the HerdrSpaceGetByHandle-not-found path of herdrSpaceTeardown. Removing
// that early-return causes the second call to err on get → RED.
func TestHerdrSpaceTeardown_DoubleTeardown_IdempotentNoPanic(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:demo2",
		HerdrWorkspaceID: "wTEST2",
		SandboxHandle:    "orca/demo2",
		SandboxID:        "sb-456",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	h := &txnHarness{}
	deps := h.deps(root)
	opts := teardownOpts{sandboxAlreadyRemoved: true}

	if err := herdrSpaceTeardown(ctx, root, "orca/demo2", deps, opts); err != nil {
		t.Fatalf("first teardown: %v", err)
	}
	// Second call: binding is gone; must return nil without panic.
	if err := herdrSpaceTeardown(ctx, root, "orca/demo2", deps, opts); err != nil {
		t.Errorf("second teardown must return nil (idempotent); got %v", err)
	}
	// bindingDelete should have been called at most once (second call hits
	// not-found at HerdrSpaceGetByHandle and returns nil before reaching delete).
	if h.bindingDeletes > 1 {
		t.Errorf("bindingDelete called %d times; want at most 1", h.bindingDeletes)
	}
}

// ── TBD-SHL-7: ensure-workspace rollback ─────────────────────────────────────

// TestHerdrSpaceEnsureWorkspaceTxn_PutFail_ClosesWorkspace asserts that when
// bindingPut fails after workspace creation, the workspace is closed (TBD-SHL-7
// orphan prevention).
//
// MUTATION TARGET: the workspaceClose call in the bindingPut-error branch of
// herdrSpaceEnsureWorkspaceTxn. Removing it leaves the workspace permanently
// orphaned and unreachable to space-prune → RED.
func TestHerdrSpaceEnsureWorkspaceTxn_PutFail_ClosesWorkspace(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	b := HerdrSpaceBinding{
		SpaceLabel:    "nexus3:no-ws",
		SandboxHandle: "orca/no-ws",
		SandboxID:     "sb-789",
		// HerdrWorkspaceID intentionally empty.
	}
	// Write initial binding directly (not via deps).
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	h := &txnHarness{putErr: errors.New("store locked")}
	deps := h.deps(root)

	_, _, err := herdrSpaceEnsureWorkspaceTxn(ctx, b, "", deps, "herdr-bin")
	if err == nil {
		t.Fatal("expected error when bindingPut fails in ensure-workspace")
	}
	// Workspace must have been closed (TBD-SHL-7 rollback).
	if len(h.workspaceClosed) == 0 {
		t.Error("workspaceClose not called on bindingPut failure in ensure-workspace (TBD-SHL-7 orphan)")
	}
}

// ── SandboxID guard ───────────────────────────────────────────────────────────

// TestHerdrSpaceTeardown_SandboxIDMismatch_Refuses asserts that teardown with
// expectedSandboxID set refuses when the binding holds a different ID.
//
// MUTATION TARGET: the opts.expectedSandboxID mismatch check in herdrSpaceTeardown.
// Removing it causes teardown to proceed and delete a binding for a live sandbox → RED.
func TestHerdrSpaceTeardown_SandboxIDMismatch_Refuses(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:xyz",
		HerdrWorkspaceID: "wXYZ",
		SandboxHandle:    "orca/xyz",
		SandboxID:        "sb-REAL",
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	h := &txnHarness{}
	deps := h.deps(root)

	err := herdrSpaceTeardown(ctx, root, "orca/xyz", deps, teardownOpts{
		expectedSandboxID:     "sb-DIFFERENT",
		sandboxAlreadyRemoved: true,
		failOpen:              false,
	})
	if err == nil {
		t.Error("expected error when SandboxID mismatches; teardown must refuse")
	}
	// Binding must still exist.
	if _, gerr := HerdrSpaceGetByLabel(ctx, root, "nexus3:xyz"); gerr != nil {
		t.Errorf("binding must be retained after SandboxID mismatch refusal; gerr=%v", gerr)
	}
}

// TestHerdrSpaceTeardown_SandboxIDMismatch_FailOpen_ReturnsNil asserts that
// a SandboxID mismatch with failOpen=true returns nil (pane must not freeze).
//
// MUTATION TARGET: the failOpen handling inside herdrTxnMaybeErr. Changing it
// to propagate the error freezes new panes via the SIGHUP path → RED.
func TestHerdrSpaceTeardown_SandboxIDMismatch_FailOpen_ReturnsNil(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()

	b := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:myrepo/fo",
		HerdrWorkspaceID: "wFO",
		SandboxHandle:    "myrepo/fo",
		SandboxID:        "sb-FO",
		WorktreeManaged:  true,
	}
	if err := HerdrSpacePut(ctx, root, b); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	h := &txnHarness{}
	deps := h.deps(root)

	err := herdrSpaceTeardown(ctx, root, "myrepo/fo", deps, teardownOpts{
		expectedSandboxID:     "sb-WRONG",
		sandboxAlreadyRemoved: true,
		failOpen:              true,
	})
	if err != nil {
		t.Errorf("failOpen teardown with SandboxID mismatch must return nil; got %v", err)
	}
}
