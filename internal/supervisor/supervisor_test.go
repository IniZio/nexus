package supervisor

import (
	"context"
	"slices"
	"strconv"
	"testing"

	"github.com/IniZio/nexus3/internal/core/govern"
	"github.com/IniZio/nexus3/internal/core/resize"
)

// testAllResizer is a minimal stub that satisfies resize.MemoryResizer,
// resize.CPUResizer, and resize.DiskResizer. All methods are no-ops that
// record the most recent call so the caller can verify side-effects.
type testAllResizer struct {
	mem   int64
	vcpus int32
}

func (r *testAllResizer) ResizeMemory(_ context.Context, b int64) (int64, error) {
	r.mem = b
	return b, nil
}
func (r *testAllResizer) CurrentMemoryBytes() int64 { return r.mem }

func (r *testAllResizer) ResizeCPU(_ context.Context, v int32) (int32, error) {
	r.vcpus = v
	return v, nil
}
func (r *testAllResizer) CurrentVCPUs() int32 { return r.vcpus }

func (r *testAllResizer) GrowDisk(_ context.Context, _ int, _ int64) error { return nil }

// testTelemetry is a TelemetrySource that returns an empty sample (no error).
type testTelemetry struct{}

func (t *testTelemetry) Poll(_ context.Context) (resize.Sample, error) {
	return resize.Sample{}, nil
}

// newTestGovernorForWiring creates a Governor with fake resizer and telemetry
// for use in wireGovernorAxes unit tests.
func newTestGovernorForWiring(t *testing.T, bounds resize.Bounds) (*govern.Governor, *testAllResizer) {
	t.Helper()
	r := &testAllResizer{vcpus: 1, mem: 512 * 1024 * 1024}
	g := govern.New(govern.Config{
		Resizer:   r,
		Telemetry: &testTelemetry{},
		Bounds:    bounds,
	})
	return g, r
}

// TestWireGovernorAxes_AllBounds verifies that all three axes (memory + CPU +
// disk) are registered when every bounds field is configured and HasWorkspaceDisk
// is true.
//
// This test is the guard against silently un-wiring an axis: removing either
// govern.NewCPUAxis or govern.NewDiskAxis from wireGovernorAxes causes
// AxisCount() to return 2 instead of 3, failing the assertion below.
//
// It would also have caught the earlier GovBounds defect (zero callers of the
// bounds-carrying constructor) — the same pattern of "code exists but is never
// invoked on the production path."
func TestWireGovernorAxes_AllBounds(t *testing.T) {
	bounds := resize.Bounds{
		MemMinBytes:  512 << 20,
		MemMaxBytes:  4096 << 20,
		VCPUMin:      1,
		VCPUMax:      4,
		DiskMaxBytes: 100 << 30,
	}
	g, r := newTestGovernorForWiring(t, bounds)

	// govern.New always registers exactly one axis (the memory axis).
	if got := g.AxisCount(); got != 1 {
		t.Fatalf("before wiring: AxisCount = %d, want 1 (memory axis only)", got)
	}

	wireGovernorAxes(g, r, r, bounds, []int{0})

	// After wiring: memory (built-in) + CPU + disk = 3.
	const wantAxes = 3
	if got := g.AxisCount(); got != wantAxes {
		t.Fatalf("after wiring all bounds: AxisCount = %d, want %d (mem+cpu+disk)", got, wantAxes)
	}
}

// TestWireGovernorAxes_NoBounds verifies that wireGovernorAxes adds no axes
// when bounds are zero (the default-off configuration). govern.New always
// registers the memory axis; no extra axes must appear.
func TestWireGovernorAxes_NoBounds(t *testing.T) {
	g, r := newTestGovernorForWiring(t, resize.Bounds{})
	wireGovernorAxes(g, r, r, resize.Bounds{}, nil)

	if got := g.AxisCount(); got != 1 {
		t.Fatalf("zero bounds: AxisCount = %d, want 1 (memory axis only)", got)
	}
}

