// ch_virtiofs.go — virtiofs device config and virtiofsd process lifecycle.
//
// # Design
//
// Each named-volume LiveMount gets one virtiofsd process on the host and one
// FsConfig device in the CH vm.create payload. Tag derivation is centralised
// in VirtiofsTag — the CLI mount argument and the CH device tag MUST match;
// independent derivation causes silent mount failures at boot.
//
// virtiofsd is spawned with Setpgid:true so that kill(-pid, SIGKILL) reaches
// any child processes it may have created. The socket is removed on both
// successful teardown and on the failure path, mirroring the spawnVMM pattern.
package cloudhypervisor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// vmFsConfig is the JSON representation of a CH virtio-fs device in vm.create.
//
// Verified against cloud-hypervisor.yaml @ v52.0 schema:
// FsConfig { required: [socket, tag], properties: { socket, tag, num_queues, ... } }
//
// NumQueues defaults to 1 in CH when omitted; nexus3 uses the default (omitempty).
type vmFsConfig struct {
	Tag       string `json:"tag"`
	Socket    string `json:"socket"`
	NumQueues int    `json:"num_queues,omitempty"`
}

// VirtiofsTag returns the virtiofs mount tag for mount index idx.
//
// This is the SINGLE SOURCE OF TRUTH for tag derivation. The CH driver emits this
// tag in FsConfig; the CLI uses it to emit the matching guest mount parameter
// (e.g. "mount -t virtiofs <tag> <guestPath>"). Both sides MUST call this function —
// deriving the tag independently in either place causes a silent mismatch that fails
// at boot with no actionable error.
//
// Tag format: "nx3fs<idx>" — unique within one VM's virtio device namespace.
// Virtiofs tags are per-VM: two different VMs may both have a tag "nx3fs0" without
// conflict.
func VirtiofsTag(idx int) string {
	return fmt.Sprintf("nx3fs%d", idx)
}

// virtiofsdSockPath returns the AF_UNIX socket path for the virtiofsd process
// serving mount index idx of sandbox id.
//
// Naming convention mirrors vsockPath: "id.String()+.vfsN". The path satisfies
// the 107-byte sun_path limit for any SocketDir that passes New()'s validation
// (which reserves space based on a 35-char suffix; this suffix is ≤33 chars).
func virtiofsdSockPath(socketDir string, id domain.SandboxID, idx int) string {
	return filepath.Join(socketDir, fmt.Sprintf("%s.vfs%d", id.String(), idx))
}

// checkVirtiofsd returns nil when binaryPath is a regular file (or symlink
// resolving to one) that is executable, and an actionable error otherwise.
// Called in New() so misconfiguration surfaces before any VM is started.
func checkVirtiofsd(binaryPath string) error {
	info, err := os.Stat(binaryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf(
				"cloudhypervisor: virtiofsd binary not found at %q: install virtiofsd 1.x and set Config.VirtiofsdPath",
				binaryPath,
			)
		}
		return fmt.Errorf("cloudhypervisor: virtiofsd stat %q: %w", binaryPath, err)
	}
	if info.Mode()&0o111 == 0 {
		return fmt.Errorf("cloudhypervisor: virtiofsd %q is not executable", binaryPath)
	}
	return nil
}

