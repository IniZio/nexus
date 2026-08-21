package supervisor

import (
	"context"
	"errors"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
)

// alwaysFailProber is a GuestProber whose Ping always returns a non-nil error.
type alwaysFailProber struct{ err error }

func (p *alwaysFailProber) Ping(_ context.Context) error { return p.err }

// alwaysOKProber is a GuestProber whose Ping always returns nil.
type alwaysOKProber struct{}

func (p *alwaysOKProber) Ping(_ context.Context) error { return nil }

// TestProbeAndSeedGuest_DeadProberReturnsError is the mutation guard for the
// ProbeGuestAgent call inside probeAndSeedGuest (D-M4, assertion a):
//
//	Delete ProbeGuestAgent(…) from probeAndSeedGuest → this test fails RED.
//
// When the guest agent is unreachable, probeAndSeedGuest must return a non-nil
// error. RunDetached checks this return and refuses to write supervisor.pid
// (the READY signal), so the spawning CLI gets a hard failure instead of a
// false success for a sandbox whose guest never came up.
func TestProbeAndSeedGuest_DeadProberReturnsError(t *testing.T) {
	seederCalled := false
	old := seedShellProfileFn
	seedShellProfileFn = func(_ context.Context, _ domain.SandboxID, _ service.GuestSeeder) error {
		seederCalled = true
		return nil
	}
	t.Cleanup(func() { seedShellProfileFn = old })

	// An already-cancelled context drains the 30s probe window instantly so the
	// test completes in microseconds rather than waiting out the full timeout.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := probeAndSeedGuest(ctx, domain.SandboxID{}, &alwaysFailProber{err: errors.New("vsock refused")}, nil)
	if err == nil {
		t.Fatal("probeAndSeedGuest with dead prober returned nil — ProbeGuestAgent call may be missing (D-M4 mutation guard)")
	}
	if seederCalled {
		t.Error("shell-profile seeder must not be called when probe fails")
	}
}

// TestProbeAndSeedGuest_LiveProberSeedIsInvoked is the mutation guard for the
// seedShellProfileFn call inside probeAndSeedGuest (D-M4, assertion b):
//
//	Delete seedShellProfileFn(…) from probeAndSeedGuest → this test fails RED.
//
// When the guest agent is reachable, the shell-profile seeder must be invoked
// so that an agent started interactively in a guest shell (the herdr pane path)
// receives its credential via the login-shell drop-in.
func TestProbeAndSeedGuest_LiveProberSeedIsInvoked(t *testing.T) {
	seedCalled := false
	old := seedShellProfileFn
	seedShellProfileFn = func(_ context.Context, _ domain.SandboxID, _ service.GuestSeeder) error {
		seedCalled = true
		return nil
	}
	t.Cleanup(func() { seedShellProfileFn = old })

	err := probeAndSeedGuest(context.Background(), domain.SandboxID{}, &alwaysOKProber{}, nil)
	if err != nil {
		t.Fatalf("probeAndSeedGuest with live prober: unexpected error %v", err)
	}
	if !seedCalled {
		t.Fatal("seedShellProfileFn was not called — SeedGuestShellProfile wiring missing from probeAndSeedGuest (D-M4 mutation guard)")
	}
}
