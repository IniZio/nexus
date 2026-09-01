//go:build linux

package supervisor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/IniZio/nexus3/internal/core/statedir"
)

// SpawnConfig carries the parameters for spawning a detached supervisor.
// It maps 1:1 with the flags accepted by `nexus3 __supervisor`.
type SpawnConfig struct {
	// Config is the supervisor configuration forwarded verbatim to the
	// subprocess as command-line flags.
	Config

	// Exe is the absolute path to the nexus3 binary to re-exec.
	// Defaults to os.Executable() when empty.
	Exe string

	// LogPath is the file where the supervisor's stdout/stderr are redirected.
	// When empty a temp file is created. The log is retained for diagnostics.
	LogPath string

	// ReadyTimeout is the maximum time to wait for supervisor.pid to appear.
	// Defaults to 5 minutes when zero.
	ReadyTimeout time.Duration

	// AdoptHandoffSock, when non-empty, spawns the subprocess in adopt mode
	// (nexus3 __supervisor --adopt-handoff-sock <path>) instead of boot mode.
	// See [SpawnAdoptDetached], which is the entry point that waits for the
	// adopt-mode readiness signal (the handoff socket appearing) rather than
	// for supervisor.pid — the pidfile is written much later in adopt mode,
	// only after a handoff has actually been offered and confirmed.
	AdoptHandoffSock string

	// Reacquire, when true, spawns the subprocess in RE-ACQUIRE mode
	// (nexus3 __supervisor --reacquire) instead of boot mode: it never boots
	// a VM, and instead rebuilds the perimeter for an already-running VM
	// through the surviving netns child's control socket. See
	// [RunReacquire] and [SpawnReacquireDetached].
	//
	// Mutually exclusive with AdoptHandoffSock: adopt mode receives the
	// perimeter fd from a LIVE outgoing supervisor, re-acquire mode exists
	// precisely because there is no live supervisor to receive it from.
	Reacquire bool
}

