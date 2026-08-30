package service_test

// start_perimeter_only_test.go verifies Service.StartPerimeterOnly, the seam
// the supervisor's adopt-mode entrypoint (RunAdopt) uses in place of Start
// when a VM predates the process and must not be rebooted (motive
// nexus3-host-supervisor-hotswap, slice 07).

import (
	"context"
	"net"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// TestStartPerimeterOnly_RefusesWhenNotRunning is the mutation-bearing proof
// that StartPerimeterOnly never wires a perimeter for a sandbox record that
// is not domain.Running — this method must never be the thing that starts a
// perimeter for a VM that is not actually up.
func TestStartPerimeterOnly_RefusesWhenNotRunning(t *testing.T) {
	guestConn, hostConn := net.Pipe()
	t.Cleanup(func() { hostConn.Close() })

	drv := &fakeNetHookDriver{
		FakeDriver: fake.New(),
		guestConn:  guestConn,
	}
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc := service.New(st, drv, lifecycle.New()).WithBroker(cred.NewBroker())

	sb, err := svc.Create(context.Background(), "proj", "not-running", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// sb.State is domain.Created here — Create does not start the VM.

	if err := svc.StartPerimeterOnly(context.Background(), sb, nil); err == nil {
		t.Fatal("expected StartPerimeterOnly to refuse a non-running sandbox")
	}

	drv.mu.Lock()
	calls := drv.calls
	drv.mu.Unlock()
	if calls != 0 {
		t.Errorf("GuestNetworkFD was called %d times despite the non-running refusal", calls)
	}
}

// TestStartPerimeterOnly_WiresPerimeterWhenRunning proves the positive path:
// given a record already marked Running (as an adopted sandbox's record
// already is, since the VM predates the adopting process), StartPerimeterOnly
// calls GuestNetworkFD exactly once — the same assembly
// TestServiceWiring_SupervisorLifecycle proves for the boot path via Start.
func TestStartPerimeterOnly_WiresPerimeterWhenRunning(t *testing.T) {
	guestConn, hostConn := net.Pipe()
	t.Cleanup(func() { hostConn.Close() })

	drv := &fakeNetHookDriver{
		FakeDriver: fake.New(),
		guestConn:  guestConn,
	}
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc := service.New(st, drv, lifecycle.New()).WithBroker(cred.NewBroker())

	sb, err := svc.Create(context.Background(), "proj", "already-running", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := st.Update(context.Background(), sb.ID, func(rec *domain.Sandbox) error {
		rec.State = domain.Running
		return nil
	}); err != nil {
		t.Fatalf("st.Update: %v", err)
	}
	sb.State = domain.Running

	if err := svc.StartPerimeterOnly(context.Background(), sb, nil); err != nil {
		t.Fatalf("StartPerimeterOnly: %v", err)
	}

	drv.mu.Lock()
	calls := drv.calls
	drv.mu.Unlock()
	if calls != 1 {
		t.Errorf("GuestNetworkFD calls: got %d, want 1", calls)
	}
}
