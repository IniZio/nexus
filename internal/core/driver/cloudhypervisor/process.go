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

// managedProcess tracks a running cloud-hypervisor VMM process.
type managedProcess struct {
	cmd       *exec.Cmd
	pid       int
	stderrBuf *vmmStderrBuf // bounded ring of VMM stderr; nil only in tests that bypass spawnVMM
	// PID alone is unsafe as a process identity across reuse. If the VMM
	// crashes and the OS recycles its PID before nexus3 restarts, a different
	// process could appear as the old VMM. The established pattern in this
	// project is PID + process start time (available from /proc/<pid>/stat
	// field 22 on Linux). This implementation records only the PID — start
	// time tracking is a documented gap. The gap is safe in practice because
	// nexus3 rebuilds its view of running VMs through Observe() after a
	// restart; it does not rely on the in-memory proc table across restarts.
}

// kill sends SIGKILL to the VMM's entire process group and waits for the
// process to exit, reaping the zombie.
//
// SysProcAttr.Setpgid: true (set in spawnVMM) makes the child a process
// group leader with pgid == pid, so syscall.Kill(-pid, SIGKILL) sends
// SIGKILL to the whole group — catching any child processes the VMM may have
// spawned. cmd.Process.Kill() and exec.CommandContext's cancel both signal
// only the leader; using -pgid is intentional here.
func (p *managedProcess) kill() {
	if p.cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-p.pid, syscall.SIGKILL)
	_ = p.cmd.Wait() // reap the zombie; ignore error (process may already be gone)
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
	return spawnVMMWithAttr(ctx, cfg, socketPath, &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	})
}

// spawnVMMInGroup is like spawnVMM but with Setpgid:false so the spawned CH
// process inherits the caller's process group. Used by RunNetnsChild where
// the child is a process group leader (Setpgid set in netnsChildAttr) and CH
// must be in the same group so that rt.Stop()'s group-kill
// (Kill(-childPgid, SIGKILL)) reaches CH.
func spawnVMMInGroup(ctx context.Context, cfg Config, socketPath string) (*managedProcess, error) {
	return spawnVMMWithAttr(ctx, cfg, socketPath, &syscall.SysProcAttr{
		Setpgid:   false,
		Pdeathsig: syscall.SIGKILL, // defense-in-depth: CH dies if child dies unexpectedly
	})
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
			// API is up.
			return &managedProcess{cmd: cmd, pid: pid, stderrBuf: stderrBuf}, nil
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