// TestWireGovernorAxes_CPUOnlyNoDisk verifies that only the CPU axis is added
// when CPU bounds are configured but disk is disabled.
func TestWireGovernorAxes_CPUOnlyNoDisk(t *testing.T) {
	bounds := resize.Bounds{
		MemMinBytes: 512 << 20,
		MemMaxBytes: 4096 << 20,
		VCPUMin:     1,
		VCPUMax:     4,
		// DiskMaxBytes deliberately zero — disk axis must not register.
	}
	g, r := newTestGovernorForWiring(t, bounds)
	wireGovernorAxes(g, r, r, bounds, nil)

	if got := g.AxisCount(); got != 2 {
		t.Fatalf("CPU-only bounds: AxisCount = %d, want 2 (mem+cpu)", got)
	}
}

// TestWireGovernorAxes_DiskNoCPU verifies that only the disk axis is added
// when disk bounds are configured but CPU bounds are not.
func TestWireGovernorAxes_DiskNoCPU(t *testing.T) {
	bounds := resize.Bounds{
		MemMinBytes:  512 << 20,
		MemMaxBytes:  4096 << 20,
		DiskMaxBytes: 100 << 30,
		// VCPUMin/VCPUMax deliberately zero — CPU axis must not register.
	}
	g, r := newTestGovernorForWiring(t, bounds)
	wireGovernorAxes(g, r, r, bounds, []int{0})

	if got := g.AxisCount(); got != 2 {
		t.Fatalf("disk-only bounds: AxisCount = %d, want 2 (mem+disk)", got)
	}
}

// TestWireGovernorAxes_DiskNotRegisteredWithoutHasDisk verifies the safety
// gate: even when DiskMaxBytes > 0, the disk axis is NOT registered if
// HasWorkspaceDisk is false. This prevents GrowDisk from targeting
// ExtraDisks[0] when no workspace disk is attached (data loss if wrong).
func TestWireGovernorAxes_DiskNotRegisteredWithoutHasDisk(t *testing.T) {
	bounds := resize.Bounds{
		MemMinBytes:  512 << 20,
		MemMaxBytes:  4096 << 20,
		DiskMaxBytes: 100 << 30,
	}
	g, r := newTestGovernorForWiring(t, bounds)
	// diskIndices=nil: disk axis must NOT register even though DiskMaxBytes is set.
	wireGovernorAxes(g, r, r, bounds, nil)

	if got := g.AxisCount(); got != 1 {
		t.Fatalf("DiskMaxBytes set but hasDisk=false: AxisCount = %d, want 1 (mem only)", got)
	}
}

// TestWireGovernorAxes_MultiDisk verifies that N DiskAxis instances are
// registered when diskIndices has N entries. Two indices must produce
// mem+cpu+disk1+disk2 = 4 axes total (with CPU bounds also set).
func TestWireGovernorAxes_MultiDisk(t *testing.T) {
	bounds := resize.Bounds{
		MemMinBytes:  512 << 20,
		MemMaxBytes:  4096 << 20,
		VCPUMin:      1,
		VCPUMax:      4,
		DiskMaxBytes: 100 << 30,
	}
	g, r := newTestGovernorForWiring(t, bounds)
	wireGovernorAxes(g, r, r, bounds, []int{1, 2})

	// memory (built-in) + CPU + disk@1 + disk@2 = 4 axes.
	const wantAxes = 4
	if got := g.AxisCount(); got != wantAxes {
		t.Fatalf("multi-disk wiring: AxisCount = %d, want %d (mem+cpu+disk1+disk2)", got, wantAxes)
	}
}

// TestWireGovernorAxes_EmptyIndices verifies that no disk axis is registered
// when diskIndices is nil (the default-off configuration).
func TestWireGovernorAxes_EmptyIndices(t *testing.T) {
	bounds := resize.Bounds{
		MemMinBytes:  512 << 20,
		MemMaxBytes:  4096 << 20,
		DiskMaxBytes: 100 << 30,
	}
	g, r := newTestGovernorForWiring(t, bounds)
	wireGovernorAxes(g, r, r, bounds, nil)

	if got := g.AxisCount(); got != 1 {
		t.Fatalf("nil diskIndices: AxisCount = %d, want 1 (mem only)", got)
	}
}

