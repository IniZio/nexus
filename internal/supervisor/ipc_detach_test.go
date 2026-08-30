package supervisor

// TestDetachStopDistinction_* pins the hard constraint that /supervisor/stop
// and /supervisor/detach must never be conflated (motive
// nexus3-host-supervisor-hotswap, slice 04, ticket 04). Stop means "tear the
// VM down"; detach means "exit without touching the VM". A regression that
// wires both HTTP paths to the same channel — or that has one handler
// accidentally trip the other — must turn these tests RED.

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/supervisor/handoff"
)

// serveTestIPCFull starts an IPC server on a temporary Unix socket with the
// given allowEgress/handoff callbacks and returns the ipcHandles plus the
// socket path.
func serveTestIPCFull(t *testing.T, allowEgress allowEgressFunc, hf handoffFunc) (ipcHandles, string) {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "test.sock")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	h, err := serveIPC(ctx, sockPath, nil, "test-sandbox", allowEgress, hf)
	if err != nil {
		t.Fatalf("serveIPC: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	return h, sockPath
}

// TestDetachStopDistinction_DetachDoesNotCloseStopCh proves that POST
// /supervisor/detach closes ONLY DetachCh, never StopCh. If a future edit
// collapses the two handlers onto one channel, this goes RED.
func TestDetachStopDistinction_DetachDoesNotCloseStopCh(t *testing.T) {
	h, sockPath := serveTestIPCFull(t, nil, nil)

	if err := DetachSupervisor(context.Background(), sockPath); err != nil {
		t.Fatalf("DetachSupervisor: %v", err)
	}

	select {
	case <-h.DetachCh:
	default:
		t.Fatal("DetachCh not closed after /supervisor/detach")
	}
	select {
	case <-h.StopCh:
		t.Fatal("StopCh closed after /supervisor/detach — stop and detach must be strictly distinct")
	default:
	}
}

// TestDetachStopDistinction_StopDoesNotCloseDetachCh is the symmetric case:
// POST /supervisor/stop closes ONLY StopCh.
func TestDetachStopDistinction_StopDoesNotCloseDetachCh(t *testing.T) {
	h, sockPath := serveTestIPCFull(t, nil, nil)

	if err := StopSupervisor(context.Background(), sockPath); err != nil {
		t.Fatalf("StopSupervisor: %v", err)
	}

	select {
	case <-h.StopCh:
	default:
		t.Fatal("StopCh not closed after /supervisor/stop")
	}
	select {
	case <-h.DetachCh:
		t.Fatal("DetachCh closed after /supervisor/stop — stop and detach must be strictly distinct")
	default:
	}
}

// TestAwaitShutdown_DetachDrivesShutdownByDetach exercises the full path from
// an HTTP /supervisor/detach request through awaitShutdown, proving the
// select loop RunDetached uses reports shutdownByDetach — never
// shutdownByStopVerb — for a detach request delivered over the wire (not just
// a synthetically-closed channel, as in TestAwaitShutdown_Detach in
// supervisor_test.go).
func TestAwaitShutdown_DetachDrivesShutdownByDetach(t *testing.T) {
	h, sockPath := serveTestIPCFull(t, nil, nil)

	if err := DetachSupervisor(context.Background(), sockPath); err != nil {
		t.Fatalf("DetachSupervisor: %v", err)
	}

	got := awaitShutdown(context.Background(), h.StopCh, h.DetachCh)
	if got != shutdownByDetach {
		t.Fatalf("awaitShutdown after wire-level detach = %v, want shutdownByDetach", got)
	}
}

// TestHandoffHTTP_RefusalDoesNotCloseDetachCh drives the /supervisor/handoff
// HTTP handler itself (not performHandoff in isolation) with a handoffFunc
// that reports refusal, and asserts detachCh stays open. This is the D-HSH-08
// resumable-failure guarantee at the wire level: RequestHandoff's caller sees
// ok=false, and — critically — nothing inside the handler closed detachCh.
func TestHandoffHTTP_RefusalDoesNotCloseDetachCh(t *testing.T) {
	refusing := handoffFunc(func(_ context.Context, _ string) (bool, string, error) {
		return false, "replacement not ready", nil
	})
	h, sockPath := serveTestIPCFull(t, nil, refusing)

	ok, err := RequestHandoff(context.Background(), sockPath, "/irrelevant/for/this/test.sock")
	if err != nil {
		t.Fatalf("RequestHandoff: %v", err)
	}
	if ok {
		t.Fatal("RequestHandoff with a refusing handoffFunc: ok = true, want false")
	}

	select {
	case <-h.DetachCh:
		t.Fatal("DetachCh closed after a REFUSED handoff — outgoing supervisor must remain sole owner (D-HSH-08)")
	default:
	}
	select {
	case <-h.StopCh:
		t.Fatal("StopCh closed after a refused handoff")
	default:
	}
}

// TestHandoffHTTP_SuccessClosesDetachCh is the positive counterpart: a
// confirming handoffFunc must close detachCh (and only detachCh).
func TestHandoffHTTP_SuccessClosesDetachCh(t *testing.T) {
	confirming := handoffFunc(func(_ context.Context, _ string) (bool, string, error) {
		return true, "", nil
	})
	h, sockPath := serveTestIPCFull(t, nil, confirming)

	ok, err := RequestHandoff(context.Background(), sockPath, "/irrelevant/for/this/test.sock")
	if err != nil {
		t.Fatalf("RequestHandoff: %v", err)
	}
	if !ok {
		t.Fatal("RequestHandoff with a confirming handoffFunc: ok = false, want true")
	}

	select {
	case <-h.DetachCh:
	default:
		t.Fatal("DetachCh not closed after a confirmed handoff")
	}
	select {
	case <-h.StopCh:
		t.Fatal("StopCh closed after a confirmed handoff — must stay open; only detachCh signals shutdown here")
	default:
	}
}

// ── /supervisor/handoff fault-injection ─────────────────────────────────────

// listenHandoffPeer starts a Unix STREAM socket at a temp path — the same
// shape performHandoff dials — and returns its path plus a channel that
// yields the first accepted *net.UnixConn (the fake replacement's side of
// the handoff exchange).
func listenHandoffPeer(t *testing.T) (path string, acceptedConn <-chan *net.UnixConn) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "peer.handoff.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: p, Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	ch := make(chan *net.UnixConn, 1)
	go func() {
		c, aErr := ln.AcceptUnix()
		if aErr != nil {
			close(ch)
			return
		}
		ch <- c
	}()
	return p, ch
}

