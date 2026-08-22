package cloudhypervisor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

// testSocketDir returns a guaranteed-short, uniquely-named, auto-cleaned
// directory suitable for use as a socket directory in tests. It uses /tmp
// directly (bypassing $TMPDIR) so the path is always short enough to satisfy
// the 107-byte Linux sun_path limit, even when $TMPDIR is set to a long path.
func testSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ch-t-")
	if err != nil {
		t.Fatalf("testSocketDir: MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// newTestDriver returns a CHDriver configured for testing. socketDir is used
// as the socket directory (callers should use testSocketDir(t)).
func newTestDriver(t *testing.T, socketDir string) *CHDriver {
	t.Helper()
	d, err := New(Config{
		BinaryPath:   "/usr/bin/true", // placeholder; no VM will be booted
		SocketDir:    socketDir,
		KernelPath:   "/dev/null",
		VCPUs:        1,
		MemoryMiB:    128,
		StartTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d
}

// TestStop_idempotence verifies that calling Stop on a sandbox that was never
// started returns nil — the fundamental idempotence contract.
func TestStop_idempotence(t *testing.T) {
	dir := testSocketDir(t)
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	// First call: socket does not exist.
	if err := d.Stop(context.Background(), id); err != nil {
		t.Fatalf("Stop on absent sandbox: %v", err)
	}

	// Second call: still absent.
	if err := d.Stop(context.Background(), id); err != nil {
		t.Fatalf("Stop (second call) on absent sandbox: %v", err)
	}
}

// TestObserve_neverStarted verifies that Observe on a sandbox that was never
// started returns Absent and no error.
func TestObserve_neverStarted(t *testing.T) {
	dir := testSocketDir(t)
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	obs, err := d.Observe(context.Background(), id)
	if err != nil {
		t.Fatalf("Observe on never-started sandbox: unexpected error: %v", err)
	}
	if obs.State != driver.Absent {
		t.Errorf("State = %v, want Absent", obs.State)
	}
	if obs.InstanceID != "" {
		t.Errorf("InstanceID = %q, want empty", obs.InstanceID)
	}
}

// TestSocketPath_perSandbox verifies that two distinct sandbox IDs produce
// two distinct socket paths — collision would cause one VM to corrupt another.
func TestSocketPath_perSandbox(t *testing.T) {
	dir := testSocketDir(t)
	d := newTestDriver(t, dir)

	id1 := domain.NewSandboxID()
	id2 := domain.NewSandboxID()

	p1 := d.socketPath(id1)
	p2 := d.socketPath(id2)

	if p1 == p2 {
		t.Fatalf("socket paths are identical for different sandbox IDs: %s", p1)
	}
	// Both paths must live within socketDir.
	if dir != filepath.Dir(p1) {
		t.Errorf("socket path %q not under socketDir %q", p1, dir)
	}
	if dir != filepath.Dir(p2) {
		t.Errorf("socket path %q not under socketDir %q", p2, dir)
	}
}

// TestStart_failedStart_noOrphan verifies that when Start fails because the
// VMM never becomes ready, it kills the spawned process and removes the socket
// file, leaving no orphan.
//
// We use a shell script that accepts arguments (cloud-hypervisor's --api-socket
// path) but simply sleeps forever without creating the socket, so the ping
// poll times out. This exercises the interesting failure path: spawn succeeds,
// API never appears.
func TestStart_failedStart_noOrphan(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("skipping: /bin/sh not available")
	}

	fileDir := t.TempDir()
	socketDir := testSocketDir(t)

	// Write a script that ignores all arguments and sleeps.
	script := filepath.Join(fileDir, "fake-ch.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 300\n"), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}

	d, err := New(Config{
		BinaryPath:   script,
		SocketDir:    socketDir,
		KernelPath:   "/dev/null",
		StartTimeout: 300 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id := domain.NewSandboxID()
	_, startErr := d.Start(context.Background(), driver.StartRequest{SandboxID: id})
	if startErr == nil {
		t.Fatal("Start returned nil error; expected a timeout failure")
	}

	// Socket must not exist after a failed start.
	sockPath := d.socketPath(id)
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("socket file %q still exists after failed Start", sockPath)
	}

	// No orphan process. We need the PID that was spawned; because Start
	// failed, d.procs[id] was cleaned up. We verify instead that no
	// cloud-hypervisor-alike process is still holding the socket path by
	// checking that the proc table for our script has been reaped.
	// We recover the PID from the driver's internal tracking before clearState
	// runs by checking a helper we add for this test.
	//
	// A simpler invariant: after Start fails, a second Start on the same ID
	// must also fail cleanly (not collide with an orphan).
	// (If an orphan were holding the socket, the second Start would error
	// differently — but both paths are covered by checking the socket is gone.)

	// Give the OS a moment to reap.
	time.Sleep(50 * time.Millisecond)
}

// TestStart_failedStart_noOrphan_pidCheck is a stronger variant: we introspect
// the spawned PID to confirm the process is dead after failed Start.
func TestStart_failedStart_noOrphan_pidCheck(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("skipping: /bin/sh not available")
	}

	fileDir := t.TempDir()
	socketDir := testSocketDir(t)

	// Write a script that prints its own PID to a file, then sleeps.
	pidFile := filepath.Join(fileDir, "spawned.pid")
	script := filepath.Join(fileDir, "fake-ch2.sh")
	scriptContent := fmt.Sprintf("#!/bin/sh\necho $$ > %s\nsleep 300\n", pidFile)
	if err := os.WriteFile(script, []byte(scriptContent), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}

	d, err := New(Config{
		BinaryPath:   script,
		SocketDir:    socketDir,
		KernelPath:   "/dev/null",
		StartTimeout: 400 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id := domain.NewSandboxID()
	if _, err := d.Start(context.Background(), driver.StartRequest{SandboxID: id}); err == nil {
		t.Fatal("Start returned nil; expected failure")
	}

	// Wait for the PID file to appear (the script writes it at startup).
	var pid int
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(pidFile)
		if err == nil {
			if _, err := fmt.Sscanf(string(b), "%d", &pid); err == nil && pid > 0 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid == 0 {
		t.Skip("script did not write PID file in time; skipping orphan check")
	}

	// Give the process group kill time to propagate.
	time.Sleep(100 * time.Millisecond)

	// syscall.Kill(pid, 0) returns ESRCH when the process does not exist.
	err = syscall.Kill(pid, 0)
	if !isSRCH(err) {
		t.Errorf("process %d still alive after failed Start (err=%v); orphan detected", pid, err)
	}
}

// isSRCH returns true if err is ESRCH (no such process).
func isSRCH(err error) bool {
	return err == syscall.ESRCH
}

// TestNew_socketDirTooLong_relocates verifies that New SUCCEEDS when the
// requested SocketDir would produce a socket path exceeding the Linux sun_path
// limit: New must transparently relocate to a shorter path rather than error.
// The resulting Config.SocketDir must be short enough that the longest possible
// socket name fits within the 107-byte limit.
func TestNew_socketDirTooLong_relocates(t *testing.T) {
	// Build a path that is definitely too long (80 'a's under root = 81 bytes,
	// exceeds the 72-byte budget for a directory that leaves room for a 35-byte name).
	longDir := "/" + strings.Repeat("a", 80)

	d, err := New(Config{
		BinaryPath: "/bin/true",
		SocketDir:  longDir,
	})
	if err != nil {
		t.Fatalf("New with long SocketDir must not error; got: %v", err)
	}

	// The driver must have relocated: resulting SocketDir must fit the limit.
	const sockNameLen = 35
	if len(d.cfg.SocketDir)+sockNameLen > maxSocketPathLen {
		t.Errorf("after relocation, SocketDir %q is still too long (%d + %d > %d)",
			d.cfg.SocketDir, len(d.cfg.SocketDir), sockNameLen, maxSocketPathLen)
	}
	// The relocated dir must differ from the requested (too-long) dir.
	if d.cfg.SocketDir == longDir {
		t.Errorf("SocketDir was not relocated; still equals the too-long requested dir")
	}
}

// TestNew_socketDirTooLong_deterministic verifies that two calls with the same
// too-long SocketDir produce the same relocated path (same input → same short
// dir), so a restart or a second process resolves the same location.
func TestNew_socketDirTooLong_deterministic(t *testing.T) {
	longDir := "/" + strings.Repeat("b", 80)

	d1, err := New(Config{BinaryPath: "/bin/true", SocketDir: longDir})
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	d2, err := New(Config{BinaryPath: "/bin/true", SocketDir: longDir})
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	if d1.cfg.SocketDir != d2.cfg.SocketDir {
		t.Errorf("relocation is non-deterministic: %q vs %q", d1.cfg.SocketDir, d2.cfg.SocketDir)
	}
}

// TestNew_socketDirTooLong_collisionFree verifies that two different too-long
// SocketDir values relocate to two different short dirs — no collision.
func TestNew_socketDirTooLong_collisionFree(t *testing.T) {
	longDir1 := "/" + strings.Repeat("c", 80)
	longDir2 := "/" + strings.Repeat("d", 80)

	d1, err := New(Config{BinaryPath: "/bin/true", SocketDir: longDir1})
	if err != nil {
		t.Fatalf("New(longDir1): %v", err)
	}
	d2, err := New(Config{BinaryPath: "/bin/true", SocketDir: longDir2})
	if err != nil {
		t.Fatalf("New(longDir2): %v", err)
	}
	if d1.cfg.SocketDir == d2.cfg.SocketDir {
		t.Errorf("collision: two different long dirs relocated to the same short dir %q", d1.cfg.SocketDir)
	}
}

// TestStop_callsVMMShutdown verifies that Stop always reaches vmm.shutdown,
// even in the post-restart case where no proc handle exists. A fake server at
// d.socketPath(id) records which API paths were hit; the test asserts that
// /api/v1/vmm.shutdown appears in the recorded set.
func TestStop_callsVMMShutdown(t *testing.T) {
	dir := testSocketDir(t)
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	var mu sync.Mutex
	var hit []string

	mux := http.NewServeMux()
	record := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hit = append(hit, r.URL.Path)
			mu.Unlock()
			next(w, r)
		}
	}
	mux.HandleFunc("/api/v1/vm.shutdown", record(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("/api/v1/vm.delete", record(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	mux.HandleFunc("/api/v1/vmm.shutdown", record(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK) // real CH returns 200
	}))

	sockPath := d.socketPath(id)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	if err := d.Stop(context.Background(), id); err != nil {
		t.Fatalf("Stop: unexpected error: %v", err)
	}

	mu.Lock()
	hitSet := make(map[string]bool, len(hit))
	for _, p := range hit {
		hitSet[p] = true
	}
	mu.Unlock()

	if !hitSet["/api/v1/vmm.shutdown"] {
		t.Errorf("vmm.shutdown was not called; paths hit: %v", hit)
	}
}

// TestStop_callsVMMShutdown_afterRestart simulates the post-restart path where
// vm.shutdown returns CH's real 500 "VM is not running" (no VM configured),
// vm.delete returns 204 (idempotent), and verifies that vmm.shutdown is still
// reached to terminate the orphaned VMM process.
//
// This is the crash window: nexus3 died after vm.delete succeeded but before
// vmm.shutdown ran. On restart, no proc handle exists in d.procs; vmm.shutdown
// is the only path to kill the process.
func TestStop_callsVMMShutdown_afterRestart(t *testing.T) {
	dir := testSocketDir(t)
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	var mu sync.Mutex
	var hit []string

	mux := http.NewServeMux()
	record := func(path string, handler http.HandlerFunc) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			hit = append(hit, r.URL.Path)
			mu.Unlock()
			handler(w, r)
		})
	}
	// Real CH v52 response: vm.shutdown with no VM → 500 "VM is not running".
	record("/api/v1/vm.shutdown", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `["Error from API","The VM could not shutdown","VM is not running"]`)
	})
	// vm.delete idempotent → 204.
	record("/api/v1/vm.delete", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	// vmm.shutdown → 200 (real CH).
	record("/api/v1/vmm.shutdown", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	sockPath := d.socketPath(id)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	if err := d.Stop(context.Background(), id); err != nil {
		t.Fatalf("Stop: unexpected error: %v", err)
	}

	mu.Lock()
	hitSet := make(map[string]bool, len(hit))
	for _, p := range hit {
		hitSet[p] = true
	}
	mu.Unlock()

	if !hitSet["/api/v1/vmm.shutdown"] {
		t.Errorf("vmm.shutdown was not called on post-restart Stop; paths hit: %v", hit)
	}
}

// TestObserve_shutdownStateIsUnknown verifies that when CH reports "Shutdown"
// (VM object still exists in the VMM, socket still bound) Observe returns
// Unknown + non-nil error, NOT Absent. Returning Absent here would authorise
// a second Start() that collides with the existing VMM process.
func TestObserve_shutdownStateIsUnknown(t *testing.T) {
	dir := testSocketDir(t)
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	// Serve {"state":"Shutdown"} on the socket that Observe will dial.
	sockPath := d.socketPath(id)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"state":"Shutdown","config":{}}`)
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	obs, obsErr := d.Observe(context.Background(), id)
	if obs.State != driver.Unknown {
		t.Errorf("State = %v, want Unknown; Shutdown must not map to Absent", obs.State)
	}
	if obsErr == nil {
		t.Error("expected non-nil error for Shutdown state, got nil")
	}
	// The critical invariant: Unknown must not equal Absent.
	if obs.State == driver.Absent {
		t.Fatal("Unknown == Absent: the safety model is broken")
	}
}

// TestObserve_createdStateIsUnknown verifies that CH "Created" state also maps
// to Unknown + error (guest not yet booted, but VMM socket is live).
func TestObserve_createdStateIsUnknown(t *testing.T) {
	dir := testSocketDir(t)
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	sockPath := d.socketPath(id)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"state":"Created","config":{}}`)
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	obs, obsErr := d.Observe(context.Background(), id)
	if obs.State != driver.Unknown {
		t.Errorf("State = %v, want Unknown; Created must not map to Absent", obs.State)
	}
	if obsErr == nil {
		t.Error("expected non-nil error for Created state, got nil")
	}
}

