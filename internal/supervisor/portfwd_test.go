//go:build linux

package supervisor

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/portfwd"
)

// fakeGuestExecer implements guestExecer for tests.
// Each call pops the next response from the queue.
type fakeGuestExecer struct {
	responses []fakeExecResponse
	calls     []agent.ExecOptions
}

type fakeExecResponse struct {
	stdout string
	code   int32
	err    error
}

func (f *fakeGuestExecer) Exec(_ context.Context, opts agent.ExecOptions) (int32, error) {
	f.calls = append(f.calls, opts)
	if len(f.responses) == 0 {
		return 0, nil
	}
	r := f.responses[0]
	f.responses = f.responses[1:]
	if r.err != nil {
		return 0, r.err
	}
	if opts.Stdout != nil && r.stdout != "" {
		opts.Stdout.Write([]byte(r.stdout)) //nolint:errcheck
	}
	return r.code, nil
}

// fakeProcNetTCP is a minimal /proc/net/tcp fragment with one LISTEN entry on port 0x1E61 = 7777.
const fakeProcNetTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1E61 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
`

// TestReadProcNet_ParsesTCPBytes verifies that singleSandboxBackend.ReadProcNet
// execs cat /proc/net/tcp{,6} in the guest and returns their bytes.
func TestReadProcNet_ParsesTCPBytes(t *testing.T) {
	execer := &fakeGuestExecer{
		responses: []fakeExecResponse{
			{stdout: fakeProcNetTCP, code: 0}, // /proc/net/tcp
			{stdout: "", code: 0},             // /proc/net/tcp6 (empty)
		},
	}
	sb := singleSandboxBackend{
		ref:    portfwd.SandboxRef{ID: "test-id", Status: portfwd.SandboxStatusRunning},
		client: execer,
	}
	tcp, tcp6, err := sb.ReadProcNet(context.Background(), "test-id")
	if err != nil {
		t.Fatalf("ReadProcNet: %v", err)
	}
	if !bytes.Contains(tcp, []byte("1E61")) {
		t.Errorf("tcp bytes must contain proc/net entry; got %q", tcp)
	}
	// Verify the two exec calls were for /proc/net/tcp and /proc/net/tcp6.
	if len(execer.calls) < 2 {
		t.Fatalf("expected 2 exec calls, got %d", len(execer.calls))
	}
	if execer.calls[0].Argv[len(execer.calls[0].Argv)-1] != "/proc/net/tcp" {
		t.Errorf("first call must target /proc/net/tcp; got %v", execer.calls[0].Argv)
	}
	if execer.calls[1].Argv[len(execer.calls[1].Argv)-1] != "/proc/net/tcp6" {
		t.Errorf("second call must target /proc/net/tcp6; got %v", execer.calls[1].Argv)
	}
	_ = tcp6 // may be empty
}

// fakeBackend implements portfwd.Backend for reconcile tests.
type fakeBackend struct {
	refs  []portfwd.SandboxRef
	binds []portfwd.PortBind
}

func (b *fakeBackend) ListSandboxes(_ context.Context) ([]portfwd.SandboxRef, error) {
	return b.refs, nil
}

func (b *fakeBackend) ReadProcNet(_ context.Context, _ string) ([]byte, []byte, error) {
	var lines []string
	lines = append(lines, "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n")
	for i, pb := range b.binds {
		// Encode port as little-endian hex per /proc/net/tcp format.
		portHex := fmt.Sprintf("%04X", pb.Port)
		lines = append(lines, fmt.Sprintf("  %2d: 00000000:%s 00000000:0000 0A 00000000:00000000 00:00000000 00000000 0 0 %d 1\n",
			i, portHex, 10000+i))
	}
	var buf bytes.Buffer
	for _, l := range lines {
		buf.WriteString(l)
	}
	return buf.Bytes(), nil, nil
}

// fakeDialer implements portForwardDialer; accepts connections and immediately closes.
type fakeDialer struct{}

func (fakeDialer) DialGuestPortForward(_ context.Context, _ string, _ uint32) (net.Conn, error) {
	// Return a pipe so forwardConn can splice without error.
	c1, c2 := net.Pipe()
	c2.Close()
	return c1, nil
}

// TestReconcile_BindsAndUnbindsHostPort verifies that reconcile binds
// 127.0.0.1:P when the guest listener appears and closes it when it vanishes.
func TestReconcile_BindsAndUnbindsHostPort(t *testing.T) {
	// Find a free port in the forwardable range [GuestPortBase=1024, GuestPortTop=11023].
	// OS ephemeral ports are typically >32768 which FilterListeners marks OutOfRange.
	var freePort uint16
	for p := uint16(8000); p <= 11000; p++ {
		probe, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			freePort = p
			probe.Close()
			break
		}
	}
	if freePort == 0 {
		t.Skip("no free port in forwardable range 8000-11000")
	}

	tmpDir := t.TempDir()
	backend := &fakeBackend{
		refs:  []portfwd.SandboxRef{{ID: "sb1", Status: portfwd.SandboxStatusRunning}},
		binds: []portfwd.PortBind{{Port: freePort, BindAddr: "0.0.0.0"}},
	}
	sup := &portForwardSupervisor{
		sandboxRef: "test/sb1",
		backend:    backend,
		disc:       &portfwd.Discoverer{Backend: backend},
		dialer:     fakeDialer{},
		stateDir:   tmpDir,
		interval:   time.Second,
		listeners:  make(map[uint16]net.Listener),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// First reconcile: port should appear.
	if err := sup.reconcile(ctx); err != nil {
		t.Fatalf("reconcile(add): %v", err)
	}
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", freePort), time.Second); err != nil {
		t.Fatalf("port %d not bound after reconcile: %v", freePort, err)
	}

	// Second reconcile with no listeners: port must be closed.
	backend.binds = nil
	if err := sup.reconcile(ctx); err != nil {
		t.Fatalf("reconcile(remove): %v", err)
	}
	// Give the Accept goroutine a moment to exit after Close.
	time.Sleep(50 * time.Millisecond)
	_, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", freePort), 100*time.Millisecond)
	if dialErr == nil {
		t.Fatalf("port %d still bound after removal reconcile", freePort)
	}

	// State file must exist.
	stateFile := filepath.Join(tmpDir, "forwards.state")
	if _, err := os.Stat(stateFile); err != nil {
		t.Errorf("forwards.state not written: %v", err)
	}
}

// blockingBackend implements portfwd.Backend with a ReadProcNet that ignores
// ctx and blocks until release is closed — the shape of the live wedge (one
// hung guest exec) that froze reconcile for the life of the sandbox.
type blockingBackend struct {
	calls   chan struct{}
	release chan struct{}
}

func (b *blockingBackend) ListSandboxes(_ context.Context) ([]portfwd.SandboxRef, error) {
	return []portfwd.SandboxRef{{ID: "sb1", Status: portfwd.SandboxStatusRunning}}, nil
}

func (b *blockingBackend) ReadProcNet(_ context.Context, _ string) ([]byte, []byte, error) {
	b.calls <- struct{}{}
	<-b.release
	return nil, nil, nil
}

// TestRun_DiscoveryTimeoutDoesNotWedgeLoop proves a hung discovery exec is
// bounded by the per-tick timeout and the loop keeps ticking: the backend
// blocks forever, yet a second ReadProcNet call must arrive.
//
// MUTATION PROOF: call p.disc.DiscoverOne directly in reconcile (no
// discoverBounded) → the first tick never returns, no second call, RED at
// the 10 s deadline.
func TestRun_DiscoveryTimeoutDoesNotWedgeLoop(t *testing.T) {
	backend := &blockingBackend{
		calls:   make(chan struct{}, 16),
		release: make(chan struct{}),
	}
	defer close(backend.release)

	sup := &portForwardSupervisor{
		sandboxRef:      "test/sb1",
		backend:         backend,
		disc:            &portfwd.Discoverer{Backend: backend},
		dialer:          fakeDialer{},
		stateDir:        t.TempDir(),
		interval:        50 * time.Millisecond,
		discoverTimeout: 200 * time.Millisecond,
		listeners:       make(map[uint16]net.Listener),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan struct{})
	go func() {
		sup.run(ctx)
		close(runDone)
	}()

	deadline := time.After(10 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-backend.calls:
		case <-deadline:
			t.Fatalf("only %d discovery call(s) within 10s: a hung ReadProcNet wedged the reconcile loop", i)
		}
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("run did not exit after ctx cancel")
	}
}

// TestReconcile_DiscoverTimeoutReturnsError pins that a single timed-out
// tick surfaces as an error (logged by run) rather than hanging reconcile.
func TestReconcile_DiscoverTimeoutReturnsError(t *testing.T) {
	backend := &blockingBackend{
		calls:   make(chan struct{}, 16),
		release: make(chan struct{}),
	}
	defer close(backend.release)

	sup := &portForwardSupervisor{
		sandboxRef:      "test/sb1",
		backend:         backend,
		disc:            &portfwd.Discoverer{Backend: backend},
		dialer:          fakeDialer{},
		stateDir:        t.TempDir(),
		interval:        time.Second,
		discoverTimeout: 100 * time.Millisecond,
		listeners:       make(map[uint16]net.Listener),
	}

	errCh := make(chan error, 1)
	go func() { errCh <- sup.reconcile(context.Background()) }()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("reconcile returned nil for a hung discovery; want timeout error")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reconcile did not return within 10s: discovery timeout not enforced")
	}
}
