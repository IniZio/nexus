package cloudhypervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ErrNoKernelConfigured is returned by Start when Config.KernelPath is empty.
// The guest image pipeline is not yet implemented; KernelPath must be provided
// explicitly before sandboxes can be started.
var ErrNoKernelConfigured = errors.New("cloudhypervisor: no guest kernel configured: guest image pipeline not yet implemented")

// maxSocketPathLen is the maximum length of a Unix socket path on Linux.
// sockaddr_un.sun_path is 108 bytes including the null terminator, so the
// usable length is 107 bytes.
const maxSocketPathLen = 107

// Default kernel cmdlines by boot mode.
const (
	defaultCmdline  = "console=ttyS0"
	diskBootCmdline = "root=/dev/vda rw init=/sbin/nexus3-agent console=ttyS0"

	// memHotplugCmdline is appended to the guest cmdline when a virtio-mem
	// hotplug region is reserved (Config.MemoryMaxMiB > Config.MemoryMiB).
	// Both tokens are REQUIRED, not optional — confirmed by
	// docs/design/auto-resize.md Leg 6 (negative control): without them
	// PUT /api/v1/vm.resize returns 204 but MemTotal does not grow.
	//   memhp_default_state=online: auto-online hotplugged memory blocks.
	//   memory_hotplug.online_policy=auto-movable: zone blocks as MOVABLE so
	//     they can be migrated back out during shrink (Leg 7 shrink path).
	// CONFIG_MEMORY_HOTPLUG_DEFAULT_ONLINE is deliberately absent from the
	// nexus3 kernel config (scripts/kernel/config-6.12.76:895) — the cmdline
	// override provides per-sandbox control without baking it into the image.
	memHotplugCmdline = " memhp_default_state=online memory_hotplug.online_policy=auto-movable"
)

// buildCmdline inserts memHotplugCmdline into base when memoryMaxMiB > 0.
//
// Placement rule: hotplug tokens are KERNEL parameters and must appear before
// the PID-1 boundary (" --"). Everything after " --" is passed by the kernel
// to PID 1 as os.Args; appending there would silently break memory hotplug
// (Leg 6: tokens never reach the kernel).
//
//   - No " --" boundary → append (safe: all of base is kernel params).
//   - " --" boundary present → insert immediately before it.
//   - memoryMaxMiB == 0 → return base unchanged (AR-N-AC1: byte-identical to
//     the no-resize path).
//
// This function is the single source of truth for cmdline assembly. It is
// package-private so driver_resize_test.go can call it directly and verify
// placement under all four cases (no-mounts/mounts × auto-resize-on/off).
func buildCmdline(base string, memoryMaxMiB uint32) string {
	if memoryMaxMiB == 0 {
		return base
	}
	if idx := strings.Index(base, " --"); idx >= 0 {
		return base[:idx] + memHotplugCmdline + base[idx:]
	}
	return base + memHotplugCmdline
}