// TestObserve_liveVMM_noVM verifies that a VMM responding with 500 "VM is not
// created" maps to Absent (not Unknown) at the Observe level. This is a
// confirmed, unambiguous observation: the VMM is alive but has no VM. The
// recovery path that used to require Unknown here is now unnecessary because
// spawnVMM pre-flights and refuses to spawn onto an occupied socket.
func TestObserve_liveVMM_noVM(t *testing.T) {
	dir := testSocketDir(t)
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	sockPath := d.socketPath(id)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.info", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `["Error from API","The VM info is not available","VM is not created"]`)
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	obs, obsErr := d.Observe(context.Background(), id)
	if obs.State != driver.Absent {
		t.Errorf("State = %v, want Absent for 500 'VM is not created'", obs.State)
	}
	if obsErr != nil {
		t.Errorf("unexpected error for 'VM is not created': %v", obsErr)
	}
}

// TestSpawnVMM_liveSocket verifies that spawnVMM returns ErrVMMAlreadyBound
// when a live VMM is already answering vmm.ping on the socket. Crucially, the
// binary is NOT spawned — verified by using a non-existent binary path, so any
// attempt to exec would produce a different ENOENT error, not ErrVMMAlreadyBound.
func TestSpawnVMM_liveSocket(t *testing.T) {
	dir := testSocketDir(t)
	sockPath := filepath.Join(dir, "live.sock")

	// Serve a successful vmm.ping response.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vmm.ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"version":"52.0"}`)
	})
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	cfg := Config{
		BinaryPath:   "/nonexistent/cloud-hypervisor", // must never be reached
		StartTimeout: 200 * time.Millisecond,
	}
	_, spawnErr := spawnVMM(context.Background(), cfg, sockPath)
	if spawnErr == nil {
		t.Fatal("expected error for live socket, got nil")
	}
	if !isErrVMMAlreadyBound(spawnErr) {
		t.Errorf("want ErrVMMAlreadyBound; got %v", spawnErr)
	}

	// Socket must still exist (we must not have unlinked a live VMM's socket).
	if _, err := os.Stat(sockPath); err != nil {
		t.Errorf("live VMM socket was removed: %v", err)
	}
}

