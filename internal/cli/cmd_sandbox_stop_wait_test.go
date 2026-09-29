package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
)

// TBD-PD-39. `nexus stop` on a sandbox with a detached supervisor announced
// "stopped sandbox X" and emitted a `sandbox.stopped` envelope carrying
// "state":"running" — a self-contradicting machine contract, reproduced in 2
// of 3 live runs.
//
// StopSupervisor returns when the /stop HTTP response arrives; the supervisor
// tears the VM down and writes the stopped record afterwards. runSandboxStop
// re-read the record and then printed success regardless of what it found.
//
// Two things had to change and both are pinned here: wait for the supervisor
// to exit, and report what the record actually says.

// stopWaitFixture builds a service holding one running sandbox whose record
// carries a supervisor socket, so runSandboxStop takes the detached branch.
func stopWaitFixture(t *testing.T) (*service.Service, domain.Sandbox) {
	t.Helper()
	svc := newTestHerdrService(t)
	ctx := context.Background()
	sb, err := svc.Create(ctx, "st", "waiter", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	started, err := svc.Start(ctx, sb.ID.String())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// A live pid plus a listening socket: runSandboxStop only takes the
	// wait-for-supervisor path for a supervisor CheckAndReconcile calls alive.
	sock := listenFakeSupervisorSock(t, t.TempDir())
	if err := svc.SetSupervisor(ctx, started.ID, os.Getpid(), sock); err != nil {
		t.Fatalf("SetSupervisor: %v", err)
	}
	fresh, err := svc.GetSandboxByID(ctx, started.ID)
	if err != nil {
		t.Fatalf("GetSandboxByID: %v", err)
	}
	return svc, fresh
}

// The wait must actually happen. Without it the command races the supervisor,
// which is the entire defect.
func TestSandboxStop_WaitsForSupervisorExit(t *testing.T) {
	svc, sb := stopWaitFixture(t)

	var waitedOn string
	orig := supervisorWaitForExit
	t.Cleanup(func() { supervisorWaitForExit = orig })
	supervisorWaitForExit = func(_ context.Context, stateDir string) error {
		waitedOn = stateDir
		// Simulate the supervisor finishing: write the stopped state.
		if _, err := svc.Stop(context.Background(), sb.ID.String()); err != nil {
			t.Errorf("stub stop: %v", err)
		}
		return nil
	}

	out, _, _ := newTestOutput(false)
	if err := runSandboxStop(context.Background(), []string{sb.ID.String()}, out, svc); err != nil {
		t.Fatalf("runSandboxStop: %v", err)
	}

	if waitedOn == "" {
		t.Fatal("stop did not wait for the supervisor to exit; it races the record write")
	}
	// WaitForExit polls <stateDir>/supervisor.pid, so it must be given the
	// socket's DIRECTORY, not the socket path.
	if waitedOn != filepath.Dir(sb.SupervisorSock) {
		t.Errorf("waited on %q, want the supervisor state dir %q", waitedOn, filepath.Dir(sb.SupervisorSock))
	}
}

// If the supervisor outruns its timeout and the record still reads `running`,
// the command must NOT claim success. This is the exact contradiction that
// made the envelope untrustworthy.
func TestSandboxStop_RefusesToClaimSuccessWhileStillRunning(t *testing.T) {
	svc, sb := stopWaitFixture(t)

	orig := supervisorWaitForExit
	t.Cleanup(func() { supervisorWaitForExit = orig })
	// Supervisor never finishes: the record stays `running`.
	supervisorWaitForExit = func(context.Context, string) error {
		return errors.New("timed out")
	}

	out, stdout, _ := newTestOutput(false)
	err := runSandboxStop(context.Background(), []string{sb.ID.String()}, out, svc)
	if err == nil {
		t.Fatal("stop reported success while the record still read `running`")
	}
	msg := err.Error()
	for _, want := range []string{"did not finish", "running"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not explain the situation (missing %q)", msg, want)
		}
	}
	if strings.Contains(stdout.String(), "stopped sandbox") {
		t.Errorf("stop printed a success line anyway:\n%s", stdout.String())
	}
}

