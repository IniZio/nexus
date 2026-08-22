//go:build integration

package cloudhypervisor

// agent_integration_test.go verifies that nexus3-agent (cmd/nexus3-agent) can
// run as the guest init process and serve the control/data planes over vsock.
//
// The tests build a static nexus3-agent binary at test time, splice it into
// the Alpine-derived initramfs artifact (replacing its /init), boot a VM, then
// exercise Exec, PTY, and snapshot+reattach via the agent.Client host API.
//
// # Guard conditions
//
// All tests skip (never fail) when the environment lacks:
//   - /dev/kvm (checked via skipUnlessKVM, defined in boot_integration_test.go)
//   - cloud-hypervisor binary (checked via skipUnlessCHBin)
//   - alpine-initramfs.cpio.gz artifact (checked via skipUnlessArtifact)
//   - vmlinux-x86_64 kernel artifact (checked via skipUnlessArtifact)
//   - gzip and cpio tools (checked locally)
//
// The go test build must supply GOOS=linux GOARCH=amd64 implicitly (the test
// host and guest are both x86-64 Linux; if cross-compilation is needed the
// build step skips).

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// skipUnlessTool skips t if the named executable is not found in PATH.
func skipUnlessTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Skipf("skipping: %q not found in PATH (required to build initramfs)", name)
	}
}

// buildNexus3Agent compiles cmd/nexus3-agent as a static Linux/amd64 binary
// and returns the path to the binary. The binary is placed in a temp directory
// that is cleaned up when t completes.
func buildNexus3Agent(t *testing.T) string {
	t.Helper()

	// Locate the repo root: go list -m outputs the module path; we need the
	// module root directory. Use go env GOMOD instead.
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	goMod := strings.TrimSpace(string(out))
	if goMod == "" || goMod == os.DevNull {
		t.Skip("skipping: go env GOMOD returned empty (not in a module)")
	}
	repoRoot := filepath.Dir(goMod)

	dir := t.TempDir()
	agentBin := filepath.Join(dir, "nexus3-agent")

	cmd := exec.Command("go", "build", "-o", agentBin,
		"github.com/IniZio/nexus3/cmd/nexus3-agent")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
	)
	if buildOut, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build nexus3-agent:\n%s\n%v", buildOut, err)
	}

	return agentBin
}

