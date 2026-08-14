package vmcfg_test

import (
	"fmt"
	"testing"

	"github.com/IniZio/nexus3/internal/core/vmcfg"
)

// mib converts MiB to bytes.
func mib(n int64) int64 { return n * 1024 * 1024 }

// gib converts GiB to bytes.
func gib(n int64) int64 { return n * 1024 * 1024 * 1024 }

// TestResolveDefaultCeilings verifies the 4× rule with driver-default boot
// sizing (BootMemMiB=0, BootVCPUs=0 → treated as 512 MiB / 1 vCPU).
func TestResolveDefaultCeilings(t *testing.T) {
	r := vmcfg.Resolve(vmcfg.Config{})

	// 512 MiB × 4 = 2048 MiB, but floor is 4096 MiB.
	if want := uint32(4096); r.MemoryMaxMiB != want {
		t.Errorf("MemoryMaxMiB: got %d, want %d", r.MemoryMaxMiB, want)
	}
	// 1 vCPU × 4 = 4, floor is 4 → equals floor.
	if want := uint32(4); r.VCPUMax != want {
		t.Errorf("VCPUMax: got %d, want %d", r.VCPUMax, want)
	}

	// Bounds.MemMinBytes == driver default boot (512 MiB)
	if want := mib(512); r.Bounds.MemMinBytes != want {
		t.Errorf("Bounds.MemMinBytes: got %d, want %d", r.Bounds.MemMinBytes, want)
	}
	if want := mib(4096); r.Bounds.MemMaxBytes != want {
		t.Errorf("Bounds.MemMaxBytes: got %d, want %d", r.Bounds.MemMaxBytes, want)
	}
	if want := int32(1); r.Bounds.VCPUMin != want {
		t.Errorf("Bounds.VCPUMin: got %d, want %d", r.Bounds.VCPUMin, want)
	}
	if want := int32(4); r.Bounds.VCPUMax != want {
		t.Errorf("Bounds.VCPUMax: got %d, want %d", r.Bounds.VCPUMax, want)
	}
	// Disk default: 100 GiB
	if want := gib(100); r.Bounds.DiskMaxBytes != want {
		t.Errorf("Bounds.DiskMaxBytes: got %d, want %d", r.Bounds.DiskMaxBytes, want)
	}
}

// TestResolveMemoryFloor verifies the 4096 MiB floor for memory ceiling.
// Subtest table covers the boundary exactly at 1024 MiB boot.
func TestResolveMemoryFloor(t *testing.T) {
	cases := []struct {
		bootMem     uint32
		wantMemMax  uint32
		description string
	}{
		{512, 4096, "default boot (512 MiB): 4×=2048 < 4096, floor applies"},
		{1023, 4096, "1023 MiB: 4×=4092 < 4096, floor applies"},
		{1024, 4096, "1024 MiB: 4×=4096 = floor, no change"},
		{1025, 4100, "1025 MiB: 4×=4100 > 4096, floor does not apply"},
		{2048, 8192, "2048 MiB: 4×=8192, well above floor"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			r := vmcfg.Resolve(vmcfg.Config{BootMemMiB: tc.bootMem})
			if r.MemoryMaxMiB != tc.wantMemMax {
				t.Errorf("BootMemMiB=%d: MemoryMaxMiB got %d, want %d",
					tc.bootMem, r.MemoryMaxMiB, tc.wantMemMax)
			}
			if wantBytes := mib(int64(tc.wantMemMax)); r.Bounds.MemMaxBytes != wantBytes {
				t.Errorf("BootMemMiB=%d: Bounds.MemMaxBytes got %d, want %d",
					tc.bootMem, r.Bounds.MemMaxBytes, wantBytes)
			}
		})
	}
}

// TestResolveVCPUFloor verifies the floor of 4 for vCPU ceiling.
func TestResolveVCPUFloor(t *testing.T) {
	cases := []struct {
		bootCPUs    uint32
		wantVCPUMax uint32
		description string
	}{
		{0, 4, "driver default (0→1 vCPU): 4×=4, equals floor"},
		{1, 4, "1 vCPU: 4×=4, equals floor"},
		{2, 8, "2 vCPUs: 4×=8 > 4, floor does not apply"},
		{4, 16, "4 vCPUs: 4×=16"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.description, func(t *testing.T) {
			r := vmcfg.Resolve(vmcfg.Config{BootVCPUs: tc.bootCPUs})
			if r.VCPUMax != tc.wantVCPUMax {
				t.Errorf("BootVCPUs=%d: VCPUMax got %d, want %d",
					tc.bootCPUs, r.VCPUMax, tc.wantVCPUMax)
			}
			if want := int32(tc.wantVCPUMax); r.Bounds.VCPUMax != want {
				t.Errorf("BootVCPUs=%d: Bounds.VCPUMax got %d, want %d",
					tc.bootCPUs, r.Bounds.VCPUMax, want)
			}
		})
	}
}

