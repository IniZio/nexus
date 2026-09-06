package perimeter

// TestPerimeterFD_* proves the handoff contract PerimeterFD exists for
// (motive nexus3-host-supervisor-hotswap, slice 04): the returned *os.File is
// an INDEPENDENT dup of the live perimeter connection. A caller that offers
// this dup to a replacement supervisor and then discards it on failure (or
// success) must not disturb the PerimeterSupervisor's own ongoing use of the
// connection.

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// unixConnPair returns a connected client/server *net.UnixConn pair backed by
// a real Unix-domain socket, so (*net.UnixConn).File() has a real fd to dup.
func unixConnPair(t *testing.T) (client, server *net.UnixConn) {
	t.Helper()
	sockPath := filepath.Join(t.TempDir(), "pair.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sockPath, Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	acceptedCh := make(chan *net.UnixConn, 1)
	acceptErrCh := make(chan error, 1)
	go func() {
		c, aErr := ln.AcceptUnix()
		if aErr != nil {
			acceptErrCh <- aErr
			return
		}
		acceptedCh <- c
	}()

	cliRaw, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sockPath, Net: "unix"})
	if err != nil {
		t.Fatalf("DialUnix: %v", err)
	}
	t.Cleanup(func() { cliRaw.Close() })

	select {
	case srv := <-acceptedCh:
		t.Cleanup(func() { srv.Close() })
		return cliRaw, srv
	case aErr := <-acceptErrCh:
		t.Fatalf("AcceptUnix: %v", aErr)
	}
	return nil, nil
}

// TestPerimeterFD_DupIsIndependent is the mutation-proof case: the dup
// returned by PerimeterFD carries traffic exchanged over the SAME underlying
// socket as the original connection (proving it is a real dup of the live
// fd, not a fresh unrelated one), and closing the dup afterward has no effect
// on the original connection's continued use.
func TestPerimeterFD_DupIsIndependent(t *testing.T) {
	client, server := unixConnPair(t)

	sup := &PerimeterSupervisor{fd: client}

	dup, err := sup.PerimeterFD()
	if err != nil {
		t.Fatalf("PerimeterFD: %v", err)
	}

	// Write through the DUP; read on the server side. If the dup were not
	// backed by the same socket, this read would hang/fail.
	const probe = "handoff-probe"
	if _, wErr := dup.Write([]byte(probe)); wErr != nil {
		t.Fatalf("write via dup: %v", wErr)
	}
	buf := make([]byte, len(probe))
	if _, rErr := server.Read(buf); rErr != nil {
		t.Fatalf("server read after dup write: %v", rErr)
	}
	if string(buf) != probe {
		t.Fatalf("server read %q, want %q", buf, probe)
	}

	// Discard the dup — simulates a refused/failed handoff discarding its
	// offer copy.
	if cErr := dup.Close(); cErr != nil {
		t.Fatalf("close dup: %v", cErr)
	}

	// The ORIGINAL connection must still be fully usable: this is the
	// resumable-failure guarantee (D-HSH-08). Closing the dup must not have
	// closed the fd underneath the original *net.UnixConn.
	const probe2 = "still-alive"
	if _, wErr := client.Write([]byte(probe2)); wErr != nil {
		t.Fatalf("original conn write after dup closed: %v — dup Close leaked into the original fd", wErr)
	}
	buf2 := make([]byte, len(probe2))
	if _, rErr := server.Read(buf2); rErr != nil {
		t.Fatalf("server read after original write: %v", rErr)
	}
	if string(buf2) != probe2 {
		t.Fatalf("server read %q, want %q", buf2, probe2)
	}
}

// TestPerimeterFD_WrongType verifies the guard: a non-*net.UnixConn fd
// returns a clear error instead of a panic or a silent nil.
func TestPerimeterFD_WrongType(t *testing.T) {
	sup := &PerimeterSupervisor{fd: os.Stdout} // any io.ReadWriteCloser that is not *net.UnixConn
	if _, err := sup.PerimeterFD(); err == nil {
		t.Fatal("PerimeterFD with a non-UnixConn fd: expected error, got nil")
	}
}