// Config holds the static configuration for the Cloud Hypervisor driver.
// It is set at construction time and must not be mutated afterwards.
type Config struct {
	// BinaryPath is the path to the cloud-hypervisor executable.
	// Required.
	BinaryPath string

	// SocketDir is the directory under which per-sandbox API sockets are
	// created. Each socket is named "<sandboxID>.sock".
	//
	// Defaults to $XDG_RUNTIME_DIR/nexus3, or $TMPDIR/nexus3-<uid> if
	// XDG_RUNTIME_DIR is unset.
	//
	// Must be short enough that the full socket path fits within the 107-byte
	// Linux sun_path limit. New returns an error if the directory is too long.
	SocketDir string

	// KernelPath is the path to the guest kernel image passed to CH's
	// --kernel / payload.kernel field. Another agent (boot-artifacts slice)
	// is responsible for ensuring this file exists and is the right format.
	KernelPath string

	// InitramfsPath is the path to the guest initramfs image (cpio.gz or
	// cpio) passed to CH's payload.initramfs field. Optional: when empty, no
	// initramfs is sent to the VMM and the kernel must find its root by other
	// means (e.g. a disk image). Without initramfs the kernel panics at the
	// init-search stage and enters an HLT loop.
	//
	// Must not be set together with DiskImagePath. When DiskImagePath is set
	// the disk-boot path is used instead of the initramfs path.
	InitramfsPath string

	// DiskImagePath is the path to a raw ext4 disk image that is attached as
	// a virtio-blk device (vda). When set, the driver boots from the disk
	// instead of an initramfs.
	//
	// When DiskImagePath is set and Cmdline is empty the driver substitutes
	// the disk-boot default cmdline:
	//
	//	root=/dev/vda rw init=/sbin/nexus3-agent console=ttyS0
	//
	// The raw ext4 image must contain /sbin/nexus3-agent as its init process.
	// image_type=raw is passed to Cloud Hypervisor to bypass auto-detection,
	// which otherwise disables sector-0 writes and breaks ext4 rw mounts
	// (see CH vmm/src/device_manager.rs "Disabling sector 0 writes").
	//
	// When empty the driver uses the initramfs boot path (InitramfsPath).
	DiskImagePath string

	// DiskDir, when set, is the directory searched for per-sandbox disk images
	// when DiskImagePath is empty. On Start the driver looks for
	// <DiskDir>/<sandboxID>.raw and uses it as the root disk. This allows the
	// generic substrate driver (SelectSubstrate) to boot pre-existing sandboxes
	// without knowing each sandbox's disk path at construction time.
	DiskDir string

	// ExtraDisks are additional raw ext4 disk images attached at boot after
	// the rootfs vda. ExtraDisks[0] becomes /dev/vdb, ExtraDisks[1] /dev/vdc,
	// and so on. See ExtraDisk for details. Only valid when DiskImagePath is set.
	ExtraDisks []ExtraDisk

	// Cmdline is the kernel command line passed to CH's payload.cmdline.
	//
	// When empty, the driver uses the default:
	//
	//	console=ttyS0
	//
	// This routes kernel messages to the guest serial port (ttyS0), which is
	// needed for any serial-capture configuration (e.g. Config.SerialOutputPath).
	// It does not change panic/halt behaviour — without an initramfs the
	// kernel still panics regardless.
	//
	// Callers MUST set Cmdline explicitly for production use. The eventual
	// nexus3 guest agent runs as PID 1 via init=/sbin/nexus3-agent; that path
	// is on the critical boot path and must appear in Cmdline.
	Cmdline string

	// SerialOutputPath is the path to a file where the guest serial port
	// (ttyS0) output is captured. When set, vm.create includes a
	// serial: {mode: "File", file: SerialOutputPath} entry so the kernel's
	// console messages (and any PID-1 stdout) are written to that file.
	//
	// Optional. When empty, no serial device is configured (serial output is
	// discarded by CH). Requires Cmdline to include console=ttyS0 for the
	// guest to actually route kernel output to the serial port.
	SerialOutputPath string

	// VCPUs is the number of virtual CPUs for each VM (used for boot_vcpus).
	// Defaults to 1.
	VCPUs uint32

	// VCPUMax is the max_vcpus ceiling advertised to CH at vm.create, which
	// fixes the upper bound for runtime vCPU hotplug. When 0, max_vcpus is set
	// equal to VCPUs (no hotplug headroom). When > VCPUs, CH allows hotplug up
	// to this ceiling and SandboxResizer.ResizeCPU may grow up to it.
	//
	// Must be > VCPUs when set; New returns an error otherwise.
	VCPUMax uint32

	// MemoryMiB is the guest RAM in mebibytes. Defaults to 512.
	MemoryMiB uint32

	// MemoryMaxMiB is the ceiling for memory hotplug in mebibytes. When 0,
	// no virtio-mem hotplug region is reserved at vm.create. When > MemoryMiB,
	// the driver reserves a (MemoryMaxMiB − MemoryMiB) MiB hotplug region using
	// VirtioMem and adds the required cmdline tokens (confirmed by spike
	// docs/design/auto-resize.md Legs 1–7):
	//
	//	memhp_default_state=online memory_hotplug.online_policy=auto-movable
	//
	// These tokens are required (not optional): without them vm.resize returns
	// 204 silently but MemTotal does not grow (Leg 6 negative control).
	//
	// Must be > MemoryMiB when set; New returns an error otherwise.
	MemoryMaxMiB uint32

	// BalloonMiB is the initial virtio-balloon device size in mebibytes.
	// When non-zero, a balloon device is attached at boot with this size.
	// When zero and FreePageReporting is true, a zero-size balloon device is
	// still attached (size=0, deflate_on_oom=true, free_page_reporting=true).
	// When both BalloonMiB and FreePageReporting are zero/false, no balloon
	// device is configured and ResizeBalloon will return an error from CH.
	BalloonMiB uint32

	// FreePageReporting enables passive free-page reporting on the
	// virtio-balloon device. When true the guest balloon driver advertises
	// pages it has freed back to the host, allowing the host to reclaim memory
	// that the guest no longer uses without any active balloon inflation step.
	// If BalloonMiB is 0 and this is true, a zero-size balloon device with
	// free_page_reporting=true and deflate_on_oom=true is created automatically.
	FreePageReporting bool

	// StartTimeout is how long to wait for the VMM API socket to become
	// responsive after spawning the process. Defaults to 10 seconds.
	StartTimeout time.Duration

	// CallTimeout is the maximum duration for each individual API call to the
	// VMM (Observe, Stop, Pause, Resume, and the post-spawn VMCreate/VMBoot in
	// Start). It is enforced at the driver boundary so that a wedged VMM cannot
	// hold the per-sandbox exclusive flock indefinitely. Defaults to 10 seconds.
	//
	// The caller's context deadline wins if it fires before CallTimeout.
	// spawnVMM's own StartTimeout is unaffected — it manages its own deadline.
	CallTimeout time.Duration

	// SnapshotDir is the root directory for snapshot artifacts. Each snapshot
	// occupies a subdirectory named by its SnapshotID (CH files) plus a
	// .payload and .commit file managed by the artifact.Store for integrity.
	//
	// Defaults to $XDG_STATE_HOME/nexus3/snapshots (or ~/.local/state/nexus3/snapshots
	// if XDG_STATE_HOME is unset) — a durable location that survives reboots.
	// SocketDir remains on the ephemeral runtime directory (sun_path limit);
	// only SnapshotDir moves to the durable state home.
	SnapshotDir string

	// NestedVirt enables opt-in nested virtualisation support.
	//
	// # Security perimeter (D-N3N-02 AC4)
	//
	// When true the outer VM's vCPUs expose the host CPU's virtualisation
	// extensions (Intel VMX / AMD SVM) to the guest, allowing the guest to
	// run its own KVM-accelerated VMs (cloud-hypervisor or QEMU in-guest).
	// This WIDENS the isolation perimeter:
	//   - The outer guest can instantiate full VMs; a compromised guest has a
	//     richer attack surface (hypervisor CVEs, /dev/kvm, etc.).
	//   - The host system must have nested KVM enabled
	//     (/sys/module/kvm_intel(amd)/parameters/nested == "1" or "Y").
	//   - The host user (and the in-userns child process) must be able to
	//     open /dev/kvm with read+write permissions.
	//
	// When false (the default) the driver still sends CpusConfig.nested=false
	// explicitly in the JSON payload — it is NEVER omitted. Cloud Hypervisor
	// v53 treats an absent CpusConfig.nested field as true (nested-virt ON by
	// default), so omitting it would silently breach this opt-in perimeter.
	// WARNING: do NOT add omitempty to the Nested field in client.go — doing so
	// would re-enable nested-by-default and violate D-N3N-02.
	// The host nested-KVM check is not run and /dev/kvm access is not required.
	//
	// Mirrors NEXUS_NESTED_VIRT in old nexus (packages/nexus).
	// Set via the NEXUS_NESTED_VIRT=1 env var or by setting this field directly.
	NestedVirt bool

}