// TestResolveExplicitCeilings verifies that non-zero ceiling fields are used
// verbatim (no 4× rule, no floor applied).
func TestResolveExplicitCeilings(t *testing.T) {
	r := vmcfg.Resolve(vmcfg.Config{
		BootMemMiB: 8192,
		BootVCPUs:  4,
		MemMaxMiB:  16384,
		VCPUsMax:   8,
		DiskMaxGiB: 200,
	})

	if want := uint32(16384); r.MemoryMaxMiB != want {
		t.Errorf("MemoryMaxMiB: got %d, want %d", r.MemoryMaxMiB, want)
	}
	if want := uint32(8); r.VCPUMax != want {
		t.Errorf("VCPUMax: got %d, want %d", r.VCPUMax, want)
	}
	if want := mib(16384); r.Bounds.MemMaxBytes != want {
		t.Errorf("Bounds.MemMaxBytes: got %d, want %d", r.Bounds.MemMaxBytes, want)
	}
	if want := int32(8); r.Bounds.VCPUMax != want {
		t.Errorf("Bounds.VCPUMax: got %d, want %d", r.Bounds.VCPUMax, want)
	}
	if want := gib(200); r.Bounds.DiskMaxBytes != want {
		t.Errorf("Bounds.DiskMaxBytes: got %d, want %d", r.Bounds.DiskMaxBytes, want)
	}
}

// TestResolveExplicitDiskDefault verifies DiskMaxGiB=0 → 100 GiB default.
func TestResolveExplicitDiskDefault(t *testing.T) {
	r := vmcfg.Resolve(vmcfg.Config{BootMemMiB: 2048, BootVCPUs: 2})
	if want := gib(100); r.Bounds.DiskMaxBytes != want {
		t.Errorf("DiskMaxBytes: got %d, want %d", r.Bounds.DiskMaxBytes, want)
	}
}

// TestResolvePID1Args verifies the PID-1 cmdline fragment format.
// The leading space is intentional: callers concatenate after "--".
func TestResolvePID1Args(t *testing.T) {
	cases := []struct {
		cfg      vmcfg.Config
		wantArgs string
	}{
		{
			// Driver defaults: boot=512, memMax=4096 MiB
			vmcfg.Config{},
			fmt.Sprintf(" --mem-ceiling=%d", mib(4096)),
		},
		{
			// Explicit 8 GiB ceiling
			vmcfg.Config{BootMemMiB: 2048, MemMaxMiB: 8192},
			fmt.Sprintf(" --mem-ceiling=%d", mib(8192)),
		},
		{
			// Boot=4096, 4×=16384, above floor
			vmcfg.Config{BootMemMiB: 4096},
			fmt.Sprintf(" --mem-ceiling=%d", mib(16384)),
		},
	}

	for _, tc := range cases {
		r := vmcfg.Resolve(tc.cfg)
		if r.PID1Args != tc.wantArgs {
			t.Errorf("PID1Args: got %q, want %q", r.PID1Args, tc.wantArgs)
		}
	}
}

// TestResolvePID1ArgsLeadingSpace asserts the leading space contract explicitly,
// since callers depend on it for correct cmdline assembly:
//
//	diskBootCmdlineBase + " --" + result.PID1Args
//	→ "...base -- --mem-ceiling=..."
func TestResolvePID1ArgsLeadingSpace(t *testing.T) {
	r := vmcfg.Resolve(vmcfg.Config{})
	if len(r.PID1Args) == 0 || r.PID1Args[0] != ' ' {
		t.Errorf("PID1Args must start with a space; got %q", r.PID1Args)
	}
}

