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

	wireGovernorAxes(g, r, r, bounds, true /*hasDisk*/, 0 /*diskIndex*/)

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
	wireGovernorAxes(g, r, r, resize.Bounds{}, false /*hasDisk*/, 0)

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
	wireGovernorAxes(g, r, r, bounds, false /*hasDisk*/, 0)

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
	wireGovernorAxes(g, r, r, bounds, true /*hasDisk*/, 0)

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
	// hasDisk=false: disk axis must NOT register even though DiskMaxBytes is set.
	wireGovernorAxes(g, r, r, bounds, false /*hasDisk*/, 0)

	if got := g.AxisCount(); got != 1 {
		t.Fatalf("DiskMaxBytes set but hasDisk=false: AxisCount = %d, want 1 (mem only)", got)
	}
}

// ── buildSupervisorArgv forward-trace tests ───────────────────────────────────
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
	const wantCmdline = "root=/dev/vda rw init=/sbin/nexus3-agent console=ttyS0 -- --workspace-mount=/dev/vdb:/workspace/repo:ext4:false:true --auto-resize --mem-ceiling=4294967296"
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
			ExtraDisks:         []string{"/data/ws.raw"},
			Cmdline:            wantCmdline,
		},
	}
	argv := buildSupervisorArgv(cfg)

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
	argv := buildSupervisorArgv(cfg)

	for _, flag := range []string{"--gov-mem-min", "--gov-mem-max", "--gov-vcpu-min", "--gov-vcpu-max", "--gov-disk-max"} {
		if slices.Contains(argv, flag) {
			t.Errorf("argv contains %q for zero bounds; passive-mode path must omit gov flags", flag)
		}
	}
	if slices.Contains(argv, "--workspace-disk-index") {
		t.Error("argv contains --workspace-disk-index when HasWorkspaceDisk=false")
	}
	if slices.Contains(argv, "--extra-disk") {
		t.Error("argv contains --extra-disk when ExtraDisks is empty")
	}
	if slices.Contains(argv, "--cmdline") {
		t.Error("argv contains --cmdline when Cmdline is empty; passive-mode path must omit it")
	}
}