// buildAgentInitramfs creates a cpio.gz initramfs that is based on the
// Alpine initramfs artifact but with /init replaced by the nexus3-agent
// binary. Returns the path to the new initramfs file.
//
// Requires: gzip, cpio tools.
func buildAgentInitramfs(t *testing.T, agentBin, baseInitramfs string) string {
	t.Helper()

	dir := t.TempDir()

	// Extract the base initramfs into a staging directory.
	stageDir := filepath.Join(dir, "stage")
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		t.Fatalf("MkdirAll stage: %v", err)
	}

	// gunzip the base initramfs, then extract via cpio.
	gunzipCmd := exec.Command("gunzip", "-c", baseInitramfs)
	cpioCmd := exec.Command("cpio", "-id")
	cpioCmd.Dir = stageDir

	gunzipOut, err := gunzipCmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe gunzip: %v", err)
	}
	cpioCmd.Stdin = gunzipOut
	var cpioErr bytes.Buffer
	cpioCmd.Stderr = &cpioErr

	if err := gunzipCmd.Start(); err != nil {
		t.Fatalf("gunzip start: %v", err)
	}
	if err := cpioCmd.Start(); err != nil {
		t.Fatalf("cpio start: %v", err)
	}
	if err := gunzipCmd.Wait(); err != nil {
		t.Fatalf("gunzip wait: %v", err)
	}
	if err := cpioCmd.Wait(); err != nil {
		t.Logf("cpio stderr: %s", cpioErr.String())
		t.Fatalf("cpio wait: %v", err)
	}

	// Replace /init with the nexus3-agent binary.
	initDst := filepath.Join(stageDir, "init")
	if err := os.Remove(initDst); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove existing /init: %v", err)
	}
	// Read the agent binary and write it.
	data, err := os.ReadFile(agentBin)
	if err != nil {
		t.Fatalf("read agentBin: %v", err)
	}
	if err := os.WriteFile(initDst, data, 0o755); err != nil {
		t.Fatalf("write /init: %v", err)
	}

	// Also install nexus3-agent as /nexus3-agent for exec tests (the agent
	// binary needs a shell; Alpine's busybox provides /bin/sh).
	agentDst := filepath.Join(stageDir, "nexus3-agent")
	if err := os.WriteFile(agentDst, data, 0o755); err != nil {
		t.Fatalf("write /nexus3-agent: %v", err)
	}

	// Pack the staging directory into a new cpio.gz.
	newInitramfs := filepath.Join(dir, "agent-initramfs.cpio.gz")

	// find + cpio + gzip pipeline
	findCmd := exec.Command("find", ".", "-print0")
	findCmd.Dir = stageDir
	cpioPackCmd := exec.Command("cpio", "--null", "-o", "-H", "newc")
	cpioPackCmd.Dir = stageDir
	gzipCmd := exec.Command("gzip", "-c")

	findOut, err := findCmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe find: %v", err)
	}
	cpioPackCmd.Stdin = findOut

	cpioPackOut, err := cpioPackCmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe cpio pack: %v", err)
	}
	gzipCmd.Stdin = cpioPackOut

	outF, err := os.Create(newInitramfs)
	if err != nil {
		t.Fatalf("create initramfs: %v", err)
	}
	gzipCmd.Stdout = outF
	defer outF.Close()

	if err := findCmd.Start(); err != nil {
		t.Fatalf("find start: %v", err)
	}
	if err := cpioPackCmd.Start(); err != nil {
		t.Fatalf("cpio pack start: %v", err)
	}
	if err := gzipCmd.Start(); err != nil {
		t.Fatalf("gzip start: %v", err)
	}

	if err := findCmd.Wait(); err != nil {
		t.Fatalf("find wait: %v", err)
	}
	if err := cpioPackCmd.Wait(); err != nil {
		t.Fatalf("cpio pack wait: %v", err)
	}
	if err := gzipCmd.Wait(); err != nil {
		t.Fatalf("gzip wait: %v", err)
	}

	return newInitramfs
}

