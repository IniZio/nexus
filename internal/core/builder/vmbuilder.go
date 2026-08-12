package builder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/perimeter/netfilter"
	"github.com/IniZio/nexus3/internal/core/perimeter/netstack"
)

// BuilderDriver is the subset of driver capabilities required by [BuildInVM].
// A [*cloudhypervisor.CHDriver] satisfies this interface; the interface is
// defined here so tests can inject a fake without importing the concrete driver.
type BuilderDriver interface {
	driver.Driver
	driver.GuestDialer
}

// GuestExecFn executes a command inside the guest VM and returns the exit
// code. The command's stderr is written to stderr (may be nil).
//
// Production callers wire this from an *agent.Client:
//
//	ac := agent.NewClient(drv, id)
//	execFn := func(ctx context.Context, argv []string, stderr io.Writer) (int32, error) {
//	    return ac.Exec(ctx, agent.ExecOptions{Argv: argv, Stderr: stderr})
//	}
//
// This indirection avoids an import cycle (agent imports builder for the
// BuildkitClient; builder cannot therefore import agent directly).
type GuestExecFn func(ctx context.Context, argv []string, stderr io.Writer) (int32, error)

// DefaultBuilderVCPUs and DefaultBuilderMemMiB are the production defaults for
// the ephemeral builder VM. These values are large enough for a real
// multi-stage buildkitd build (pulling base images, compiling, etc.) without
// OOM-killing the guest. The E2E proof (TestBuilderVME2E) exercises these
// exact values.
//
// DefaultBuilderMemMiB is 8 GiB: apt-heavy multi-stage builds (e.g. debian +
// compiler toolchains) fill the buildkitd overlay cache and blew through 2 GiB
// in live testing, OOM-killing the guest mid-build. 8 GiB matches the ceiling
// proven in old-nexus production runs.
const (
	DefaultBuilderVCPUs  uint8  = 2
	DefaultBuilderMemMiB uint16 = 8192
)

