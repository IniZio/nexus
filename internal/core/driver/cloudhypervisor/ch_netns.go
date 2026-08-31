// ch_netns.go — "netns-runtime" mechanism: run the CH VMM + TAP/bridge +
// frame pump inside a per-sandbox rootless user+network namespace so filtered
// egress needs zero host CAP_NET_ADMIN.
//
// # Design
//
// Parent (host process, in package cloudhypervisor):
//  1. Creates an AF_UNIX SOCK_DGRAM socketpair, keeping BOTH ends as *os.File
//     (perimFile = host/perimeter-facing, pumpFile = to inherit into child).
//  2. Re-execs the current binary with SysProcAttr{CLONE_NEWUSER|CLONE_NEWNET,
//     uid/gid maps, GidMappingsEnableSetgroups=false}. pumpFile is passed via
//     cmd.ExtraFiles; all config goes via env vars.
//  3. Closes its copy of pumpFile after spawn. Wraps perimFile as net.Conn
//     via net.FileConn for reading guest Ethernet frames.
//
// Child (re-exec'd, inside user+network namespace):
//  4. Detects NEXUS3_NETNS_RUN=1. Has effective CAP_NET_ADMIN in-ns (uid 0).
//  5. Calls createTapBridge → openHostTap → spawnVMM (CH inherits netns) →
//     tapPump(hostTapFile, pumpConn).
//
// The CH API socket lives under /tmp which is in the shared mount namespace, so
// the parent can reach it from the host netns without any special handoff.
//
// # Dispatch without editing main.go (S0)
//
// The child entry (RunNetnsChild) is exported so the TEST BINARY acts as the
// re-exec image: TestMain in ch_netns_test.go checks the sentinel and calls
// RunNetnsChild. S1 wires the same sentinel into cmd/nexus3/main.go.
//
// S1: wire this sentinel dispatch into cmd/nexus3/main.go
package cloudhypervisor

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// Sentinel and env-var names for the netns-runtime re-exec protocol.
const (
	// NetnsRunEnv is the sentinel environment variable. When set to "1", the
	// process is a re-exec'd child running inside a user+network namespace.
	//
	// S1: wire this sentinel dispatch into cmd/nexus3/main.go
	NetnsRunEnv = "NEXUS3_NETNS_RUN"

	netnsEnvPumpFD        = "NEXUS3_NETNS_PUMP_FD"
	netnsEnvGuestTap      = "NEXUS3_NETNS_GUEST_TAP"
	netnsEnvHostTap       = "NEXUS3_NETNS_HOST_TAP"
	netnsEnvBridge        = "NEXUS3_NETNS_BRIDGE"
	netnsEnvAPISocket     = "NEXUS3_NETNS_API_SOCKET"
	netnsEnvCHBin         = "NEXUS3_NETNS_CH_BIN"
	netnsEnvStartTimeoutMS = "NEXUS3_NETNS_START_TIMEOUT_MS"

	// NetnsEnvGuestTap and NetnsEnvAPISocket are exported aliases of the
	// unexported env-var names above, for ticket 11's netns identity
	// backfill (internal/supervisor/netns_backfill.go). The backfill reads
	// these two vars — plus NetnsRunEnv — from a live candidate child's
	// /proc/<pid>/environ, verbatim from the same env StartNetnsRuntime set
	// at spawn time (ch_netns.go:217-232), rather than inferring them from
	// the process tree's shape.
	NetnsEnvGuestTap  = netnsEnvGuestTap
	NetnsEnvAPISocket = netnsEnvAPISocket

	// netnsEnvRestoreURL carries the "file://<dir>" URL the child should pass
	// to vm.restore after spawning CH. When absent (empty), the child runs in
	// boot mode: it just spawns CH and pumps frames; the parent issues
	// vm.create + vm.boot over the shared API socket. When set (restore mode),
	// the child issues vm.restore before starting the frame pump, so the VM is
	// Running by the time tapPump blocks.
	netnsEnvRestoreURL = "NEXUS3_NETNS_RESTORE_URL"
)