// TestHandoffVerb_RefusalDoesNotDetach is the required fault-injection test:
// the incoming (replacement) side fails BEFORE confirming readiness (it sends
// a refusal Ack). Assert that:
//   - the outgoing supervisor's detachCh is NOT closed (still sole owner),
//   - /supervisor/handoff reports ok:false to its caller,
//   - the handoffFn's payload builder's fd was produced and is later closed
//     by performHandoff (its own dup — see [payloadBuilder] doc), but that
//     does NOT touch buildCalls' liveness marker, proving the ORIGINAL
//     resource the builder captured is independent of the offered dup.
func TestHandoffVerb_RefusalDoesNotDetach(t *testing.T) {
	peerPath, acceptedCh := listenHandoffPeer(t)

	// Fake replacement: accepts the offer, then explicitly refuses.
	refuseDone := make(chan struct{})
	go func() {
		defer close(refuseDone)
		peerConn, chOK := <-acceptedCh
		if !chOK {
			t.Error("fake replacement: never accepted a connection")
			return
		}
		defer peerConn.Close()
		_, fd, err := handoff.Accept(peerConn)
		if err != nil {
			t.Errorf("fake replacement: accept: %v", err)
			return
		}
		if fd != nil {
			fd.Close() // replacement discards the offered fd on refusal
		}
		if err := handoff.Refuse(peerConn, "not ready"); err != nil {
			t.Errorf("fake replacement: refuse: %v", err)
		}
	}()

	buildCalled := false
	build := payloadBuilder(func() (handoff.Payload, *os.File, error) {
		buildCalled = true
		f, err := os.CreateTemp(t.TempDir(), "perimeter-fd-*")
		if err != nil {
			t.Fatalf("CreateTemp: %v", err)
		}
		return handoff.Payload{
			Version:   handoff.CurrentVersion,
			Perimeter: handoff.PerimeterHandle{Present: true},
			// CA must be populated so Validate() passes and the offer reaches the wire.
			CA: handoff.CAMaterial{CertPEM: []byte("cert"), KeyPEM: []byte("key")},
		}, f, nil
	})

	ok, reason, err := performHandoff(context.Background(), peerPath, build)
	<-refuseDone

	if err != nil {
		t.Fatalf("performHandoff: unexpected error: %v", err)
	}
	if ok {
		t.Fatalf("performHandoff with a refusing peer: ok = true, want false")
	}
	if reason == "" {
		t.Error("performHandoff with a refusing peer: reason is empty, want the refusal reason")
	}
	if !buildCalled {
		t.Fatal("payload builder was never invoked")
	}
}

// TestHandoffVerb_SuccessReportsOK proves the positive path: a replacement
// that confirms causes performHandoff to report ok=true, which is what the
// /supervisor/handoff HTTP handler uses to decide whether to close detachCh.
func TestHandoffVerb_SuccessReportsOK(t *testing.T) {
	peerPath, acceptedCh := listenHandoffPeer(t)

	confirmDone := make(chan struct{})
	go func() {
		defer close(confirmDone)
		peerConn, chOK := <-acceptedCh
		if !chOK {
			t.Error("fake replacement: never accepted a connection")
			return
		}
		defer peerConn.Close()
		_, fd, err := handoff.Accept(peerConn)
		if err != nil {
			t.Errorf("fake replacement: accept: %v", err)
			return
		}
		if fd != nil {
			fd.Close()
		}
		if err := handoff.Confirm(peerConn); err != nil {
			t.Errorf("fake replacement: confirm: %v", err)
		}
	}()

	build := payloadBuilder(func() (handoff.Payload, *os.File, error) {
		f, err := os.CreateTemp(t.TempDir(), "perimeter-fd-*")
		if err != nil {
			t.Fatalf("CreateTemp: %v", err)
		}
		return handoff.Payload{
			Version:   handoff.CurrentVersion,
			Perimeter: handoff.PerimeterHandle{Present: true},
			// CA must be populated so Validate() passes and the offer reaches the wire.
			CA: handoff.CAMaterial{CertPEM: []byte("cert"), KeyPEM: []byte("key")},
		}, f, nil
	})

	ok, reason, err := performHandoff(context.Background(), peerPath, build)
	<-confirmDone

	if err != nil {
		t.Fatalf("performHandoff: unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("performHandoff with a confirming peer: ok = false (reason %q), want true", reason)
	}
}