// BuildInVM boots an ephemeral builder VM described by spec, executes an
// in-guest image build over the VM's local buildkitd socket, harvests the
// produced artifact ext4 into cache, and returns the content-addressable
// digest.
//
// # Teardown guarantee
//
// A [Lifecycle] ensures that the guest flushes all persistent disk writes
// (sync) and the VMM is unconditionally stopped on every exit path:
//
//   - Success
//   - Build error (execFn returns non-zero or error)
//   - Transfer failure (ArtifactFromDisk fails after VMM stop)
//   - Panic (deferred panicSafeStop fires)
//   - Context cancellation (stopFn always uses context.Background())
//
// The ephemeral builder SandboxID is never persisted to the sandbox store.
//
// # Caller responsibilities
//
// drv must have Config.ExtraDisks wired from spec before it is passed here:
//
//	ExtraDisks[0] = {Path: spec.ContextDiskPath}   // vdb
//	ExtraDisks[1] = {Path: spec.ArtifactDiskPath}  // vdc
//	ExtraDisks[2+] = {Path: cache[i].ImagePath}    // vdd+
//
// G7/cmd is responsible for this wiring. G3 owns only the lifecycle.
//
// execFn is called with argv = [agentInstallPath, "--builder-role"] to trigger
// the in-guest build. G7 wires execFn from an *agent.Client.
//
// Live end-to-end testing (real builder VM + real build) is deferred to G8.
func BuildInVM(
	ctx context.Context,
	drv BuilderDriver,
	spec BuilderVMSpec,
	cache *image.Cache,
	execFn GuestExecFn,
) (digest string, err error) {
	id := domain.NewSandboxID()

	lc := newLifecycle(
		func(stopCtx context.Context) error { return drv.Stop(stopCtx, id) },
		func(syncCtx context.Context) error { return guestSync(syncCtx, execFn) },
	)

	// Cover panic / early-exit that occurs before the explicit SyncAndStop.
	started := false
	defer func() {
		if started {
			lc.panicSafeStop()
		}
	}()

	// ── 1. Boot the builder VM ────────────────────────────────────────────────
	if _, err := drv.Start(ctx, driver.StartRequest{SandboxID: id}); err != nil {
		return "", fmt.Errorf("builder vm: start: %w", err)
	}
	started = true

	// ── 1.5a. Start perimeter so the builder VM has internet access ───────────
	// CHDriver.Start always launches StartNetnsRuntime: a TAP/bridge pump that
	// forwards guest Ethernet frames to rt.PerimConn (AF_UNIX socketpair).
	// Without a consumer, frames are dropped → DNS timeouts and no network.
	// Start a wildcard-egress netstack perimeter (same code path as regular
	// sandboxes, no MITM, passthrough dialer) so buildkitd can pull base images.
	var perimCancel context.CancelFunc
	if hook, ok := drv.(driver.NetworkHook); ok {
		fd, fdErr := hook.GuestNetworkFD(ctx, id)
		if fdErr == nil {
			al, alErr := netfilter.NewAllowList(nil, nil, nil)
			if alErr == nil {
				// Broad (wildcard) egress for the duration of the build so that
				// buildkitd can pull arbitrary base images and package indexes.
				// This intentionally allows any outbound connection from the
				// builder VM. It does NOT affect the created SANDBOX's egress
				// policy, which remains default-deny and is configured separately
				// after this builder VM exits.
				al.AllowAllFor(24 * time.Hour)
				al.Start(5 * time.Minute)
				stack := netstack.New(al, nil, netstack.WithDialer(
					func(_ context.Context, network, addr string) (net.Conn, error) {
						return net.Dial(network, addr)
					},
				))
				var perimCtx context.Context
				perimCtx, perimCancel = context.WithCancel(context.Background())
				go func() {
					defer al.Stop()
					defer fd.Close()
					_ = stack.Run(perimCtx, id, fd)
				}()
			} else {
				fd.Close()
			}
		}
	}
	defer func() {
		if perimCancel != nil {
			perimCancel()
		}
	}()

	// ── 1.5. Wait for the builder VM agent to be reachable ───────────────────
	// drv.Start returns when the VMM API socket is ready, not when the guest
	// has fully booted and the nexus3-agent vsock listener is accepting
	// connections. Attempting the exec RPC before the listener is up causes an
	// immediate EOF. Poll until the agent is reachable or the context expires.
	if waitErr := waitForBuilderAgent(ctx, drv, id); waitErr != nil {
		return "", fmt.Errorf("builder vm: wait for agent: %w", waitErr)
	}

	// ── 2. Run the in-guest build ─────────────────────────────────────────────
	// Exec nexus3-agent --builder-role inside the VM. The builder role
	// (internal/core/agent/builder_role_linux.go) mounts /dev/vdb, starts
	// buildkitd, solves the Containerfile, writes the rootfs ext4 to /dev/vdc,
	// then calls syscall.Sync(). The exec blocks until the role completes.
	buildErr := guestBuild(ctx, execFn, spec.CacheDisks)

	// ── 3. Sync + Stop — always, even when build failed ───────────────────────
	tearErr := lc.SyncAndStop(ctx)
	started = false // prevent double-stop in the defer above

	if buildErr != nil {
		return "", fmt.Errorf("builder vm: in-guest build: %w", buildErr)
	}
	if tearErr != nil {
		return "", fmt.Errorf("builder vm: teardown: %w", tearErr)
	}

	// ── 4. Harvest artifact AFTER VMM is stopped ──────────────────────────────
	// The artifact disk is now stable: guest synced, VMM killed. Read the
	// raw ext4 from the host-side disk image path, hash it, and store it.
	if spec.ArtifactDiskPath == "" {
		return "", errors.New("builder vm: spec.ArtifactDiskPath is empty")
	}
	digest, err = ArtifactFromDisk(ctx, spec.ArtifactDiskPath, cache)
	if err != nil {
		return "", fmt.Errorf("builder vm: harvest artifact: %w", err)
	}
	return digest, nil
}

