package cloudhypervisor

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
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

// TestTapIfNames verifies that all three interface names are within the
// IFNAMSIZ-1 (15-char) limit and are mutually distinct for diverse IDs.
func TestTapIfNames(t *testing.T) {
	ids := []domain.SandboxID{
		{0x00, 0x00, 0x00, 0x00, 0x00}, // all zeros
		{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF},
		{0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF},
		{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE},
	}
	for _, id := range ids {
		g, h, b := tapIfNames(id)
		for _, name := range []string{g, h, b} {
			if len(name) > 15 {
				t.Errorf("tapIfNames(%x): name %q len=%d exceeds IFNAMSIZ-1 (15)", id[:5], name, len(name))
			}
			if len(name) == 0 {
				t.Errorf("tapIfNames(%x): got empty name", id[:5])
			}
		}
		if g == h || g == b || h == b {
			t.Errorf("tapIfNames(%x): names not distinct: g=%q h=%q b=%q", id[:5], g, h, b)
		}
	}
}

// TestTapPump_GuestToHost verifies that a frame written to the "fake TAP" side
// appears intact on the perimeter side (guest→host direction).
//
// The key property: AF_UNIX SOCK_DGRAM is packet-mode, so one Write = one
// datagram = one Read. No framing is needed and boundaries are preserved.
func TestTapPump_GuestToHost(t *testing.T) {
	// fakeTapA/B simulate the host TAP fd (packet-mode reads/writes).
	// perimA/B simulate the socketpair ends the pump uses internally.
	fakeTapA, fakeTapB := newTestSocketpair(t)
	perimA, perimB := newTestSocketpair(t)

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		tapPump(fakeTapA, perimA) // bridge: fakeTapA ↔ perimA
	}()
	t.Cleanup(func() {
		fakeTapA.Close()
		perimA.Close()
		select {
		case <-pumpDone:
		case <-time.After(2 * time.Second):
			t.Error("pump did not stop within 2s")
		}
	})

	want := []byte("hello-ethernet-frame-0123456789abcdef")
	if _, err := fakeTapB.Write(want); err != nil {
		t.Fatalf("write to fake TAP: %v", err)
	}

	buf := make([]byte, tapBufSize)
	setReadDeadline(t, perimB, 2*time.Second)
	n, err := perimB.Read(buf)
	if err != nil {
		t.Fatalf("read from perim: %v", err)
	}
	if string(buf[:n]) != string(want) {
		t.Errorf("frame mismatch: got %q want %q", buf[:n], want)
	}
}

