package cli

// Tests for the herdr-space cascade wired into runSandboxRmFull.
//
// The seam is the closeWorkspace func parameter introduced on runSandboxRmFull;
// tests inject a fake instead of shelling out to the real herdr binary.
//
// Mutation-proof contract (read before changing these tests):
//
//  1. TestSandboxRm_HerdrCascade_ClosesRecordedWorkspace: RED when the
//     herdrSpaceTeardownOnRm call is deleted from runSandboxRmFull.
//
//  2. TestSandboxRm_HerdrCascade_ToleratesHerdrFailure: RED when the non-fatal
//     treatment of closeWorkspace errors is removed (i.e. error propagated).

import (
	"context"
	"errors"
	"testing"

	"github.com/IniZio/nexus3/internal/core/service"
)

// fakeWorkspaceCloser records calls and optionally returns an error.
type fakeWorkspaceCloser struct {
	closed []string
	err    error
}

func (f *fakeWorkspaceCloser) close(_ context.Context, workspaceID string) error {
	f.closed = append(f.closed, workspaceID)
	return f.err
}

// TestSandboxRm_HerdrCascade_ClosesRecordedWorkspace verifies that removing a
// sandbox whose handle appears in the binding file closes the recorded
// HerdrWorkspaceID and removes the binding.
//
// MUTATION TARGET: delete the herdrSpaceTeardownOnRm call in runSandboxRmFull.
// Expected RED: closer.closed is empty, binding is still present.
func TestSandboxRm_HerdrCascade_ClosesRecordedWorkspace(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	sb, err := svc.Create(ctx, "proj", "cascade-box", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Write a herdr binding for the sandbox.
	root := t.TempDir()
	binding := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:proj-cascade-box",
		HerdrWorkspaceID: "wTEST",
		SandboxHandle:    sb.Handle(),
		SandboxID:        sb.ID.String(),
	}
	if err := HerdrSpacePut(ctx, root, binding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	closer := &fakeWorkspaceCloser{}
	out, _, _ := capture(true)
	if err := runSandboxRmFull(ctx, []string{sb.Handle()}, out, svc, root, closer.close); err != nil {
		t.Fatalf("runSandboxRmFull: %v", err)
	}

	// The workspace must have been closed with the exact recorded ID.
	if len(closer.closed) != 1 || closer.closed[0] != "wTEST" {
		t.Errorf("workspace close: got %v, want [wTEST]", closer.closed)
	}

	// The binding must be gone.
	if _, err := HerdrSpaceGetByHandle(ctx, root, sb.Handle()); !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("binding still present after sandbox rm; err=%v", err)
	}
}