// ── BuildSupervisorArgv forward-trace tests ───────────────────────────────────
//
// These tests close the "value has no production origin" defect class by
// asserting that a SpawnConfig with realistic bounds actually produces the
// expected --gov-mem-max / --workspace-disk-index flags in the supervisor argv.
// Removing GovBounds or HasWorkspaceDisk from the SpawnConfig (as the old orca
// path did) causes these tests to fail — that is the intended bite.

// TestBuildSupervisorArgv_GovBoundsForwarded verifies that non-zero GovBounds
// produce the corresponding --gov-* flags in the supervisor argv.
// This is the primary forward-trace assertion: if cmd_orca.go fails to set
// GovBounds in the SpawnConfig, the argv will lack --gov-mem-max and the
// governor will exit with bounds_not_configured on every boot.
func TestBuildSupervisorArgv_GovBoundsForwarded(t *testing.T) {
	// wantCmdline must track what cmd_sandbox.go produces via workspaceMountCmdline +
	// autoResizePID1Args. Auto-resize is unconditional; the only PID-1 token it
	// appends is --mem-ceiling=<bytes>. If either helper changes its output, this
	// constant must change too (the test asserts passthrough, not construction).
	const wantCmdline = "root=/dev/vda rw init=/sbin/nexus3-agent console=ttyS0 -- --workspace-mount=/dev/vdb:/workspace/repo:ext4:false:true --mem-ceiling=4294967296"
	cfg := SpawnConfig{
		Config: Config{
			SandboxRef: "abc123",
			StoreRoot:  "/store",
			StateDir:   "/state",
			CHBin:      "/usr/bin/cloud-hypervisor",
			SocketDir:  "/run/nexus3",
			KernelPath: "/boot/vmlinux",
			DiskPath:   "/data/sb.raw",
			GovBounds: resize.Bounds{
				MemMinBytes:  512 << 20,
				MemMaxBytes:  4096 << 20,
				VCPUMin:      1,
				VCPUMax:      4,
				DiskMaxBytes: 100 << 30,
			},
			BootVCPUs:          1,
			HasWorkspaceDisk:   true,
			WorkspaceDiskIndex: 0,
			WorkspaceGuestPath: "/workspace/proj",
			ExtraDisks:         []string{"/data/ws.raw"},
			Cmdline:            wantCmdline,
		},
	}
	argv := BuildSupervisorArgv(cfg)

	// Helper: find --flag value in argv.
	findFlag := func(flag string) (string, bool) {
		for i, a := range argv {
			if a == flag && i+1 < len(argv) {
				return argv[i+1], true
			}
		}
		return "", false
	}

	// GovBounds must be present — these are zero when GovBounds is not forwarded.
	if v, ok := findFlag("--gov-mem-max"); !ok {
		t.Error("argv missing --gov-mem-max; GovBounds not forwarded to supervisor")
	} else if v != strconv.FormatInt(4096<<20, 10) {
		t.Errorf("--gov-mem-max = %q, want %d", v, int64(4096<<20))
	}
	if _, ok := findFlag("--gov-mem-min"); !ok {
		t.Error("argv missing --gov-mem-min")
	}
	if _, ok := findFlag("--gov-vcpu-min"); !ok {
		t.Error("argv missing --gov-vcpu-min")
	}
	if _, ok := findFlag("--gov-vcpu-max"); !ok {
		t.Error("argv missing --gov-vcpu-max")
	}
	if _, ok := findFlag("--gov-disk-max"); !ok {
		t.Error("argv missing --gov-disk-max")
	}

	// BootVCPUs must be present.
	if _, ok := findFlag("--boot-vcpus"); !ok {
		t.Error("argv missing --boot-vcpus; BootVCPUs not forwarded")
	}

	// HasWorkspaceDisk=true must produce --workspace-disk-index.
	if v, ok := findFlag("--workspace-disk-index"); !ok {
		t.Error("argv missing --workspace-disk-index; HasWorkspaceDisk not forwarded")
	} else if v != "0" {
		t.Errorf("--workspace-disk-index = %q, want 0", v)
	}

	// WorkspaceGuestPath must produce --workspace-guest-path (GIT-SEED).
	if v, ok := findFlag("--workspace-guest-path"); !ok {
		t.Error("argv missing --workspace-guest-path; WorkspaceGuestPath not forwarded")
	} else if v != "/workspace/proj" {
		t.Errorf("--workspace-guest-path = %q, want /workspace/proj", v)
	}

	// ExtraDisks must be forwarded as --extra-disk flags.
	if !slices.Contains(argv, "--extra-disk") {
		t.Error("argv missing --extra-disk; ExtraDisks not forwarded to supervisor")
	}
	if idx := slices.Index(argv, "--extra-disk"); idx >= 0 && argv[idx+1] != "/data/ws.raw" {
		t.Errorf("--extra-disk value = %q, want /data/ws.raw", argv[idx+1])
	}

	// Cmdline must be forwarded via --cmdline when non-empty.
	if v, ok := findFlag("--cmdline"); !ok {
		t.Error("argv missing --cmdline; Cmdline not forwarded to supervisor")
	} else if v != wantCmdline {
		t.Errorf("--cmdline = %q, want %q", v, wantCmdline)
	}
}