// bootAgentVM starts a Cloud Hypervisor VM with nexus3-agent as init.
// Returns the driver, socket dir, and the sandbox ID. Cleanup is registered
// automatically via t.Cleanup.
func bootAgentVM(t *testing.T, chBin, kernelPath, initramfsPath string) (*CHDriver, domain.SandboxID) {
	t.Helper()

	socketDir, err := os.MkdirTemp("/tmp", "ch-agent-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	// Capture the guest serial port (ttyS0) to a file so that nexus3-agent's
	// diagnostic output is visible in t.Logf on test failure.
	serialPath := filepath.Join(socketDir, "serial.log")

	drv, err := New(Config{
		BinaryPath:    chBin,
		SocketDir:     socketDir,
		KernelPath:    kernelPath,
		InitramfsPath: initramfsPath,
		// console=ttyS0: kernel + agent output on serial; panic=1: reboot on
		// kernel panic; init=/init: explicitly select our agent binary.
		Cmdline:          "console=ttyS0 panic=1 init=/init",
		SerialOutputPath: serialPath,
		VCPUs:            1,
		MemoryMiB:        256,
		StartTimeout:     20 * time.Second,
	})
	if err != nil {
		os.RemoveAll(socketDir)
		t.Fatalf("New CHDriver: %v", err)
	}

	id := domain.NewSandboxID()

	var vmmPID int
	t.Cleanup(func() {
		// Print the serial log before stopping the VM so the output is
		// visible in -v mode (and on failure without -v).
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 {
			t.Logf("guest serial output (%s):\n%s", serialPath, content)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = drv.Stop(ctx, id)
		if vmmPID != 0 {
			_ = syscall.Kill(-vmmPID, syscall.SIGKILL)
		}
		drv.clearState(id)
		os.RemoveAll(socketDir)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if _, err := drv.Start(ctx, driver.StartRequest{SandboxID: id}); err != nil {
		t.Fatalf("drv.Start: %v", err)
	}

	// Record VMM PID for cleanup.
	drv.mu.Lock()
	if proc := drv.procs[id]; proc != nil {
		vmmPID = proc.pid
	}
	drv.mu.Unlock()

	// Give nexus3-agent time to initialise its vsock listeners.
	// In practice it binds within 200-500 ms of the kernel entering userspace.
	//
	// This fixed budget is also the regression guard for the boot-critical-path
	// defect: any blocking work added to PID-1 startup AHEAD of the vsock
	// listeners (the egress DNS probe used to sit there and cost up to 3s)
	// pushes the bind past this sleep and every agent test fails with
	// "read handshake reply: EOF". Do not raise it to make a slow boot pass —
	// move the slow work off the pre-bind path instead.
	time.Sleep(2 * time.Second)

	return drv, id
}

// ── tests ─────────────────────────────────────────────────────────────────────

// TestAgentExec boots a VM with nexus3-agent as init, executes a command via
// the host agent.Client, and asserts the output arrives over the data plane.
func TestAgentExec(t *testing.T) {
	skipUnlessKVM(t)
	chBin := skipUnlessCHBin(t)
	kernelPath := skipUnlessArtifact(t, "vmlinux-x86_64")
	baseInitramfs := skipUnlessArtifact(t, "alpine-initramfs.cpio.gz")
	skipUnlessTool(t, "cpio")
	skipUnlessTool(t, "gzip")

	agentBin := buildNexus3Agent(t)
	initramfsPath := buildAgentInitramfs(t, agentBin, baseInitramfs)

	drv, id := bootAgentVM(t, chBin, kernelPath, initramfsPath)

	c := agent.NewClient(drv, id)

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	exitCode, err := c.Exec(ctx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "echo hello-nexus3"},
		Stdout: &stdout,
		Stderr: os.Stderr,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exit code: got %d, want 0", exitCode)
	}
	if !strings.Contains(stdout.String(), "hello-nexus3") {
		t.Errorf("expected 'hello-nexus3' in output, got %q", stdout.String())
	}
}

// TestAgentPTY boots a VM with nexus3-agent as init, opens a PTY session via
// the host agent.Client, and verifies interactive output arrives.
func TestAgentPTY(t *testing.T) {
	skipUnlessKVM(t)
	chBin := skipUnlessCHBin(t)
	kernelPath := skipUnlessArtifact(t, "vmlinux-x86_64")
	baseInitramfs := skipUnlessArtifact(t, "alpine-initramfs.cpio.gz")
	skipUnlessTool(t, "cpio")
	skipUnlessTool(t, "gzip")

	agentBin := buildNexus3Agent(t)
	initramfsPath := buildAgentInitramfs(t, agentBin, baseInitramfs)

	drv, id := bootAgentVM(t, chBin, kernelPath, initramfsPath)

	c := agent.NewClient(drv, id)

	var stdout bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	exitCode, err := c.Exec(ctx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "echo pty-guest"},
		Stdout: &stdout,
		Stderr: os.Stderr,
		Pty:    newPtyOpts("xterm-256color", 24, 80),
	})
	if err != nil {
		t.Fatalf("Exec (PTY): %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exit code: got %d, want 0", exitCode)
	}
	// PTY output may include CR/LF sequences; just check containment.
	if !strings.Contains(stdout.String(), "pty-guest") {
		t.Errorf("expected 'pty-guest' in PTY output, got %q", stdout.String())
	}
}

// syncBuf is a bytes.Buffer safe for concurrent Write/snapshot calls.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// snapshot returns a copy of the current buffer contents.
func (s *syncBuf) snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := make([]byte, s.buf.Len())
	copy(cp, s.buf.Bytes())
	return cp
}