// spawnVirtiofsd starts a virtiofsd process serving sharedDir on socketPath.
// It waits up to 10 s for the socket file to appear (virtiofsd creates it once
// ready to accept connections), mirroring spawnVMM's readiness polling.
//
// On success it returns a managedProcess whose kill() method sends SIGKILL to
// the whole process group. On failure (start error or timeout) it kills any
// started process and removes socketPath — no orphans are left behind.
//
// The caller must remove socketPath when the process is no longer needed
// (clearState calls virtiofsdSockPath + os.Remove for each tracked process).
func spawnVirtiofsd(ctx context.Context, binaryPath, socketPath, sharedDir string, readOnly bool) (*managedProcess, error) {
	// Remove any stale socket from a previous run so virtiofsd can bind.
	_ = os.Remove(socketPath)

	args := []string{
		"--shared-dir", sharedDir,
		"--socket-path", socketPath,
		// "none" disables virtiofsd's own sandboxing (no user-namespace pivot),
		// required when running without CAP_SYS_ADMIN. In nexus3 the sandbox
		// boundary is the VM itself; per-process isolation is not needed here.
		"--sandbox", "none",
		// Disable seccomp so virtiofsd starts in environments where the
		// necessary syscalls are not in the allow-list (e.g. nested KVM VMs).
		"--seccomp", "none",
	}
	if readOnly {
		args = append(args, "--readonly")
	}

	stderrBuf := newVMMStderrBuf(64 * 1024)
	cmd := exec.Command(binaryPath, args...)
	cmd.Stdout = nil
	cmd.Stderr = stderrBuf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	setPdeathsig(cmd.SysProcAttr)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("cloudhypervisor: virtiofsd start %s: %w", socketPath, err)
	}
	pid := cmd.Process.Pid

	cleanup := func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = os.Remove(socketPath)
	}

	const readyTimeout = 10 * time.Second
	deadline := time.Now().Add(readyTimeout)

	for {
		if ctx.Err() != nil || time.Now().After(deadline) {
			tail := stderrBuf.Tail()
			cleanup()
			if tail != "" {
				return nil, fmt.Errorf(
					"cloudhypervisor: virtiofsd socket %s not ready within %s\nvirtiofsd stderr:\n%s",
					socketPath, readyTimeout, tail,
				)
			}
			return nil, fmt.Errorf(
				"cloudhypervisor: virtiofsd socket %s not ready within %s",
				socketPath, readyTimeout,
			)
		}
		// Liveness must be checked BEFORE the socket, and the socket check must
		// not be trusted on its own. virtiofsd creates the socket file and only
		// then begins serving, so a virtiofsd that dies immediately after
		// binding leaves the socket behind. Treating "socket exists" as
		// readiness therefore reports success for a dead backend, and CH goes on
		// to attach a vhost-user-fs device whose backend is gone — the guest
		// then hangs probing the device, with no error anywhere on the host.
		if exited, state := processExited(pid); exited {
			tail := stderrBuf.Tail()
			cleanup()
			if tail != "" {
				return nil, fmt.Errorf(
					"cloudhypervisor: virtiofsd for %s exited during startup (state %s)\nvirtiofsd stderr:\n%s",
					sharedDir, state, tail,
				)
			}
			return nil, fmt.Errorf(
				"cloudhypervisor: virtiofsd for %s exited during startup (state %s) with no stderr output",
				sharedDir, state,
			)
		}
		if _, statErr := os.Stat(socketPath); statErr == nil {
			// Socket file created and the process is still alive.
			return &managedProcess{cmd: cmd, pid: pid, stderrBuf: stderrBuf}, nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// spawnVirtiofsdForMounts spawns one virtiofsd per LiveMount in d.cfg.LiveMounts,
// registering each *managedProcess in d.virtiofsdProcs[id] immediately after a
// successful spawn so that clearState (via cleanup() in Start) can kill it even
// if a subsequent mount's spawn fails.
//
// Returns the assembled []vmFsConfig for inclusion in VMCreateWithNet.
// On failure the partially-spawned set is already registered; the caller's
// cleanup() → clearState path handles teardown.
//
// Called from Start, after the CH API is responsive and before vm.create —
// virtiofsd sockets must be ready before CH tries to connect to them.
func (d *CHDriver) spawnVirtiofsdForMounts(ctx context.Context, id domain.SandboxID) ([]vmFsConfig, error) {
	mounts := d.cfg.LiveMounts
	if len(mounts) == 0 {
		return nil, nil
	}
	if d.cfg.VirtiofsdPath == "" {
		return nil, fmt.Errorf(
			"cloudhypervisor: %d live mount(s) configured but Config.VirtiofsdPath is empty: set VirtiofsdPath to the virtiofsd binary",
			len(mounts),
		)
	}

	fsCfgs := make([]vmFsConfig, 0, len(mounts))
	for i, lm := range mounts {
		sockPath := virtiofsdSockPath(d.cfg.SocketDir, id, i)
		vp, err := spawnVirtiofsd(ctx, d.cfg.VirtiofsdPath, sockPath, lm.HostPath, lm.ReadOnly)
		if err != nil {
			return nil, fmt.Errorf("cloudhypervisor: virtiofsd[%d] for %s: %w", i, lm.HostPath, err)
		}
		// Register immediately: if the next iteration fails, clearState kills this proc.
		d.mu.Lock()
		d.virtiofsdProcs[id] = append(d.virtiofsdProcs[id], vp)
		d.mu.Unlock()

		fsCfgs = append(fsCfgs, vmFsConfig{
			Tag:    VirtiofsTag(i),
			Socket: sockPath,
		})
	}
	return fsCfgs, nil
}

// processExited reports whether pid has exited or become a zombie, along with
// the raw /proc state character for diagnostics. It deliberately does NOT call
// cmd.Wait: managedProcess.kill owns the single Wait that reaps the child, and
// a second concurrent Wait would race it. A zombie counts as exited — the
// process is dead and only the exit status remains uncollected.
//
// On a kernel without /proc, or if the stat file cannot be parsed, this returns
// false so an unreadable /proc degrades to the previous socket-only behaviour
// rather than failing every spawn.
func processExited(pid int) (bool, string) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		// No such process: it exited and was already reaped.
		if os.IsNotExist(err) {
			return true, "gone"
		}
		return false, ""
	}
	// Field 3 is the state character. comm (field 2) is parenthesised and may
	// itself contain spaces, so scan from the LAST ')' rather than splitting.
	close := bytes.LastIndexByte(raw, ')')
	if close < 0 || close+2 >= len(raw) {
		return false, ""
	}
	fields := bytes.Fields(raw[close+1:])
	if len(fields) == 0 {
		return false, ""
	}
	state := string(fields[0])
	return state == "Z" || state == "X" || state == "x", state
}