// ── awaitShutdown mode tests ──────────────────────────────────────────────────
//
// awaitShutdown is the extracted SELECT loop from RunDetached step 7. These
// tests verify the two shutdown triggers without needing a real VM or process.

// TestAwaitShutdown_StopVerb verifies that closing the stopCh (i.e. the caller
// sends POST /supervisor/stop) returns shutdownByStopVerb. This is the
// completion signal for ephemeral mode and the stop-on-request path for
// long-lived mode.
func TestAwaitShutdown_StopVerb(t *testing.T) {
	stopCh := make(chan struct{})
	ctx := context.Background()

	// Close the stop channel to simulate POST /supervisor/stop.
	close(stopCh)

	got := awaitShutdown(ctx, stopCh)
	if got != shutdownByStopVerb {
		t.Errorf("awaitShutdown with closed stopCh = %v, want shutdownByStopVerb (%v)", got, shutdownByStopVerb)
	}
}

// TestAwaitShutdown_Signal verifies that cancelling the context (i.e. OS
// delivers SIGTERM/SIGINT) returns shutdownBySignal. This is the primary exit
// path for long-lived persistent-perimeter supervisors.
func TestAwaitShutdown_Signal(t *testing.T) {
	stopCh := make(chan struct{}) // never closed — stop verb not sent
	ctx, cancel := context.WithCancel(context.Background())

	// Simulate SIGTERM by cancelling the context.
	cancel()

	got := awaitShutdown(ctx, stopCh)
	if got != shutdownBySignal {
		t.Errorf("awaitShutdown with cancelled ctx = %v, want shutdownBySignal (%v)", got, shutdownBySignal)
	}
}

// TestAwaitShutdown_BothReadyNoDeadlock verifies that when both the stop
// channel and the signal context are already closed/cancelled before
// awaitShutdown is called, the function returns promptly without deadlock or
// panic. Go's select is non-deterministic when multiple cases are ready, so
// either shutdownCause is a valid outcome; the assertion merely confirms the
// returned value is one of the two defined constants.
func TestAwaitShutdown_BothReadyNoDeadlock(t *testing.T) {
	stopCh := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	close(stopCh)
	cancel()

	got := awaitShutdown(ctx, stopCh)
	if got != shutdownByStopVerb && got != shutdownBySignal {
		t.Errorf("awaitShutdown with both ready = %v, want shutdownByStopVerb or shutdownBySignal", got)
	}
}

// ── Ephemeral flag forwarding tests ─────────────────────────────────────────