// NetnsRuntime is the parent-side handle to a running netns-runtime child.
// The child hosts the CH VMM, TAP/bridge topology, and frame pump inside an
// isolated user+network namespace. The parent communicates with CH via the
// shared API socket and receives guest Ethernet frames via PerimConn.
type NetnsRuntime struct {
	// PerimConn is the perimeter-facing (parent-side) end of the AF_UNIX
	// SOCK_DGRAM socketpair. Each Read returns exactly one raw Ethernet frame
	// from the guest NIC. Write injects frames toward the guest.
	PerimConn net.Conn

	// APISocket is the path to the CH REST API Unix socket. The caller must
	// poll it (e.g. via newClient.Ping) until CH is ready, then call
	// vm.create and vm.boot before reading frames from PerimConn.
	APISocket string

	// GuestTap is the guest-side TAP interface name to include in vm.create.
	GuestTap string

	// ChildPID is the OS pid of the netns child process (the re-exec'd
	// binary running inside the isolated user+network namespace). Exported
	// so the caller (the per-sandbox supervisor) can persist it onto
	// domain.Sandbox.NetnsChildPID for a future process to adopt via
	// [AdoptNetnsRuntime].
	ChildPID int

	// ChildPGID is the process group ID of the netns child. Because
	// netnsChildAttr sets Setpgid:true, pgid == child.pid — so at creation
	// time ChildPGID == ChildPID. CH runs with Setpgid:false
	// (spawnVMMInGroup) so it inherits this pgid. Stop() sends
	// Kill(-ChildPGID, SIGKILL) to reach both the child and CH in one call.
	// Exported for the same persistence reason as ChildPID (see
	// domain.Sandbox.NetnsChildPGID).
	ChildPGID int

	// ChildStartTime is the kernel's starttime for the netns child process
	// (field 22 of /proc/<ChildPID>/stat, clock ticks since boot). It is
	// populated by StartNetnsRuntime immediately after the child is spawned
	// and must be persisted alongside ChildPID/ChildPGID so that
	// AdoptNetnsRuntime can verify the pid has not been recycled.
	//
	// NOTE: domain.Sandbox needs a NetnsChildStartTime uint64 field to carry
	// this value between supervisor instances. Until that field exists there is
	// no way to adopt at all: AdoptNetnsRuntime REFUSES a zero starttime rather
	// than proceeding unguarded, so whichever change wires the record
	// persistence for NetnsChildPID/NetnsChildPGID must write this field too.
	ChildStartTime uint64

	// cmd is the child process running inside the user+network namespace.
	// nil when this NetnsRuntime was built by [AdoptNetnsRuntime] rather
	// than [StartNetnsRuntime] — this process did not fork the child, so it
	// cannot cmd.Wait() on it. Stop() branches on this to choose its
	// confirmation strategy; see the non-parent path there.
	cmd *exec.Cmd

	// stderrBuf captures the child's stderr for diagnostics.
	stderrBuf *vmmStderrBuf

	// stopOnce ensures Stop() is idempotent.
	stopOnce sync.Once
}

// netnsSocketpairFiles creates an AF_UNIX SOCK_DGRAM socketpair and returns
// both ends as *os.File so one can be passed via cmd.ExtraFiles.
//
// Unlike unixgramPair (which wraps via net.FileConn and closes the raw fds),
// this function returns the raw os.File wrappers. The caller is responsible
// for closing both files (or transferring one to ExtraFiles).
func netnsSocketpairFiles() (perimFile, pumpFile *os.File, err error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("netns socketpair(AF_UNIX, SOCK_DGRAM): %w", err)
	}
	return os.NewFile(uintptr(fds[0]), "netns-perim"),
		os.NewFile(uintptr(fds[1]), "netns-pump"),
		nil
}

