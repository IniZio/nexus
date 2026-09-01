package recovery

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/store"
)

var errSpawnRefused = errors.New("spawn refused: netns child did not answer")

// newDeadSupervisorSandbox records a Running sandbox whose supervisor is
// reported dead by the injected liveness check, optionally carrying the netns
// control socket a crash-path re-acquisition requires.
func newDeadSupervisorSandbox(t *testing.T, withControlSocket bool) (store.Store, *fake.FakeDriver, domain.Sandbox) {
	t.Helper()
	ctx := context.Background()

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := fake.New()

	sb := domain.Sandbox{
		ID: domain.NewSandboxID(), Name: "dead-sup", Project: "hsh",
		State:         domain.Running,
		SupervisorPID: 424242,
		NetnsChildPID: 4242, NetnsChildPGID: 4242, NetnsChildStartTime: 987654,
		GuestTapName: "nx3h-0102030405", CHAPISocket: "/tmp/x.sock",
	}
	if withControlSocket {
		sb.NetnsControlSocket = "/tmp/netns-control/x.sock"
		sb.NetnsControlToken = "/tmp/netns-control/x.token"
	}
	if err := st.Create(ctx, sb); err != nil {
		t.Fatalf("Create: %v", err)
	}
	drv.SetRunning(sb.ID)
	return st, drv, sb
}

// assertVMStillRunning fails unless the driver still observes id as Running.
// This is the D-HSH-04 guard: recovery may adopt a live VM, never stop one.
func assertVMStillRunning(t *testing.T, drv *fake.FakeDriver, id domain.SandboxID) {
	t.Helper()
	obs, err := drv.Observe(context.Background(), id)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.State != driver.Running {
		t.Fatalf("VM state = %s, want %s — recovery may adopt a live VM, never stop one (D-HSH-04)",
			obs.State, driver.Running)
	}
}

// deadSupervisor is a liveness check that always reports the supervisor dead.
func deadSupervisor(int, string) (bool, error) { return false, nil }

func outcomeFor(t *testing.T, rep Report, id domain.SandboxID) SandboxOutcome {
	t.Helper()
	for _, o := range rep.Outcomes {
		if o.ID == id {
			return o
		}
	}
	t.Fatalf("no outcome for %s in %+v", id, rep.Outcomes)
	return SandboxOutcome{}
}

// TestSpawn_CalledForAdoptableWithControlSocket asserts the spawner receives
// the adoptable sandbox, and that the outcome reports both the adoption and
// the CA loss.
func TestSpawn_CalledForAdoptableWithControlSocket(t *testing.T) {
	ctx := context.Background()
	st, drv, sb := newDeadSupervisorSandbox(t, true)

	var got []domain.Sandbox
	rep, err := New(st, drv).
		WithSupervisorCheck(deadSupervisor).
		WithAdoptSpawner(func(s domain.Sandbox) (bool, error) {
			got = append(got, s)
			return true, nil
		}).
		Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}

	if len(got) != 1 || got[0].ID != sb.ID {
		t.Fatalf("spawner not called for the adoptable sandbox: got %+v", got)
	}
	// The spawner must receive the netns identity it needs — passing a record
	// stripped of it would make every spawn refuse at preflight.
	if got[0].NetnsControlSocket == "" || got[0].NetnsChildStartTime == 0 {
		t.Errorf("spawner received a record missing netns identity: %+v", got[0])
	}
	o := outcomeFor(t, rep, sb.ID)
	if o.Kind != OutcomeAdopted {
		t.Errorf("outcome kind = %s, want %s after a successful spawn", o.Kind, OutcomeAdopted)
	}
	if !strings.Contains(o.Reason, "replacement supervisor was started") {
		t.Errorf("reason does not report the spawn: %q", o.Reason)
	}
	if !strings.Contains(o.Reason, "TLS") {
		t.Errorf("reason does not report the CA loss / TLS breakage: %q", o.Reason)
	}
}

