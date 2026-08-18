package fake_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
)

var ctx = context.Background()

func newID() domain.SandboxID { return domain.NewSandboxID() }

// RunState zero-value safety

func TestRunStateZeroIsUnknown(t *testing.T) {
	var s driver.RunState
	if s != driver.Unknown {
		t.Fatalf("zero RunState must be Unknown, got %v", s)
	}
	if s == driver.Absent {
		t.Fatal("Unknown must not equal Absent")
	}
}

// Simulated scenarios

func TestObserve_Running(t *testing.T) {
	f := fake.New()
	id := newID()
	f.SetRunning(id)

	obs, err := f.Observe(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs.State != driver.Running {
		t.Fatalf("want Running, got %v", obs.State)
	}
	if obs.InstanceID == "" {
		t.Fatal("InstanceID must be non-empty for a running VM")
	}
}

func TestObserve_Paused(t *testing.T) {
	f := fake.New()
	id := newID()
	f.SetPaused(id)

	obs, err := f.Observe(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs.State != driver.Paused {
		t.Fatalf("want Paused, got %v", obs.State)
	}
}

func TestObserve_Absent(t *testing.T) {
	f := fake.New()
	id := newID()
	// Never started.

	obs, err := f.Observe(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error for absent VM: %v", err)
	}
	if obs.State != driver.Absent {
		t.Fatalf("want Absent, got %v", obs.State)
	}
}

// TestObserve_Unknown asserts that an injected observation failure yields
// Unknown + non-nil error, and that Unknown != Absent.
func TestObserve_Unknown(t *testing.T) {
	f := fake.New()
	id := newID()
	f.SetRunning(id) // VM exists, but we'll fail to observe it.

	injected := errors.New("substrate unreachable")
	f.SetObserveError(injected)

	obs, err := f.Observe(ctx, id)
	if err == nil {
		t.Fatal("want non-nil error when observation fails")
	}
	if !errors.Is(err, injected) {
		t.Fatalf("want injected error, got %v", err)
	}
	if obs.State != driver.Unknown {
		t.Fatalf("want Unknown, got %v", obs.State)
	}
	if obs.State == driver.Absent {
		t.Fatal("Unknown must not equal Absent — conflating them deletes live VMs")
	}
}

// TestSimulateHostReboot checks that Paused VMs become Absent while Running
// VMs are unaffected.
func TestSimulateHostReboot(t *testing.T) {
	f := fake.New()

	paused := newID()
	running := newID()
	f.SetPaused(paused)
	f.SetRunning(running)

	f.SimulateHostReboot()

	obs, err := f.Observe(ctx, paused)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs.State != driver.Absent {
		t.Fatalf("after host reboot, Paused VM must be Absent, got %v", obs.State)
	}

	obs, err = f.Observe(ctx, running)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs.State != driver.Running {
		t.Fatalf("after host reboot, Running VM must remain Running, got %v", obs.State)
	}
}

// TestSimulateCrash checks that a crashing VMM makes a Running VM absent.
func TestSimulateCrash(t *testing.T) {
	f := fake.New()
	id := newID()
	f.SetRunning(id)

	f.SimulateCrash(id)

	obs, err := f.Observe(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obs.State != driver.Absent {
		t.Fatalf("after crash, VM must be Absent, got %v", obs.State)
	}
}

// Start / Stop

func TestStart_CreatesRunningVM(t *testing.T) {
	f := fake.New()
	id := newID()

	iid, err := f.Start(ctx, driver.StartRequest{SandboxID: id, ImageDigest: "sha256:abc"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if iid == "" {
		t.Fatal("Start must return a non-empty InstanceID")
	}

	obs, err := f.Observe(ctx, id)
	if err != nil {
		t.Fatalf("Observe after Start: %v", err)
	}
	if obs.State != driver.Running {
		t.Fatalf("want Running after Start, got %v", obs.State)
	}
	if obs.InstanceID != iid {
		t.Fatalf("InstanceID mismatch: want %q, got %q", iid, obs.InstanceID)
	}
}

func TestStart_InjectError(t *testing.T) {
	f := fake.New()
	id := newID()
	f.SetStartError(errors.New("disk full"))

	_, err := f.Start(ctx, driver.StartRequest{SandboxID: id})
	if err == nil {
		t.Fatal("want error from injected start error")
	}

	// VM must not have been created.
	obs, _ := f.Observe(ctx, id)
	if obs.State != driver.Absent {
		t.Fatalf("after failed Start, VM must be Absent, got %v", obs.State)
	}
}

// TestStop_Idempotent verifies that stopping an already-absent VM is not
// an error.
func TestStop_Idempotent(t *testing.T) {
	f := fake.New()
	id := newID()

	// Stop a VM that was never started.
	if err := f.Stop(ctx, id); err != nil {
		t.Fatalf("Stop on absent VM must not error, got: %v", err)
	}

	// Start then stop twice.
	f.Start(ctx, driver.StartRequest{SandboxID: id}) //nolint:errcheck
	if err := f.Stop(ctx, id); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := f.Stop(ctx, id); err != nil {
		t.Fatalf("second Stop (idempotent): %v", err)
	}
}

func TestStop_LeavesVMAbsent(t *testing.T) {
	f := fake.New()
	id := newID()
	f.SetRunning(id)

	if err := f.Stop(ctx, id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	obs, _ := f.Observe(ctx, id)
	if obs.State != driver.Absent {
		t.Fatalf("after Stop, VM must be Absent, got %v", obs.State)
	}
}

// Optional capabilities

func TestCapabilityAssertions(t *testing.T) {
	f := fake.New()

	var d driver.Driver = f

	if _, ok := d.(driver.PauseResumer); !ok {
		t.Error("fake must satisfy driver.PauseResumer")
	}
	if _, ok := d.(driver.Snapshotter); !ok {
		t.Error("fake must satisfy driver.Snapshotter")
	}
	if _, ok := d.(driver.Forker); !ok {
		t.Error("fake must satisfy driver.Forker")
	}
}

func TestCapabilitiesHelper(t *testing.T) {
	caps := driver.Capabilities(fake.New())
	want := map[string]bool{
		"PauseResumer":    true,
		"GuestDialer":     true,
		"Snapshotter":     true,
		"Forker":          true,
		"SnapshotRemover": true,
	}
	for _, c := range caps {
		if !want[c] {
			t.Errorf("unexpected capability: %q", c)
		}
		delete(want, c)
	}
	for c := range want {
		t.Errorf("missing capability: %q", c)
	}
}

// PauseResumer

func TestPauseResume(t *testing.T) {
	f := fake.New()
	id := newID()
	f.SetRunning(id)

	pr := driver.PauseResumer(f)

	if err := pr.Pause(ctx, id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	obs, _ := f.Observe(ctx, id)
	if obs.State != driver.Paused {
		t.Fatalf("after Pause, want Paused, got %v", obs.State)
	}

	if err := pr.Resume(ctx, id); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	obs, _ = f.Observe(ctx, id)
	if obs.State != driver.Running {
		t.Fatalf("after Resume, want Running, got %v", obs.State)
	}
}

// Call log

// TestCallLog_ObserveBeforeStart verifies the call log records ordering.
func TestCallLog_ObserveBeforeStart(t *testing.T) {
	f := fake.New()
	id := newID()

	f.Observe(ctx, id)                               //nolint:errcheck
	f.Start(ctx, driver.StartRequest{SandboxID: id}) //nolint:errcheck
	f.Observe(ctx, id)                               //nolint:errcheck

	calls := f.Calls()
	if len(calls) < 3 {
		t.Fatalf("want at least 3 calls, got %d", len(calls))
	}
	if calls[0].Kind != fake.CallObserve {
		t.Errorf("first call must be Observe, got %v", calls[0].Kind)
	}
	if calls[1].Kind != fake.CallStart {
		t.Errorf("second call must be Start, got %v", calls[1].Kind)
	}
	if calls[2].Kind != fake.CallObserve {
		t.Errorf("third call must be Observe, got %v", calls[2].Kind)
	}
}

// Concurrency / race detector

// TestConcurrentAccess exercises Observe, Start, and Stop concurrently.
// Run with go test -race to detect data races.
func TestConcurrentAccess(t *testing.T) {
	f := fake.New()
	id := newID()

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			switch i % 3 {
			case 0:
				f.Observe(ctx, id) //nolint:errcheck
			case 1:
				f.Start(ctx, driver.StartRequest{SandboxID: id}) //nolint:errcheck
			case 2:
				f.Stop(ctx, id) //nolint:errcheck
			}
		}(i)
	}
	wg.Wait()
}

// TestConcurrentObserveAndSimulate exercises SimulateHostReboot concurrently
// with Observe to catch races in the simulation helpers.
func TestConcurrentObserveAndSimulate(t *testing.T) {
	f := fake.New()
	id := newID()
	f.SetPaused(id)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.Observe(ctx, id) //nolint:errcheck
		}()
	}
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f.SimulateHostReboot()
		}()
	}
	wg.Wait()
}