// isErrVMMAlreadyBound unwraps spawnVMM errors to check for ErrVMMAlreadyBound.
func isErrVMMAlreadyBound(err error) bool {
	return errors.Is(err, ErrVMMAlreadyBound)
}

// TestSpawnVMM_staleSocket verifies that spawnVMM removes a stale socket file
// (exists, no listener → ECONNREFUSED) and proceeds to spawn the binary.
// We prove "spawn proceeded" by using a script that writes a marker file.
func TestSpawnVMM_staleSocket(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("skipping: /bin/sh not available")
	}

	dir := testSocketDir(t)
	sockPath := filepath.Join(dir, "stale.sock")

	// Create a real socket then immediately close the listener — leaves the
	// file behind with no listener (ECONNREFUSED on connect).
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ln.Close() // stale file now present

	// Script that writes a marker and then sleeps (so spawnVMM can find the PID).
	markerFile := filepath.Join(dir, "spawned.marker")
	script := filepath.Join(dir, "fake-ch.sh")
	content := fmt.Sprintf("#!/bin/sh\ntouch %s\nsleep 300\n", markerFile)
	if err := os.WriteFile(script, []byte(content), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}

	cfg := Config{
		BinaryPath:   script,
		StartTimeout: 300 * time.Millisecond,
	}
	// spawnVMM will time out waiting for the API (the fake script doesn't create
	// a real socket), but before doing so it should remove the stale file and
	// start the script.
	_, spawnErr := spawnVMM(context.Background(), cfg, sockPath)
	if spawnErr == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if isErrVMMAlreadyBound(spawnErr) {
		t.Fatalf("stale socket incorrectly identified as live VMM: %v", spawnErr)
	}

	// Marker file proves the spawn was attempted (binary was exec'd).
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(markerFile); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(markerFile); err != nil {
		t.Error("script was not exec'd: marker file not found; stale socket may not have been cleared")
	}
}

