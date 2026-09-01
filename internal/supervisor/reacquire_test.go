package supervisor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
)

// completeReacquirableSandbox is a record that passes every preflight gate.
// Each negative case below zeroes exactly ONE field, so a test that fails
// proves that specific gate is load-bearing rather than that the record was
// broadly invalid.
func completeReacquirableSandbox() domain.Sandbox {
	var id domain.SandboxID
	for i := range id {
		id[i] = byte(i + 1)
	}
	return domain.Sandbox{
		ID:                  id,
		NetnsChildPID:       4242,
		NetnsChildPGID:      4242,
		NetnsChildStartTime: 987654,
		GuestTapName:        "nx3h-0102030405",
		CHAPISocket:         "/tmp/nexus3/sock/x.sock",
		NetnsControlSocket:  "/tmp/nexus3/sock/netns-control/x.sock",
		NetnsControlToken:   "/tmp/nexus3/sock/netns-control/x.token",
	}
}

// TestReacquirePreflight_RefusesIncompleteIdentity asserts the NEGATIVE
// direction of the fail-closed rail: every missing identity value REFUSES.
// A replacement that cannot fully re-acquire must leave the VM alone, so
// each of these must fail BEFORE any contact is made with the child.
func TestReacquirePreflight_RefusesIncompleteIdentity(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*domain.Sandbox)
		wantSub string
	}{
		{"zero child pid", func(s *domain.Sandbox) { s.NetnsChildPID = 0 }, "no netns child pid"},
		{"negative child pid", func(s *domain.Sandbox) { s.NetnsChildPID = -1 }, "no netns child pid"},
		{"zero child pgid", func(s *domain.Sandbox) { s.NetnsChildPGID = 0 }, "no netns child pgid"},
		{"zero starttime", func(s *domain.Sandbox) { s.NetnsChildStartTime = 0 }, "pid-reuse guard"},
		{"empty guest tap", func(s *domain.Sandbox) { s.GuestTapName = "" }, "no guest tap name"},
		{"empty api socket", func(s *domain.Sandbox) { s.CHAPISocket = "" }, "no CH API socket"},
		{"empty control socket", func(s *domain.Sandbox) { s.NetnsControlSocket = "" }, "no netns control socket"},
		{"empty control token", func(s *domain.Sandbox) { s.NetnsControlToken = "" }, "no netns control token"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sb := completeReacquirableSandbox()
			tc.mutate(&sb)

			err := reacquirePreflight(sb)
			if err == nil {
				t.Fatal("preflight ACCEPTED an incomplete identity; a partial re-acquisition would silently bypass egress policy")
			}
			if !errors.Is(err, ErrNotReacquirable) {
				t.Fatalf("err = %v, want it to wrap ErrNotReacquirable", err)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %q, want it to contain %q", err, tc.wantSub)
			}
		})
	}
}

// TestReacquirePreflight_AcceptsCompleteIdentity is the positive control: it
// proves the negative cases above fail for the reason claimed (the one
// zeroed field) rather than because the fixture never passed at all.
func TestReacquirePreflight_AcceptsCompleteIdentity(t *testing.T) {
	if err := reacquirePreflight(completeReacquirableSandbox()); err != nil {
		t.Fatalf("preflight refused a complete identity: %v", err)
	}
}

// refusingAdopter fails installation, standing in for a driver that already
// has a runtime registered for the sandbox.
type refusingAdopter struct{ called bool }

func (r *refusingAdopter) AdoptRuntime(id domain.SandboxID, rt *cloudhypervisor.NetnsRuntime) error {
	r.called = true
	return errors.New("a runtime is already registered for this sandbox")
}

// TestReacquirePerimeterForSandbox_RefusesBeforeContact asserts that an
// incomplete record never reaches the driver at all. The adopter records
// whether it was called; a call would mean the code proceeded past a gate
// that must have stopped it.
func TestReacquirePerimeterForSandbox_RefusesBeforeContact(t *testing.T) {
	sb := completeReacquirableSandbox()
	sb.NetnsChildStartTime = 0 // the pid-reuse guard

	adopter := &refusingAdopter{}
	res, err := ReacquirePerimeterForSandbox(context.Background(), sb, adopter)
	if err == nil {
		t.Fatal("expected a refusal for a record with no pid-reuse guard")
	}
	if !errors.Is(err, ErrNotReacquirable) {
		t.Fatalf("err = %v, want ErrNotReacquirable", err)
	}
	if adopter.called {
		t.Fatal("driver.AdoptRuntime was called despite a failed preflight")
	}
	if res.Runtime != nil {
		t.Fatal("a runtime was returned for a refused re-acquisition")
	}
}

// TestReacquirePerimeterForSandbox_RefusesWhenChildAbsent asserts that a
// record which passes preflight but names a control socket that does not
// exist still refuses cleanly, without reaching the driver.
func TestReacquirePerimeterForSandbox_RefusesWhenChildAbsent(t *testing.T) {
	sb := completeReacquirableSandbox()
	sb.NetnsControlSocket = "/nonexistent/nexus3-test/control.sock"
	sb.NetnsControlToken = "/nonexistent/nexus3-test/control.token"

	adopter := &refusingAdopter{}
	res, err := ReacquirePerimeterForSandbox(context.Background(), sb, adopter)
	if err == nil {
		t.Fatal("expected a refusal when the control socket does not exist")
	}
	if adopter.called {
		t.Fatal("driver.AdoptRuntime was called despite an unreachable child")
	}
	if res.Runtime != nil {
		t.Fatal("a runtime was returned for a refused re-acquisition")
	}
}