// TestBuildSupervisorArgv_EphemeralForwarded verifies that Ephemeral=true in
// SpawnConfig produces --ephemeral in the supervisor argv.
func TestBuildSupervisorArgv_EphemeralForwarded(t *testing.T) {
	cfg := SpawnConfig{
		Config: Config{
			SandboxRef: "abc123",
			StoreRoot:  "/store",
			StateDir:   "/state",
			CHBin:      "/usr/bin/cloud-hypervisor",
			SocketDir:  "/run/nexus3",
			KernelPath: "/boot/vmlinux",
			DiskPath:   "/data/sb.raw",
			Ephemeral:  true,
		},
	}
	argv := BuildSupervisorArgv(cfg)
	if !slices.Contains(argv, "--ephemeral") {
		t.Error("argv missing --ephemeral when Config.Ephemeral=true")
	}
}

// TestBuildSupervisorArgv_NotEphemeralOmitsFlag verifies that Ephemeral=false
// (the default) produces NO --ephemeral flag. This ensures existing long-lived
// supervisor spawns are byte-identical after the change.
func TestBuildSupervisorArgv_NotEphemeralOmitsFlag(t *testing.T) {
	cfg := SpawnConfig{
		Config: Config{
			SandboxRef: "abc123",
			StoreRoot:  "/store",
			StateDir:   "/state",
			CHBin:      "/usr/bin/cloud-hypervisor",
			SocketDir:  "/run/nexus3",
			KernelPath: "/boot/vmlinux",
			DiskPath:   "/data/sb.raw",
			// Ephemeral: false (zero value) — default long-lived mode.
		},
	}
	argv := BuildSupervisorArgv(cfg)
	if slices.Contains(argv, "--ephemeral") {
		t.Error("argv contains --ephemeral for non-ephemeral config; long-lived mode must not emit this flag")
	}
}

// TestBuildSupervisorArgv_ParentPipeFDForwarded verifies that a non-zero
// ParentPipeFD in Config produces --parent-pipe-fd <n> in the argv. This
// ensures the supervisor receives the watchdog fd when spawned in ephemeral
// mode — without which SIGKILL on the CLI would orphan the VM indefinitely.
func TestBuildSupervisorArgv_ParentPipeFDForwarded(t *testing.T) {
	cfg := SpawnConfig{
		Config: Config{
			SandboxRef:   "abc123",
			StoreRoot:    "/store",
			StateDir:     "/state",
			CHBin:        "/usr/bin/cloud-hypervisor",
			SocketDir:    "/run/nexus3",
			KernelPath:   "/boot/vmlinux",
			DiskPath:     "/data/sb.raw",
			Ephemeral:    true,
			ParentPipeFD: 3,
		},
	}
	argv := BuildSupervisorArgv(cfg)
	findFlag := func(flag string) (string, bool) {
		for i, a := range argv {
			if a == flag && i+1 < len(argv) {
				return argv[i+1], true
			}
		}
		return "", false
	}
	v, ok := findFlag("--parent-pipe-fd")
	if !ok {
		t.Error("argv missing --parent-pipe-fd when ParentPipeFD=3")
	} else if v != "3" {
		t.Errorf("--parent-pipe-fd = %q, want \"3\"", v)
	}
}

// TestBuildSupervisorArgv_ZeroParentPipeFDOmitsFlag verifies that the zero
// value (no watchdog pipe) produces no --parent-pipe-fd flag — ensuring the
// non-ephemeral orca path is byte-identical after the change.
func TestBuildSupervisorArgv_ZeroParentPipeFDOmitsFlag(t *testing.T) {
	cfg := SpawnConfig{
		Config: Config{
			SandboxRef: "abc123",
			StoreRoot:  "/store",
			StateDir:   "/state",
			CHBin:      "/usr/bin/cloud-hypervisor",
			SocketDir:  "/run/nexus3",
			KernelPath: "/boot/vmlinux",
			DiskPath:   "/data/sb.raw",
			// ParentPipeFD: 0 (zero value) — no watchdog.
		},
	}
	argv := BuildSupervisorArgv(cfg)
	if slices.Contains(argv, "--parent-pipe-fd") {
		t.Error("argv contains --parent-pipe-fd for non-ephemeral config with zero ParentPipeFD")
	}
}