// BuildSupervisorArgv constructs the argv slice for `nexus3 __supervisor`
// from cfg. Extracted from SpawnDetached for unit-testability: callers can
// verify that a realistic SpawnConfig produces the expected flags without
// actually forking a subprocess.
func BuildSupervisorArgv(cfg SpawnConfig) []string {
	args := []string{
		HiddenSubcommand,
		"--sandbox-ref", cfg.SandboxRef,
		"--store-root", cfg.StoreRoot,
		"--state-dir", cfg.StateDir,
		"--ch-bin", cfg.CHBin,
		"--socket-dir", cfg.SocketDir,
		"--kernel", cfg.KernelPath,
		"--disk", cfg.DiskPath,
	}
	if cfg.CredsFile != "" {
		args = append(args, "--creds-file", cfg.CredsFile)
	}
	// GovBounds: pass each field only when non-zero so the supervisor's flag
	// defaults (zero = passive mode) remain correct when auto-resize is off.
	if cfg.GovBounds.MemMinBytes != 0 {
		args = append(args, "--gov-mem-min", strconv.FormatInt(cfg.GovBounds.MemMinBytes, 10))
	}
	if cfg.GovBounds.MemMaxBytes != 0 {
		args = append(args, "--gov-mem-max", strconv.FormatInt(cfg.GovBounds.MemMaxBytes, 10))
	}
	if cfg.GovBounds.VCPUMin != 0 {
		args = append(args, "--gov-vcpu-min", strconv.Itoa(int(cfg.GovBounds.VCPUMin)))
	}
	if cfg.GovBounds.VCPUMax != 0 {
		args = append(args, "--gov-vcpu-max", strconv.Itoa(int(cfg.GovBounds.VCPUMax)))
	}
	if cfg.GovBounds.DiskMaxBytes != 0 {
		args = append(args, "--gov-disk-max", strconv.FormatInt(cfg.GovBounds.DiskMaxBytes, 10))
	}
	// MemoryMiB is omitted when zero so the supervisor's default (512 MiB)
	// remains correct for callers that do not configure it explicitly.
	if cfg.MemoryMiB != 0 {
		args = append(args, "--memory", strconv.FormatUint(uint64(cfg.MemoryMiB), 10))
	}
	// BootVCPUs and workspace-disk-index are omitted when zero/false so the
	// supervisor's flag defaults (0 / no-disk) remain correct for callers that
	// do not configure them.
	if cfg.BootVCPUs != 0 {
		args = append(args, "--boot-vcpus", strconv.Itoa(int(cfg.BootVCPUs)))
	}
	// NestedVirt: forward when true; omit otherwise so the flag default (false)
	// preserves nested-OFF without flag-presence checks (D-N3N-02).
	if cfg.NestedVirt {
		args = append(args, "--nested")
	}
	if cfg.HasWorkspaceDisk {
		args = append(args, "--workspace-disk-index", strconv.Itoa(cfg.WorkspaceDiskIndex))
	}
	// WorkspaceGuestPath: forwarded when non-empty so the supervisor can seed
	// the operator's git identity into the guest (GIT-SEED, D-PD-29).
	if cfg.WorkspaceGuestPath != "" {
		args = append(args, "--workspace-guest-path", cfg.WorkspaceGuestPath)
	}
	// ExtraDisks: one --extra-disk flag per path. The supervisor re-attaches
	// them in order so ExtraDisks[i] maps to the same guest device as at
	// initial boot.
	for _, p := range cfg.ExtraDisks {
		args = append(args, "--extra-disk", p)
	}
	// ResizableDiskIndices: one --resizable-disk-index flag per 0-based index.
	// These tell the supervisor which ExtraDisks entries to hand to the disk
	// governor's DiskAxis. For builder VMs this is [2] (the buildkit cache disk
	// at ExtraDisks[2]=vdd). Omitting this field means no DiskAxis is registered
	// and the disk governor never fires — the assertion-mechanism drift that
	// caused the autogrow feature to be silently dead.
	for _, idx := range cfg.ResizableDiskIndices {
		args = append(args, "--resizable-disk-index", strconv.Itoa(idx))
	}
	// LiveMounts / VirtiofsdPath: forwarded so the supervisor re-attaches the
	// virtiofs shares on every boot. Without these the supervisor boots the VM
	// with no fs device and memory.shared=false, while the guest cmdline still
	// carries --workspace-mount=nx3fs0:...:virtiofs — the guest agent then
	// blocks forever on a mount tag that has no backing device and never
	// listens on vsock, so every exec fails with "read handshake reply: EOF".
	if cfg.VirtiofsdPath != "" {
		args = append(args, "--virtiofsd", cfg.VirtiofsdPath)
	}
	for _, lm := range cfg.LiveMounts {
		args = append(args, "--mount", EncodeLiveMount(lm))
	}
	// Cmdline: pass only when non-empty so the driver's disk-boot default
	// remains correct for callers that do not need a custom cmdline.
	if cfg.Cmdline != "" {
		args = append(args, "--cmdline", cfg.Cmdline)
	}
	// Ephemeral: forward when true; omit otherwise so the default (persistent
	// perimeter) behaviour is preserved without flag-presence checks.
	if cfg.Ephemeral {
		args = append(args, "--ephemeral")
	}
	// ParentPipeFD: forward when set so the supervisor opens the watchdog pipe.
	// Set by SpawnDetached when Ephemeral is true and a pipe was created.
	if cfg.ParentPipeFD > 0 {
		args = append(args, "--parent-pipe-fd", strconv.Itoa(cfg.ParentPipeFD))
	}
	// AdoptHandoffSock: selects adopt mode (RunAdopt) over boot mode
	// (RunDetached) in runSupervisorMain. See SpawnAdoptDetached.
	if cfg.AdoptHandoffSock != "" {
		args = append(args, "--adopt-handoff-sock", cfg.AdoptHandoffSock)
	}
	// Reacquire: selects re-acquire mode (RunReacquire) over boot mode
	// (RunDetached) in runSupervisorMain. See SpawnReacquireDetached.
	if cfg.Reacquire {
		args = append(args, "--reacquire")
	}
	return args
}

