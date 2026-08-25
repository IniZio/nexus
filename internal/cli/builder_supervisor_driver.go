// builder_supervisor_driver.go — UNI-WIRE slice
//
// supervisorBuilderDriver implements builder.BuilderDriver by routing the
// builder VM's lifecycle through a detached supervisor process
// (supervisor.SpawnDetached with Ephemeral:true). Contrast with the previous
// inline approach (cloudhypervisor.New + CHDriver.Start directly in the CLI),
// which orphaned cloud-hypervisor on CLI exit.
//
// # SIGKILL safety (UNI-TEARDOWN)
//
// SpawnDetached (ephemeral mode) creates a parent-watchdog pipe. The write
// end is held by this struct (watchdogW); the supervisor holds the read end.
// When the CLI is SIGKILL'd, the OS closes watchdogW, the supervisor reads
// EOF, and shuts down cleanly (calling svc.Remove to stop the VM and delete
// the transient __builder record).
//
// # Stop ordering (UNI-TEARDOWN)
//
// supervisorBuilderDriver.Stop sends POST /supervisor/stop and then polls
// supervisor.WaitForExit until the supervisor process exits. This means Stop
// returns only AFTER the supervisor has completed svc.Remove (VM stopped,
// record deleted). BuildInVM's defer st.Delete then runs against an already-
// absent record and is a safe no-op (ErrNotFound, ignored).
//
// Without WaitForExit, Stop returned as soon as the supervisor acknowledged
// the IPC (before calling svc.Remove), and the CLI's defer st.Delete raced
// with the supervisor's store.Update — if the delete won, CHDriver.Stop was
// never called and the cloud-hypervisor process was orphaned.

package cli

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/vmcfg"
	"github.com/IniZio/nexus3/internal/supervisor"
)

// supervisorBuilderDriver implements builder.BuilderDriver by delegating VM
// boot and teardown to a detached supervisor process.
//
// # Perimeter
//
// The perimeter (network egress for the builder VM) is managed by the
// supervisor process, not the CLI. vmbuilder's NetworkHook type assertion
// will fail on this driver — that is correct and intentional. The supervisor's
// perimeter goroutine handles all guest network frames.
//
// # DialGuest
//
// dialerDrv is a *cloudhypervisor.CHDriver configured with the same socketDir
// as the supervisor's own CHDriver. It is never Started from the CLI side —
// it only provides DialGuest so the CLI process can reach the vsock listener
// in the supervisor-owned VM.
type supervisorBuilderDriver struct {
	// dialerDrv is used for DialGuest only. Shares socketDir with the
	// supervisor's CHDriver so vsock paths resolve correctly from the CLI.
	dialerDrv *cloudhypervisor.CHDriver

	// ── static config (set at construction) ──────────────────────────────────
	storeRoot  string // FileStore root; the supervisor opens the same store
	stateBase  string // base dir for per-build supervisor state dirs
	socketDir  string // must match dialerDrv's SocketDir
	kernelPath string
	diskPath   string   // builder rootfs (vda)
	extraDisks []string // [vdb=context, vdc=artifact, vdd+=cache]
	ar         vmcfg.Result
	bootMemMiB uint32
	bootVCPUs  uint32
	logPath    string
	// cacheDiskMountPaths holds the in-guest mount paths for the cache disks
	// at extraDisks[2+] (i.e. /dev/vdd, /dev/vde, …). These are appended to
	// the kernel cmdline as --cache-disk=<dev>:<mountpath> so that PID-1
	// (regular-agent mode) can serve cache-disk telemetry on vsock:3002 without
	// needing the builder-role child to bind a second telemetry server.
	cacheDiskMountPaths []string

	// ── state set by Start ───────────────────────────────────────────────────
	mu        sync.Mutex
	lastID    domain.SandboxID // captured from StartRequest; read by StartedID
	sockPath  string           // supervisor.sock path; read by Stop
	watchdogW *os.File         // write end of parent-watchdog pipe; closed by Stop
}

// Name satisfies driver.Driver.
func (d *supervisorBuilderDriver) Name() string { return "supervisor-builder" }

// Observe satisfies driver.Driver. BuildInVM never calls Observe; this stub
// returns Unknown so unexpected callers see an explicit signal rather than a
// silent zero value.
func (d *supervisorBuilderDriver) Observe(_ context.Context, _ domain.SandboxID) (driver.Observation, error) {
	return driver.Observation{State: driver.Unknown, Detail: "supervisor-builder: Observe not supported"}, nil
}

