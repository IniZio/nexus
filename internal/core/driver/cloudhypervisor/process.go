package cloudhypervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// ErrVMMAlreadyBound is returned by spawnVMM when a live cloud-hypervisor
// process is already bound to the socket path and responding to vmm.ping.
// Callers should use errors.Is to check for this sentinel.
var ErrVMMAlreadyBound = errors.New("cloudhypervisor: live VMM already bound to socket")

// probeTimeout is the maximum time spawnVMM waits for vmm.ping to respond
// during the pre-flight socket check. Short by design: a live VMM answers
// immediately; anything slower is treated as undetermined.
const probeTimeout = 500 * time.Millisecond

// vmmStderrBuf is a bounded, thread-safe byte buffer that retains the last
// maxSize bytes written to it. It is used to capture VMM stderr so that a
// failed Start() can include hypervisor-side diagnostic context in the error.
//
// Memory is bounded: once the internal slice exceeds maxSize, the oldest bytes
// are dropped and the backing array is replaced so the GC can reclaim it. A
// chatty or wedged VMM therefore cannot grow this buffer unboundedly.
type vmmStderrBuf struct {
	mu      sync.Mutex
	data    []byte
	maxSize int
}

func newVMMStderrBuf(maxSize int) *vmmStderrBuf {
	return &vmmStderrBuf{maxSize: maxSize}
}

func (b *vmmStderrBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.maxSize {
		// Keep only the last maxSize bytes. Copy to a new backing array so the
		// GC can reclaim the old one; a plain reslice keeps the large backing
		// array alive for the lifetime of the vmmStderrBuf.
		tail := b.data[len(b.data)-b.maxSize:]
		fresh := make([]byte, b.maxSize)
		copy(fresh, tail)
		b.data = fresh
	}
	return len(p), nil
}

// Tail returns the buffered VMM stderr output as a string. If the buffer is
// empty, the empty string is returned.
func (b *vmmStderrBuf) Tail() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

// killFn is the function used by kill() to signal a process group. It is a
// package-level variable so tests can inject a stub to assert signal counts
// without affecting any real process.
var killFn func(int, syscall.Signal) error = syscall.Kill

// managedProcess tracks a running cloud-hypervisor VMM or virtiofsd process.
type managedProcess struct {
	cmd       *exec.Cmd
	pid       int
	stderrBuf *vmmStderrBuf // bounded ring of VMM stderr; nil only in tests that bypass spawnVMM
	// deathCh is closed exactly once by reapWatcher when the child has been
	// reaped. kill() checks it before signalling (a closed deathCh means the
	// PID slot may have been recycled) and drains it after signalling (so
	// kill() returns only after the zombie is gone). nil only for
	// managedProcess values constructed directly in tests that bypass
	// newManagedProcess.
	deathCh chan struct{}
	// PID alone is unsafe as a process identity across reuse. If the VMM
	// crashes and the OS recycles its PID before nexus3 restarts, a different
	// process could appear as the old VMM. The established pattern in this
	// project is PID + process start time (available from /proc/<pid>/stat
	// field 22 on Linux). This implementation records only the PID — start
	// time tracking is a documented gap. The gap is safe in practice because
	// nexus3 rebuilds its view of running VMs through Observe() after a
	// restart; it does not rely on the in-memory proc table across restarts.
}

// newManagedProcess constructs a managedProcess and starts the reapWatcher
// goroutine. Must be called only AFTER the process's readiness check has
// passed: the failure-path cleanup() closure calls cmd.Wait() directly, and
// starting a concurrent reapWatcher before that call would race it.
func newManagedProcess(cmd *exec.Cmd, pid int, stderrBuf *vmmStderrBuf) *managedProcess {
	p := &managedProcess{
		cmd:       cmd,
		pid:       pid,
		stderrBuf: stderrBuf,
		deathCh:   make(chan struct{}),
	}
	go p.reapWatcher()
	return p
}