// StartNetnsRuntime re-execs the current binary inside a new user+network
// namespace to host the CH VMM, TAP/bridge topology, and frame pump.
//
// restoreURL selects the child's operating mode:
//   - "" (empty, boot mode): child spawns CH and pumps frames; the caller must
//     poll rt.APISocket until CH is ready, then call VMCreateWithNet + VMBoot.
//   - "file://<dir>" (restore mode): child spawns CH, issues vm.restore from
//     restoreURL, then pumps frames; the caller polls for Running state
//     (VMInfo returns Running) rather than issuing vm.create + vm.boot.
//
// socketPath is the path to the CH REST API socket that the child will pass
// to spawnVMM (--api-socket). It must be accessible from both the child
// (create) and the parent (connect); /tmp satisfies this because only the
// mount namespace is shared.
func StartNetnsRuntime(ctx context.Context, cfg Config, id domain.SandboxID, socketPath, restoreURL string) (*NetnsRuntime, error) {
	guestTap, hostTap, bridge := tapIfNames(id)

	// Create the socketpair as raw *os.File for ExtraFiles handoff.
	perimFile, pumpFile, err := netnsSocketpairFiles()
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: StartNetnsRuntime: %w", err)
	}

	// Resolve the binary path for re-exec.
	self, err := os.Executable()
	if err != nil {
		perimFile.Close()
		pumpFile.Close()
		return nil, fmt.Errorf("cloudhypervisor: StartNetnsRuntime: os.Executable: %w", err)
	}

	startTimeoutMS := int64(cfg.StartTimeout / time.Millisecond)
	if startTimeoutMS <= 0 {
		startTimeoutMS = 15_000
	}

	// pumpFile is ExtraFiles[0] → fd 3 in the child process.
	// (fd 0/1/2 are stdin/stdout/stderr; ExtraFiles[0] is the next available fd.)
	const pumpFDInChild = 3

	// Inherit PATH so the child can locate system tools (ip, etc.) used by
	// createTapBridge and other helpers. Deliberately omit all other env vars
	// to keep the child's environment minimal and auditable.
	pathEnv := "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	if p := os.Getenv("PATH"); p != "" {
		pathEnv = "PATH=" + p
	}

	// Use exec.Command (NOT exec.CommandContext) so the child's lifetime is
	// controlled solely by rt.Stop() — not by the caller's context.
	//
	// exec.CommandContext installs a goroutine that sends SIGKILL to the child
	// process whenever ctx is cancelled or times out. This is dangerous here:
	// callers often pass a short-lived "start" context (e.g. a 120 s boot
	// timeout), cancel it after drv.Start returns, and then keep the VM
	// running for minutes. If exec.CommandContext were used, that cancellation
	// would kill the netns child — and with it CH, the vsock socket, and the
	// frame pump — mid-exec, producing:
	//
	//   "agent: pump: read frame: EOF"                     (vsock dies)
	//   "cannot receive packets from @...: i/o timeout"   (TAP conn dead)
	//
	// The ctx here is used only to detect cancellation that happened BEFORE or
	// DURING cmd.Start(); see the explicit ctx.Err() check below.
	cmd := exec.Command(self)
	cmd.Env = []string{
		NetnsRunEnv + "=1",
		fmt.Sprintf("%s=%d", netnsEnvPumpFD, pumpFDInChild),
		fmt.Sprintf("%s=%s", netnsEnvGuestTap, guestTap),
		fmt.Sprintf("%s=%s", netnsEnvHostTap, hostTap),
		fmt.Sprintf("%s=%s", netnsEnvBridge, bridge),
		fmt.Sprintf("%s=%s", netnsEnvAPISocket, socketPath),
		fmt.Sprintf("%s=%s", netnsEnvCHBin, cfg.BinaryPath),
		fmt.Sprintf("%s=%d", netnsEnvStartTimeoutMS, startTimeoutMS),
		pathEnv,
	}
	// Restore mode: pass the snapshot URL so RunNetnsChild issues vm.restore
	// after spawning CH instead of waiting for the parent to call vm.create+boot.
	if restoreURL != "" {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", netnsEnvRestoreURL, restoreURL))
	}
	cmd.SysProcAttr = netnsChildAttr()
	// ExtraFiles[0] becomes fd 3 in the child (after stdin/stdout/stderr).
	cmd.ExtraFiles = []*os.File{pumpFile}

	// Capture child stderr for diagnostics (bounded ring).
	stderrBuf := newVMMStderrBuf(64 * 1024)
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		perimFile.Close()
		pumpFile.Close()
		return nil, fmt.Errorf("cloudhypervisor: StartNetnsRuntime: spawn child: %w", err)
	}

	// Record the child's pid as the process group leader id.
	// netnsChildAttr sets Setpgid:true so pgid == child.pid.
	childPgid := cmd.Process.Pid

	// One-shot cancellation check: if the caller's ctx was already cancelled
	// or expired by the time cmd.Start() returned, kill the child immediately
	// and surface the context error. This handles the narrow window between
	// the caller's deadline expiry and our cmd.Start() call.
	// (Ongoing lifetime management is intentionally NOT done here — see the
	// exec.Command comment above for why we avoid exec.CommandContext.)
	if err := ctx.Err(); err != nil {
		_ = syscall.Kill(-childPgid, syscall.SIGKILL)
		_ = cmd.Wait()
		perimFile.Close()
		pumpFile.Close()
		return nil, fmt.Errorf("cloudhypervisor: StartNetnsRuntime: %w", err)
	}

	// Parent closes pumpFile (child has it); keeps perimFile.
	// Explicit fd-handoff ordering: PARENT closes pumpFile here; CHILD never
	// receives perimFile — so teardown cannot hang on pump EOF.
	pumpFile.Close()

	// Wrap perimFile as net.Conn. net.FileConn dups the fd internally; close
	// the os.File wrapper after wrapping.
	perimConn, err := net.FileConn(perimFile)
	perimFile.Close()
	if err != nil {
		_ = syscall.Kill(-childPgid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, fmt.Errorf("cloudhypervisor: StartNetnsRuntime: net.FileConn(perim): %w", err)
	}

	// Capture the child's starttime from /proc for use by AdoptNetnsRuntime on
	// the next supervisor start. A failure here is not fatal to THIS boot — the
	// VM is up and this supervisor is the child's parent, so it can Stop() via
	// cmd.Wait() without any identity token. It only costs the NEXT supervisor
	// the ability to adopt: AdoptNetnsRuntime refuses a zero starttime rather
	// than killing a possibly-recycled group. Log it so that later refusal is
	// traceable to its cause instead of looking like corruption.
	childStartTime, stErr := readProcStartTime(childPgid)
	if stErr != nil {
		slog.Warn("cloudhypervisor: could not read netns child starttime; this VM will not be adoptable by a replacement supervisor",
			"pid", childPgid, "err", stErr)
	}

	return &NetnsRuntime{
		PerimConn:      perimConn,
		APISocket:      socketPath,
		GuestTap:       guestTap,
		ChildPID:       childPgid,
		ChildPGID:      childPgid,
		ChildStartTime: childStartTime,
		cmd:            cmd,
		stderrBuf:      stderrBuf,
	}, nil
}