// TestResolveBoundsMinFields verifies MemMinBytes and VCPUMin reflect boot sizing.
func TestResolveBoundsMinFields(t *testing.T) {
	r := vmcfg.Resolve(vmcfg.Config{BootMemMiB: 2048, BootVCPUs: 4})
	if want := mib(2048); r.Bounds.MemMinBytes != want {
		t.Errorf("Bounds.MemMinBytes: got %d, want %d", r.Bounds.MemMinBytes, want)
	}
	if want := int32(4); r.Bounds.VCPUMin != want {
		t.Errorf("Bounds.VCPUMin: got %d, want %d", r.Bounds.VCPUMin, want)
	}
}

// TestResolveBuilderVMPattern mirrors the builder call site:
//   builderGovBounds := buildAutoResizeBounds(builderBootMemMiB, 0, builderBootVCPUs, 0, 0)
//
// Default builder spec: 8192 MiB / 2 vCPUs → expect 32 GiB / 8 vCPUs ceilings.
func TestResolveBuilderVMPattern(t *testing.T) {
	const builderBootMemMiB = uint32(8192)
	const builderBootVCPUs = uint32(2)

	r := vmcfg.Resolve(vmcfg.Config{
		BootMemMiB: builderBootMemMiB,
		BootVCPUs:  builderBootVCPUs,
		// All ceiling fields zero → defaults apply.
	})

	if want := uint32(32768); r.MemoryMaxMiB != want { // 8192 × 4 = 32768 MiB = 32 GiB
		t.Errorf("MemoryMaxMiB: got %d MiB, want %d MiB (32 GiB)", r.MemoryMaxMiB, want)
	}
	if want := uint32(8); r.VCPUMax != want { // 2 × 4 = 8
		t.Errorf("VCPUMax: got %d, want %d", r.VCPUMax, want)
	}
}

// TestResolveExplicitCeilingBelowBootNoBoundsValidation documents the current
// behaviour: the package does NOT validate that explicit ceilings are >= boot
// sizing.  This results in Bounds where MemMaxBytes < MemMinBytes, which is a
// latent bug in the original buildAutoResizeBounds; Resolve preserves that
// behaviour exactly so this slice does not change observable semantics.
//
// UNI-WIRE or a follow-up slice should add validation here and at the CLI
// flag-parsing layer.
func TestResolveExplicitCeilingBelowBootNoBoundsValidation(t *testing.T) {
	// 512 MiB boot, but 256 MiB ceiling — logically invalid, not rejected today.
	r := vmcfg.Resolve(vmcfg.Config{
		BootMemMiB: 512,
		MemMaxMiB:  256, // explicitly below boot
	})
	// Current behaviour: Bounds.MemMaxBytes < Bounds.MemMinBytes, no error.
	if r.Bounds.MemMaxBytes >= r.Bounds.MemMinBytes {
		t.Errorf("expected MemMaxBytes (%d) < MemMinBytes (%d) to document missing validation",
			r.Bounds.MemMaxBytes, r.Bounds.MemMinBytes)
	}
	// MemoryMaxMiB is returned verbatim.
	if r.MemoryMaxMiB != 256 {
		t.Errorf("MemoryMaxMiB: got %d, want 256", r.MemoryMaxMiB)
	}
}

// TestResolveResultAndBoundsConsistency cross-checks that Result.MemoryMaxMiB
// and Result.VCPUMax are byte-consistent with Bounds.MemMaxBytes and
// Bounds.VCPUMax for several configs.
func TestResolveResultAndBoundsConsistency(t *testing.T) {
	configs := []vmcfg.Config{
		{},
		{BootMemMiB: 1024, BootVCPUs: 2},
		{BootMemMiB: 4096, BootVCPUs: 4, MemMaxMiB: 16384, VCPUsMax: 16, DiskMaxGiB: 50},
	}
	for _, c := range configs {
		r := vmcfg.Resolve(c)
		if got, want := r.Bounds.MemMaxBytes, mib(int64(r.MemoryMaxMiB)); got != want {
			t.Errorf("config %+v: Bounds.MemMaxBytes %d != MemoryMaxMiB-in-bytes %d", c, got, want)
		}
		if got, want := r.Bounds.VCPUMax, int32(r.VCPUMax); got != want {
			t.Errorf("config %+v: Bounds.VCPUMax %d != VCPUMax %d", c, got, want)
		}
		// PID1Args must encode MemoryMaxMiB.
		wantPID1 := fmt.Sprintf(" --mem-ceiling=%d", mib(int64(r.MemoryMaxMiB)))
		if r.PID1Args != wantPID1 {
			t.Errorf("config %+v: PID1Args %q != want %q", c, r.PID1Args, wantPID1)
		}
	}
}
