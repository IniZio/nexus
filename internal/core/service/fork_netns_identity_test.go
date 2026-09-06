package service_test

// fork_netns_identity_test.go proves that Service.Fork persists the five netns
// adoption identity fields for networked forked children (D-HSH-25, AC-1..4).
//
// The five fields — NetnsChildPID, NetnsChildPGID, NetnsChildStartTime,
// GuestTapName, CHAPISocket — are the ones supervisor-upgrade guards at
// cmd_supervisor_upgrade.go:159-165. A networked forked child missing any one
// of them is treated identically to a vsock-only child: supervisor-upgrade
// refuses with supervisor_upgrade_incomplete_netns_code. These tests prove the
// fields arrive from the real call site (the type assertion in service.go), not
// from a hand-made stand-in.
//
// Driver shape: in production the driver is always CHDriver, which ALWAYS
// implements driver.NetnsStateProvider. The distinction between "networked" and
// "vsock-only" is not which interface the driver implements; it is what
// NetnsState returns (ok=true vs ok=false). Both tests therefore use a driver
// that implements NetnsStateProvider. The vsock test passes returnOK=false to
// simulate "no netns runtime registered for this child ID" (the branch
// CHDriver takes at ch_net.go:515-517 when d.nets[id]==nil).

import (
	"context"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── forkNetnsDriver ──────────────────────────────────────────────────────────

// forkNetnsDriver wraps FakeDriver and implements driver.NetnsStateProvider —
// mirroring the production CHDriver which always implements the interface.
// returnOK controls whether NetnsState signals an active netns runtime:
//
//   - returnOK=true:  networked fork — child has a live netns runtime.
//   - returnOK=false: vsock-only fork — no netns runtime registered for child.
//
// The sentinel identity (wantIdentity) is returned in BOTH cases so that a
// mutation which assigns regardless of ok still produces a detectable non-zero
// value in the vsock test.
type forkNetnsDriver struct {
	*fake.FakeDriver
	wantIdentity driver.NetnsIdentity
	returnOK     bool
}

func (f *forkNetnsDriver) NetnsState(_ domain.SandboxID) (driver.NetnsIdentity, bool) {
	return f.wantIdentity, f.returnOK
}

var _ driver.NetnsStateProvider = (*forkNetnsDriver)(nil)

func newForkNetnsSvc(t *testing.T, identity driver.NetnsIdentity, returnOK bool) *service.Service {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := &forkNetnsDriver{
		FakeDriver:   fake.New(),
		wantIdentity: identity,
		returnOK:     returnOK,
	}
	return service.New(st, drv, lifecycle.New())
}

// startedForkParent creates a sandbox and moves it to Running state.
func startedForkParent(t *testing.T, svc *service.Service) domain.Sandbox {
	t.Helper()
	c := context.Background()
	sb, err := svc.Create(c, "proj", "netns-fork-parent", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Start(c, sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return sb
}

// sentinel is a non-zero NetnsIdentity used as the driver's return value in
// both tests. Its non-zero ChildPID makes it detectable if unconditionally
// assigned to a record that should carry zero fields (vsock AC-2).
var sentinel = driver.NetnsIdentity{
	ChildPID:       12345,
	ChildPGID:      12340,
	ChildStartTime: 9876543210,
	GuestTap:       "tap-test-0",
	APISocket:      "/run/nexus3/test.sock",
	ControlSocket:  "/run/nexus3/ctrl.sock",
	ControlToken:   "tok-abc",
}

// ── TestFork_NetnsIdentity_Networked ─────────────────────────────────────────

// TestFork_NetnsIdentity_Networked asserts that a forked child of a networked
// sandbox carries all five identity fields matching the netns runtime the driver
// registered during ForkFrom (AC-1).
//
// Mutation-proof: removing the
//
//	if nsp, ok := s.driver.(driver.NetnsStateProvider); ok { ... }
//
// block in service.go makes this test fail because child.NetnsChildPID == 0.
func TestFork_NetnsIdentity_Networked(t *testing.T) {
	svc := newForkNetnsSvc(t, sentinel, true /* networked: returnOK=true */)
	c := context.Background()

	parent := startedForkParent(t, svc)

	children, err := svc.Fork(c, parent.ID.String(), 1)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("Fork returned %d children, want 1", len(children))
	}
	child := children[0]

	// All five identity fields must be present on the returned record.
	// Missing any one means supervisor-upgrade refuses hot-swap for this child.
	if child.NetnsChildPID != sentinel.ChildPID {
		t.Errorf("NetnsChildPID = %d, want %d — identity not persisted on networked fork path",
			child.NetnsChildPID, sentinel.ChildPID)
	}
	if child.NetnsChildPGID != sentinel.ChildPGID {
		t.Errorf("NetnsChildPGID = %d, want %d", child.NetnsChildPGID, sentinel.ChildPGID)
	}
	if child.NetnsChildStartTime != sentinel.ChildStartTime {
		t.Errorf("NetnsChildStartTime = %d, want %d", child.NetnsChildStartTime, sentinel.ChildStartTime)
	}
	if child.GuestTapName != sentinel.GuestTap {
		t.Errorf("GuestTapName = %q, want %q", child.GuestTapName, sentinel.GuestTap)
	}
	if child.CHAPISocket != sentinel.APISocket {
		t.Errorf("CHAPISocket = %q, want %q", child.CHAPISocket, sentinel.APISocket)
	}
}

// ── TestFork_NetnsIdentity_Vsock ─────────────────────────────────────────────

// TestFork_NetnsIdentity_Vsock asserts that a vsock-only forked child's record
// carries zero/empty netns identity fields (AC-2). The driver DOES implement
// NetnsStateProvider (mirroring CHDriver, which always does), but returns
// ok=false — the same branch CHDriver takes at ch_net.go:515-517 when no
// netns runtime was registered for the child ID.
//
// Mutation-proof (the fail-open this motive has shipped three times):
// removing the "if hasNetns" guard in service.go makes the service assign
// sentinel unconditionally. The vsock test then fails because
// child.NetnsChildPID == 12345 instead of 0.
func TestFork_NetnsIdentity_Vsock(t *testing.T) {
	// returnOK=false: driver reports no netns runtime for any child ID.
	// wantIdentity=sentinel: non-zero so that unconditional assignment is detectable.
	svc := newForkNetnsSvc(t, sentinel, false /* vsock-only: returnOK=false */)
	c := context.Background()

	sb, err := svc.Create(c, "proj", "vsock-fork-parent", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Start(c, sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	children, err := svc.Fork(c, sb.ID.String(), 1)
	if err != nil {
		t.Fatalf("Fork: %v", err)
	}
	if len(children) != 1 {
		t.Fatalf("Fork returned %d children, want 1", len(children))
	}
	child := children[0]

	// All five fields must be zero/empty — supervisor-upgrade refuses these,
	// which is correct: a vsock-only child is not hot-swappable.
	// A fail-open mutation (assign regardless of hasNetns) would set
	// NetnsChildPID=12345 here, turning this test RED.
	if child.NetnsChildPID != 0 {
		t.Errorf("vsock child NetnsChildPID = %d, want 0 (sentinel identity was assigned despite ok=false)",
			child.NetnsChildPID)
	}
	if child.NetnsChildPGID != 0 {
		t.Errorf("vsock child NetnsChildPGID = %d, want 0", child.NetnsChildPGID)
	}
	if child.NetnsChildStartTime != 0 {
		t.Errorf("vsock child NetnsChildStartTime = %d, want 0", child.NetnsChildStartTime)
	}
	if child.GuestTapName != "" {
		t.Errorf("vsock child GuestTapName = %q, want empty", child.GuestTapName)
	}
	if child.CHAPISocket != "" {
		t.Errorf("vsock child CHAPISocket = %q, want empty", child.CHAPISocket)
	}
}