// readProcStartTime reads field 22 (starttime) from /proc/<pid>/stat and
// returns it as a uint64. The starttime is the number of clock ticks since
// boot at which the process was created; the kernel never reuses it for a
// recycled pid, making it a reliable identity token alongside the pid.
//
// Parsing is robust against a comm field (field 2) containing spaces or
// parentheses: the suffix after the LAST ')' in the line contains the
// remaining fields starting at field 3 (state). Field 22 (starttime) is at
// 0-indexed position 19 in that suffix.
func readProcStartTime(pid int) (uint64, error) {
	st, err := ReadProcStat(pid)
	if err != nil {
		return 0, err
	}
	return st.StartTime, nil
}

// ProcStat holds the /proc/<pid>/stat fields needed to identify and adopt a
// netns child: PPID (field 4), PGID (field 5), and StartTime (field 22).
// Exported for reuse by internal/supervisor's netns identity backfill
// (ticket 11), which must settle a candidate pid's pgid and starttime from
// ONE /proc read taken after it has picked that pid — not from two separate
// reads that could race a pid-reuse window between them.
type ProcStat struct {
	PPID      int
	PGID      int
	StartTime uint64
}

// ReadProcStat reads and parses /proc/<pid>/stat.
//
// Parsing is robust against a comm field (field 2) containing spaces or
// parentheses: the suffix after the LAST ')' in the line contains the
// remaining fields starting at field 3 (state). Field 4 (ppid), field 5
// (pgid), and field 22 (starttime) are at 0-indexed positions 1, 2, and 19
// in that suffix, respectively.
func ReadProcStat(pid int) (ProcStat, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ProcStat{}, fmt.Errorf("read /proc/%d/stat: %w", pid, err)
	}
	line := strings.TrimRight(string(data), "\n")
	idx := strings.LastIndex(line, ")")
	if idx < 0 {
		return ProcStat{}, fmt.Errorf("parse /proc/%d/stat: no ')' found in %q", pid, line)
	}
	// fields[0] = state (field 3), fields[1] = ppid (field 4),
	// fields[2] = pgid (field 5), fields[19] = starttime (field 22).
	fields := strings.Fields(line[idx+1:])
	if len(fields) < 20 {
		return ProcStat{}, fmt.Errorf("parse /proc/%d/stat: too few fields after ')': got %d, want ≥20", pid, len(fields))
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return ProcStat{}, fmt.Errorf("parse /proc/%d/stat: ppid: %w", pid, err)
	}
	pgid, err := strconv.Atoi(fields[2])
	if err != nil {
		return ProcStat{}, fmt.Errorf("parse /proc/%d/stat: pgid: %w", pid, err)
	}
	st, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return ProcStat{}, fmt.Errorf("parse /proc/%d/stat: starttime: %w", pid, err)
	}
	return ProcStat{PPID: ppid, PGID: pgid, StartTime: st}, nil
}