// TestSandboxRm_HerdrCascade_ToleratesHerdrFailure verifies that a closeWorkspace
// error does NOT fail sandbox rm — the removal is already done and must succeed.
//
// MUTATION TARGET: change the non-fatal log+continue to return the error.
// Expected RED: runSandboxRmFull returns non-nil error.
func TestSandboxRm_HerdrCascade_ToleratesHerdrFailure(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	sb, err := svc.Create(ctx, "proj", "tolerate-box", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	root := t.TempDir()
	binding := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:proj-tolerate-box",
		HerdrWorkspaceID: "wFAIL",
		SandboxHandle:    sb.Handle(),
		SandboxID:        sb.ID.String(),
	}
	if err := HerdrSpacePut(ctx, root, binding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	// Inject a closer that always fails.
	closer := &fakeWorkspaceCloser{err: errors.New("herdr unreachable")}
	out, _, _ := capture(true)
	if err := runSandboxRmFull(ctx, []string{sb.Handle()}, out, svc, root, closer.close); err != nil {
		t.Errorf("runSandboxRmFull must succeed even when herdr fails; got %v", err)
	}

	// The close was attempted.
	if len(closer.closed) == 0 {
		t.Error("closeWorkspace was never called")
	}

	// MUTATION TARGET: make HerdrSpaceDelete unconditional (remove the early
	// return on closeWorkspace failure). Expected RED: binding is gone.
	//
	// The binding must SURVIVE a failed close so space-prune can recover the
	// live workspace. A transient herdr outage must not permanently orphan it.
	got, err := HerdrSpaceGetByHandle(ctx, root, sb.Handle())
	if err != nil {
		t.Errorf("binding must be retained after failed close, but HerdrSpaceGetByHandle: %v", err)
	} else if got.HerdrWorkspaceID != "wFAIL" {
		t.Errorf("retained binding has wrong workspace_id: got %q, want %q", got.HerdrWorkspaceID, "wFAIL")
	}
}

// TestSandboxRm_HerdrCascade_DeletesBindingOnSuccess verifies that when
// closeWorkspace succeeds, the binding IS deleted so space-prune does not see
// a ghost entry.
//
// MUTATION TARGET: make HerdrSpaceDelete a no-op (remove the call entirely).
// Expected RED: binding still present after successful rm.
func TestSandboxRm_HerdrCascade_DeletesBindingOnSuccess(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	sb, err := svc.Create(ctx, "proj", "delete-success-box", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	root := t.TempDir()
	binding := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:proj-delete-success-box",
		HerdrWorkspaceID: "wSUCCESS",
		SandboxHandle:    sb.Handle(),
		SandboxID:        sb.ID.String(),
	}
	if err := HerdrSpacePut(ctx, root, binding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	// Inject a closer that always succeeds.
	closer := &fakeWorkspaceCloser{} // err is nil by default
	out, _, _ := capture(true)
	if err := runSandboxRmFull(ctx, []string{sb.Handle()}, out, svc, root, closer.close); err != nil {
		t.Fatalf("runSandboxRmFull must succeed; got %v", err)
	}

	if len(closer.closed) == 0 {
		t.Error("closeWorkspace was never called")
	}

	// Binding must be gone after a successful close.
	_, err = HerdrSpaceGetByHandle(ctx, root, sb.Handle())
	if !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("binding must be deleted after successful close; HerdrSpaceGetByHandle = %v, want ErrHerdrSpaceNotFound", err)
	}
}

// TestSandboxRm_HerdrCascade_NoBindingIsNoop verifies that rm of a sandbox
// that has no herdr binding succeeds without error (no binding = nothing to do).
func TestSandboxRm_HerdrCascade_NoBindingIsNoop(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	sb, err := svc.Create(ctx, "proj", "no-binding-box", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	root := t.TempDir() // empty — no bindings
	closer := &fakeWorkspaceCloser{}
	out, _, _ := capture(true)
	if err := runSandboxRmFull(ctx, []string{sb.Handle()}, out, svc, root, closer.close); err != nil {
		t.Fatalf("runSandboxRmFull: %v", err)
	}
	if len(closer.closed) != 0 {
		t.Errorf("expected no workspace close calls, got %v", closer.closed)
	}
}

// ── M-1: prefix resolution is tested (not just exact handle) ─────────────────

// TestSandboxRm_HerdrCascade_IDPrefixResolvesAndCloses verifies that passing an
// ID prefix to runSandboxRmFull resolves to the correct sandbox and closes its
// recorded herdr workspace.
//
// MUTATION TARGET: revert runSandboxRmFull to exact-match-only ID lookup
// (removing svc.Get prefix resolution). Expected RED: closer.closed is empty
// or the workspace ID does not match "wPREFIX".
func TestSandboxRm_HerdrCascade_IDPrefixResolvesAndCloses(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	sb, err := svc.Create(ctx, "proj", "prefix-box", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	root := t.TempDir()
	binding := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:proj-prefix-box",
		HerdrWorkspaceID: "wPREFIX",
		SandboxHandle:    sb.Handle(),
		SandboxID:        sb.ID.String(),
	}
	if err := HerdrSpacePut(ctx, root, binding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	// Use a short prefix of the full ID (first 6 chars after "sb-").
	fullID := sb.ID.String()
	// A prefix of 8 characters is enough to be unique in a single-sandbox store.
	prefix := fullID[:8]

	closer := &fakeWorkspaceCloser{}
	out, _, _ := capture(true)
	if err := runSandboxRmFull(ctx, []string{prefix}, out, svc, root, closer.close); err != nil {
		t.Fatalf("runSandboxRmFull with prefix: %v", err)
	}

	if len(closer.closed) != 1 || closer.closed[0] != "wPREFIX" {
		t.Errorf("workspace close via prefix: got %v, want [wPREFIX]", closer.closed)
	}
}

// TestSandboxRm_HerdrCascade_AmbiguousPrefixErrorsBeforeStop verifies that an
// ambiguous ID prefix causes runSandboxRmFull to return an error BEFORE calling
// stopDetachedSupervisor or touching any herdr binding.
//
// Note: the ambiguity check at the callsite was reviewed and determined to be
// an equivalent mutant — removing it produces no observable difference because
// svc.Get still returns ErrAmbiguous, target stays nil, the closer is never
// called, and the same error propagates. No mutation target is asserted here.
func TestSandboxRm_HerdrCascade_AmbiguousPrefixErrorsBeforeStop(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	// Create two sandboxes; the store assigns them sequential IDs that share a
	// prefix. We pass a prefix that matches both to force ErrAmbiguous.
	sb1, err := svc.Create(ctx, "proj", "ambig-a", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create sb1: %v", err)
	}
	sb2, err := svc.Create(ctx, "proj", "ambig-b", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create sb2: %v", err)
	}

	// Find the longest common prefix of the two IDs.
	id1, id2 := sb1.ID.String(), sb2.ID.String()
	commonLen := 0
	for commonLen < len(id1) && commonLen < len(id2) && id1[commonLen] == id2[commonLen] {
		commonLen++
	}
	if commonLen == 0 {
		// IDs share no common prefix — cannot construct an ambiguous ref; skip.
		t.Skip("sandbox IDs share no common prefix; cannot construct ambiguous ref")
	}
	ambigPrefix := id1[:commonLen]

	root := t.TempDir()
	closer := &fakeWorkspaceCloser{}
	out, _, _ := capture(true)

	err = runSandboxRmFull(ctx, []string{ambigPrefix}, out, svc, root, closer.close)
	if err == nil {
		t.Fatal("expected error for ambiguous prefix, got nil")
	}
	// Must be a CodedError wrapping ErrAmbiguous.
	var coded *CodedError
	if !errors.As(err, &coded) || coded.Code != sandboxErrCodeAmbiguousRef {
		t.Errorf("expected CodedError with code %q, got: %v", sandboxErrCodeAmbiguousRef, err)
	}
	// closeWorkspace must NOT have been called.
	if len(closer.closed) != 0 {
		t.Errorf("closer must not be called for ambiguous ref; got %v", closer.closed)
	}
}

// TestHerdrSpaceTeardownOnRm_NoHerdr_BindingRetained verifies that when herdr
// is not available (empty herdrBin), herdrSpaceTeardownOnRm retains the binding.
//
// This test uses herdrWorkspaceClose directly as the closer (empty herdrBin),
// exercising the REAL herdrWorkspaceClose code path rather than an injected
// fake.  That makes Fix 2 (herdrWorkspaceClose returns error for empty bin)
// an integral part of the assertion — if Fix 2 is reverted (empty bin → nil),
// the closer succeeds silently, the binding is deleted, and this test is RED.
//
// MUTATION TARGET (Fix 2): revert herdrWorkspaceClose to return nil when
// herdrBin == "" (restore the old "no-op" behaviour).
// Expected RED: closer returns nil → teardown succeeds → binding deleted.
func TestHerdrSpaceTeardownOnRm_NoHerdr_BindingRetained(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	binding := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:proj-rm-no-herdr",
		HerdrWorkspaceID: "wNOHERDR",
		SandboxHandle:    "proj/rm-no-herdr",
		SandboxID:        "sb-nh",
	}
	if err := HerdrSpacePut(ctx, root, binding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	// Use the real herdrWorkspaceClose with an empty herdrBin — simulates
	// what production code does when resolveHerdrBin fails (Fix 1 makes the
	// outer closer return herdrBinErr; this test proves the same invariant at
	// the herdrWorkspaceClose level directly).
	closer := func(ctx context.Context, workspaceID string) error {
		return herdrWorkspaceClose(ctx, "", workspaceID)
	}
	_ = herdrSpaceTeardown(ctx, root, binding.SandboxHandle, txnDeps{
		workspaceClose: closer,
		bindingDelete: func(ctx context.Context, label string) error {
			return HerdrSpaceDelete(ctx, root, label)
		},
	}, teardownOpts{
		expectedSandboxID:     binding.SandboxID,
		sandboxAlreadyRemoved: true,
		failOpen:              true,
	})

	// Binding must survive — the workspace is live and herdr was unavailable.
	got, err := HerdrSpaceGetByHandle(ctx, root, binding.SandboxHandle)
	if err != nil {
		t.Errorf("binding must be retained when herdr absent; HerdrSpaceGetByHandle: %v", err)
	} else if got.HerdrWorkspaceID != "wNOHERDR" {
		t.Errorf("retained binding has wrong workspace_id: got %q, want %q", got.HerdrWorkspaceID, "wNOHERDR")
	}
}

// TestHerdrSpaceTeardownOnRm_DifferentSandboxID_WorkspaceRetained is the w4B
// regression guard. The binding is keyed by handle, so a handle collision
// leaves one binding pointing at whichever sandbox wrote last. Removing a
// DIFFERENT sandbox that shares the handle must NOT close that live workspace.
//
// MUTATION TARGET: delete the `b.SandboxID != sandboxID` guard in
// herdrSpaceTeardownOnRm. Expected RED: closer is invoked on wLIVE and the
// binding is deleted even though a different sandbox was removed.
func TestHerdrSpaceTeardownOnRm_DifferentSandboxID_WorkspaceRetained(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	// Binding belongs to sandbox sb-LIVE (workspace wLIVE).
	binding := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:proj-collide",
		HerdrWorkspaceID: "wLIVE",
		SandboxHandle:    "proj/collide",
		SandboxID:        "sb-LIVE",
	}
	if err := HerdrSpacePut(ctx, root, binding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	var closed []string
	closer := func(_ context.Context, workspaceID string) error {
		closed = append(closed, workspaceID)
		return nil
	}

	// Remove a DIFFERENT sandbox (sb-OTHER) that shares the handle.
	_ = herdrSpaceTeardown(ctx, root, binding.SandboxHandle, txnDeps{
		workspaceClose: closer,
		bindingDelete: func(ctx context.Context, label string) error {
			return HerdrSpaceDelete(ctx, root, label)
		},
	}, teardownOpts{
		expectedSandboxID:     "sb-OTHER",
		sandboxAlreadyRemoved: true,
		failOpen:              true,
	})

	if len(closed) != 0 {
		t.Errorf("closer must not run when a different sandbox is removed; closed=%v", closed)
	}
	got, err := HerdrSpaceGetByHandle(ctx, root, binding.SandboxHandle)
	if err != nil {
		t.Fatalf("binding for sb-LIVE must be retained; HerdrSpaceGetByHandle: %v", err)
	}
	if got.HerdrWorkspaceID != "wLIVE" {
		t.Errorf("retained binding workspace = %q; want wLIVE", got.HerdrWorkspaceID)
	}
}

// TestHerdrSpaceTeardownOnRm_MatchingSandboxID_WorkspaceClosed proves the
// positive path: when the removed sandbox IS the one the binding points at,
// the workspace is closed and the binding deleted.
func TestHerdrSpaceTeardownOnRm_MatchingSandboxID_WorkspaceClosed(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()

	binding := HerdrSpaceBinding{
		SpaceLabel:       "nexus3:proj-match",
		HerdrWorkspaceID: "wMATCH",
		SandboxHandle:    "proj/match",
		SandboxID:        "sb-MATCH",
	}
	if err := HerdrSpacePut(ctx, root, binding); err != nil {
		t.Fatalf("HerdrSpacePut: %v", err)
	}

	var closed []string
	closer := func(_ context.Context, workspaceID string) error {
		closed = append(closed, workspaceID)
		return nil
	}

	_ = herdrSpaceTeardown(ctx, root, binding.SandboxHandle, txnDeps{
		workspaceClose: closer,
		bindingDelete: func(ctx context.Context, label string) error {
			return HerdrSpaceDelete(ctx, root, label)
		},
	}, teardownOpts{
		expectedSandboxID:     "sb-MATCH",
		sandboxAlreadyRemoved: true,
		failOpen:              true,
	})

	if len(closed) != 1 || closed[0] != "wMATCH" {
		t.Errorf("closer must close wMATCH exactly once; closed=%v", closed)
	}
	if _, err := HerdrSpaceGetByHandle(ctx, root, binding.SandboxHandle); !errors.Is(err, ErrHerdrSpaceNotFound) {
		t.Errorf("binding must be deleted after matching teardown; got err=%v", err)
	}
}