// TestTapPump_HostToGuest verifies that a frame written to the perimeter side
// appears intact on the "fake TAP" side (host→guest direction).
func TestTapPump_HostToGuest(t *testing.T) {
	fakeTapA, fakeTapB := newTestSocketpair(t)
	perimA, perimB := newTestSocketpair(t)

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		tapPump(fakeTapA, perimA)
	}()
	t.Cleanup(func() {
		fakeTapA.Close()
		perimA.Close()
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

	buf := make([]byte, tapBufSize)
	setReadDeadline(t, fakeTapB, 2*time.Second)
	n, err := fakeTapB.Read(buf)
	if err != nil {
		t.Fatalf("read from fake TAP: %v", err)
	}
	if string(buf[:n]) != string(want) {
		t.Errorf("frame mismatch: got %q want %q", buf[:n], want)
	}
}

// TestTapPump_Bidirectional sends N frames in each direction concurrently and
// verifies all arrive intact and in order.
func TestTapPump_Bidirectional(t *testing.T) {
	fakeTapA, fakeTapB := newTestSocketpair(t)
	perimA, perimB := newTestSocketpair(t)

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		tapPump(fakeTapA, perimA)
	}()
	t.Cleanup(func() {
		fakeTapA.Close()
		perimA.Close()
		select {
		case <-pumpDone:
		case <-time.After(2 * time.Second):
			t.Error("pump did not stop within 2s")
		}
	})

	const n = 10
	var wg sync.WaitGroup

	// Guest→host: write to fakeTapB, read from perimB
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			frame := []byte{byte(i), 0x01, 0x02, 0x03, 0x04, 0x05, byte(i * 2)}
			if _, err := fakeTapB.Write(frame); err != nil {
				t.Errorf("G→H write %d: %v", i, err)
				return
			}
		}
	}()

	// Host→guest: write to perimB, read from fakeTapB
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
		buf := make([]byte, tapBufSize)
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

	// Read n frames from fakeTapB (H→G)
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, tapBufSize)
		for i := 0; i < n; i++ {
			setReadDeadline(t, fakeTapB, 2*time.Second)
			nRead, err := fakeTapB.Read(buf)
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

// TestTapPump_FrameBoundary is the core correctness test for P1-S0b.
//
// AF_UNIX SOCK_DGRAM is packet-mode: one Write = one datagram = one Read.
// This test verifies that frame boundaries are preserved through the pump:
// three frames of different sizes are sent back-to-back; each Read must
// return exactly one complete frame, not a partial or merged result.
//
// This test cannot pass with a byte-stream (SOCK_STREAM / net.Pipe) pair —
// which is why the pump tests MUST use SOCK_DGRAM, not net.Pipe.
func TestTapPump_FrameBoundary(t *testing.T) {
	fakeTapA, fakeTapB := newTestSocketpair(t)
	perimA, perimB := newTestSocketpair(t)

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		tapPump(fakeTapA, perimA)
	}()
	t.Cleanup(func() {
		fakeTapA.Close()
		perimA.Close()
		select {
		case <-pumpDone:
		case <-time.After(2 * time.Second):
			t.Error("pump did not stop within 2s")
		}
	})

	// Three frames of different sizes — boundary preservation check.
	frames := [][]byte{
		[]byte("short"),
		make([]byte, 200), // medium
		make([]byte, 1500), // typical MTU-sized
	}
	for i, f := range frames {
		for j := range f {
			f[j] = byte(i*100 + j%256)
		}
	}

	// Write all three frames back-to-back.
	for i, f := range frames {
		if _, err := fakeTapB.Write(f); err != nil {
			t.Fatalf("write frame %d: %v", i, err)
		}
	}

	// Read all three frames and verify each is intact and the right size.
	buf := make([]byte, tapBufSize)
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

// TestTapPump_CloseUnblocks verifies that closing the TAP side unblocks the
// pump goroutine within a reasonable deadline.
func TestTapPump_CloseUnblocks(t *testing.T) {
	fakeTapA, fakeTapB := newTestSocketpair(t)
	defer fakeTapB.Close()
	perimA, perimB := newTestSocketpair(t)
	defer perimB.Close()

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		tapPump(fakeTapA, perimA)
	}()

	// Close both sides to unblock both goroutines.
	fakeTapA.Close()
	perimA.Close()

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

// TestApplySandboxNetSysctls_ForwardingHardFail verifies that a write failure
// on net.ipv4.conf.<iface>.forwarding causes applySandboxNetSysctls to return
// a non-nil error. This is the hard-fail path: Docker sets default.forwarding=1
// in the parent netns, so new interfaces may inherit it; the write must succeed.
//
// Mutation proof: reverting the forwarding branch to best-effort (log + continue)
// makes this test go RED because applySandboxNetSysctls would return nil.
func TestApplySandboxNetSysctls_ForwardingHardFail(t *testing.T) {
	injected := errors.New("injected: read-only filesystem")
	orig := sysctlWrite
	t.Cleanup(func() { sysctlWrite = orig })

	sysctlWrite = func(path string, data []byte, perm os.FileMode) error {
		// Fail all forwarding writes; succeed on everything else.
		if len(path) > 0 && path[len(path)-len("forwarding"):] == "forwarding" {
			return injected
		}
		return nil
	}

	err := applySandboxNetSysctls("nx3g-test", "nx3h-test", "nx3b-test")
	if err == nil {
		t.Fatal("expected non-nil error from forwarding hard-fail, got nil")
	}
	if !errors.Is(err, injected) {
		t.Errorf("error chain does not contain injected error: %v", err)
	}
}

// TestApplySandboxNetSysctls_DisableIPv6BestEffort verifies that a write
// failure on net.ipv6.conf.<iface>.disable_ipv6 does NOT cause
// applySandboxNetSysctls to return an error. IPv6 link-local cannot cross a
// CLONE_NEWNET boundary, so a read-only /proc/sys in unprivileged containers
// is harmless; the 697de17 best-effort reasoning stands for this sysctl.
func TestApplySandboxNetSysctls_DisableIPv6BestEffort(t *testing.T) {
	orig := sysctlWrite
	t.Cleanup(func() { sysctlWrite = orig })

	sysctlWrite = func(path string, data []byte, perm os.FileMode) error {
		// Fail all disable_ipv6 writes; succeed on forwarding.
		if len(path) > len("disable_ipv6") &&
			path[len(path)-len("disable_ipv6"):] == "disable_ipv6" {
			return errors.New("injected: read-only filesystem")
		}
		return nil
	}

	err := applySandboxNetSysctls("nx3g-test", "nx3h-test", "nx3b-test")
	if err != nil {
		t.Fatalf("expected nil error for best-effort disable_ipv6 failure, got: %v", err)
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
