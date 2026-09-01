package supervisor

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/store"
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

// TestRunReacquire_RefusesIncompleteIdentity asserts RunReacquire re-runs the
// fail-closed preflight itself rather than trusting its spawner. The spawner
// (recovery) has already checked; a check must never be satisfied by "the
// caller already verified it", because the spawner and this process are
// separated by a fork and an argv round-trip.
func TestRunReacquire_RefusesIncompleteIdentity(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.NewFileStore(dir)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// A record that is adoptable-looking but carries NO control socket.
	sb := domain.Sandbox{
		ID: domain.NewSandboxID(), Name: "no-ctl", Project: "hsh",
		State:         domain.Running,
		NetnsChildPID: 4242, NetnsChildPGID: 4242, NetnsChildStartTime: 987654,
		GuestTapName: "nx3h-0102030405", CHAPISocket: "/tmp/x.sock",
	}
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}

	err = RunReacquire(Config{
		SandboxRef: sb.ID.String(),
		StoreRoot:  dir,
		StateDir:   t.TempDir(),
		CHBin:      "/usr/local/bin/cloud-hypervisor",
		SocketDir:  t.TempDir(),
		KernelPath: "/nonexistent/vmlinux",
		DiskPath:   "/nonexistent/disk.raw",
	})
	if err == nil {
		t.Fatal("RunReacquire accepted a record with no control socket; it would have booted a supervisor " +
			"that can never rebuild the perimeter")
	}
	if !errors.Is(err, ErrNotReacquirable) {
		t.Fatalf("err = %v, want it to wrap ErrNotReacquirable", err)
	}
}

// TestSpawnReacquireDetached_RefusesLivePidfile asserts the two-owners guard:
// a pidfile naming a LIVE process means something is already supervising this
// sandbox, and spawning a second supervisor over a live one is worse than the
// bug being fixed.
func TestSpawnReacquireDetached_RefusesLivePidfile(t *testing.T) {
	stateDir := t.TempDir()
	// This test process is, by definition, alive.
	if err := os.WriteFile(PidfilePath(stateDir), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		t.Fatalf("write pidfile: %v", err)
	}

	_, err := SpawnReacquireDetached(SpawnConfig{Config: Config{StateDir: stateDir}})
	if err == nil {
		t.Fatal("SpawnReacquireDetached spawned a second supervisor over a live one")
	}
	if !strings.Contains(err.Error(), "live pid") {
		t.Fatalf("err = %v, want a refusal naming the live pid", err)
	}
	// The live pidfile must be left alone.
	if _, statErr := os.Stat(PidfilePath(stateDir)); statErr != nil {
		t.Errorf("the live supervisor's pidfile was removed: %v", statErr)
	}
}

// TestSpawnReacquireDetached_RefusesAdoptSock asserts adopt and re-acquire
// are mutually exclusive: adopt receives the perimeter fd from a LIVE
// outgoing supervisor, re-acquire exists precisely because there is none.
func TestSpawnReacquireDetached_RefusesAdoptSock(t *testing.T) {
	_, err := SpawnReacquireDetached(SpawnConfig{
		Config:           Config{StateDir: t.TempDir()},
		AdoptHandoffSock: "/tmp/handoff.sock",
	})
	if err == nil {
		t.Fatal("SpawnReacquireDetached accepted an AdoptHandoffSock")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err = %v, want a mutual-exclusion refusal", err)
	}
}

// TestBuildSupervisorArgv_CarriesReacquireFlag asserts the spawn mode
// actually reaches the forked process. Without the flag in argv the child
// would silently run RunDetached and BOOT A SECOND VM for a sandbox that
// already has one running.
func TestBuildSupervisorArgv_CarriesReacquireFlag(t *testing.T) {
	args := BuildSupervisorArgv(SpawnConfig{
		Config:    Config{SandboxRef: "sb-x", StoreRoot: "/s", StateDir: "/st"},
		Reacquire: true,
	})
	found := false
	for _, a := range args {
		if a == "--reacquire" {
			found = true
		}
	}
	if !found {
		t.Fatalf("--reacquire missing from argv %v — the child would run RunDetached and boot a "+
			"SECOND VM for a sandbox that already has one running", args)
	}

	// And absent when not requested.
	plain := BuildSupervisorArgv(SpawnConfig{Config: Config{SandboxRef: "sb-x"}})
	for _, a := range plain {
		if a == "--reacquire" {
			t.Fatalf("--reacquire present in a non-reacquire argv %v", plain)
		}
	}
}
