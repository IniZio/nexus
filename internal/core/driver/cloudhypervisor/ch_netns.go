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
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
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

	// childPgid is the process group ID of the netns child. Because
	// netnsChildAttr sets Setpgid:true, pgid == child.pid. CH runs with
	// Setpgid:false (spawnVMMInGroup) so it inherits this pgid. Stop() sends
	// Kill(-childPgid, SIGKILL) to reach both the child and CH in one call.
	childPgid int

	// cmd is the child process running inside the user+network namespace.
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

	return &NetnsRuntime{
		PerimConn: perimConn,
		APISocket: socketPath,
		GuestTap:  guestTap,
		childPgid: childPgid,
		cmd:       cmd,
		stderrBuf: stderrBuf,
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

// Stop kills the child process group (child + CH grandchild), waits for the
// child to exit, and closes PerimConn. Idempotent.
//
// Kill(-childPgid, SIGKILL) sends SIGKILL to every process in the group:
//   - the netns child itself
//   - CH (spawned with Setpgid:false via spawnVMMInGroup, so it inherits the
//     child's pgid rather than starting its own group)
//
// This is reliable and explicit, unlike Pdeathsig alone (which can be lost
// when Go retires OS threads). Pdeathsig remains in spawnVMMInGroup as
// defense-in-depth (CH dies if the child dies before Stop is called).
func (rt *NetnsRuntime) Stop() {
	rt.stopOnce.Do(func() {
		if rt.childPgid != 0 {
			_ = syscall.Kill(-rt.childPgid, syscall.SIGKILL)
		}
		if rt.cmd != nil {
			_ = rt.cmd.Wait()
		}
		if rt.PerimConn != nil {
			_ = rt.PerimConn.Close()
		}
	})
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
	_ = proc // CH process; killed by parent's group-kill in rt.Stop()

	// Step 5 (cont.): run the frame pump. Blocks until both fds are closed.
	// tapPump copies Ethernet frames between the host TAP fd and the pump-end
	// of the socketpair. The parent reads frames from the perimeter end.
	tapPump(hostTapFile, pumpConn)
	// tapPump returned; both goroutines exited. Child exits cleanly.
}
