package service_test

// supervisor_test.go verifies two properties of the PerimeterSupervisor:
//
//  1. Goroutine shutdown — after Close() all goroutines spawned by the
//     supervisor (frame pump, HTTP server, AllowList DNS-refresh) have exited.
//     Uses perimeter.NoOpPerimeter (not *netstack.Stack) to avoid gvproxy's
//     internal goroutines, which are not tracked by the supervisor's WaitGroup.
//
//  2. Service wiring — service.Start assembles a supervisor when the driver
//     implements driver.NetworkHook and a broker is attached, and service.Stop
//     tears it down by closing the transferred fd.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
	"github.com/IniZio/nexus3/internal/core/perimeter/netfilter"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── fakeNetHookDriver ─────────────────────────────────────────────────────────

// fakeNetHookDriver wraps FakeDriver and adds driver.NetworkHook so that
// service.startSupervisor fires when a broker is attached.
type fakeNetHookDriver struct {
	*fake.FakeDriver
	mu        sync.Mutex
	guestConn net.Conn // transferred on the first GuestNetworkFD call
	calls     int      // how many times GuestNetworkFD was called
}

// GuestNetworkFD implements driver.NetworkHook. Transfers guestConn exactly
// once; subsequent calls return an error to mirror the production single-use
// semantics.
func (f *fakeNetHookDriver) GuestNetworkFD(_ context.Context, _ domain.SandboxID) (io.ReadWriteCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.guestConn == nil {
		return nil, fmt.Errorf("GuestNetworkFD: fd already transferred (call %d)", f.calls)
	}
	c := f.guestConn
	f.guestConn = nil // transfer ownership
	return c, nil
}

// Compile-time assertion: fakeNetHookDriver implements driver.NetworkHook.
var _ driver.NetworkHook = (*fakeNetHookDriver)(nil)

// ── TestPerimeterSupervisor_GoroutineShutdown ─────────────────────────────────

// TestPerimeterSupervisor_GoroutineShutdown asserts that after
// PerimeterSupervisor.Close() all goroutines spawned by the supervisor
// (frame pump, HTTP server, AllowList DNS-refresh) have exited.
func TestPerimeterSupervisor_GoroutineShutdown(t *testing.T) {
	id := domain.NewSandboxID()
	broker := cred.NewBroker()

	// net.Pipe() returns a pair of synchronised net.Conn endpoints.
	// guestConn is the fd handed to the supervisor (and read by the frame pump).
	// hostConn is kept open for the test; closing guestConn (via sup.Close)
	// causes any pending Read on guestConn to return an error, unblocking the
	// frame pump goroutine.
	guestConn, hostConn := net.Pipe()
	defer hostConn.Close()

	// AllowList with no allowed hosts (deny-all policy, no DNS lookups during
	// refreshDomains — the domain slice is empty so the iteration is instant).
	al, err := netfilter.NewAllowList(nil, nil, nil)
	if err != nil {
		t.Fatalf("NewAllowList: %v", err)
	}

	// mitm.Proxy with empty AllowedHosts; no HTTPS connections are made in this
	// test so the CONNECT rejection path is never exercised.
	proxy, err := mitm.New(mitm.Config{
		SandboxID:    id,
		AllowedHosts: nil,
		Broker:       broker,
	})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}

	// Use NoOpPerimeter (not *netstack.Stack) so the only goroutines added are
	// the three owned by the supervisor itself (frame pump, http.Serve, AllowList
	// refresh). netstack.Stack's gvproxy VirtualNetwork spawns additional
	// goroutines internally that are outside the supervisor's WaitGroup and would
	// make the goroutine-count assertion racy.
	pump := &perimeter.NoOpPerimeter{}

	// Snapshot baseline BEFORE creating the supervisor so we account for any
	// goroutines from the test runtime itself.
	baseline := runtime.NumGoroutine()

	ctx := context.Background()
	sup, err := perimeter.Start(ctx, id, guestConn, pump, proxy, al)
	if err != nil {
		t.Fatalf("perimeter.Start: %v", err)
	}

	// Verify that the supervisor actually added goroutines (at least 3).
	afterStart := runtime.NumGoroutine()
	if afterStart <= baseline {
		t.Errorf("expected goroutines to increase after Start; baseline=%d, after=%d",
			baseline, afterStart)
	}

	// Close the supervisor. After this returns, the two WaitGroup goroutines
	// (frame pump + http.Serve) are confirmed done. The AllowList goroutine
	// exits asynchronously shortly after the stop channel is closed.
	if err := sup.Close(); err != nil {
		t.Errorf("supervisor.Close: %v", err)
	}

	// Poll until the goroutine count drops back to baseline (or one above, to
	// tolerate a transient scheduling artefact) with a 5-second deadline. The
	// AllowList goroutine is the only remaining stragglers; it exits as soon as
	// its select sees the closed stop channel.
	const (
		deadline = 5 * time.Second
		interval = 20 * time.Millisecond
	)
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	for {
		current := runtime.NumGoroutine()
		if current <= baseline {
			break
		}
		select {
		case <-timer.C:
			t.Errorf("goroutines did not settle after %v: baseline=%d, current=%d",
				deadline, baseline, runtime.NumGoroutine())
			return
		default:
			time.Sleep(interval)
		}
	}
}