// TestBuildSupervisorArgv_MemoryForwarded verifies that a non-zero MemoryMiB
// produces --memory <MiB> in the supervisor argv.
// This is the forward-trace assertion for the regression where MemoryMiB was
// set by the builder to 8192 but the flag was never emitted, causing every
// supervisor-spawned VM to boot at the supervisor default (512 MiB) instead.
func TestBuildSupervisorArgv_MemoryForwarded(t *testing.T) {
	cfg := SpawnConfig{
		Config: Config{
			SandboxRef: "abc123",
			StoreRoot:  "/store",
			StateDir:   "/state",
			CHBin:      "/usr/bin/cloud-hypervisor",
			SocketDir:  "/run/nexus3",
			KernelPath: "/boot/vmlinux",
			DiskPath:   "/data/sb.raw",
			MemoryMiB:  8192,
		},
	}
	argv := BuildSupervisorArgv(cfg)
	findFlag := func(flag string) (string, bool) {
		for i, a := range argv {
			if a == flag && i+1 < len(argv) {
				return argv[i+1], true
			}
		}
		return "", false
	}
	v, ok := findFlag("--memory")
	if !ok {
		t.Fatal("argv missing --memory; MemoryMiB not forwarded to supervisor (regression: VM would boot at 512 MiB instead of 8192 MiB)")
	}
	if v != "8192" {
		t.Errorf("--memory = %q, want \"8192\"", v)
	}
}

// TestBuildSupervisorArgv_ZeroMemoryOmitsFlag verifies that MemoryMiB=0
// produces NO --memory flag, preserving the supervisor's own default (512 MiB).
// This ensures existing callers that do not set MemoryMiB are byte-identical.
func TestBuildSupervisorArgv_ZeroMemoryOmitsFlag(t *testing.T) {
	cfg := SpawnConfig{
		Config: Config{
			SandboxRef: "abc123",
			StoreRoot:  "/store",
			StateDir:   "/state",
			CHBin:      "/usr/bin/cloud-hypervisor",
			SocketDir:  "/run/nexus3",
			KernelPath: "/boot/vmlinux",
			DiskPath:   "/data/sb.raw",
			// MemoryMiB: 0 (zero value) — supervisor default applies.
		},
	}
	argv := BuildSupervisorArgv(cfg)
	if slices.Contains(argv, "--memory") {
		t.Error("argv contains --memory for zero MemoryMiB; supervisor default must apply (zero must be omitted)")
	}
}

// TestBuildSupervisorArgv_ZeroBoundsNoGovFlags verifies that zero GovBounds
// produce NO --gov-* flags — the supervisor's passive-mode defaults apply.
// This preserves argv byte-identity for sandboxes with auto-resize off.
func TestBuildSupervisorArgv_ZeroBoundsNoGovFlags(t *testing.T) {
	cfg := SpawnConfig{
		Config: Config{
			SandboxRef: "abc123",
			StoreRoot:  "/store",
			StateDir:   "/state",
			CHBin:      "/usr/bin/cloud-hypervisor",
			SocketDir:  "/run/nexus3",
			KernelPath: "/boot/vmlinux",
			DiskPath:   "/data/sb.raw",
			// GovBounds: zero — passive mode.
			// HasWorkspaceDisk: false — no disk axis.
		},
	}
	argv := BuildSupervisorArgv(cfg)

	for _, flag := range []string{"--gov-mem-min", "--gov-mem-max", "--gov-vcpu-min", "--gov-vcpu-max", "--gov-disk-max"} {
		if slices.Contains(argv, flag) {
			t.Errorf("argv contains %q for zero bounds; passive-mode path must omit gov flags", flag)
		}
	}
	if slices.Contains(argv, "--workspace-disk-index") {
		t.Error("argv contains --workspace-disk-index when HasWorkspaceDisk=false")
	}
	if slices.Contains(argv, "--workspace-guest-path") {
		t.Error("argv contains --workspace-guest-path when WorkspaceGuestPath is empty")
	}
	if slices.Contains(argv, "--extra-disk") {
		t.Error("argv contains --extra-disk when ExtraDisks is empty")
	}
	if slices.Contains(argv, "--cmdline") {
		t.Error("argv contains --cmdline when Cmdline is empty; passive-mode path must omit it")
	}
}