// CHDriver implements driver.Driver, driver.PauseResumer, driver.Snapshotter,
// driver.Forker, driver.GuestDialer, and driver.NetworkHook for Cloud Hypervisor.
// Each sandbox gets its own cloud-hypervisor process; there is no central daemon.
// Construct with New.
//
// NetworkHook is implemented via a two-TAP/L2-bridge topology: see ch_net.go.
type CHDriver struct {
	cfg Config

	mu    sync.Mutex
	procs map[domain.SandboxID]*managedProcess
	nets  map[domain.SandboxID]*netState // per-sandbox network resources; see ch_net.go

	snapshotStore *artifact.Store
}

// New validates cfg, creates the socket directory if necessary, and returns a
// ready CHDriver.
func New(cfg Config) (*CHDriver, error) {
	if cfg.SocketDir == "" {
		dir, err := defaultSocketDir()
		if err != nil {
			return nil, fmt.Errorf("cloudhypervisor: determine socket dir: %w", err)
		}
		cfg.SocketDir = dir
	}

	// Validate that the longest possible socket path for this directory
	// fits within the Linux sun_path limit (107 usable bytes).
	// Longest name: "sb-" + 26 Crockford chars + ".sock" = 34 chars; +1 for "/".
	const sockNameLen = 35
	if len(cfg.SocketDir)+sockNameLen > maxSocketPathLen {
		return nil, fmt.Errorf(
			"cloudhypervisor: SocketDir %q is too long: socket path would exceed %d bytes (Linux sun_path limit)",
			cfg.SocketDir, maxSocketPathLen,
		)
	}

	if cfg.VCPUs == 0 {
		cfg.VCPUs = 1
	}
	if cfg.MemoryMiB == 0 {
		cfg.MemoryMiB = 512
	}
	if cfg.CallTimeout <= 0 {
		cfg.CallTimeout = 10 * time.Second
	}

	// Validate auto-resize ceilings after defaults are applied so the
	// comparisons reflect the resolved VCPUs/MemoryMiB values.
	if cfg.VCPUMax > 0 && cfg.VCPUMax <= cfg.VCPUs {
		return nil, fmt.Errorf("cloudhypervisor: VCPUMax (%d) must be > VCPUs (%d) when set", cfg.VCPUMax, cfg.VCPUs)
	}
	if cfg.MemoryMaxMiB > 0 && cfg.MemoryMaxMiB <= cfg.MemoryMiB {
		return nil, fmt.Errorf("cloudhypervisor: MemoryMaxMiB (%d) must be > MemoryMiB (%d) when set", cfg.MemoryMaxMiB, cfg.MemoryMiB)
	}

	if err := os.MkdirAll(cfg.SocketDir, 0o700); err != nil {
		return nil, fmt.Errorf("cloudhypervisor: create socket dir: %w", err)
	}

	if cfg.SnapshotDir == "" {
		dir, err := defaultSnapshotDir()
		if err != nil {
			return nil, fmt.Errorf("cloudhypervisor: determine snapshot dir: %w", err)
		}
		cfg.SnapshotDir = dir
	}
	snapshotStore, err := artifact.NewStore(cfg.SnapshotDir)
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: init snapshot store: %w", err)
	}

	return &CHDriver{
		cfg:           cfg,
		procs:         make(map[domain.SandboxID]*managedProcess),
		nets:          make(map[domain.SandboxID]*netState),
		snapshotStore: snapshotStore,
	}, nil
}

