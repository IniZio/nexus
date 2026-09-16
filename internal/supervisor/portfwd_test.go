//go:build linux

package supervisor

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/portfwd"
)

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

const fakeProcNetTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1E61 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0
`

// fakeProcNetTCP6 is a /proc/net/tcp6 LISTEN row on [::]:8080 (0x1F90).
const fakeProcNetTCP6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 23456 1 0000000000000000 100 0 0 10 0
`

// F5: every guest exec is an independent hang window (guest reaper race) and
// eagerly allocates a 16 MiB output ring, so one discovery tick must issue
// exactly ONE exec covering both tables, and both tables must still be parsed.
func TestReadProcNet_SingleExecCoversTCPAndTCP6(t *testing.T) {
	execer := &fakeGuestExecer{
		responses: []fakeExecResponse{
			{stdout: fakeProcNetTCP + fakeProcNetTCP6, code: 0}, // cat /proc/net/tcp /proc/net/tcp6
			{stdout: "", code: 0},                                // a second exec must never be issued
		},
	}
	sb := &singleSandboxBackend{
		ref:    portfwd.SandboxRef{ID: "test-id", Status: portfwd.SandboxStatusRunning},
		client: execer,
	}
	disc := &portfwd.Discoverer{Backend: sb}
	lsnrs, err := disc.DiscoverOne(context.Background(), sb.ref)
	if err != nil {
		t.Fatalf("DiscoverOne: %v", err)
	}
	if got := len(execer.calls); got != 1 {
		t.Fatalf("exec calls per discovery tick: got %d, want 1 (argv: %v)", got, argvs(execer.calls))
	}
	wantArgv := []string{"cat", "/proc/net/tcp", "/proc/net/tcp6"}
	if got := execer.calls[0].Argv; fmt.Sprint(got) != fmt.Sprint(wantArgv) {
		t.Errorf("exec argv: got %v, want %v", got, wantArgv)
	}
	ports := map[uint16]bool{}
	for _, l := range lsnrs {
		ports[l.Port] = true
	}
	if !ports[0x1E61] || !ports[0x1F90] {
		t.Errorf("listeners must include tcp 0x1E61 and tcp6 0x1F90; got %v", lsnrs)
	}
}

func argvs(calls []agent.ExecOptions) [][]string {
	out := make([][]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.Argv)
	}
	return out
}

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

type fakeDialer struct{}

func (fakeDialer) DialGuestPortForward(_ context.Context, _ string, _ uint32) (net.Conn, error) {
	// Return a pipe so forwardConn can splice without error.
	c1, c2 := net.Pipe()
	c2.Close()
	return c1, nil
}

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

	if err := sup.reconcile(ctx); err != nil {
		t.Fatalf("reconcile(add): %v", err)
	}
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", freePort), time.Second); err != nil {
		t.Fatalf("port %d not bound after reconcile: %v", freePort, err)
	}

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

	stateFile := filepath.Join(tmpDir, "forwards.state")
	if _, err := os.Stat(stateFile); err != nil {
		t.Errorf("forwards.state not written: %v", err)
	}
}

// freeForwardablePort finds a bindable port inside [GuestPortBase, GuestPortTop].
func freeForwardablePort(t *testing.T) uint16 {
	t.Helper()
	for p := uint16(8000); p <= 11000; p++ {
		probe, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err == nil {
			probe.Close()
			return p
		}
	}
	t.Skip("no free port in forwardable range 8000-11000")
	return 0
}

func mergedEntry(t *testing.T, dir string, port uint16) portfwd.Entry {
	t.Helper()
	st, err := portfwd.Merge(dir, time.Now())
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	for _, e := range st.Forwards {
		if e.Port == port {
			return e
		}
	}
	t.Fatalf("port %d absent from merged state: %+v", port, st.Forwards)
	return portfwd.Entry{}
}

// AC-6: a forwardable port whose host bind fails must never read as live.
func TestReconcile_BindFailureWritesErrorThenRetries(t *testing.T) {
	port := freeForwardablePort(t)
	squatter, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("pre-bind %d: %v", port, err)
	}
	defer squatter.Close()

	tmpDir := t.TempDir()
	backend := &fakeBackend{
		refs:  []portfwd.SandboxRef{{ID: "sb1", Status: portfwd.SandboxStatusRunning}},
		binds: []portfwd.PortBind{{Port: port, BindAddr: "0.0.0.0"}},
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

	if err := sup.reconcile(ctx); err != nil {
		t.Fatalf("reconcile(bind fails): %v", err)
	}
	if got := mergedEntry(t, tmpDir, port); got.Status != "error" {
		t.Fatalf("status after failed bind = %q, want %q", got.Status, "error")
	} else if !strings.Contains(got.Error, "address already in use") {
		t.Fatalf("merged Error after failed bind = %q, want address already in use", got.Error)
	}
	if _, bound := sup.listeners[port]; bound {
		t.Fatalf("port %d recorded in listeners despite bind failure", port)
	}
	bindErr, ok := sup.bindErrs[port]
	if !ok || bindErr == nil || !strings.Contains(bindErr.Error(), "address already in use") {
		t.Fatalf("bindErrs[%d] = %v, want address already in use", port, bindErr)
	}

	squatter.Close()
	if err := sup.reconcile(ctx); err != nil {
		t.Fatalf("reconcile(retry): %v", err)
	}
	if got := mergedEntry(t, tmpDir, port); got.Status != "live" {
		t.Fatalf("status after retry = %q, want %q", got.Status, "live")
	} else if got.Error != "" {
		t.Fatalf("merged Error after retry = %q, want empty", got.Error)
	}
	if _, ok := sup.bindErrs[port]; ok {
		t.Fatalf("bindErrs still holds port %d after successful retry", port)
	}
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err != nil {
		t.Fatalf("port %d not bound after retry: %v", port, err)
	}
}

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

// TestRun_DiscoveryTimeoutDoesNotWedgeLoop proves hung discovery is bounded by per-tick timeout.
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