// reapWatcher blocks until the child exits, reaps the zombie, and closes
// deathCh. It uses syscall.Wait4 directly instead of cmd.Wait() for two
// reasons:
//
//  1. cmd.Wait() blocks in awaitGoroutines until every internal io.Copy
//     goroutine drains its pipe. In the netns child path (spawnVMMInGroup /
//     RunNetnsChild) an fd can leak across the fork boundary and prevent
//     pipe write-ends from closing, keeping cmd.Wait() blocked indefinitely.
//     See ch_netns.go's goroutine at the Wait4 loop for the same rationale.
//
//  2. RunNetnsChild already has its own goroutine calling syscall.Wait4 on
//     the same pid (ch_netns.go). If reapWatcher gets there first, the
//     netns loop receives ECHILD and breaks safely (see that loop's comment).
//     If the netns loop gets there first, reapWatcher receives ECHILD and
//     breaks here. Both orders converge correctly.
func (p *managedProcess) reapWatcher() {
	var ws syscall.WaitStatus
	for {
		_, err := syscall.Wait4(p.pid, &ws, 0, nil)
		if err == nil {
			break // reaped
		}
		if errors.Is(err, syscall.EINTR) {
			continue // interrupted by signal; retry
		}
		break // ECHILD (reaped by concurrent waiter) or unexpected error
	}
	close(p.deathCh)
}

// kill sends SIGKILL to the process group and waits for the child to be reaped.
//
// SysProcAttr.Setpgid: true (set in spawnVMM / spawnVirtiofsd) makes the child
// a process group leader with pgid == pid, so Kill(-pid, SIGKILL) sends SIGKILL
// to the whole group — catching any child processes the VMM may have spawned.
//
// GUARD: if deathCh is already closed the process has been reaped and its PID
// slot may have been recycled. Never signal a recycled PID — return immediately.
// This window matters because mid-life death is the scenario this ticket exists
// to handle: a VM dies at 14:09, d.procs[id] still holds the entry, and Stop()
// calls kill() hours later.
//
// For managedProcess values constructed without newManagedProcess (nil deathCh),
// kill() falls back to a direct Wait to preserve backward compatibility.
func (p *managedProcess) kill() {
	if p.cmd.Process == nil {
		return
	}
	// Check before signalling: if already reaped, the PID may be recycled.
	//
	// Declining to signal also declines to sweep any group member that outlived
	// the leader. That is the correct trade: the obvious alternative — probe
	// kill(-pid, 0) and signal only if the group answers — is UNSAFE, because a
	// recycled PID that has become a new group leader answers that probe too.
	// This guard is safe in both directions. The residual is near-empty in
	// practice: virtiofsd runs --sandbox none (threads, not forked children),
	// cloud-hypervisor is threaded, and CH additionally carries Pdeathsig.
	if p.deathCh != nil {
		select {
		case <-p.deathCh:
			// Release the process handle so the pidfd is closed now rather than
			// at the next GC. Release neither waits nor signals, so it keeps the
			// Wait4 ownership semantics intact. cmd.Wait() must NOT be used here
			// — it would reintroduce the pipe-drain block the Wait4 switch exists
			// to avoid.
			_ = p.cmd.Process.Release()
			return // already reaped — never signal a recycled PID
		default:
		}
	}
	_ = killFn(-p.pid, syscall.SIGKILL)
	if p.deathCh != nil {
		<-p.deathCh // wait for reapWatcher to finish (returns only after zombie gone)
	} else {
		_ = p.cmd.Wait() // legacy path: tests that construct managedProcess directly
	}
}

// spawnVMM spawns a cloud-hypervisor process with --api-socket socketPath,
// Setpgid:true (CH is its own process group leader), and polls vmm.ping
// until the API is responsive. It returns a managedProcess on success.
//
// This is the standard host-process path. Use spawnVMMInGroup when CH must
// inherit the caller's process group (netns child path — so the group-kill
// in rt.Stop reaches CH).
//
// Pre-flight: before spawning, spawnVMM probes socketPath with vmm.ping:
//   - Ping succeeds (live VMM)          → return ErrVMMAlreadyBound; do not spawn.
//   - Ping fails with isAbsent (ENOENT/ECONNREFUSED) → stale or absent socket;
//     remove any stale file and proceed to spawn.
//   - Ping fails for any other reason   → socket state undetermined (hung VMM?);
//     return the probe error; do NOT remove the socket or spawn.
//
// On any failure after the process starts (poll timeout, parent context
// cancel) spawnVMM kills the child and removes socketPath before returning
// the error — no orphan processes or stale sockets are left behind.
func spawnVMM(ctx context.Context, cfg Config, socketPath string) (*managedProcess, error) {
	attr := &syscall.SysProcAttr{Setpgid: true}
	setPdeathsig(attr)
	return spawnVMMWithAttr(ctx, cfg, socketPath, attr)
}