// SpawnDetached forks and detaches a supervisor process for the given sandbox.
// It returns the PID of the supervisor once supervisor.pid appears (READY).
//
// The subprocess is launched with Setsid:true so it is in a new session and
// survives the calling process's exit. It logs to LogPath (or a temp file).
//
// When cfg.Ephemeral is true, SpawnDetached creates a parent-watchdog pipe:
// the write end is returned as the second value and must be closed by the
// caller when the supervisor is no longer needed (normally via
// supervisorBuilderDriver.Stop). The read end is passed to the supervisor
// as ExtraFiles[0] (fd 3) so the supervisor can detect CLI death. For
// non-ephemeral callers the returned *os.File is always nil.
//
// The caller is responsible for stopping the supervisor via StopSupervisor
// when it is no longer needed.
func SpawnDetached(cfg SpawnConfig) (pid int, watchdog *os.File, err error) {
	// Remove any supervisor.err left by a previous run. A stale file would
	// cause a new failure to be attributed to the wrong cause (D-M4-T2).
	_ = os.Remove(filepath.Join(cfg.StateDir, supervisorErrFile))

	exe := cfg.Exe
	if exe == "" {
		exe, err = os.Executable()
		if err != nil {
			return 0, nil, fmt.Errorf("spawn supervisor: resolve executable: %w", err)
		}
	}

	readyTimeout := cfg.ReadyTimeout
	if readyTimeout == 0 {
		readyTimeout = 5 * time.Minute
	}

	// Parent-watchdog pipe (ephemeral mode only).
	// The read end is passed to the supervisor as fd 3 (ExtraFiles[0]).
	// The write end is returned to the caller; when the caller exits for any
	// reason the write end closes and the supervisor reads EOF on fd 3.
	var pipeR, pipeW *os.File
	if cfg.Ephemeral {
		pipeR, pipeW, err = os.Pipe()
		if err != nil {
			return 0, nil, fmt.Errorf("spawn supervisor: create parent-watchdog pipe: %w", err)
		}
		// Tell the supervisor which fd holds the pipe read end.
		cfg.ParentPipeFD = 3 // first ExtraFiles entry → fd 3 in child
	}

	args := BuildSupervisorArgv(cfg)

	// Set up log file for supervisor stdout/stderr.
	logPath := cfg.LogPath
	if logPath == "" {
		logPath = cfg.StateDir + "/supervisor.log"
	}
	logFile, logErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, statedir.FileMode)
	if logErr != nil {
		if pipeR != nil {
			pipeR.Close()
		}
		if pipeW != nil {
			pipeW.Close()
		}
		return 0, nil, fmt.Errorf("spawn supervisor: open log file %s: %w", logPath, logErr)
	}

	cmd := exec.Command(exe, args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if pipeR != nil {
		cmd.ExtraFiles = []*os.File{pipeR} // becomes fd 3 in supervisor
	}

	if startErr := cmd.Start(); startErr != nil {
		_ = logFile.Close()
		if pipeR != nil {
			pipeR.Close()
		}
		if pipeW != nil {
			pipeW.Close()
		}
		return 0, nil, fmt.Errorf("spawn supervisor: exec: %w", startErr)
	}
	// Close parent's copy of the read end; only the supervisor needs it.
	if pipeR != nil {
		pipeR.Close()
	}
	// Reap the detached child when it eventually exits so it does not become
	// a zombie. Stdout/Stderr are file redirections so Wait does not block.
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	_ = logFile.Close()

	spawnPid := cmd.Process.Pid
	pidfile := PidfilePath(cfg.StateDir)
	deadline := time.Now().Add(readyTimeout)

	for time.Now().Before(deadline) {
		// Check if supervisor exited before writing pidfile.
		if killErr := syscall.Kill(spawnPid, 0); killErr != nil {
			if pipeW != nil {
				pipeW.Close()
			}
			// If the supervisor wrote its failure reason to supervisor.err,
			// surface it directly so the user sees the real cause rather than
			// the generic "process exited before writing pidfile" message.
			if reason, readErr := os.ReadFile(filepath.Join(cfg.StateDir, supervisorErrFile)); readErr == nil && len(reason) > 0 {
				return 0, nil, fmt.Errorf("spawn supervisor: %s", string(reason))
			}
			// Name the log file actually in use: cfg.LogPath may redirect it
			// away from the sandbox's state dir, and pointing at a path that
			// was never written sends the reader hunting for evidence that
			// does not exist.
			return 0, nil, fmt.Errorf("spawn supervisor: process exited before writing pidfile (pid %d); see %s", spawnPid, logPath)
		}
		data, readErr := os.ReadFile(pidfile)
		if readErr == nil && len(data) > 0 {
			// Pidfile written: supervisor is ready.
			return spawnPid, pipeW, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	terminateSupervisor(spawnPid, exited, terminateSupervisorGrace)
	if pipeW != nil {
		pipeW.Close()
	}
	return 0, nil, fmt.Errorf("spawn supervisor: timed out waiting for %s (pid %d)", pidfile, spawnPid)
}

// SpawnAdoptDetached forks and detaches a supervisor process in adopt mode
// (cfg.AdoptHandoffSock must be non-empty). Unlike [SpawnDetached], it does
// NOT wait for supervisor.pid — in adopt mode that file is written only
// after a handoff has actually been offered and confirmed, which has not
// happened yet when this function is spawning the process. Instead it waits
// for cfg.AdoptHandoffSock to exist: [net.Listen] creates the socket's
// filesystem entry as soon as the adopt-mode process binds it, strictly
// before it calls Accept, so polling for the path's existence (not dialing
// it, which would consume the one Accept the caller's own handoff dial is
// waiting to fill) is a safe, non-consuming readiness signal.
//
// The caller is responsible for the actual handoff request (RequestHandoff)
// once this returns, and for terminating the spawned process if the handoff
// does not succeed — a spawned adopt-mode process that never receives an
// offer exits on its own once adoptHandoffAcceptTimeout elapses.
func SpawnAdoptDetached(cfg SpawnConfig) (pid int, err error) {
	if cfg.AdoptHandoffSock == "" {
		return 0, fmt.Errorf("spawn adopt supervisor: AdoptHandoffSock is required")
	}
	_ = os.Remove(filepath.Join(cfg.StateDir, supervisorErrFile))

	exe := cfg.Exe
	if exe == "" {
		exe, err = os.Executable()
		if err != nil {
			return 0, fmt.Errorf("spawn adopt supervisor: resolve executable: %w", err)
		}
	}

	readyTimeout := cfg.ReadyTimeout
	if readyTimeout == 0 {
		readyTimeout = 30 * time.Second
	}

	args := BuildSupervisorArgv(cfg)

	// Log to the SAME default path as a boot-mode spawn (supervisor.log), not
	// a separate supervisor-adopt.log. An adopted supervisor is a
	// continuation of the same sandbox's supervision, not a new one — an
	// operator debugging a hotswap should find supervisor.adopted and
	// supervisor.adopt.ready immediately after the outgoing side's last line
	// in ONE file, rather than discovering the outgoing log "stops dead" and
	// having to know a second, differently-named file exists. Both the
	// outgoing and incoming processes append (O_APPEND) to this path
	// concurrently for the brief window before the outgoing side exits; that
	// is safe on Linux for writes of this size.
	logPath := cfg.LogPath
	if logPath == "" {
		logPath = cfg.StateDir + "/supervisor.log"
	}
	logFile, logErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, statedir.FileMode)
	if logErr != nil {
		return 0, fmt.Errorf("spawn adopt supervisor: open log file %s: %w", logPath, logErr)
	}

	cmd := exec.Command(exe, args...)
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if startErr := cmd.Start(); startErr != nil {
		_ = logFile.Close()
		return 0, fmt.Errorf("spawn adopt supervisor: exec: %w", startErr)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	_ = logFile.Close()

	spawnPid := cmd.Process.Pid
	deadline := time.Now().Add(readyTimeout)
	for time.Now().Before(deadline) {
		if killErr := syscall.Kill(spawnPid, 0); killErr != nil {
			if reason, readErr := os.ReadFile(filepath.Join(cfg.StateDir, supervisorErrFile)); readErr == nil && len(reason) > 0 {
				return 0, fmt.Errorf("spawn adopt supervisor: %s", string(reason))
			}
			return 0, fmt.Errorf("spawn adopt supervisor: process exited before listening on handoff socket (pid %d); see %s", spawnPid, logPath)
		}
		if _, statErr := os.Stat(cfg.AdoptHandoffSock); statErr == nil {
			return spawnPid, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	terminateSupervisor(spawnPid, exited, terminateSupervisorGrace)
	return 0, fmt.Errorf("spawn adopt supervisor: timed out waiting for handoff listener at %s (pid %d)", cfg.AdoptHandoffSock, spawnPid)
}

// terminateSupervisorGrace is how long terminateSupervisor waits for a SIGTERM'd
// supervisor to tear its VM down before escalating to SIGKILL.
const terminateSupervisorGrace = 10 * time.Second

// terminateSupervisor shuts down a supervisor that never reported READY,
// escalating SIGTERM → SIGKILL.
//
// SIGKILL alone orphans the VM. Reproduced 2026-08-19 on the launch path: the
// process tree is supervisor → netns child → cloud-hypervisor, and the netns
// child is its own process-group leader (netnsChildAttr sets Setpgid with
// CLONE_NEWUSER|CLONE_NEWNET). SIGKILLing the supervisor left the netns child
// reparented to PID 1 and cloud-hypervisor still serving the VM, while the
// caller's cleanup deleted the sandbox record — a running VM with no record,
// exactly the orphan class the reaper exists to eliminate.
//
// Pdeathsig does not cover this. It is set on cloud-hypervisor relative to its
// own parent (the netns child), so it fires when the netns child dies — not
// when the supervisor above it does.
//
// Killing the process group does not cover it either: Setsid makes the
// supervisor a group leader, but the netns child starts its own group, so
// Kill(-supervisorPgid) stops at the supervisor.
//
// SIGTERM is what works, because the supervisor already knows how to tear its
// own VM down: RunDetached listens on signal.NotifyContext(SIGTERM, SIGINT)
// (supervisor.go:212) and its shutdown path calls svc.Remove/svc.Stop, which
// stops the VM through the driver. SIGKILL remains as the last resort for a
// supervisor wedged before it installed that handler — in which case the VM is
// orphaned as before, and only `nexus3 reap` can reclaim it (TBD-PD-30).
func terminateSupervisor(pid int, exited <-chan struct{}, grace time.Duration) {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return // already gone
	}
	// Wait on the reaping goroutine's channel rather than polling Kill(pid, 0):
	// a process that has exited but not yet been reaped is a zombie, and
	// Kill(pid, 0) succeeds against a zombie. Polling would therefore report a
	// cleanly-exited supervisor as still running and escalate to SIGKILL for no
	// reason. The channel closes only after Wait returns, which is unambiguous.
	select {
	case <-exited:
		return // shut down on its own; its VM went with it
	case <-time.After(grace):
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// SpawnReacquireDetached forks and detaches a supervisor in RE-ACQUIRE mode
// for a sandbox whose VM is alive but whose supervisor is dead.
//
// It is the spawn half of the crash-recovery path: recovery classifies a
// sandbox [recovery.OutcomeAdoptable] and calls this, which starts a
// long-lived [RunReacquire] process that rebuilds the perimeter through the
// surviving netns child's control socket.
//
// # The stale-pidfile hazard
//
// Readiness is the pidfile appearing, exactly as in [SpawnDetached]. But the
// supervisor this replaces was SIGKILLed, so its deferred pidfile cleanup
// never ran and a STALE pidfile is almost always still present. Left alone,
// SpawnDetached would read that stale file immediately and report the new
// process ready before it had acquired anything.
//
// So the pidfile is cleared first — but only after confirming it does not
// name a LIVE process. A live pid there means something is already
// supervising this sandbox, and spawning a second supervisor over a live one
// creates two owners for the same VM: worse than the bug being fixed. That
// case REFUSES and touches nothing.
func SpawnReacquireDetached(cfg SpawnConfig) (pid int, err error) {
	if cfg.AdoptHandoffSock != "" {
		return 0, fmt.Errorf("spawn reacquire supervisor: AdoptHandoffSock must be empty (adopt and re-acquire are mutually exclusive)")
	}
	cfg.Reacquire = true

	// Clear any CA outcome left by a PREVIOUS re-acquisition of this sandbox.
	// Without this, a second recovery would read the first run's answer and
	// attribute it to the new supervisor. Failure to clear is fatal here (unlike
	// the write side): reporting a stale outcome as this run's is precisely the
	// stale-assertion defect being fixed, so a spawner that cannot guarantee
	// freshness must not proceed to a state where the CLI trusts the file.
	if err := ClearCAOutcome(cfg.StateDir); err != nil {
		return 0, fmt.Errorf("spawn reacquire supervisor: clear stale CA outcome: %w", err)
	}

	pidfile := PidfilePath(cfg.StateDir)
	if data, readErr := os.ReadFile(pidfile); readErr == nil {
		if existing, convErr := strconv.Atoi(strings.TrimSpace(string(data))); convErr == nil && existing > 0 {
			if PidAlive(existing) {
				return 0, fmt.Errorf("spawn reacquire supervisor: pidfile %s names live pid %d; refusing to spawn a second supervisor for this sandbox", pidfile, existing)
			}
		}
		// Stale (the SIGKILLed supervisor's own pidfile): clear it so the
		// readiness poll below observes the NEW process, not the dead one.
		if rmErr := os.Remove(pidfile); rmErr != nil && !os.IsNotExist(rmErr) {
			return 0, fmt.Errorf("spawn reacquire supervisor: remove stale pidfile %s: %w", pidfile, rmErr)
		}
	}

	spawnPid, _, spawnErr := SpawnDetached(cfg)
	if spawnErr != nil {
		return 0, spawnErr
	}
	return spawnPid, nil
}