// The JSON envelope must never disagree with itself: kind `sandbox.stopped`
// with data.state `running` is the machine-readable form of the same lie.
func TestSandboxStop_EnvelopeStateMatchesItsKind(t *testing.T) {
	svc, sb := stopWaitFixture(t)

	orig := supervisorWaitForExit
	t.Cleanup(func() { supervisorWaitForExit = orig })
	supervisorWaitForExit = func(context.Context, string) error {
		_, _ = svc.Stop(context.Background(), sb.ID.String())
		return nil
	}

	out, stdout, _ := newTestOutput(true)
	if err := runSandboxStop(context.Background(), []string{sb.ID.String()}, out, svc); err != nil {
		t.Fatalf("runSandboxStop: %v", err)
	}

	var env struct {
		Kind string `json:"kind"`
		Data struct {
			State string `json:"state"`
		} `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal envelope: %v\n%s", err, stdout.String())
	}
	if env.Kind != "sandbox.stopped" {
		t.Errorf("kind = %q, want sandbox.stopped", env.Kind)
	}
	if env.Data.State != "stopped" {
		t.Errorf("envelope kind is sandbox.stopped but data.state is %q — the contract contradicts itself", env.Data.State)
	}
}

// A supervisor that died leaves the VM up with the record still `running`.
// Stop used to ask the dead supervisor (conn refused), clear its fields and
// report a timeout while never touching the VM. It must fall back to the
// driver. vhost distinguishes the vhost-user record from the legacy tap one.
func runDeadSupervisorStop(t *testing.T, vhost string) {
	t.Helper()
	ctx := context.Background()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	fk := fake.New()
	svc := service.New(st, fk, lifecycle.New())
	sb, err := svc.Create(ctx, "st", "dead", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := svc.Start(ctx, sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Reaped child pid: guaranteed dead, so CheckAndReconcile reports it gone.
	dead := exec.Command("true")
	if err := dead.Run(); err != nil {
		t.Fatalf("run true: %v", err)
	}
	sock := filepath.Join(t.TempDir(), "supervisor.sock")
	if err := st.Update(ctx, sb.ID, func(rec *domain.Sandbox) error {
		rec.SupervisorPID = dead.Process.Pid
		rec.SupervisorSock = sock
		rec.NetnsChildPID = 999999
		rec.VhostSocket = vhost
		return nil
	}); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	orig := supervisorWaitForExit
	t.Cleanup(func() { supervisorWaitForExit = orig })
	supervisorWaitForExit = func(context.Context, string) error { return nil }

	fk.ResetCalls()
	out, _, _ := newTestOutput(false)
	if err := runSandboxStop(ctx, []string{sb.ID.String()}, out, svc); err != nil {
		t.Fatalf("runSandboxStop: %v", err)
	}

	got, err := svc.GetSandboxByID(ctx, sb.ID)
	if err != nil {
		t.Fatalf("GetSandboxByID: %v", err)
	}
	if got.State != domain.Stopped {
		t.Errorf("state = %s, want stopped", got.State)
	}
	stopped := false
	for _, c := range fk.Calls() {
		if c.Kind == fake.CallStop && c.ID == sb.ID {
			stopped = true
		}
	}
	if !stopped {
		t.Error("driver Stop was never called; the VM would stay up")
	}
	if got.SupervisorPID != 0 || got.SupervisorSock != "" {
		t.Errorf("supervisor fields not cleared: pid=%d sock=%q", got.SupervisorPID, got.SupervisorSock)
	}
	if got.NetnsChildPID != 0 || got.VhostSocket != "" {
		t.Errorf("netns fields not cleared: pid=%d vhost=%q", got.NetnsChildPID, got.VhostSocket)
	}
}

func TestSandboxStop_DeadSupervisorFallsBackToDriverStop(t *testing.T) {
	runDeadSupervisorStop(t, "/run/nexus/vhost.sock")
}

func TestSandboxStop_DeadSupervisorLegacyTapFallsBackToDriverStop(t *testing.T) {
	runDeadSupervisorStop(t, "")
}