// TestBuildSupervisorDriverConfig_ForwardsBootVCPUs asserts that the vCPU count
// the supervisor was started with reaches the DRIVER as boot_vcpus.
//
// The pre-existing argv test (--boot-vcpus present in the spawn argv) asserted
// only that the value was TRANSPORTED to the supervisor process. It passed for
// the entire time the value was being dropped on the floor: BootVCPUs was
// parsed, handed to the resize governor, and never placed in the driver config,
// so every supervisor-backed sandbox booted with one vCPU regardless of --vcpus.
// Transport and effect are different claims and need different assertions.
func TestBuildSupervisorDriverConfig_ForwardsBootVCPUs(t *testing.T) {
	cfg := Config{
		CHBin:      "/usr/bin/cloud-hypervisor",
		SocketDir:  "/run/user/1000/n3",
		KernelPath: "/k",
		DiskPath:   "/d",
		MemoryMiB:  4096,
		BootVCPUs:  6,
	}

	got := buildSupervisorDriverConfig(cfg, 16384, 24, nil)

	if got.VCPUs != 6 {
		t.Errorf("driver VCPUs = %d, want 6 — boot_vcpus never reached the driver, "+
			"so the guest boots on the driver's 1-vCPU default", got.VCPUs)
	}
	// Memory is the control: it was always forwarded correctly, so a failure
	// here means the extraction broke something rather than the vCPU wiring.
	if got.MemoryMiB != 4096 {
		t.Errorf("driver MemoryMiB = %d, want 4096", got.MemoryMiB)
	}
	// VCPUMax must stay independent of VCPUs: it is the hotplug ceiling, and
	// collapsing the two is what makes the bug invisible (slots exist, and no
	// CPU is ever present in them).
	if got.VCPUMax != 24 {
		t.Errorf("driver VCPUMax = %d, want 24", got.VCPUMax)
	}
}

// TestBuildSupervisorDriverConfig_FreePageReportingEnabled is a regression
// test for the Phase 1 memory-reclaim fix.
//
// Before the fix, FreePageReporting was never set in the config returned by
// buildSupervisorDriverConfig.  cloud-hypervisor therefore started with no
// virtio-balloon device (vm.info showed "balloon: null"), which meant idle
// sandboxes had no channel to return memory to the host — live measurement
// showed only ~5 % reclaim.  With FreePageReporting: true the guest attaches
// a zero-size balloon with deflate_on_oom + free_page_reporting, and idle
// reclaim rises to ~92 % within 90 s.
//
// The builder VM config is a separate code path
// (buildBuilderSupervisorDriverConfig or equivalent) and deliberately does
// NOT set FreePageReporting; this test is scoped to buildSupervisorDriverConfig
// which backs agent sandboxes only.
func TestBuildSupervisorDriverConfig_FreePageReportingEnabled(t *testing.T) {
	cfg := Config{
		CHBin:      "/usr/bin/cloud-hypervisor",
		SocketDir:  "/run/user/1000/n3",
		KernelPath: "/k",
		DiskPath:   "/d",
		MemoryMiB:  4096,
		BootVCPUs:  2,
	}

	got := buildSupervisorDriverConfig(cfg, 16384, 8, nil)

	if !got.FreePageReporting {
		t.Errorf("driver FreePageReporting = false, want true — "+
			"without this the guest has no virtio-balloon device and idle "+
			"sandboxes cannot return RAM to the host (regression: balloon:null, ~5%% reclaim)")
	}
}