// waitForBuilderAgent polls the agent control port on the builder VM until the
// vsock listener accepts a connection. The poll interval is 500 ms. It returns
// as soon as the connection succeeds (the caller should Close it), or when ctx
// expires.
func waitForBuilderAgent(ctx context.Context, drv driver.GuestDialer, id domain.SandboxID) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		conn, err := drv.DialGuest(dialCtx, id, driver.AgentControlPort)
		cancel()
		if err == nil {
			conn.Close()
			return nil
		}
		// Back off before the next attempt.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// guestBuild execs the builder role inside the VM via execFn.
//
// cacheDisks is the ordered list of ecosystem cache disks from BuilderVMSpec.
// For each cache disk, a "--cache-disk=<device>:<mountpath>" argument is
// appended so that RunBuilderRole mounts them before starting buildkitd.
// Device names are /dev/vdd, /dev/vde, ... (cache disks occupy ExtraDisks[2+]
// = the block device slots after vdb/context and vdc/artifact).
func guestBuild(ctx context.Context, execFn GuestExecFn, cacheDisks []CacheDiskSpec) error {
	var stderr sbuilder
	argv := []string{agentInstallPath, "--builder-role"}
	for i, cd := range cacheDisks {
		// ExtraDisks[2+] map to /dev/vdd, /dev/vde, /dev/vdf, ...
		// 'd' + 0 = 'd' (vdd), 'd' + 1 = 'e' (vde), etc.
		dev := fmt.Sprintf("/dev/vd%c", 'd'+i)
		argv = append(argv, fmt.Sprintf("--cache-disk=%s:%s", dev, cd.MountPath))
	}
	exitCode, err := execFn(ctx, argv, &stderr)
	if err != nil {
		return fmt.Errorf("exec builder role: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("builder role exited %d: %s", exitCode, stderr.String())
	}
	return nil
}

// guestSync execs "sync" inside the VM to flush all pending writes to the
// virtio-blk backends. Called by the Lifecycle before VMM stop.
//
// The builder VM runs nexus3-agent as PID 1 (init=/sbin/nexus3-agent) so its
// process environment has no PATH from the kernel. Use the absolute path to
// the busybox sync applet to avoid exec.LookPath failures.
func guestSync(ctx context.Context, execFn GuestExecFn) error {
	// Try absolute paths first (avoids exec.LookPath relying on os.Getenv("PATH")
	// which is empty when nexus3-agent is PID 1).
	for _, syncBin := range []string{"/bin/sync", "/usr/bin/sync", "sync"} {
		_, err := execFn(ctx, []string{syncBin}, nil)
		if err == nil {
			return nil
		}
		// If not found, try the next candidate; otherwise propagate the error.
		if !isExecNotFound(err) {
			return err
		}
	}
	return fmt.Errorf("sync: not found in common paths or PATH")
}

// isExecNotFound returns true when err is an "executable not found" RPC error.
func isExecNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "executable file not found") || strings.Contains(s, "no such file or directory")
}

// sbuilder is a minimal io.Writer that collects stderr for error messages.
type sbuilder struct {
	buf []byte
}

func (s *sbuilder) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	return len(p), nil
}

func (s *sbuilder) String() string { return string(s.buf) }

// Ensure sbuilder satisfies io.Writer at compile time.
var _ io.Writer = (*sbuilder)(nil)

// VCPUs returns the effective vCPU count for a BuilderVMSpec, substituting
// DefaultBuilderVCPUs when the spec field is zero.
func VCPUs(spec BuilderVMSpec) uint8 {
	if spec.VCPUs == 0 {
		return DefaultBuilderVCPUs
	}
	return spec.VCPUs
}

// MemMiB returns the effective guest memory for a BuilderVMSpec, substituting
// DefaultBuilderMemMiB when the spec field is zero.
func MemMiB(spec BuilderVMSpec) uint16 {
	if spec.MemoryMiB == 0 {
		return DefaultBuilderMemMiB
	}
	return spec.MemoryMiB
}
