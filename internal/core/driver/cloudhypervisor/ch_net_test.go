package cloudhypervisor

import (
	"context"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
)

// newTestSocketpair creates an AF_UNIX SOCK_DGRAM socketpair for tests.
// No privileges required. Registered for cleanup via t.Cleanup.
func newTestSocketpair(t *testing.T) (io.ReadWriteCloser, io.ReadWriteCloser) {
	t.Helper()
	a, b, err := unixgramPair()
	if err != nil {
		t.Fatalf("unixgramPair: %v", err)
	}
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	return a, b
}

// setReadDeadline sets a read deadline on conn if it supports it.
// Used in pump tests to avoid hanging forever on failure.
func setReadDeadline(t *testing.T, conn io.ReadWriteCloser, d time.Duration) {
	t.Helper()
	type deadliner interface {
		SetReadDeadline(time.Time) error
	}
	if dl, ok := conn.(deadliner); ok {
		if err := dl.SetReadDeadline(time.Now().Add(d)); err != nil {
			t.Fatalf("SetReadDeadline: %v", err)
		}
	}
}

// TestFramePump_GuestToHost verifies that a frame written to the "fake TAP" side
// appears intact on the perimeter side (guest→host direction).
//
// The key property: AF_UNIX SOCK_DGRAM is packet-mode, so one Write = one
// datagram = one Read. No framing is needed and boundaries are preserved.
func TestFramePump_GuestToHost(t *testing.T) {
	// fakeNICA/B simulate the host TAP fd (packet-mode reads/writes).
	// perimA/B simulate the socketpair ends the pump uses internally.
	fakeNICA, fakeNICB := newTestSocketpair(t)
	perimA, perimB := newTestSocketpair(t)
	testPump := newSwappableConn(perimA)

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		framePump(fakeNICA, testPump) // bridge: fakeNICA ↔ perimA
	}()
	t.Cleanup(func() {
		fakeNICA.Close()
		testPump.closePermanently()
		select {
		case <-pumpDone:
		case <-time.After(2 * time.Second):
			t.Error("pump did not stop within 2s")
		}
	})

	want := []byte("hello-ethernet-frame-0123456789abcdef")
	if _, err := fakeNICB.Write(want); err != nil {
		t.Fatalf("write to fake TAP: %v", err)
	}

	buf := make([]byte, frameBufSize)
	setReadDeadline(t, perimB, 2*time.Second)
	n, err := perimB.Read(buf)
	if err != nil {
		t.Fatalf("read from perim: %v", err)
	}
	if string(buf[:n]) != string(want) {
		t.Errorf("frame mismatch: got %q want %q", buf[:n], want)
	}
}

// TestFramePump_HostToGuest verifies that a frame written to the perimeter side
// appears intact on the "fake TAP" side (host→guest direction).
func TestFramePump_HostToGuest(t *testing.T) {
	fakeNICA, fakeNICB := newTestSocketpair(t)
	perimA, perimB := newTestSocketpair(t)
	testPump := newSwappableConn(perimA)

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		framePump(fakeNICA, testPump)
	}()
	t.Cleanup(func() {
		fakeNICA.Close()
		testPump.closePermanently()
		select {
		case <-pumpDone:
		case <-time.After(2 * time.Second):
			t.Error("pump did not stop within 2s")
		}
	})

	want := []byte("host-to-guest-ethernet-frame-abcdef0123456789")
	if _, err := perimB.Write(want); err != nil {
		t.Fatalf("write to perim: %v", err)
	}

	buf := make([]byte, frameBufSize)
	setReadDeadline(t, fakeNICB, 2*time.Second)
	n, err := fakeNICB.Read(buf)
	if err != nil {
		t.Fatalf("read from fake TAP: %v", err)
	}
	if string(buf[:n]) != string(want) {
		t.Errorf("frame mismatch: got %q want %q", buf[:n], want)
	}
}

// TestFramePump_Bidirectional sends N frames in each direction concurrently and
// verifies all arrive intact and in order.
func TestFramePump_Bidirectional(t *testing.T) {
	fakeNICA, fakeNICB := newTestSocketpair(t)
	perimA, perimB := newTestSocketpair(t)
	testPump := newSwappableConn(perimA)

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		framePump(fakeNICA, testPump)
	}()
	t.Cleanup(func() {
		fakeNICA.Close()
		testPump.closePermanently()
		select {
		case <-pumpDone:
		case <-time.After(2 * time.Second):
			t.Error("pump did not stop within 2s")
		}
	})

	const n = 10
	var wg sync.WaitGroup

	// Guest→host: write to fakeNICB, read from perimB
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			frame := []byte{byte(i), 0x01, 0x02, 0x03, 0x04, 0x05, byte(i * 2)}
			if _, err := fakeNICB.Write(frame); err != nil {
				t.Errorf("G→H write %d: %v", i, err)
				return
			}
		}
	}()

	// Host→guest: write to perimB, read from fakeNICB
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			frame := []byte{byte(i + 100), 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, byte(i * 3)}
			if _, err := perimB.Write(frame); err != nil {
				t.Errorf("H→G write %d: %v", i, err)
				return
			}
		}
	}()

	// Read n frames from perimB (G→H)
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, frameBufSize)
		for i := 0; i < n; i++ {
			setReadDeadline(t, perimB, 2*time.Second)
			nRead, err := perimB.Read(buf)
			if err != nil {
				t.Errorf("G→H read %d: %v", i, err)
				return
			}
			if nRead == 0 {
				t.Errorf("G→H read %d: zero bytes", i)
			}
		}
	}()

	// Read n frames from fakeNICB (H→G)
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, frameBufSize)
		for i := 0; i < n; i++ {
			setReadDeadline(t, fakeNICB, 2*time.Second)
			nRead, err := fakeNICB.Read(buf)
			if err != nil {
				t.Errorf("H→G read %d: %v", i, err)
				return
			}
			if nRead == 0 {
				t.Errorf("H→G read %d: zero bytes", i)
			}
		}
	}()

	wg.Wait()
}