// defaultSocketDir returns $XDG_RUNTIME_DIR/nexus3 or a tmp fallback.
func defaultSocketDir() (string, error) {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "nexus3"), nil
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("nexus3-%d", os.Getuid())), nil
}

// defaultSnapshotDir returns the durable default root for snapshot artifacts.
// It delegates to store.DefaultRoot() so that the driver and the CLI snapshot
// commands always open the same on-disk directory — preventing the two-store
// split where the driver writes to an ephemeral path and the CLI reads a
// different durable one.
func defaultSnapshotDir() (string, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return "", fmt.Errorf("determine snapshot dir: %w", err)
	}
	return filepath.Join(root, "snapshots"), nil
}

// SnapshotStore returns the driver's canonical artifact.Store for snapshot
// artifacts. The store is rooted at cfg.SnapshotDir (the durable snapshot
// root). Callers may read or list snapshots via the returned store; writing
// is reserved for TakeSnapshot and must not be done by external callers.
func (d *CHDriver) SnapshotStore() *artifact.Store {
	return d.snapshotStore
}

// callCtx returns a child context bounded by cfg.CallTimeout, taking the
// earlier of parent's deadline and the configured timeout. It is applied at
// the driver boundary (not in newClient) so spawnVMM's own StartTimeout is
// never shortened by this bound.
func (d *CHDriver) callCtx(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := d.cfg.CallTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return context.WithTimeout(parent, timeout)
}

// socketPath returns the per-sandbox API socket path.
func (d *CHDriver) socketPath(id domain.SandboxID) string {
	return filepath.Join(d.cfg.SocketDir, id.String()+".sock")
}

// iidPath returns the per-sandbox instance-ID sidecar file path.
// The InstanceID is stored in a small file alongside the socket so it
// survives nexus3 restarts (the CH API does not expose a nexus3 InstanceID).
//
// Note: the SandboxID String() value appears in the filename, not the
// InstanceID itself. This is intentional — domain.Sandbox.InstanceID must
// never be used as a key in runtime-scoped resources.
func (d *CHDriver) iidPath(id domain.SandboxID) string {
	return filepath.Join(d.cfg.SocketDir, id.String()+".iid")
}

// writeInstanceID persists iid to the sidecar file for id.
func (d *CHDriver) writeInstanceID(id domain.SandboxID, iid string) error {
	return os.WriteFile(d.iidPath(id), []byte(iid), 0o600)
}

// readInstanceID reads the sidecar file for id and returns the stored
// InstanceID, or "" if the file does not exist.
func (d *CHDriver) readInstanceID(id domain.SandboxID) string {
	b, err := os.ReadFile(d.iidPath(id))
	if err != nil {
		return ""
	}
	return string(b)
}