// TestAgentSnapshotReattach boots a VM, starts a slow exec that emits "NEXUS"
// then sleeps then emits "3". While the sleep is in progress (VM is paused and
// resumed), a second data-plane connection (Attach) is opened from byte offset 5
// and must receive "3" — proving that the guest ring survives pause+resume and
// that host clients can reattach from a known offset.
func TestAgentSnapshotReattach(t *testing.T) {
	skipUnlessKVM(t)
	chBin := skipUnlessCHBin(t)
	kernelPath := skipUnlessArtifact(t, "vmlinux-x86_64")
	baseInitramfs := skipUnlessArtifact(t, "alpine-initramfs.cpio.gz")
	skipUnlessTool(t, "cpio")
	skipUnlessTool(t, "gzip")

	agentBin := buildNexus3Agent(t)
	initramfsPath := buildAgentInitramfs(t, agentBin, baseInitramfs)

	drv, id := bootAgentVM(t, chBin, kernelPath, initramfsPath)

	c := agent.NewClient(drv, id)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Assign a known session ID so we can reattach to the same session below.
	sessionID := fmt.Sprintf("snap-reattach-%d", os.Getpid())

	// Exec a command that prints "NEXUS", sleeps 3 s, then prints "3".
	// Total ring content will be "NEXUS3" (6 bytes).
	var part1 syncBuf
	execErrCh := make(chan error, 1)
	go func() {
		_, err := c.Exec(ctx, agent.ExecOptions{
			SessionID: sessionID,
			Argv:      []string{"/bin/sh", "-c", "printf NEXUS; sleep 3; printf 3"},
			Stdout:    &part1,
			Stderr:    os.Stderr,
		})
		execErrCh <- err
	}()

	// Poll until "NEXUS" (5 bytes) arrives in the ring (before sleep fires).
	t.Log("waiting for NEXUS in exec output …")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains(part1.snapshot(), []byte("NEXUS")) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !bytes.Contains(part1.snapshot(), []byte("NEXUS")) {
		t.Fatal("timed out waiting for NEXUS in exec output")
	}
	offset := uint64(5) // len("NEXUS")

	// Pause and resume the VM while sleep is running.
	if err := drv.Pause(ctx, id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	time.Sleep(200 * time.Millisecond) // let state settle
	if err := drv.Resume(ctx, id); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	time.Sleep(500 * time.Millisecond) // let agent re-bind vsock listeners

	// Open a second data-plane connection from offset 5.
	// The guest ring has "NEXUS3"; bytes[5:] == "3".
	var reattachOut bytes.Buffer
	attachCtx, attachCancel := context.WithTimeout(ctx, 10*time.Second)
	defer attachCancel()

	_, attachErr := c.Attach(attachCtx, agent.AttachOptions{
		SessionID:        sessionID,
		ResumeFromOffset: offset,
		Stdout:           &reattachOut,
		Stderr:           os.Stderr,
	})
	if attachErr != nil {
		t.Fatalf("Attach from offset %d: %v", offset, attachErr)
	}
	if !bytes.Contains(reattachOut.Bytes(), []byte("3")) {
		t.Errorf("reattach from offset %d: got %q, want to contain '3'", offset, reattachOut.Bytes())
	}

	// Wait for the exec goroutine; it exits after "3" is emitted.
	if err := <-execErrCh; err != nil {
		t.Errorf("Exec goroutine: %v", err)
	}
}

// ── utilities ─────────────────────────────────────────────────────────────────

// newPtyOpts builds an *agentpb.PtyOptions for test PTY sessions.
func newPtyOpts(term string, rows, cols uint32) *agentpb.PtyOptions {
	return &agentpb.PtyOptions{
		Term: term,
		InitialSize: &agentpb.WinSize{
			Rows: rows,
			Cols: cols,
		},
	}
}
