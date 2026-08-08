package perimeter_test

// seam_test.go proves the driver.NetworkHook → perimeter.Perimeter interface
// seam and ownership/handoff discipline WITHOUT any VM, TAP device, kernel
// capability, or disk image. It uses an AF_UNIX SOCK_DGRAM socketpair as a
// stand-in for the VM-side transport: each Write is a distinct datagram and
// each Read returns exactly one datagram — the same framing contract the
// Perimeter interface requires (IFF_NO_PI TAP: one Read = one Ethernet frame).
//
// This test is intentionally build-tag-free so it runs on every developer
// machine and in CI without special privileges.

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/perimeter"
)

// networkHookDouble is a test-double implementing driver.NetworkHook.
// It holds one end of an AF_UNIX SOCK_DGRAM socketpair.
// GuestNetworkFD transfers ownership exactly once; a second call returns an
// error — the one-call/ownership-once semantic the real driver will enforce.
type networkHookDouble struct {
	guestEnd *os.File
	called   bool
}

// newNetworkHookDouble creates a connected AF_UNIX SOCK_DGRAM pair.
// Returns the double (which holds the guest-side end that the perimeter reads)
// and the host-side end (used by the test to inject frames).
func newNetworkHookDouble(t *testing.T) (*networkHookDouble, *os.File) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	guestEnd := os.NewFile(uintptr(fds[0]), "socketpair-guest")
	hostEnd := os.NewFile(uintptr(fds[1]), "socketpair-host")
	return &networkHookDouble{guestEnd: guestEnd}, hostEnd
}

// GuestNetworkFD implements driver.NetworkHook. Ownership is transferred on
// the first call; the second call returns an error.
func (d *networkHookDouble) GuestNetworkFD(_ context.Context, _ domain.SandboxID) (io.ReadWriteCloser, error) {
	if d.called {
		return nil, fmt.Errorf("seam_test double: GuestNetworkFD called more than once (fd already transferred)")
	}
	d.called = true
	f := d.guestEnd
	d.guestEnd = nil // transfer ownership
	return f, nil
}

// Compile-time assertion: networkHookDouble satisfies driver.NetworkHook.
var _ driver.NetworkHook = (*networkHookDouble)(nil)

// countingNoOp is a variant of perimeter.NoOpPerimeter that counts frames read
// and signals a channel when a target count is reached. Used only in this test.
type countingNoOp struct {
	target int64
	count  atomic.Int64
	done   chan struct{} // closed when count reaches target
}

func (c *countingNoOp) Run(ctx context.Context, _ domain.SandboxID, rw io.ReadWriteCloser) error {
	const maxFrameSize = 65536
	buf := make([]byte, maxFrameSize)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if _, err := rw.Read(buf); err != nil {
			return err
		}
		if n := c.count.Add(1); n >= c.target {
			close(c.done)
			return nil // target reached; stop cleanly
		}
	}
}

// Compile-time assertion: countingNoOp satisfies perimeter.Perimeter.
var _ perimeter.Perimeter = (*countingNoOp)(nil)

// TestSeam_NetworkHookToPerimeter proves the interface seam end-to-end:
//  1. driver.NetworkHook is discoverable via type assertion.
//  2. GuestNetworkFD transfers ownership on the first call; the second call
//     returns an error (one-call guard).
//  3. NoOpPerimeter (counting variant) drains all N raw datagrams written by
//     the host side — demonstrating the frame-drain path with zero privilege.
func TestSeam_NetworkHookToPerimeter(t *testing.T) {
	const nFrames = 5

	dbl, hostEnd := newNetworkHookDouble(t)
	defer hostEnd.Close()

	id := domain.NewSandboxID()

	// 1. Capability discovery via type assertion.
	var hook driver.NetworkHook
	var ok bool
	if hook, ok = any(dbl).(driver.NetworkHook); !ok {
		t.Fatal("type assertion driver.NetworkHook failed on networkHookDouble")
	}

	// 2a. First call: ownership transfer succeeds.
	guestEnd, err := hook.GuestNetworkFD(context.Background(), id)
	if err != nil {
		t.Fatalf("GuestNetworkFD first call: %v", err)
	}
	defer guestEnd.Close()

	// 2b. Second call: must return an error (ownership already transferred).
	if _, err2 := hook.GuestNetworkFD(context.Background(), id); err2 == nil {
		t.Fatal("GuestNetworkFD second call: expected error, got nil")
	}

	// 3. Write all N frames to the host end BEFORE starting the perimeter so
	// they are buffered in the kernel socket and a single Read-loop can drain
	// them without blocking. AF_UNIX SOCK_DGRAM preserves message boundaries:
	// each Write → one datagram → one Read.
	frame := make([]byte, 64)
	for i := range frame {
		frame[i] = byte(i)
	}
	for i := 0; i < nFrames; i++ {
		if _, err := hostEnd.Write(frame); err != nil {
			t.Fatalf("write frame %d: %v", i, err)
		}
	}

	// Start the counting perimeter in a goroutine. It runs until all N frames
	// are counted (returns nil) or the safety-timeout context fires.
	cp := &countingNoOp{target: nFrames, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	perimDone := make(chan error, 1)
	go func() {
		perimDone <- cp.Run(ctx, id, guestEnd)
	}()

	// Wait for perimeter to read all N frames or a safety timeout.
	select {
	case <-cp.done:
		// All frames read; stop the goroutine cleanly.
	case <-time.After(5 * time.Second):
		t.Fatal("timed out: perimeter did not read all frames within 5 s")
	}

	// Wait for Run to return.
	if runErr := <-perimDone; runErr != nil {
		t.Fatalf("perimeter.Run returned error: %v", runErr)
	}

	// Assert exact frame count.
	if got := cp.count.Load(); got != nFrames {
		t.Errorf("perimeter read %d frames, want %d", got, nFrames)
	}
}

// TestNoOpPerimeter_satisfiesPerimeter ensures the production NoOpPerimeter
// still satisfies the perimeter.Perimeter interface.
func TestNoOpPerimeter_satisfiesPerimeter(t *testing.T) {
	var _ perimeter.Perimeter = (*perimeter.NoOpPerimeter)(nil)
}