// newInstanceID generates a random hex string for use as an InstanceID.
func newInstanceID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("cloudhypervisor: generate instance ID: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// clearState removes the socket file, the IID sidecar, the proc entry, and
// all sandbox network resources for id. It is idempotent: missing files and
// absent map entries are not errors.
func (d *CHDriver) clearState(id domain.SandboxID) {
	_ = os.Remove(d.socketPath(id))
	_ = os.Remove(d.vsockPath(id))
	_ = os.Remove(d.iidPath(id))

	// teardownSandboxNet acquires d.mu internally; must not be called while
	// d.mu is held. It closes fds, waits for pump goroutines, and deletes
	// the kernel interfaces.
	d.teardownSandboxNet(id)

	d.mu.Lock()
	delete(d.procs, id)
	d.mu.Unlock()
}

// Name returns the human-readable substrate name.
func (d *CHDriver) Name() string { return "cloud-hypervisor" }

// Observe queries the substrate for the actual run state of the VM identified
// by id. See driver.Driver.Observe for the full contract.
//
// Socket absent (ENOENT / ECONNREFUSED)              → Absent, nil.
// VMM alive but no VM yet (500 "VM is not created")  → Absent, nil.
// VM Running or Paused                               → corresponding state, nil.
// Any other outcome (CH state Created/Shutdown,
// unrecognised state, timeout, parse failure)        → Unknown, non-nil error.
//
// Absent covers both the "dead VMM" and "live VMM with no VM" cases because
// in both cases there is no VM to observe. The concern that returning Absent
// for a live VMM would authorise a colliding Start() is addressed by
// spawnVMM's pre-flight check: it refuses to spawn onto a socket that already
// has a live VMM, returning ErrVMMAlreadyBound instead.
func (d *CHDriver) Observe(ctx context.Context, id domain.SandboxID) (driver.Observation, error) {
	ctx, cancel := d.callCtx(ctx)
	defer cancel()

	c := newClient(d.socketPath(id))

	state, err := c.VMInfo(ctx)
	if err != nil {
		if isAbsent(err) {
			return driver.Observation{
				State:  driver.Absent,
				Detail: "VMM socket not reachable: no process listening",
			}, nil
		}
		return driver.Observation{
			State:  driver.Unknown,
			Detail: err.Error(),
		}, fmt.Errorf("cloudhypervisor: observe %s: %w", id, err)
	}

	// VMInfo returns nil error for Running, Paused, and Absent.
	obs := driver.Observation{State: state}
	switch state {
	case driver.Running, driver.Paused:
		obs.InstanceID = d.readInstanceID(id)
		obs.Detail = fmt.Sprintf("state=%s instanceID=%s", state, obs.InstanceID)
	case driver.Absent:
		obs.Detail = "VMM alive but no VM has been created"
	default:
		obs.Detail = "state unknown"
	}
	return obs, nil
}

// Start spawns a cloud-hypervisor VMM for req.SandboxID inside an isolated
// user+network namespace (via StartNetnsRuntime), waits for its API to become
// responsive, then calls vm.create and vm.boot.
//
// The netns child process hosts CH, the TAP/bridge topology, and the frame
// pump. The driver communicates with CH via the shared API socket (mount
// namespace is not isolated) and receives guest Ethernet frames via
// rt.PerimConn.
//
// On any failure after the netns child has been started, Start kills the
// child process (which kills CH via Pdeathsig=SIGKILL) and removes all
// per-sandbox files before returning.
func (d *CHDriver) Start(ctx context.Context, req driver.StartRequest) (string, error) {
	id := req.SandboxID

	// Guard: refuse before spawning anything when no kernel is configured.
	// Propagating CH's config-validation error would obscure the real cause —
	// the guest image pipeline is not yet implemented.
	if d.cfg.KernelPath == "" {
		return "", fmt.Errorf("cloudhypervisor: start %s: %w", id, ErrNoKernelConfigured)
	}

	// Resolve the disk image path into a local variable so we never mutate
	// shared config (d.cfg is owned by the driver and may be read concurrently
	// on other Start calls; writing to it would be a data race and would also
	// permanently corrupt the path for any subsequent sandbox started with a
	// different ID).
	diskImagePath := d.cfg.DiskImagePath
	if diskImagePath == "" && d.cfg.DiskDir != "" {
		candidate := filepath.Join(d.cfg.DiskDir, id.String()+".raw")
		if _, serr := os.Stat(candidate); serr == nil {
			diskImagePath = candidate
		}
	}

	socketPath := d.socketPath(id)

	// Pre-flight: probe the socket before launching the netns child.
	// This mirrors spawnVMM's pre-flight to preserve the ErrVMMAlreadyBound
	// invariant: on a live-VMM collision the caller must not call clearState
	// (the socket belongs to a foreign process that must not be unlinked).
	{
		probeCtx, probeCancel := context.WithTimeout(ctx, probeTimeout)
		pingErr := newClient(socketPath).Ping(probeCtx)
		probeCancel()
		switch {
		case pingErr == nil:
			// A live VMM is already answering. Refuse to collide.
			return "", fmt.Errorf("cloudhypervisor: start %s: %s: %w", id, socketPath, ErrVMMAlreadyBound)
		case isAbsent(pingErr):
			// Socket absent or stale. Remove any stale file.
			_ = os.Remove(socketPath)
		default:
			// Socket state undetermined (hung VMM, I/O error, …).
			return "", fmt.Errorf("cloudhypervisor: start %s: pre-flight ping %s: %w", id, socketPath, pingErr)
		}
	}

	// Launch the netns child. StartNetnsRuntime re-execs this binary with
	// NEXUS3_NETNS_RUN=1 inside CLONE_NEWUSER|CLONE_NEWNET, creates the
	// TAP/bridge topology, spawnVMM (CH), and the frame pump, then returns
	// without waiting for CH to be API-ready.
	rt, err := StartNetnsRuntime(ctx, d.cfg, id, socketPath, "") // "" = boot mode; parent issues vm.create + vm.boot
	if err != nil {
		return "", fmt.Errorf("cloudhypervisor: start %s: %w", id, err)
	}

	// Register netState immediately so cleanup() → clearState →
	// teardownSandboxNet → rt.Stop() reaches the child on any error path.
	d.mu.Lock()
	d.nets[id] = &netState{rt: rt, perimConn: rt.PerimConn}
	d.mu.Unlock()

	cleanup := func() {
		d.clearState(id) // → teardownSandboxNet → rt.Stop() (kills child + waits)
	}

	// withStderr wraps err with the netns child's stderr tail when available.
	// Using fmt.Errorf with %w preserves the error chain for errors.Is.
	withStderr := func(err error) error {
		if tail := rt.ChildStderr(); tail != "" {
			return fmt.Errorf("%w\nVMM stderr:\n%s", err, tail)
		}
		return err
	}

	// Poll CH's API socket until it becomes responsive. StartNetnsRuntime
	// returns before CH is ready (the child still needs to set up TAP/bridge
	// and wait for CH to bind its socket). Mount namespace is shared, so the
	// socket path is visible here on the host.
	{
		timeout := d.cfg.StartTimeout
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		pollCtx, pollCancel := context.WithTimeout(ctx, timeout)
		defer pollCancel()

		c := newClient(socketPath)
		for {
			if err := pollCtx.Err(); err != nil {
				tail := rt.ChildStderr()
				cleanup()
				if tail != "" {
					return "", fmt.Errorf("cloudhypervisor: start %s: VMM API not ready within %s: %w\nVMM stderr:\n%s", id, timeout, err, tail)
				}
				return "", fmt.Errorf("cloudhypervisor: start %s: VMM API not ready within %s: %w", id, timeout, err)
			}
			if pingErr := c.Ping(pollCtx); pingErr == nil {
				break
			}
			select {
			case <-pollCtx.Done():
			case <-time.After(50 * time.Millisecond):
			}
		}
	}

	// Bound post-readiness API calls (vm.create, vm.boot).
	apiCtx, apiCancel := d.callCtx(ctx)
	defer apiCancel()

	c := newClient(socketPath)

	vcpus := d.cfg.VCPUs
	if vcpus == 0 {
		vcpus = 1
	}
	memMiB := d.cfg.MemoryMiB
	if memMiB == 0 {
		memMiB = 512
	}

	// Resolve the kernel command line. The default depends on the boot mode:
	//   - disk boot (DiskImagePath set):  diskBootCmdline (package const)
	//   - initramfs boot:                 defaultCmdline   (package const)
	// When Cmdline is set explicitly it overrides the mode-specific default.
	cmdline := d.cfg.Cmdline
	if cmdline == "" {
		if diskImagePath != "" {
			cmdline = diskBootCmdline
		} else {
			cmdline = defaultCmdline
		}
	}
	cmdline = buildCmdline(cmdline, d.cfg.MemoryMaxMiB)

	// Nested-virt preflight: check host support and /dev/kvm access before
	// constructing vmcfg. We fail loudly here so the error is attributed to
	// Start (not to a mysterious vmm startup failure later).
	cpusCfg := &vmCPUsConfig{
		BootVCPUs: vcpus,
		MaxVCPUs:  vcpus,
	}
	// When auto-resize is enabled, MaxVCPUs must exceed BootVCPUs at vm.create
	// because CH fixes the ceiling at that point — it cannot be raised later.
	if d.cfg.VCPUMax > vcpus {
		cpusCfg.MaxVCPUs = d.cfg.VCPUMax
	}
	if d.cfg.NestedVirt {
		if err := nestedVirtPreflight(); err != nil {
			return "", err
		}
		cpusCfg.Nested = true
	}

	// Build the memory config. When MemoryMaxMiB is set, reserve a VirtioMem
	// hotplug region of (MemoryMaxMiB − MemoryMiB) MiB. The method MUST be
	// "VirtioMem" — CH v52.0 defaults to "Acpi" and that must not be left
	// implicit (confirmed by spike Leg 1 finding).
	memCfg := &vmMemoryConfig{
		SizeBytes: uint64(memMiB) * 1024 * 1024,
	}
	if d.cfg.MemoryMaxMiB > 0 {
		hotplugMiB := d.cfg.MemoryMaxMiB - memMiB
		memCfg.HotplugSize = uint64(hotplugMiB) * 1024 * 1024
		memCfg.HotplugMethod = "VirtioMem"
	}

	vmcfg := vmConfig{
		Payload: vmPayloadConfig{
			Kernel:    d.cfg.KernelPath,
			Cmdline:   cmdline,
			Initramfs: d.cfg.InitramfsPath, // omitempty: omitted when empty or disk boot
		},
		CPUs:   cpusCfg,
		Memory: memCfg,
	}

	// Attach a virtio-balloon device when requested. free_page_reporting
	// requires a balloon device to exist, so if only FreePageReporting is set
	// we still create a zero-size balloon (size=0 keeps it fully deflated at
	// boot while reporting remains active). deflate_on_oom is always true to
	// prevent the balloon from starving the guest under memory pressure.
	if d.cfg.BalloonMiB > 0 || d.cfg.FreePageReporting {
		vmcfg.Balloon = &balloonConfig{
			SizeBytes:         uint64(d.cfg.BalloonMiB) * 1024 * 1024,
			DeflateOnOOM:      true,
			FreePageReporting: d.cfg.FreePageReporting,
		}
	}

	// When DiskImagePath is set, attach it as a virtio-blk device (vda).
	// image_type=raw bypasses CH's auto-detection which otherwise disables
	// sector-0 writes, breaking ext4 rw mount (EXT4-fs: I/O error while
	// writing superblock).
	if diskImagePath != "" {
		vmcfg.Disks = []vmDiskConfig{
			{Path: diskImagePath, ImageType: "Raw"},
		}
		for _, ed := range d.cfg.ExtraDisks {
			// ExtraDisks are scratch/data disks (vdb+), never the rootfs.
			// O_DIRECT bypasses host page cache so large writes (e.g. buildkit
			// layer export) do not fill host dirty pages and trigger OOM.
			// The rootfs vda (above) always uses buffered I/O.
			vmcfg.Disks = append(vmcfg.Disks, vmDiskConfig{
				Path:      ed.Path,
				ImageType: "Raw",
				Direct:    true,
			})
		}
	}

	if d.cfg.SerialOutputPath != "" {
		vmcfg.Serial = &vmSerialConfig{
			Mode: "File",
			File: d.cfg.SerialOutputPath,
		}
	}

	vsock := &vmVsockConfig{
		CID:    guestCID,
		Socket: d.vsockPath(id),
	}

	// Build the net config from the guest TAP name allocated by the netns child.
	// The TAP lives inside the child's netns; CH (also inside the netns) calls
	// TUNSETIFF on it at vm.boot. NumQueues=2 matches the host-netns legacy path.
	netCfg := vmNetConfig{
		Tap:       rt.GuestTap,
		Mac:       sandboxMac(id),
		NumQueues: 2,
	}

	if err := c.VMCreateWithNet(apiCtx, vmcfg, vsock, []vmNetConfig{netCfg}); err != nil {
		wrapped := withStderr(fmt.Errorf("cloudhypervisor: start %s: %w", id, err))
		cleanup()
		return "", wrapped
	}

	if err := c.VMBoot(apiCtx); err != nil {
		wrapped := withStderr(fmt.Errorf("cloudhypervisor: start %s: %w", id, err))
		cleanup()
		return "", wrapped
	}

	iid, err := newInstanceID()
	if err != nil {
		cleanup()
		return "", fmt.Errorf("cloudhypervisor: start %s: %w", id, err)
	}

	if err := d.writeInstanceID(id, iid); err != nil {
		cleanup()
		return "", fmt.Errorf("cloudhypervisor: start %s: write instance ID: %w", id, err)
	}

	// Note: d.procs[id] is NOT set on the netns path. Kill ownership is via
	// d.nets[id].rt → rt.Stop(), reached through clearState → teardownSandboxNet.
	// Stop()'s belt-and-braces proc.kill() is a no-op when d.procs[id] is nil.

	return iid, nil
}

// Stop terminates the VM identified by id. It is idempotent: stopping an
// absent VM is not an error.
//
// Stop sequence:
//  1. vm.shutdown  — politely ask the guest OS to power off (best-effort).
//  2. vm.delete    — remove the VM config from VMM memory (best-effort).
//  3. vmm.shutdown — ask the VMM process to exit via the API (authoritative).
//  4. proc.kill()  — belt-and-braces SIGKILL on any tracked proc handle.
//
// Steps 1 and 2 are best-effort: a non-absent error (e.g. CH's 500 "VM is not
// running" when there is nothing to shut down) is logged but does not abort
// the sequence. VMMShutdown (step 3) always runs unless the socket is absent,
// ensuring no orphaned cloud-hypervisor process survives a nexus3 restart.
func (d *CHDriver) Stop(ctx context.Context, id domain.SandboxID) error {
	c := newClient(d.socketPath(id))

	// Each HTTP step gets its own bounded deadline so that a wedged VMM on
	// one step does not prevent subsequent steps from running. In particular,
	// the proc.kill() + clearState in steps 3–4 must always execute.
	// Worst-case total: 3 × CallTimeout.

	// Step 1: vm.shutdown — best-effort graceful guest power-off.
	// VMShutdown already tolerates CH's "no VM to shut down" responses (500).
	// ENOENT/ECONNREFUSED → socket absent → nothing to do.
	shutCtx, shutCancel := d.callCtx(ctx)
	shutErr := c.VMShutdown(shutCtx)
	shutCancel()
	if shutErr != nil {
		if isAbsent(shutErr) {
			d.clearState(id)
			return nil
		}
		// Non-absent error: VMM is alive but confused. Continue to kill it.
	}

	// Step 2: vm.delete — best-effort; remove VM config from VMM memory.
	// Ignore errors: if this fails we still proceed to terminate the process.
	delCtx, delCancel := d.callCtx(ctx)
	_ = c.VMDelete(delCtx)
	delCancel()

	// Step 3: vmm.shutdown — terminate the VMM process via the API.
	// This is the authoritative kill step and covers the post-restart case
	// where d.procs[id] is nil (no in-memory handle). Without this, vm.delete
	// removes the VM object from VMM memory but the cloud-hypervisor process
	// stays alive with its socket unlinked and unreachable.
	vmmCtx, vmmCancel := d.callCtx(ctx)
	vmmErr := c.VMMShutdown(vmmCtx)
	vmmCancel()
	if vmmErr != nil && !isAbsent(vmmErr) {
		return fmt.Errorf("cloudhypervisor: stop %s: vmm.shutdown: %w", id, vmmErr)
	}

	// Step 4: belt-and-braces SIGKILL on any tracked proc handle.
	d.mu.Lock()
	proc := d.procs[id]
	d.mu.Unlock()
	if proc != nil {
		proc.kill()
	}

	d.clearState(id)
	return nil
}

// Pause suspends execution of the VM identified by id.
// Implements driver.PauseResumer.
func (d *CHDriver) Pause(ctx context.Context, id domain.SandboxID) error {
	ctx, cancel := d.callCtx(ctx)
	defer cancel()

	c := newClient(d.socketPath(id))
	if err := c.VMPause(ctx); err != nil {
		return fmt.Errorf("cloudhypervisor: pause %s: %w", id, err)
	}
	return nil
}

// Resume restarts execution of a previously paused VM.
// Implements driver.PauseResumer.
func (d *CHDriver) Resume(ctx context.Context, id domain.SandboxID) error {
	ctx, cancel := d.callCtx(ctx)
	defer cancel()

	c := newClient(d.socketPath(id))
	if err := c.VMResume(ctx); err != nil {
		return fmt.Errorf("cloudhypervisor: resume %s: %w", id, err)
	}
	return nil
}

// ResizeBalloon adjusts the virtio-balloon device size of the running VM
// identified by id. balloonMiB is the new target balloon size in mebibytes:
//   - 0  = fully deflated (all pages returned to the guest).
//   - >0 = inflate to this size (host reclaims that many MiB from the guest).
//
// The VM must have been started with a balloon device (Config.BalloonMiB > 0
// or Config.FreePageReporting = true); CH returns an error if no balloon
// device is present.
//
// Callers use ResizeBalloon after a workload peak to actively reclaim host
// memory: inflate (balloonMiB > 0) to shrink the guest's backing footprint,
// then deflate (balloonMiB = 0) when the guest needs its memory back.
// Passive free-page reclaim (FreePageReporting) supplements this but does not
// replace it for large one-time reclaim events.
func (d *CHDriver) ResizeBalloon(ctx context.Context, id domain.SandboxID, balloonMiB uint32) error {
	balloonBytes := uint64(balloonMiB) * 1024 * 1024
	c := newClient(d.socketPath(id))
	callCtx, cancel := d.callCtx(ctx)
	defer cancel()
	if err := c.VMResize(callCtx, nil, nil, &balloonBytes); err != nil {
		return fmt.Errorf("cloudhypervisor: resize balloon %s: %w", id, err)
	}
	return nil
}

// Compile-time interface assertions.
var (
	_ driver.Driver        = (*CHDriver)(nil)
	_ driver.PauseResumer  = (*CHDriver)(nil)
	_ driver.Snapshotter   = (*CHDriver)(nil)
	_ driver.Forker        = (*CHDriver)(nil)
	_ driver.NetworkHook   = (*CHDriver)(nil)
	// GuestDialer assertion is in ch_vsock.go.
	// NetworkHook assertion is also in ch_net.go.
)