// AdoptNetnsRuntime rebuilds a NetnsRuntime for a netns child this process
// did NOT fork, from state persisted by the process that did — the four
// values [StartNetnsRuntime] captures on domain.Sandbox (NetnsChildPID,
// NetnsChildPGID, GuestTapName, CHAPISocket) — plus the perimeter fd
// transferred over the handoff transport.
//
// perimFile is the *os.File returned by internal/supervisor/handoff.Accept
// when Payload.Perimeter.Present is true. AdoptNetnsRuntime takes ownership
// of it: on success it is wrapped (and closed) via net.FileConn; on failure
// the caller retains ownership and must close it itself (AdoptNetnsRuntime
// does not close it on the error paths, mirroring Accept's own contract that
// a rejected fd is the caller's to dispose of).
//
// childStartTime is field 22 of /proc/<childPID>/stat (clock ticks since
// boot) as persisted by the previous supervisor via NetnsRuntime.ChildStartTime.
// AdoptNetnsRuntime reads the live value from /proc and refuses the adoption if
// the pid's identity has changed — protecting against pid reuse where Stop()
// would otherwise send Kill(-ChildPGID, SIGKILL) to an arbitrary host process
// group. It is REQUIRED: zero is refused, not treated as "skip the check". See
// the ChildStartTime comment on NetnsRuntime for the pending domain.Sandbox
// field note.
//
// The returned NetnsRuntime has cmd == nil: it was not produced by
// exec.Command in this process, so Stop() cannot cmd.Wait() on it and takes
// the non-parent confirmation path instead (see Stop).
func AdoptNetnsRuntime(childPID, childPGID int, childStartTime uint64, guestTap, apiSocket string, perimFile *os.File) (*NetnsRuntime, error) {
	if perimFile == nil {
		return nil, fmt.Errorf("cloudhypervisor: AdoptNetnsRuntime: perimFile is nil")
	}
	if childPID <= 0 {
		return nil, fmt.Errorf("cloudhypervisor: AdoptNetnsRuntime: childPID=%d must be positive", childPID)
	}
	if childPGID <= 0 {
		return nil, fmt.Errorf("cloudhypervisor: AdoptNetnsRuntime: childPGID=%d must be positive", childPGID)
	}
	if apiSocket == "" {
		return nil, fmt.Errorf("cloudhypervisor: AdoptNetnsRuntime: apiSocket is empty")
	}
	if guestTap == "" {
		return nil, fmt.Errorf("cloudhypervisor: AdoptNetnsRuntime: guestTap is empty")
	}

	// PID-reuse guard: verify the process at childPID is still the same process
	// whose starttime was persisted. This gate is FAIL-CLOSED — a missing
	// starttime refuses the adoption rather than proceeding unguarded.
	//
	// An earlier revision skipped the check when childStartTime was 0, for
	// backward compatibility with records predating the field. This branch
	// (nexus3-hotswap-04) wires service.Start to write the adoption identity
	// onto domain.Sandbox, and recover.go to clear it on substrate loss — so
	// a zero here means the identity was lost or never set, which is exactly
	// when Stop()'s Kill(-ChildPGID, SIGKILL) is most likely to hit a
	// recycled group. Fail open on the signal that decides whether to send
	// SIGKILL and the guard is decorative.
	if childStartTime == 0 {
		return nil, fmt.Errorf("cloudhypervisor: AdoptNetnsRuntime: childStartTime is 0 for pid %d; refusing to adopt without a pid-reuse guard", childPID)
	}
	currentST, err := readProcStartTime(childPID)
	if err != nil {
		// /proc/<pid>/stat absent: the process died (or was never this pid).
		return nil, fmt.Errorf("cloudhypervisor: AdoptNetnsRuntime: pid %d no longer exists (died or pid recycled): %w", childPID, err)
	}
	if currentST != childStartTime {
		return nil, fmt.Errorf("cloudhypervisor: AdoptNetnsRuntime: pid %d starttime mismatch: persisted=%d current=%d — pid was recycled", childPID, childStartTime, currentST)
	}

	// net.FileConn dups the fd internally; close our wrapper once dup'd,
	// same ordering StartNetnsRuntime uses for perimFile.
	perimConn, err := net.FileConn(perimFile)
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: AdoptNetnsRuntime: net.FileConn(perim): %w", err)
	}
	perimFile.Close()

	return &NetnsRuntime{
		PerimConn:      perimConn,
		APISocket:      apiSocket,
		GuestTap:       guestTap,
		ChildPID:       childPID,
		ChildPGID:      childPGID,
		ChildStartTime: childStartTime,
		// cmd is deliberately left nil: this process did not fork the
		// child, so it has no *exec.Cmd to Wait() on.
	}, nil
}