// spawnVMMInGroup is like spawnVMM but with Setpgid:false so the spawned CH
// process inherits the caller's process group. Used by RunNetnsChild where
// the child is a process group leader (Setpgid set in netnsChildAttr) and CH
// must be in the same group so that rt.Stop()'s group-kill
// (Kill(-childPgid, SIGKILL)) reaches CH.
func spawnVMMInGroup(ctx context.Context, cfg Config, socketPath string) (*managedProcess, error) {
	attr := &syscall.SysProcAttr{Setpgid: false}
	setPdeathsig(attr) // defense-in-depth: CH dies if child dies unexpectedly (Linux-only)
	return spawnVMMWithAttr(ctx, cfg, socketPath, attr)
}

// spawnVMMWithAttr is the shared implementation of spawnVMM and spawnVMMInGroup,
// parameterized by SysProcAttr. Callers choose Setpgid:true (host path, CH owns
// its group) or Setpgid:false (netns child path, CH inherits child's group).
func spawnVMMWithAttr(ctx context.Context, cfg Config, socketPath string, attr *syscall.SysProcAttr) (*managedProcess, error) {
	// Pre-flight: probe the socket before spawning.
	//
	// Drop the os.Stat pre-check: the dial result already distinguishes all
	// three cases (ENOENT = absent, ECONNREFUSED = stale, else = undetermined).
	// Note: there is a small TOCTOU window between this probe and the bind in
	// cmd.Start; it is narrowed to milliseconds and acceptable because the
	// caller holds the per-sandbox exclusive flock.
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	pingErr := newClient(socketPath).Ping(probeCtx)
	cancel()

	switch {
	case pingErr == nil:
		// A live VMM is already answering on this socket. Refuse to collide.
		return nil, fmt.Errorf("%s: %w", socketPath, ErrVMMAlreadyBound)
	case isAbsent(pingErr):
		// Socket absent (ENOENT) or stale file (ECONNREFUSED). Remove any
		// stale socket file; ignore the error if there was nothing to remove.
		_ = os.Remove(socketPath)
	default:
		// Socket exists but did not respond in time (hung VMM, I/O error, …).
		// We cannot confirm the VMM is dead, so do not remove the socket.
		return nil, fmt.Errorf("cloudhypervisor: pre-flight ping %s: %w", socketPath, pingErr)
	}

	// stderrBuf retains the last 64 KB of VMM stderr. A failed Start() includes
	// the tail in its error so operators get hypervisor-side context without
	// reconfiguring anything. 64 KB is sufficient for hundreds of log lines;
	// memory is bounded regardless of how chatty the VMM is.
	const stderrBufSize = 64 * 1024
	stderrBuf := newVMMStderrBuf(stderrBufSize)

	cmd := exec.Command(cfg.BinaryPath, "--api-socket", socketPath)
	cmd.SysProcAttr = attr
	cmd.Stdout = io.Discard
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cloudhypervisor: spawn VMM: %w", err)
	}

	pid := cmd.Process.Pid

	// cleanup is called on all failure paths after cmd.Start() succeeds.
	// Kill the process group (covers both Setpgid:true and Setpgid:false paths).
	cleanup := func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = os.Remove(socketPath)
	}

	timeout := cfg.StartTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	pollCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c := newClient(socketPath)

	for {
		if err := pollCtx.Err(); err != nil {
			// Context expired or was cancelled before the API became ready.
			// Include any VMM stderr to explain why the process didn't start.
			tail := stderrBuf.Tail()
			cleanup()
			if tail != "" {
				return nil, fmt.Errorf("cloudhypervisor: VMM API not ready within %s: %w\nVMM stderr:\n%s", timeout, err, tail)
			}
			return nil, fmt.Errorf("cloudhypervisor: VMM API not ready within %s: %w", timeout, err)
		}

		pingErr := c.Ping(pollCtx)
		if pingErr == nil {
			// API is up. Start the reapWatcher goroutine now — after readiness
			// is confirmed — so it does not race the failure-path cleanup().
			return newManagedProcess(cmd, pid, stderrBuf), nil
		}

		// Wait 50 ms before the next poll, but wake immediately if either
		// context expires.
		select {
		case <-pollCtx.Done():
			// Will be caught at the top of the next loop iteration.
		case <-time.After(50 * time.Millisecond):
		}
	}
}