// ── TestServiceWiring_SupervisorLifecycle ─────────────────────────────────────

// TestServiceWiring_SupervisorLifecycle proves that service.Start assembles a
// PerimeterSupervisor (calls GuestNetworkFD exactly once) and service.Stop
// tears it down by closing the transferred fd — verified by observing io.EOF
// on the host side of the net.Pipe after Stop returns.
//
// The real netstack.Stack is used here (pure userspace gvisor — no
// CAP_NET_ADMIN, no disk image). Nothing writes to hostConn, so AcceptVfkit
// blocks until the cancelled context causes a read-deadline unblock.
func TestServiceWiring_SupervisorLifecycle(t *testing.T) {
	// Build a net.Pipe pair. guestConn is returned by GuestNetworkFD and
	// transferred to the supervisor. hostConn stays in the test.
	guestConn, hostConn := net.Pipe()
	// Do NOT defer hostConn.Close() before asserting io.EOF — we read from it.

	drv := &fakeNetHookDriver{
		FakeDriver: fake.New(),
		guestConn:  guestConn,
	}

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	svc := service.New(st, drv, lifecycle.New()).
		WithBroker(cred.NewBroker())

	// Create → Start (triggers supervisor assembly via NetworkHook path).
	sb, err := svc.Create(context.Background(), "proj", "box", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := svc.Start(context.Background(), sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// GuestNetworkFD must have been called exactly once.
	drv.mu.Lock()
	gotCalls := drv.calls
	drv.mu.Unlock()
	if gotCalls != 1 {
		t.Errorf("GuestNetworkFD calls after Start: got %d, want 1", gotCalls)
	}

	// Stop — triggers closeSupervisor → sup.Close() → fd.Close() on guestConn.
	// sup.Close() does not return until wg.Wait() completes, so guestConn is
	// guaranteed closed by the time svc.Stop() returns.
	if _, err := svc.Stop(context.Background(), sb.ID.String()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// After Stop returns, guestConn is closed. Reading from the host end of the
	// pipe must return io.EOF. We wrap in a goroutine+select because
	// hostConn.SetReadDeadline returns io.ErrClosedPipe once the remote end is
	// closed (net.Pipe shares the done channel), so we cannot use a deadline;
	// we rely instead on the 2s fallback to detect regressions.
	readErrCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		_, err := hostConn.Read(buf)
		readErrCh <- err
	}()
	select {
	case readErr := <-readErrCh:
		if !errors.Is(readErr, io.EOF) {
			t.Errorf("hostConn.Read after Stop: got %v, want io.EOF", readErr)
		}
	case <-time.After(2 * time.Second):
		t.Error("hostConn.Read did not return within 2s; guestConn may not be closed by supervisor")
	}
	hostConn.Close()
}
