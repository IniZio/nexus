package cloudhypervisor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

// buildFakeCHBinary compiles a tiny helper binary that mimics cloud-hypervisor
// just enough to satisfy Start()'s readiness poll: it listens on the
// --api-socket path with an HTTP server that answers any request with 200,
// then blocks forever. It never creates or boots a VM — the regression test
// below only needs CH's API socket to become responsive, so Start() proceeds
// past the poll loop into the nested-virt preflight.
func buildFakeCHBinary(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("skipping: go toolchain not on PATH")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "fakech.go")
	bin := filepath.Join(dir, "fakech")
	const source = `package main

import (
	"net"
	"net/http"
	"os"
)

func main() {
	sock := os.Args[2] // argv: fakech --api-socket <path>
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		os.Exit(1)
	}
	http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
}
`
	if err := os.WriteFile(src, []byte(source), 0o600); err != nil {
		t.Fatalf("write fake CH source: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", bin, src)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("skipping: cannot build fake CH helper binary: %v\n%s", err, out)
	}
	return bin
}

// TestStart_nestedVirtPreflightFailure_killsSpawnedChild is the regression
// test for ticket 10's create-path orphan leak: when Config.NestedVirt is
// true and nestedVirtPreflight() fails, that happens AFTER the netns child +
// CH have already been spawned and confirmed API-ready by the poll loop
// above it in Start(). Every OTHER post-spawn failure branch in Start() calls
// cleanup() before returning; this branch did not, so the child+CH pair
// leaked with zero resource-file footprint: create.go's own deferred cleanup
// only removes the disk copy and lets clearState remove the socket file, but
// neither touches a process that survives its own socket file's removal —
// exactly the invisible-to-reap orphan shape this ticket investigates.
//
// Before the fix (driver.go: `if err := nestedVirtPreflight(); err != nil {
// return "", err }`) this test is RED: the fake CH server is still answering
// pings after Start() returns its error. After the fix (cleanup() is called
// first) the test is GREEN.
func TestStart_nestedVirtPreflightFailure_killsSpawnedChild(t *testing.T) {
	bin := buildFakeCHBinary(t)
	socketDir := testSocketDir(t)

	d, err := New(Config{
		BinaryPath:   bin,
		SocketDir:    socketDir,
		KernelPath:   "/dev/null",
		StartTimeout: 5 * time.Second,
		NestedVirt:   true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	// Force the preflight to fail deterministically without touching the
	// real /dev/kvm or sysfs nested-virt state: pass the sysfs check, fail
	// the /dev/kvm open.
	prevOpener := kvmDeviceOpener
	prevSysfs := sysfsNestedParamReader
	kvmDeviceOpener = func() error { return errors.New("forced failure for test") }
	sysfsNestedParamReader = func(string) (string, error) { return "1", nil }
	t.Cleanup(func() {
		kvmDeviceOpener = prevOpener
		sysfsNestedParamReader = prevSysfs
	})

	id := domain.NewSandboxID()
	_, startErr := d.Start(context.Background(), driver.StartRequest{SandboxID: id})
	if startErr == nil {
		t.Fatal("Start returned nil error; expected nested-virt preflight failure")
	}

	// Give the kill signal and process-group teardown a moment to land.
	sockPath := d.socketPath(id)
	deadline := time.Now().Add(2 * time.Second)
	var pingErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		pingErr = newClient(sockPath).Ping(ctx)
		cancel()
		if pingErr != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if pingErr == nil {
		t.Fatalf("fake CH server at %s is still answering after Start() failed on nested-virt "+
			"preflight — the netns child + CH pair leaked (cleanup() was not called)", sockPath)
	}
}