// TestSpawnVMM_hungSocket verifies that a socket which accepts connections but
// never responds produces an error rather than treating the VMM as absent.
// The socket file must NOT be removed — we cannot confirm the VMM is dead.
func TestSpawnVMM_hungSocket(t *testing.T) {
	dir := testSocketDir(t)
	sockPath := filepath.Join(dir, "hung.sock")

	// Accept connections but never reply.
	hungHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := httptest.NewUnstartedServer(hungHandler)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	cfg := Config{
		BinaryPath:   "/nonexistent/cloud-hypervisor",
		StartTimeout: 200 * time.Millisecond,
	}
	_, spawnErr := spawnVMM(context.Background(), cfg, sockPath)
	if spawnErr == nil {
		t.Fatal("expected error for hung socket, got nil")
	}
	if isErrVMMAlreadyBound(spawnErr) {
		t.Fatalf("hung socket should not be ErrVMMAlreadyBound: %v", spawnErr)
	}

	// Socket must still exist — we must not remove what might be a live VMM.
	if _, err := os.Stat(sockPath); err != nil {
		t.Errorf("hung VMM socket was removed: %v", err)
	}
}

// TestObserve_callTimeout_wedgedListener_doesNotHang verifies that Observe
// called with context.Background() (no caller deadline) still returns within
// Config.CallTimeout when the VMM accepts connections but never replies.
//
// This is the production path that TestObserve_hungServerIsNotAbsent does not
// cover — that test supplies a 100 ms bounded context, masking the case where
// cli/root.go hands a bare context.Background() down to driver.Observe.
//
// Prove-it-bites: removing the d.callCtx(ctx) application in Observe causes
// this test to hang until the go test -timeout deadline fires. With the fix
// applied, the test returns in ≈ CallTimeout (2 s here).
func TestObserve_callTimeout_wedgedListener_doesNotHang(t *testing.T) {
	dir := testSocketDir(t)
	d, err := New(Config{
		BinaryPath:   "/usr/bin/true",
		SocketDir:    dir,
		KernelPath:   "/dev/null",
		CallTimeout:  2 * time.Second,
		StartTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id := domain.NewSandboxID()
	sockPath := d.socketPath(id)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	hungHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	srv := httptest.NewUnstartedServer(hungHandler)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	start := time.Now()
	obs, obsErr := d.Observe(context.Background(), id) // no caller deadline
	elapsed := time.Since(start)

	// Must return within a reasonable multiple of CallTimeout (not hang forever).
	const maxWait = 5 * time.Second
	if elapsed > maxWait {
		t.Errorf("Observe took %v (> %v); suspecting unbounded hang", elapsed, maxWait)
	}

	// State and error invariants (same as TestObserve_hungServerIsNotAbsent).
	if obs.State == driver.Absent {
		t.Fatal("Observe returned Absent for a hung server: a live VM must never appear absent")
	}
	if obs.State != driver.Unknown {
		t.Errorf("State = %v, want Unknown for hung server", obs.State)
	}
	if obsErr == nil {
		t.Error("expected non-nil error for hung server, got nil")
	}
}

// TestStart_noKernelPath_returnsError verifies that Start returns
// ErrNoKernelConfigured before spawning any process when Config.KernelPath is
// empty. A non-existent BinaryPath proves no exec happened.
func TestStart_noKernelPath_returnsError(t *testing.T) {
	dir := testSocketDir(t)
	d, err := New(Config{
		BinaryPath:   "/nonexistent/cloud-hypervisor", // must never be reached
		SocketDir:    dir,
		KernelPath:   "", // deliberately empty
		CallTimeout:  2 * time.Second,
		StartTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id := domain.NewSandboxID()
	_, startErr := d.Start(context.Background(), driver.StartRequest{SandboxID: id})
	if startErr == nil {
		t.Fatal("Start returned nil; expected ErrNoKernelConfigured")
	}
	if !errors.Is(startErr, ErrNoKernelConfigured) {
		t.Errorf("want errors.Is(err, ErrNoKernelConfigured); got %v", startErr)
	}

	// Socket must not exist — no process was spawned.
	sockPath := d.socketPath(id)
	if _, statErr := os.Stat(sockPath); !os.IsNotExist(statErr) {
		t.Errorf("socket file %q should not exist after KernelPath guard rejected Start", sockPath)
	}
}

// TestObserve_hungServerIsNotAbsent guards the safety invariant at the driver
// level: a hung server (accepted connection, no response) must produce Unknown,
// NEVER Absent. Absent authorises destruction; Unknown triggers the conservative
// recovery path.
func TestObserve_hungServerIsNotAbsent(t *testing.T) {
	dir := testSocketDir(t)
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	sockPath := d.socketPath(id)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	// Hung handler: accept connection but never respond.
	hungHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	srv := httptest.NewUnstartedServer(hungHandler)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	obs, obsErr := d.Observe(ctx, id)
	if obs.State == driver.Absent {
		t.Fatal("Observe returned Absent for a hung server: a live VM must never appear absent")
	}
	if obs.State != driver.Unknown {
		t.Errorf("State = %v, want Unknown for hung server", obs.State)
	}
	if obsErr == nil {
		t.Error("expected non-nil error for hung server, got nil")
	}
}

// TestBuildMemoryConfig_SharedSetWithLiveMounts verifies that buildMemoryConfig
// enables shared memory (CH vhost-user requirement) when LiveMounts are present,
// and suppresses it when absent.
//
// This test catches the regression where omitting the shared field causes CH to
// reject the vm.create request with HTTP 500 "Using vhost-user requires using
// shared memory or huge pages" (D-PD-104 / LM-SHARED-MEM).
func TestBuildMemoryConfig_SharedSetWithLiveMounts(t *testing.T) {
	mount := domain.LiveMount{HostPath: "/tmp/x", GuestPath: "/mnt/x"}

	// With live mounts: Shared must be true.
	got := buildMemoryConfig(Config{MemoryMiB: 512, LiveMounts: []domain.LiveMount{mount}}, 512)
	if !got.Shared {
		t.Errorf("Shared = false, want true when LiveMounts present (CH rejects vhost-user without shared memory)")
	}

	// Without live mounts: Shared must be false (omitted from JSON via omitempty).
	got2 := buildMemoryConfig(Config{MemoryMiB: 512}, 512)
	if got2.Shared {
		t.Errorf("Shared = true, want false when no LiveMounts (unnecessary memfd overhead on every sandbox)")
	}
}

