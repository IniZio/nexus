package builder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/perimeter/netfilter"
	"github.com/IniZio/nexus3/internal/core/perimeter/netstack"
)

// BuilderDriver is the subset of driver capabilities required by [BuildInVM].
// A [*cloudhypervisor.CHDriver] satisfies this interface; the interface is
// defined here so tests can inject a fake without importing the concrete driver.
//
// # Injection seam for UNI-WIRE
//
// The boot mechanism (drv.Start) is already injectable through this interface.
// UNI-WIRE replaces the concrete CH driver with a supervisor-backed
// implementation so that [BuildInVM] launches the VM under a detached
// supervisor process without any further rewrite of this function.
type BuilderDriver interface {
	driver.Driver
	driver.GuestDialer
}

// BuilderStore is the minimal persistence interface required by [BuildInVM] to
// track the builder VM's lifetime. When non-nil, BuildInVM creates a transient
// sandbox record before boot and deletes it on every exit path — success,
// failure, panic, and context cancellation.
//
// A *store.FileStore satisfies this interface. Pass nil to run without any
// persistent record (unit tests / legacy callers that have not yet been wired
// to a real store).
type BuilderStore interface {
	// Create persists a new sandbox record. Called once before VM boot.
	Create(ctx context.Context, sb domain.Sandbox) error
	// Update performs a locked read-modify-write on the record. Called once
	// after a successful boot to stamp the instanceID and Running state.
	Update(ctx context.Context, id domain.SandboxID, fn func(*domain.Sandbox) error) error
	// Delete removes the record. Called on every exit path.
	Delete(ctx context.Context, id domain.SandboxID) error
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
// When st is non-nil, a transient sandbox record is created before boot and
// deleted on every exit path above. The record allows a supervisor process to
// discover and re-own the running builder VM. It is always removed before
// BuildInVM returns — callers see no residual record in the sandbox list.
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
	st BuilderStore,
) (digest string, err error) {
	id := domain.NewSandboxID()

	// ── 0. Persist transient record ───────────────────────────────────────────
	// The record exists only while the builder VM runs: a supervisor can
	// discover and re-own the VM by reading this record. It is unconditionally
	// deleted on every exit path — success, failure, panic, context
	// cancellation — because the builder is ephemeral and must not appear in
	// `nexus3 sandbox list` beyond its own lifetime.
	//
	// Defer ordering (LIFO): delete-record is registered FIRST so it executes
	// LAST, after panicSafeStop. This guarantees the VM is already stopped
	// before its record is removed.
	if st != nil {
		transient := domain.Sandbox{
			ID:           id,
			Name:         id.String(),
			Project:      "__builder",
			State:        domain.Created,
			RemoveOnExit: true,        // marks this record as transient / ephemeral
			CreatorPID:   os.Getpid(), // used by service.List to reap stale orphans

			// The builder VM needs wildcard egress: buildkitd resolves and
			// pulls arbitrary base images and package indexes, so no finite
			// allowlist can describe it.
			//
			// This MUST be stated on the record. In production the driver is
			// the CLI's supervisorBuilderDriver, which boots the VM under a
			// DETACHED supervisor process; that supervisor builds the
			// perimeter by reading this record's Envelope back from the
			// store. A zero Envelope means OpenEgress=false, and since
			// D-PD-33 an empty AllowedHosts no longer implies allow-all — so
			// the builder VM got a default-deny perimeter and every registry
			// connection was refused. DNS still resolved, which made the
			// failure read like a network fault rather than a policy one.
			//
			// This opens egress for the EPHEMERAL BUILDER VM only. It does
			// not touch the egress policy of the sandbox being built, which
			// is configured separately after this builder VM exits. The
			// builder supervisor is spawned with no CredsFile, so no
			// credential is ever seeded into the builder guest.
			Envelope: domain.Envelope{OpenEgress: true},
		}
		if createErr := st.Create(ctx, transient); createErr != nil {
			return "", fmt.Errorf("builder vm: persist transient record: %w", createErr)
		}
		defer func() {
			// Always use background context: the caller's ctx may be cancelled
			// before we reach cleanup, but the record must always be removed.
			_ = st.Delete(context.Background(), id)
		}()
	}

	lc := newLifecycle(
		func(stopCtx context.Context) error { return drv.Stop(stopCtx, id) },
		func(syncCtx context.Context) error { return guestSync(syncCtx, execFn) },
	)

	// Cover panic / early-exit that occurs before the explicit SyncAndStop.
	// Registered AFTER the delete-record defer (LIFO), so it executes BEFORE
	// delete — the VM is stopped before its record is removed.
	started := false
	defer func() {
		if started {
			lc.panicSafeStop()
		}
	}()

	// ── 1. Boot the builder VM ────────────────────────────────────────────────
	instanceID, startErr := drv.Start(ctx, driver.StartRequest{SandboxID: id})
	if startErr != nil {
		// The cache disks were never attached to the VM, so no guest writes
		// could be in flight. The dirty markers set by ensureCacheDiskAt are
		// a false alarm — clear them so the next build can reuse warm cache
		// rather than wiping a healthy disk.
		for _, cd := range spec.CacheDisks {
			if err := markCacheDiskClean(cd.ImagePath); err != nil {
				log.Printf("builder vm: clear dirty marker after start failure %s: %v", cd.ImagePath, err)
			}
		}
		return "", fmt.Errorf("builder vm: start: %w", startErr)
	}
	started = true

	// ── 1.25. Stamp instanceID into the transient record ─────────────────────
	// Non-fatal: a supervisor that observes State=Created will retry. The
	// critical invariant is that the record EXISTS (ensuring delete on exit),
	// not that it always reflects the Running state immediately after boot.
	if st != nil {
		_ = st.Update(context.Background(), id, func(rec *domain.Sandbox) error {
			rec.InstanceID = instanceID
			rec.State = domain.Running
			return nil
		})
	}

	// ── 1.5a. Start perimeter so the builder VM has internet access ───────────
	// CHDriver.Start always launches StartNetnsRuntime: a TAP/bridge pump that
	// forwards guest Ethernet frames to rt.PerimConn (AF_UNIX socketpair).
	// Without a consumer, frames are dropped → DNS timeouts and no network.
	// Start a wildcard-egress netstack perimeter (same code path as regular
	// sandboxes, no MITM, passthrough dialer) so buildkitd can pull base images.
	//
	// NOTE: this in-process perimeter only runs for drivers that implement
	// driver.NetworkHook. The production CLI driver does NOT — it delegates to
	// a detached supervisor that owns the perimeter itself, driven by the
	// transient record's Envelope set above. Do not read this block as the
	// thing that gives the builder its egress; for `nexus3 sandbox create
	// --file` it never executes.
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
		// The cache disks were attached to the VMM but the guest agent never
		// responded — no build commands were issued, so no cache writes could
		// be pending. Clear the dirty markers so the next build reuses the
		// warm disk instead of wiping it.
		for _, cd := range spec.CacheDisks {
			if err := markCacheDiskClean(cd.ImagePath); err != nil {
				log.Printf("builder vm: clear dirty marker after agent wait failure %s: %v", cd.ImagePath, err)
			}
		}
		return "", fmt.Errorf("builder vm: wait for agent: %w", waitErr)
	}

	// ── 2. Run the in-guest build ─────────────────────────────────────────────
	// Exec nexus3-agent --builder-role inside the VM. The builder role
	// (internal/core/agent/builder_role_linux.go) mounts /dev/vdb, starts
	// buildkitd, solves the Containerfile, writes the rootfs ext4 to /dev/vdc,
	// then calls syscall.Sync(). The exec blocks until the role completes.
	buildErr := guestBuild(ctx, execFn, spec.CacheDisks, spec.ToolRecipe, spec.TargetArch)

	// ── 3. Sync + Stop — always, even when build failed ───────────────────────
	tearErr := lc.SyncAndStop()
	started = false // prevent double-stop in the defer above

	// ── 3.5. Clear the cache-disk fencing marker on confirmed clean sync ──────
	// Both conditions must hold before the marker is cleared:
	//
	//   tearErr == nil  — the guest "sync" exec returned exit 0 AND the VMM
	//                     stopped cleanly: a positive confirmation that every
	//                     attached cache disk's writes reached the host.
	//
	//   ctx.Err() == nil — the caller's context was NOT cancelled. This is
	//                      required because lc.SyncAndStop() deliberately runs
	//                      guestSync on context.Background() (not on the
	//                      caller's ctx), so tearErr alone cannot witness
	//                      caller cancellation. When ctx is cancelled (create-
	//                      timeout expiry or Ctrl-C), the guest may still be
	//                      mid-write when the sync runs; even if sync returns
	//                      exit 0 the flush is not trustworthy. Ratified
	//                      operator decision D-4: "safety wins, a cancelled
	//                      create may go cold."
	//
	// This runs regardless of buildErr: a failed build can still have flushed
	// valid, crash-consistent cache state. If either condition fails the
	// marker is left set, and the next lease wipes the disk instead of
	// risking a poisoned reuse.
	if tearErr == nil && ctx.Err() == nil {
		for _, cd := range spec.CacheDisks {
			if err := markCacheDiskClean(cd.ImagePath); err != nil {
				log.Printf("builder vm: mark cache disk clean %s: %v", cd.ImagePath, err)
			}
		}
	}

	if buildErr != nil {
		return "", fmt.Errorf("builder vm: in-guest build: %w", wrapOutOfSpaceErr(buildErr))
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
//
// When recipe has non-empty Packages, it is JSON-serialised and appended as
// "--tool-recipe=<json>" so that RunBuilderRole forwards it to the
// SolveRequest's ToolRecipe field inside the VM. "--target-arch=<arch>" is
// appended alongside it. Both args are omitted when recipe.Packages is empty
// so that zero-recipe builds produce no spurious cmdline token.
func guestBuild(ctx context.Context, execFn GuestExecFn, cacheDisks []CacheDiskSpec, recipe cred.ToolRecipe, targetArch string) error {
	var stderr sbuilder
	argv := []string{agentInstallPath, "--builder-role"}
	for i, cd := range cacheDisks {
		// ExtraDisks[2+] map to /dev/vdd, /dev/vde, /dev/vdf, ...
		// 'd' + 0 = 'd' (vdd), 'd' + 1 = 'e' (vde), etc.
		dev := fmt.Sprintf("/dev/vd%c", 'd'+i)
		argv = append(argv, fmt.Sprintf("--cache-disk=%s:%s", dev, cd.MountPath))
	}
	if len(recipe.Packages) > 0 {
		recipeJSON, err := json.Marshal(recipe)
		if err != nil {
			return fmt.Errorf("guestBuild: marshal tool recipe: %w", err)
		}
		argv = append(argv,
			"--tool-recipe="+string(recipeJSON),
			"--target-arch="+targetArch,
		)
	}
	exitCode, err := execFn(ctx, argv, &stderr)
	if err != nil {
		if s := stderr.String(); s != "" {
			return fmt.Errorf("exec builder role: %w\nin-guest output:\n%s", err, s)
		}
		return fmt.Errorf("exec builder role: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("builder role exited %d: %s", exitCode, stderr.String())
	}

	// Manifest-channel (mechanism a): forward the collected in-guest stderr
	// to the host log on the success path.
	//
	// The in-guest logRootfsSizeManifest call (buildkit_linux.go) emits
	// rootfs-size-manifest lines BEFORE the integrity gates; they arrive here
	// via the vsock exec pipe into the sbuilder buffer, but guestBuild
	// previously discarded that buffer on success. Forwarding it lets
	// ParseManifestStageA (internal/test/repro/probes.go) produce real per-file
	// probes on a successful build.
	//
	// The sentinel line "manifest-channel: active" lets ParseManifestStageA
	// distinguish two cases that both produce no manifest data entries:
	//   - channel active, logRootfsSizeManifest suppressed → HarnessIntegrityFailure
	//   - channel not deployed (old binary, or build failed) → not_collected
	//
	// W29 owns internal/core/builder/buildkit.go; this file does not overlap.
	log.Printf("in-guest build: manifest-channel: active")
	if s := stderr.String(); s != "" {
		fmt.Fprint(log.Writer(), s) //nolint:errcheck
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
		exitCode, err := execFn(ctx, []string{syncBin}, nil)
		if err != nil {
			// If not found, try the next candidate; otherwise propagate the error.
			if isExecNotFound(err) {
				continue
			}
			return err
		}
		if exitCode != 0 {
			// A non-zero exit from sync means writes did NOT reach the host
			// (e.g. EIO on the virtio-blk device). Treat this as a hard error
			// so the caller does NOT clear the dirty marker and serve a poisoned
			// cache disk as warm.
			return fmt.Errorf("sync exited %d: data may not have reached host", exitCode)
		}
		return nil
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

// wrapOutOfSpaceErr inspects err for known buildkit cache-disk-full signatures
// and, when matched, wraps it with an actionable message. The original error is
// always preserved so callers can use errors.Is / errors.Unwrap.
//
// Matched signatures (case-insensitive):
//   - "no space left on device"  — kernel ENOSPC surfaced by runc or the overlay
//   - "ResourceExhausted"        — gRPC status code buildkit returns for ENOSPC
//
// The bare "/var/lib/buildkit" path clause was removed: the export scratch path
// is now /var/lib/buildkit/nexus3-export, so any error mentioning that path
// (including ErrRootfsHollow) would have been mislabeled "cache disk full".
// The two genuine ENOSPC signals above cover all real disk-full cases.
func wrapOutOfSpaceErr(err error) error {
	if err == nil {
		return nil
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "no space left on device") ||
		strings.Contains(s, "resourceexhausted") {
		return fmt.Errorf(
			"builder vm: buildkit cache disk (/var/lib/buildkit) is full — "+
				"the auto-grow ceiling was reached or growth is disabled; "+
				"a very large build context/COPY can exceed the cache. "+
				"Original: %w",
			err,
		)
	}
	return err
}

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
