package supervisor

// TestRemoveOwnSocket_* proves the D-HSH-09 fix: RunDetached's socket cleanup
// must not unlink a REPLACEMENT supervisor's freshly rebound sockPath. The
// scenario: process A binds sockPath, then (simulating a hotswap) process B
// removes sockPath and rebinds it to a NEW inode at the SAME path, all while
// A is still mid-shutdown. A's cleanup must detect the inode changed and
// leave B's socket alone.

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveOwnSocket_RemovesOwnUnreplacedSocket(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "supervisor.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sockPath, Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()
	bindStat, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("os.Stat after bind: %v", err)
	}

	removeOwnSocket(sockPath, bindStat)

	if _, statErr := os.Stat(sockPath); !os.IsNotExist(statErr) {
		t.Fatalf("removeOwnSocket did not remove the still-owned socket: stat err = %v", statErr)
	}
}

func TestRemoveOwnSocket_LeavesReplacementSocketAlone(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "supervisor.sock")

	// Process A binds sockPath and captures its bind-time stat, exactly as
	// serveIPC does.
	lnA, err := net.ListenUnix("unix", &net.UnixAddr{Name: sockPath, Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix (A): %v", err)
	}
	defer lnA.Close()
	bindStatA, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("os.Stat after A's bind: %v", err)
	}

	// Simulate a hotswap replacement (process B) rebinding the SAME path to a
	// NEW inode while A is still holding its own (now-unlinked-by-name, but
	// still open) listener fd — exactly the race D-HSH-09 describes.
	if rmErr := os.Remove(sockPath); rmErr != nil {
		t.Fatalf("os.Remove (simulating B's rebind): %v", rmErr)
	}
	lnB, err := net.ListenUnix("unix", &net.UnixAddr{Name: sockPath, Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix (B): %v", err)
	}
	defer lnB.Close()

	// A's deferred cleanup fires now, using its OWN (now-stale) bind-time stat.
	removeOwnSocket(sockPath, bindStatA)

	// B's socket must still be present at sockPath.
	if _, statErr := os.Stat(sockPath); statErr != nil {
		t.Fatalf("removeOwnSocket unlinked the REPLACEMENT's socket: stat err = %v", statErr)
	}
}

func TestRemoveOwnSocket_NilBindStatIsNoop(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "supervisor.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sockPath, Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	defer ln.Close()

	removeOwnSocket(sockPath, nil) // must not panic

	if _, statErr := os.Stat(sockPath); statErr != nil {
		t.Fatalf("removeOwnSocket(nil) unexpectedly removed the socket: %v", statErr)
	}
}