// TestSpawn_RefusedWithoutControlSocket is the fail-closed, non-retroactive
// case: a VM booted before the control-socket mechanism must be reported and
// NOT spawned against.
func TestSpawn_RefusedWithoutControlSocket(t *testing.T) {
	ctx := context.Background()
	st, drv, sb := newDeadSupervisorSandbox(t, false)

	calls := 0
	rep, err := New(st, drv).
		WithSupervisorCheck(deadSupervisor).
		WithAdoptSpawner(func(domain.Sandbox) (bool, error) { calls++; return true, nil }).
		Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if calls != 0 {
		t.Fatalf("spawner was called for a sandbox with no control socket (%d calls)", calls)
	}
	o := outcomeFor(t, rep, sb.ID)
	if o.Kind != OutcomeAdoptable {
		t.Errorf("outcome kind = %s, want %s — it must still be reported", o.Kind, OutcomeAdoptable)
	}
	if !strings.Contains(o.Reason, "predates the netns control socket") {
		t.Errorf("reason does not explain why no replacement was started: %q", o.Reason)
	}
}

// TestSpawn_FailureLeavesSandboxAdoptable asserts that a failed spawn does
// NOT report success and does NOT touch the VM: the sandbox stays adoptable
// so a later run (or an operator) can retry.
func TestSpawn_FailureLeavesSandboxAdoptable(t *testing.T) {
	ctx := context.Background()
	st, drv, sb := newDeadSupervisorSandbox(t, true)

	rep, err := New(st, drv).
		WithSupervisorCheck(deadSupervisor).
		WithAdoptSpawner(func(domain.Sandbox) (bool, error) {
			return false, errSpawnRefused
		}).
		Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	o := outcomeFor(t, rep, sb.ID)
	if o.Kind != OutcomeAdoptable {
		t.Fatalf("a FAILED spawn reported kind %s; it must remain %s so the failure is visible "+
			"and retryable, not read as a successful adoption", o.Kind, OutcomeAdoptable)
	}
	if !strings.Contains(o.Reason, "adopt spawn failed") {
		t.Errorf("reason does not surface the spawn failure: %q", o.Reason)
	}
	if !strings.Contains(o.Reason, "untouched") {
		t.Errorf("reason does not state the VM was left alone: %q", o.Reason)
	}
	// D-HSH-04: the VM must still be running — recovery may adopt, never stop.
	assertVMStillRunning(t, drv, sb.ID)
}

// TestSpawn_UnwiredIsReportOnly asserts the pre-existing AC-8 behaviour is
// preserved when no spawner is wired: classify and report, never act.
func TestSpawn_UnwiredIsReportOnly(t *testing.T) {
	ctx := context.Background()
	st, drv, sb := newDeadSupervisorSandbox(t, true)

	rep, err := New(st, drv).WithSupervisorCheck(deadSupervisor).Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	o := outcomeFor(t, rep, sb.ID)
	if o.Kind != OutcomeAdoptable {
		t.Errorf("kind = %s, want %s", o.Kind, OutcomeAdoptable)
	}
	assertVMStillRunning(t, drv, sb.ID)
}

// TestSpawn_NotCalledForHealthySandbox asserts the dangerous direction: a
// sandbox whose supervisor is ALIVE must never be spawned against, or two
// supervisors would own the same VM.
func TestSpawn_NotCalledForHealthySandbox(t *testing.T) {
	ctx := context.Background()
	st, drv, sb := newDeadSupervisorSandbox(t, true)

	calls := 0
	rep, err := New(st, drv).
		WithSupervisorCheck(func(int, string) (bool, error) { return true, nil }). // ALIVE
		WithAdoptSpawner(func(domain.Sandbox) (bool, error) { calls++; return true, nil }).
		Recover(ctx)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if calls != 0 {
		t.Fatalf("spawner was called for a sandbox with a LIVE supervisor (%d calls)", calls)
	}
	if o := outcomeFor(t, rep, sb.ID); o.Kind == OutcomeAdoptable {
		t.Errorf("a sandbox with a live supervisor was classified adoptable: %+v", o)
	}
}
