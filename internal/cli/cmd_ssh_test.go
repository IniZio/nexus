package cli

import (
	"bytes"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// newSSHTestService builds a service with a FakeDriver and a temp file store.
func newSSHTestService(t *testing.T) (*service.Service, *fake.FakeDriver) {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	drv := fake.New()
	return service.New(st, drv, lifecycle.New()), drv
}

// TestSSHStdio_splicesData verifies that runSSHStdio copies data between the
// in-memory conn and the provided stdin/stdout without requiring a real VM.
func TestSSHStdio_splicesData(t *testing.T) {
	svc, drv := newSSHTestService(t)

	// Create a sandbox so it can be resolved.
	sb, err := svc.Create(context.Background(), "proj", "box", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Mark it Running in the fake driver so DialGuest doesn't fail on state
	// checks (fake.DialGuest doesn't check state, but record the ID for
	// GuestConn retrieval).
	drv.SetRunning(sb.ID)

	// Use a cancellable context so we can terminate the splice loop.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// stdin provides data that should flow to the conn.
	stdinData := []byte("hello from host")
	stdinR := bytes.NewReader(stdinData)

	// stdout captures data that flows from the conn.
	var stdoutBuf bytes.Buffer

	// Run the splice in a goroutine; cancel the context to stop it.
	errCh := make(chan error, 1)
	go func() {
		errCh <- runSSHStdio(ctx, sb.Handle(), svc, stdinR, &stdoutBuf)
	}()

	// The fake driver created an in-memory pipe. Retrieve the guest side.
	// Poll briefly waiting for the goroutine to call DialGuest.
	var guestConn net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		guestConn = drv.GuestConn(sb.ID)
		if guestConn != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if guestConn == nil {
		t.Fatal("GuestConn: fake driver never got a DialGuest call within 2s")
	}

	// Write data from guest side → stdout.
	guestData := []byte("hello from guest")
	if _, err := guestConn.Write(guestData); err != nil {
		t.Fatalf("guestConn.Write: %v", err)
	}

	// Close the guest conn to trigger EOF on the splice loop.
	guestConn.Close()

	// Cancel context in case the stdin direction blocks.
	cancel()

	if err := <-errCh; err != nil {
		t.Fatalf("runSSHStdio: %v", err)
	}

	got := stdoutBuf.String()
	if got != string(guestData) {
		t.Errorf("stdout = %q, want %q", got, string(guestData))
	}
}

// slowWriter delays every write, widening the window in which a splice
// goroutine is still writing into the caller's buffer.
type slowWriter struct {
	delay time.Duration
	buf   *bytes.Buffer
}

func (w *slowWriter) Write(p []byte) (int, error) {
	time.Sleep(w.delay)
	return w.buf.Write(p)
}

// runSSHStdioDrainCase asserts that runSSHStdio does not return while the
// conn→stdout goroutine is still writing into the caller-supplied writer.
// stop decides how the splice is terminated: by stdin EOF or by ctx cancel.
func runSSHStdioDrainCase(t *testing.T, stopVia string) {
	t.Helper()
	svc, drv := newSSHTestService(t)

	sb, err := svc.Create(context.Background(), "proj", "box", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	drv.SetRunning(sb.ID)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// stdin stays open until we decide to end the splice.
	stdinR, stdinW := io.Pipe()
	defer stdinW.Close()

	var stdoutBuf bytes.Buffer
	stdout := &slowWriter{delay: 200 * time.Millisecond, buf: &stdoutBuf}

	errCh := make(chan error, 1)
	go func() {
		errCh <- runSSHStdio(ctx, sb.Handle(), svc, stdinR, stdout)
	}()

	var guestConn net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		guestConn = drv.GuestConn(sb.ID)
		if guestConn != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if guestConn == nil {
		t.Fatal("GuestConn: fake driver never got a DialGuest call within 2s")
	}
	defer guestConn.Close()

	// net.Pipe is synchronous: once Write returns, the splice has the bytes and
	// is parked inside slowWriter.Write.
	guestData := []byte("hello from guest")
	if _, err := guestConn.Write(guestData); err != nil {
		t.Fatalf("guestConn.Write: %v", err)
	}

	switch stopVia {
	case "stdin-eof":
		stdinW.Close()
	case "ctx-cancel":
		cancel()
	default:
		t.Fatalf("unknown stopVia %q", stopVia)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("runSSHStdio: %v", err)
	}

	// runSSHStdio has returned, so nothing may still be writing to stdoutBuf.
	if got := stdoutBuf.String(); got != string(guestData) {
		t.Errorf("stdout after return = %q, want %q (splice goroutine outlived the call)", got, string(guestData))
	}
}

// TestSSHStdio_drainsStdoutOnStdinEOF is a regression test for a data race:
// runSSHStdio used to return as soon as the stdin→conn direction hit EOF,
// leaving the conn→stdout goroutine writing into the caller's writer.
func TestSSHStdio_drainsStdoutOnStdinEOF(t *testing.T) {
	runSSHStdioDrainCase(t, "stdin-eof")
}

// TestSSHStdio_drainsStdoutOnCtxCancel covers the same defect on the
// ctx.Done() return path, which used to return with both goroutines live.
func TestSSHStdio_drainsStdoutOnCtxCancel(t *testing.T) {
	runSSHStdioDrainCase(t, "ctx-cancel")
}

// TestSSHStdio_notFound verifies that a missing sandbox ref returns an error.
func TestSSHStdio_notFound(t *testing.T) {
	svc, _ := newSSHTestService(t)

	err := runSSHStdio(context.Background(), "noproject/nosandbox", svc, strings.NewReader(""), io.Discard)
	if err == nil {
		t.Fatal("expected error for missing sandbox ref, got nil")
	}
}

// TestConfigSSH_writesStanza verifies the config stanza is written to
// ~/.ssh/config in the temp home dir.
func TestConfigSSH_writesStanza(t *testing.T) {
	svc, _ := newSSHTestService(t)

	sb, err := svc.Create(context.Background(), "myproj", "mybox", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	homeDir := t.TempDir()
	out, _, _ := capture(false)
	if err := runConfigSSHWithHome(context.Background(), sb.Handle(), svc, homeDir, out); err != nil {
		t.Fatalf("runConfigSSHWithHome: %v", err)
	}

	configPath := filepath.Join(homeDir, ".ssh", "config")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	text := string(content)
	if !strings.Contains(text, "# nexus3 sandbox: myproj/mybox") {
		t.Errorf("config missing marker line; got:\n%s", text)
	}
	if !strings.Contains(text, "Host nexus3-myproj-mybox") {
		t.Errorf("config missing Host line; got:\n%s", text)
	}
	if !strings.Contains(text, "ProxyCommand nexus3 ssh --stdio myproj/mybox") {
		t.Errorf("config missing ProxyCommand line; got:\n%s", text)
	}
}

// TestConfigSSH_idempotent verifies that running config-ssh twice does not
// duplicate the stanza.
func TestConfigSSH_idempotent(t *testing.T) {
	svc, _ := newSSHTestService(t)

	sb, err := svc.Create(context.Background(), "myproj", "mybox", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	homeDir := t.TempDir()
	out, _, _ := capture(false)

	// First run.
	if err := runConfigSSHWithHome(context.Background(), sb.Handle(), svc, homeDir, out); err != nil {
		t.Fatalf("first runConfigSSHWithHome: %v", err)
	}

	// Second run.
	if err := runConfigSSHWithHome(context.Background(), sb.Handle(), svc, homeDir, out); err != nil {
		t.Fatalf("second runConfigSSHWithHome: %v", err)
	}

	configPath := filepath.Join(homeDir, ".ssh", "config")
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	// Count occurrences of the marker.
	count := strings.Count(string(content), "# nexus3 sandbox: myproj/mybox")
	if count != 1 {
		t.Errorf("marker appears %d times, want 1; config:\n%s", count, string(content))
	}
}

// TestConfigSSH_notFound verifies error on missing sandbox.
func TestConfigSSH_notFound(t *testing.T) {
	svc, _ := newSSHTestService(t)
	homeDir := t.TempDir()
	out, _, _ := capture(false)

	err := runConfigSSHWithHome(context.Background(), "ghost/sandbox", svc, homeDir, out)
	if err == nil {
		t.Fatal("expected error for missing sandbox, got nil")
	}
}