// ChildStderr returns the buffered child stderr output as a string.
// Useful for diagnostics when the child fails to start CH.
func (rt *NetnsRuntime) ChildStderr() string {
	if rt.stderrBuf == nil {
		return ""
	}
	return rt.stderrBuf.Tail()
}

// Stop kills the child process group (child + CH grandchild), confirms the
// group is gone, and closes PerimConn. Idempotent.
//
// Kill(-ChildPGID, SIGKILL) sends SIGKILL to every process in the group:
//   - the netns child itself
//   - CH (spawned with Setpgid:false via spawnVMMInGroup, so it inherits the
//     child's pgid rather than starting its own group)
//
// This is reliable and explicit, unlike Pdeathsig alone (which can be lost
// when Go retires OS threads). Pdeathsig remains in spawnVMMInGroup as
// defense-in-depth (CH dies if the child dies before Stop is called).
//
// # Parent vs. non-parent confirmation (TBD-3)
//
// Sending the kill is not enough: the caller needs to know the VM is
// actually gone before it can safely reuse the sandbox's resources (socket
// path, TAP names, cache-disk slot). A NetnsRuntime built by
// [StartNetnsRuntime] can cmd.Wait() to get that confirmation, because this
// process is cmd's parent. A NetnsRuntime built by [AdoptNetnsRuntime]
// (cmd == nil) is not the child's parent — wait(2) only works on your own
// children, so cmd.Wait() is not available here, and the two remaining ways
// to confirm death are:
//
//   - pidfd_open(2) on ChildPID, then poll/wait on the pidfd for exit. This
//     is race-free against pid reuse and doesn't require being the parent —
//     but it only confirms ChildPID itself has exited, not the process
//     group. This runtime never persists CH's own pid (only the netns
//     child's PID/PGID and CH's API socket path are in domain.Sandbox), so a
//     pidfd on ChildPID alone cannot attest that CH — a separate process in
//     the same group — is also gone. Confirming the child dead while CH
//     lingers is exactly the orphan this mechanism exists to prevent.
//   - Poll the process group via kill(-ChildPGID, 0) until it returns ESRCH.
//     ESRCH on a pgid means literally nobody is left in that group — it
//     covers the netns child, CH, and any future group member, with no need
//     to track individual pids. It also reuses the exact mechanism the
//     parent-owned path already uses to signal the group, so both Stop
//     paths reason about the same unit (the pgid), not two different units.
//
// This chose the pgid-poll: it is the only one of the two that answers the
// question Stop actually needs answered ("is the whole group gone"), not
// just "is this one pid gone". The cost is a bounded busy-poll instead of a
// blocking wait; netnsAdoptStopTimeout caps it so a group that never fully
// reaps (e.g. no subreaper present) cannot hang Stop forever — the same
// bounded-return contract TestLifecycle_StopBounded already holds the
// parent-owned path to.
func (rt *NetnsRuntime) Stop() {
	rt.stopOnce.Do(func() {
		if rt.ChildPGID != 0 {
			_ = syscall.Kill(-rt.ChildPGID, syscall.SIGKILL)
		}
		if rt.cmd != nil {
			_ = rt.cmd.Wait()
		} else if rt.ChildPGID != 0 {
			// FIX-2: propagate the group-exit result. Changing Stop's return
			// type would require updating all callers (t.Cleanup, goroutines,
			// ch_net.go teardownSandboxNet) which span multiple slices; instead
			// we log at warn so operators can detect a group that failed to
			// reap within the timeout — which means TAP names, the socket path,
			// and cache-disk slot may be transiently unavailable.
			if !waitForGroupExit(rt.ChildPGID, netnsAdoptStopTimeout) {
				slog.Warn("cloudhypervisor: Stop: process group did not confirm exit within timeout; "+
					"socket, TAP, and cache-disk slot may be transiently unavailable",
					"pgid", rt.ChildPGID,
					"timeout", netnsAdoptStopTimeout)
			}
		}
		if rt.PerimConn != nil {
			_ = rt.PerimConn.Close()
		}
	})
}