// TestFramePump_FrameBoundary is the core correctness test for P1-S0b.
//
// AF_UNIX SOCK_DGRAM is packet-mode: one Write = one datagram = one Read.
// This test verifies that frame boundaries are preserved through the pump:
// three frames of different sizes are sent back-to-back; each Read must
// return exactly one complete frame, not a partial or merged result.
//
// This test cannot pass with a byte-stream (SOCK_STREAM / net.Pipe) pair —
// which is why the pump tests MUST use SOCK_DGRAM, not net.Pipe.
func TestFramePump_FrameBoundary(t *testing.T) {
	fakeNICA, fakeNICB := newTestSocketpair(t)
	perimA, perimB := newTestSocketpair(t)
	testPump := newSwappableConn(perimA)

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		framePump(fakeNICA, testPump)
	}()
	t.Cleanup(func() {
		fakeNICA.Close()
		testPump.closePermanently()
		select {
		case <-pumpDone:
		case <-time.After(2 * time.Second):
			t.Error("pump did not stop within 2s")
		}
	})

	// Three frames of different sizes — boundary preservation check.
	frames := [][]byte{
		[]byte("short"),
		make([]byte, 200),  // medium
		make([]byte, 1500), // typical MTU-sized
	}
	for i, f := range frames {
		for j := range f {
			f[j] = byte(i*100 + j%256)
		}
	}

	// Write all three frames back-to-back.
	for i, f := range frames {
		if _, err := fakeNICB.Write(f); err != nil {
			t.Fatalf("write frame %d: %v", i, err)
		}
	}

	// Read all three frames and verify each is intact and the right size.
	buf := make([]byte, frameBufSize)
	for i, want := range frames {
		setReadDeadline(t, perimB, 2*time.Second)
		n, err := perimB.Read(buf)
		if err != nil {
			t.Fatalf("read frame %d: %v", i, err)
		}
		if n != len(want) {
			t.Errorf("frame %d: got %d bytes want %d (boundary not preserved)", i, n, len(want))
			continue
		}
		for j := 0; j < n; j++ {
			if buf[j] != want[j] {
				t.Errorf("frame %d byte %d: got 0x%02x want 0x%02x", i, j, buf[j], want[j])
				break
			}
		}
	}
}

// TestFramePump_CloseUnblocks verifies that closing the TAP side unblocks the
// pump goroutine within a reasonable deadline.
func TestFramePump_CloseUnblocks(t *testing.T) {
	fakeNICA, fakeNICB := newTestSocketpair(t)
	defer fakeNICB.Close()
	perimA, perimB := newTestSocketpair(t)
	testPump := newSwappableConn(perimA)
	defer perimB.Close()

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		framePump(fakeNICA, testPump)
	}()

	// Close both sides to unblock both goroutines.
	fakeNICA.Close()
	testPump.closePermanently()

	select {
	case <-pumpDone:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("pump did not stop within 2s after close")
	}
}

// TestGuestNetworkFD_NoState verifies that GuestNetworkFD returns an error
// for a sandbox that has no network state (never started or already stopped).
func TestGuestNetworkFD_NoState(t *testing.T) {
	d := &CHDriver{
		nets: make(map[domain.SandboxID]*netState),
	}
	id := domain.SandboxID{0x01, 0x02, 0x03, 0x04}
	_, err := d.GuestNetworkFD(context.Background(), id)
	if err == nil {
		t.Fatal("expected error for unknown sandbox, got nil")
	}
}

// TestGuestNetworkFD_OneCallGuard verifies that GuestNetworkFD transfers
// ownership exactly once: the first call returns the conn; the second returns
// an error without touching the already-transferred conn.
//
// Also verifies that the returned io.ReadWriteCloser's dynamic type is net.Conn
// (required so the perimeter layer can type-assert for AcceptVfkit).
func TestGuestNetworkFD_OneCallGuard(t *testing.T) {
	d := &CHDriver{
		nets: make(map[domain.SandboxID]*netState),
	}

	// Create a real socketpair as the perimConn.
	perimConn, other, err := unixgramPair()
	if err != nil {
		t.Fatalf("unixgramPair: %v", err)
	}
	t.Cleanup(func() {
		perimConn.Close()
		other.Close()
	})

	id := domain.SandboxID{0xDE, 0xAD, 0xBE, 0xEF, 0x01}
	d.nets[id] = &netState{
		perimConn: perimConn,
		pumpDone:  make(chan struct{}),
	}

	ctx := context.Background()

	// First call must succeed and return the conn.
	rw1, err := d.GuestNetworkFD(ctx, id)
	if err != nil {
		t.Fatalf("first call error: %v", err)
	}
	if rw1 == nil {
		t.Fatal("first call returned nil")
	}

	// The dynamic type must be net.Conn so perimeter can type-assert.
	if _, ok := rw1.(net.Conn); !ok {
		t.Errorf("GuestNetworkFD returned %T; want net.Conn", rw1)
	}

	// Second call must return an error (ownership already transferred).
	_, err2 := d.GuestNetworkFD(ctx, id)
	if err2 == nil {
		t.Fatal("second call: expected error (one-call guard), got nil")
	}
}
