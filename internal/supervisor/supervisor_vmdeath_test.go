package supervisor

// supervisor_vmdeath_test.go — tests for AC-12a/b: VM-death detection and
// honest store reconciliation.
//
// AC-12a: a sandbox whose netns child has exited is no longer reported as
//         "running" — the store record is reconciled to Stopped.
// AC-12b: the StopReason is StopReasonMemoryLost, not StopReasonClean.
//
// TestVMDeath_AwaitShutdown_ReturnsVMDeathCause exercises the selector arm.
//
// TestVMDeath_ReconcileVMDeath_* drive reconcileVMDeath directly — the
// function extracted from RunDetached's shutdownByVMDeath branch so that
// tests reach the real body, not a hand-copy of it.
//
// Gap: the call site in RunDetached (reconcileVMDeath(reconCtx, st, sb.ID))
// is not exercised from a unit test — booting a real VM is required. That
// gap is acknowledged; breaking the call removes it from the production path
// rather than inverting its logic, so the risk profile is lower.

import (
	"context"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// fakeReconciler is a minimal vmDeathReconciler for tests.
type fakeReconciler struct {
	record domain.Sandbox
}

func (f *fakeReconciler) Update(_ context.Context, _ domain.SandboxID, fn func(*domain.Sandbox) error) error {
	return fn(&f.record)
}

// TestVMDeath_AwaitShutdown_ReturnsVMDeathCause verifies that awaitShutdown
// returns shutdownByVMDeath when vmDeadCh is closed.
//
// MUTATION PROOF: removing the case <-vmDeadCh arm causes this test to hang
// (Background context never cancels, stopCh never closes) — genuine FAILURE.
func TestVMDeath_AwaitShutdown_ReturnsVMDeathCause(t *testing.T) {
	t.Parallel()
	vmDeadCh := make(chan struct{})
	close(vmDeadCh)

	got := awaitShutdown(context.Background(), make(chan struct{}), nil, vmDeadCh)
	if got != shutdownByVMDeath {
		t.Fatalf("FAIL: awaitShutdown with closed vmDeadCh = %v, want shutdownByVMDeath", got)
	}
}

// TestVMDeath_ReconcileVMDeath_StateStopped verifies AC-12a: reconcileVMDeath
// writes State=Stopped. Calls the REAL function (not a copy of its body).
//
// MUTATION PROOF (AC-12a): change rec.State = domain.Stopped in
// reconcileVMDeath to rec.State = domain.Running — this test FAILs.
// Substitution count asserted before mutation: exactly 1.
func TestVMDeath_ReconcileVMDeath_StateStopped(t *testing.T) {
	t.Parallel()
	var id domain.SandboxID
	id[0] = 1
	f := &fakeReconciler{record: domain.Sandbox{ID: id, State: domain.Running}}

	if err := reconcileVMDeath(context.Background(), f, id); err != nil {
		t.Fatalf("reconcileVMDeath: %v", err)
	}

	if f.record.State == domain.Running {
		t.Errorf("FAIL AC-12a: state is still Running after reconcileVMDeath")
	}
	if f.record.State != domain.Stopped {
		t.Errorf("FAIL AC-12a: want State=Stopped, got %v", f.record.State)
	}
}

// TestVMDeath_ReconcileVMDeath_StopReasonMemoryLost verifies AC-12b:
// reconcileVMDeath writes StopReason=MemoryLost, not Clean.
//
// MUTATION PROOF (AC-12b): change rec.StopReason = domain.StopReasonMemoryLost
// in reconcileVMDeath to domain.StopReasonClean — this test FAILs.
// Substitution count asserted before mutation: exactly 1.
func TestVMDeath_ReconcileVMDeath_StopReasonMemoryLost(t *testing.T) {
	t.Parallel()
	var id domain.SandboxID
	id[0] = 2
	f := &fakeReconciler{record: domain.Sandbox{ID: id, State: domain.Running}}

	if err := reconcileVMDeath(context.Background(), f, id); err != nil {
		t.Fatalf("reconcileVMDeath: %v", err)
	}

	if f.record.StopReason == domain.StopReasonClean {
		t.Errorf("FAIL AC-12b: StopReason=clean — must be %q for an unexpected VM death", domain.StopReasonMemoryLost)
	}
	if f.record.StopReason != domain.StopReasonMemoryLost {
		t.Errorf("FAIL AC-12b: want StopReason=%q, got %q", domain.StopReasonMemoryLost, f.record.StopReason)
	}
}

// TestVMDeath_ReconcileVMDeath_AdoptionFieldsCleared verifies that
// reconcileVMDeath zeros the netns adoption fields so a stale record cannot
// cause AdoptNetnsRuntime to target a recycled pid group.
func TestVMDeath_ReconcileVMDeath_AdoptionFieldsCleared(t *testing.T) {
	t.Parallel()
	var id domain.SandboxID
	id[0] = 3
	f := &fakeReconciler{record: domain.Sandbox{
		ID:                  id,
		State:               domain.Running,
		NetnsChildPID:       12345,
		NetnsChildPGID:      12345,
		NetnsChildStartTime: 9876543210,
		GuestTapName:        "nx3g-test",
		CHAPISocket:         "/tmp/ch.sock",
	}}

	if err := reconcileVMDeath(context.Background(), f, id); err != nil {
		t.Fatalf("reconcileVMDeath: %v", err)
	}

	r := f.record
	if r.NetnsChildPID != 0 {
		t.Errorf("NetnsChildPID not cleared: %d", r.NetnsChildPID)
	}
	if r.NetnsChildPGID != 0 {
		t.Errorf("NetnsChildPGID not cleared: %d", r.NetnsChildPGID)
	}
	if r.NetnsChildStartTime != 0 {
		t.Errorf("NetnsChildStartTime not cleared: %d", r.NetnsChildStartTime)
	}
	if r.GuestTapName != "" {
		t.Errorf("GuestTapName not cleared: %q", r.GuestTapName)
	}
	if r.CHAPISocket != "" {
		t.Errorf("CHAPISocket not cleared: %q", r.CHAPISocket)
	}
}