// netnsAdoptStopTimeout bounds the non-parent confirmation poll in Stop.
// Reaping an orphaned group is normally near-instant (the kernel's nearest
// subreaper, or init, reaps as soon as the kill is delivered) — this is a
// safety net against a group that never gets reaped, not the expected path.
const netnsAdoptStopTimeout = 5 * time.Second

// netnsGroupExitPollInterval is the sleep between waitForGroupExit's
// kill(-pgid, 0) probes.
const netnsGroupExitPollInterval = 20 * time.Millisecond

// waitForGroupExit polls kill(-pgid, 0) until it returns ESRCH (no process
// in the group remains — including zombies still awaiting reap, which are
// still valid kill(2) targets and so keep this loop from returning early) or
// timeout elapses. Returns true iff ESRCH was observed before the deadline.
func waitForGroupExit(pgid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if err := syscall.Kill(-pgid, 0); errors.Is(err, syscall.ESRCH) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(netnsGroupExitPollInterval)
	}
}

// RunNetnsChild is the exported child-side entry point for the netns-runtime.
// It is invoked when the re-exec'd process detects NetnsRunEnv=1. It runs
// entirely inside the new user+network namespace with effective CAP_NET_ADMIN.
//
// Sequence: createTapBridge → openHostTap → spawnVMM → tapPump (blocks).
// The process exits when tapPump returns (both fds closed by parent teardown).
//
// S1: wire this sentinel dispatch into cmd/nexus3/main.go
func RunNetnsChild() {
	pumpFD, err := strconv.Atoi(os.Getenv(netnsEnvPumpFD))
	if err != nil {
		fmt.Fprintf(os.Stderr, "netns child: parse %s: %v\n", netnsEnvPumpFD, err)
		os.Exit(1)
	}
	guestTap := os.Getenv(netnsEnvGuestTap)
	hostTap := os.Getenv(netnsEnvHostTap)
	bridge := os.Getenv(netnsEnvBridge)
	socketPath := os.Getenv(netnsEnvAPISocket)
	chBin := os.Getenv(netnsEnvCHBin)

	timeoutMS, _ := strconv.ParseInt(os.Getenv(netnsEnvStartTimeoutMS), 10, 64)
	if timeoutMS <= 0 {
		timeoutMS = 15_000
	}

	// Wrap the inherited pump fd as a net.Conn.
	// net.FileConn dups the fd; close the os.File wrapper after wrapping so
	// the original fd is not leaked past this point.
	pumpFile := os.NewFile(uintptr(pumpFD), "netns-pump")
	pumpConn, err := net.FileConn(pumpFile)
	pumpFile.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "netns child: net.FileConn(pump fd=%d): %v\n", pumpFD, err)
		os.Exit(1)
	}
	defer pumpConn.Close()

	// Set up the TAP/bridge topology inside the netns.
	// createTapBridge calls applySandboxNetSysctls (LEAK-TIGHT).
	if err := createTapBridge(guestTap, hostTap, bridge); err != nil {
		fmt.Fprintf(os.Stderr, "netns child: createTapBridge: %v\n", err)
		os.Exit(1)
	}
	// deleteTapBridge is intentionally NOT deferred here: when the child exits
	// (process death), the kernel destroys all interfaces in the netns
	// automatically; no explicit cleanup is needed or possible after exit.

	hostTapFile, err := openHostTap(hostTap)
	if err != nil {
		fmt.Fprintf(os.Stderr, "netns child: openHostTap(%s): %v\n", hostTap, err)
		os.Exit(1)
	}
	defer hostTapFile.Close()

	// Spawn CH inside the netns (CH inherits the child's netns).
	// spawnVMMInGroup sets Setpgid:false so CH inherits this child's process
	// group (pgid == child.pid, set by netnsChildAttr Setpgid:true). The
	// parent's rt.Stop() then sends Kill(-childPgid, SIGKILL) to reach both.
	cfg := Config{
		BinaryPath:   chBin,
		StartTimeout: time.Duration(timeoutMS) * time.Millisecond,
	}
	ctx := context.Background()
	proc, err := spawnVMMInGroup(ctx, cfg, socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "netns child: spawnVMMInGroup: %v\n", err)
		os.Exit(1)
	}
	// Restore mode: if netnsEnvRestoreURL is set, issue vm.restore now so the
	// VM reaches Running state before we start pumping frames. Boot mode (empty):
	// the parent issues vm.create + vm.boot over the shared API socket; no action
	// here. The normal boot path is byte-for-byte unchanged.
	if restoreURL := os.Getenv(netnsEnvRestoreURL); restoreURL != "" {
		chc := newClient(socketPath)
		if err := chc.VMRestore(ctx, restoreURL); err != nil {
			fmt.Fprintf(os.Stderr, "netns child: vm.restore %s: %v\n", restoreURL, err)
			os.Exit(1)
		}
		// vm.restore brings the VM to Paused state; vm.resume transitions it
		// to Running. Without this call the parent's VMInfo poll would never
		// see Running and would time out after StartTimeout.
		if err := chc.VMResume(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "netns child: vm.resume: %v\n", err)
			os.Exit(1)
		}
	}
	// Reap CH and exit this process when CH dies.
	//
	// Without this, a CH crash or kill leaves a zombie grandchild and this
	// process stuck in tapPump forever — an orphaned launcher with ppid 1 and
	// a Z-state child.
	//
	// pumpConn.Close() alone is insufficient: tapPump has two goroutines and
	// waits for BOTH to exit. Closing pumpConn unblocks the conn→TAP goroutine
	// (net.Conn read returns an error). But the TAP→conn goroutine reads from
	// hostTapFile which was opened with O_RDWR (no O_NONBLOCK) — a blocking fd
	// that Go's netpoller does not manage. Closing hostTapFile from this
	// goroutine does not interrupt the blocking read() in the TAP→conn
	// goroutine, so tapPump never returns regardless.
	//
	// os.Exit(0) is the correct termination path: this process's sole purpose
	// was to host CH and pump frames; once CH exits there is nothing left to do.
	// All goroutines — including the stuck TAP read — are torn down by the
	// process exit, and the kernel closes every fd.
	//
	// When rt.Stop() kills the whole process group first: this goroutine is
	// killed with this process; CH is also killed by the group signal; CH's
	// zombie (if any) is reparented to init which reaps it — no leak there.
	go func() {
		// Wait for CH to exit and reap its zombie.
		//
		// We use syscall.Wait4 directly instead of proc.cmd.Wait() because
		// cmd.Wait() blocks in awaitGoroutines until ALL internal io.Copy
		// goroutines finish draining their pipes. Those goroutines finish when
		// the write ends of CH's stdout/stderr pipes close. In the user+network
		// namespace context, an fd can leak across the fork boundary and prevent
		// those write ends from closing, keeping cmd.Wait() blocked indefinitely.
		//
		// syscall.Wait4 is a direct syscall: it waits only for process exit and
		// does not involve any pipe-draining machinery. It returns as soon as CH
		// exits (or is already a zombie), which is the only signal we care about.
		var ws syscall.WaitStatus
		for {
			wpid, err := syscall.Wait4(proc.pid, &ws, 0, nil)
			if err == nil && wpid == proc.pid {
				break // CH reaped
			}
			if errors.Is(err, syscall.EINTR) {
				continue // interrupted by signal, retry
			}
			break // unexpected error (ECHILD if reaped elsewhere, etc.) — exit anyway
		}
		os.Exit(0) // exit this process: no reason to outlive CH
	}()

	// Step 5 (cont.): run the frame pump. Blocks until both fds are closed.
	// tapPump copies Ethernet frames between the host TAP fd and the pump-end
	// of the socketpair. The parent reads frames from the perimeter end.
	tapPump(hostTapFile, pumpConn)
	// tapPump returned; both goroutines exited. Child exits cleanly.
}