// Start spawns a detached supervisor process that boots the builder VM.
// supervisor.SpawnDetached blocks until supervisor.pid appears (VM running
// and perimeter active), so Start returns only when the VM is ready for
// vsock connections.
func (d *supervisorBuilderDriver) Start(_ context.Context, req driver.StartRequest) (string, error) {
	// Each build gets its own state dir, keyed by SandboxID, so concurrent
	// builds do not share supervisor.pid / supervisor.sock files.
	stateDir := filepath.Join(d.stateBase, req.SandboxID.String())
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return "", fmt.Errorf("builder supervisor: create state dir: %w", err)
	}

	chBin, _ := exec.LookPath("cloud-hypervisor")

	// The builder declares its cache disk(s) as resizable via the same generic
	// field; no builder-specific governor branch.
	var cacheDiskIndices []int
	for i := 2; i < len(d.extraDisks); i++ {
		cacheDiskIndices = append(cacheDiskIndices, i)
	}

	// Build --cache-disk= args for PID-1's telemetry server. PID-1 runs as a
	// regular agent (isBuilderRole=false) and binds vsock:3002; it needs these
	// args to report cache-disk fill levels to the governor. Device names follow
	// the /dev/vd{b+i} convention: extraDisks[2] → /dev/vdd, [3] → /dev/vde, …
	cacheDiskCmdline := ""
	for i, mountPath := range d.cacheDiskMountPaths {
		dev := fmt.Sprintf("/dev/vd%c", 'd'+i)
		cacheDiskCmdline += fmt.Sprintf(" --cache-disk=%s:%s", dev, mountPath)
	}

	spawnCfg := supervisor.SpawnConfig{
		Config: supervisor.Config{
			SandboxRef: req.SandboxID.String(),
			StoreRoot:  d.storeRoot,
			StateDir:   stateDir,
			CHBin:      chBin,
			SocketDir:  d.socketDir,
			KernelPath: d.kernelPath,
			DiskPath:   d.diskPath,
			ExtraDisks: d.extraDisks,
			GovBounds:  d.ar.Bounds,
			MemoryMiB:  d.bootMemMiB,
			BootVCPUs:  d.bootVCPUs,
			ResizableDiskIndices: cacheDiskIndices,
			// Cmdline: full kernel cmdline. The supervisor's CHDriver inserts
			// memhp kernel params before "--" when MemoryMaxMiB > 0; the
			// PID-1 auto-resize section (--mem-ceiling=<bytes> and
			// --cache-disk=<dev>:<path> entries) comes after.
			Cmdline:   diskBootCmdlineBase + " --" + d.ar.PID1Args + cacheDiskCmdline,
			Ephemeral: true,
		},
		LogPath: d.logPath,
	}
	pid, watchdogW, err := supervisor.SpawnDetached(spawnCfg)
	if err != nil {
		return "", fmt.Errorf("builder supervisor: spawn: %w", err)
	}
	d.mu.Lock()
	d.lastID = req.SandboxID
	d.sockPath = supervisor.SockPath(stateDir)
	d.watchdogW = watchdogW // non-nil: supervisor reads EOF when CLI exits
	d.mu.Unlock()
	return fmt.Sprintf("supervisor-pid-%d", pid), nil
}

// Stop sends POST /supervisor/stop to the ephemeral supervisor, then waits
// for the supervisor process to exit before returning. Waiting ensures the
// supervisor has completed svc.Remove (VM stopped, __builder record deleted)
// before BuildInVM's defer st.Delete runs — preventing a race where the CLI
// deletes the record before the supervisor can call CHDriver.Stop.
//
// After WaitForExit, the parent-watchdog write end is closed (the supervisor
// is already gone, so the pipe is no longer needed).
func (d *supervisorBuilderDriver) Stop(ctx context.Context, _ domain.SandboxID) error {
	d.mu.Lock()
	sockPath := d.sockPath
	d.mu.Unlock()
	if sockPath == "" {
		return nil // Start was never called
	}

	// Signal the supervisor to stop. The supervisor acknowledges immediately
	// (before calling svc.Remove), so StopSupervisor returning does NOT mean
	// the VM is stopped — WaitForExit below provides that guarantee.
	if err := supervisor.StopSupervisor(ctx, sockPath); err != nil {
		// If the supervisor is already gone (e.g. it received SIGTERM from
		// outside), treat that as success: the VM was already stopped.
		return err
	}

	// Wait for the supervisor process to exit. The pidfile is removed by the
	// supervisor's defer at the very end of RunDetached — after svc.Remove
	// completes. When WaitForExit returns, the VM is fully stopped and the
	// __builder store record has been deleted.
	stateDir := filepath.Dir(sockPath)
	if err := supervisor.WaitForExit(ctx, stateDir); err != nil {
		return fmt.Errorf("builder supervisor: wait for supervisor exit: %w", err)
	}

	// Close the watchdog pipe write end now that the supervisor is gone.
	d.mu.Lock()
	w := d.watchdogW
	d.watchdogW = nil
	d.mu.Unlock()
	if w != nil {
		_ = w.Close()
	}
	return nil
}

// DialGuest connects to the given port inside the builder VM via CH's vsock
// AF_UNIX multiplexer. Delegates to dialerDrv which shares socketDir with
// the supervisor's CHDriver, so the vsock socket path is correct.
func (d *supervisorBuilderDriver) DialGuest(ctx context.Context, id domain.SandboxID, port uint32) (net.Conn, error) {
	return d.dialerDrv.DialGuest(ctx, id, port)
}

// StartedID returns the SandboxID captured during the last Start call. The
// execFn closure in runSandboxCreate reads this after BuildInVM has already
// called Start, so the value is always set before execFn runs.
func (d *supervisorBuilderDriver) StartedID() domain.SandboxID {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastID
}
