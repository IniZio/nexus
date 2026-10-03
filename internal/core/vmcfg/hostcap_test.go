package vmcfg_test

import (
	"os"
	"testing"

	"github.com/IniZio/nexus/internal/core/vmcfg"
)

// TestMain pins a host with unknown CPU count (boot 1, fixed 4-vCPU floor) and
// 4 GiB RAM so the floor assertions stay host-independent.
func TestMain(m *testing.M) {
	vmcfg.HostCapacityFunc = func() vmcfg.HostCapacity { return vmcfg.HostCapacity{RAMMiB: 4096} }
	os.Exit(m.Run())
}

func TestResolveHostDerivedCeilings(t *testing.T) {
	tests := []struct {
		name     string
		host     vmcfg.HostCapacity
		cfg      vmcfg.Config
		wantMem  uint32
		wantCPUs uint32
	}{
		{"small host 2cpu/4GiB -> mem floor, cpu clamped to host", vmcfg.HostCapacity{NCPU: 2, RAMMiB: 4096}, vmcfg.Config{}, 4096, 2},
		{"12cpu/30GiB", vmcfg.HostCapacity{NCPU: 12, RAMMiB: 30 * 1024}, vmcfg.Config{}, 7680, 12},
		{"64cpu/256GiB clamps mem to 8192, cpu 4x8", vmcfg.HostCapacity{NCPU: 64, RAMMiB: 256 * 1024}, vmcfg.Config{}, 8192, 32},
		{"nested on small host >= 8192", vmcfg.HostCapacity{NCPU: 2, RAMMiB: 4096}, vmcfg.Config{Nested: true}, 8192, 2},
		{"nested on big host", vmcfg.HostCapacity{NCPU: 64, RAMMiB: 256 * 1024}, vmcfg.Config{Nested: true}, 8192, 32},
		{"4x boot mem wins over host; cpu clamped", vmcfg.HostCapacity{NCPU: 2, RAMMiB: 4096}, vmcfg.Config{BootMemMiB: 4096, BootVCPUs: 8}, 16384, 8},
		{"explicit overrides win", vmcfg.HostCapacity{NCPU: 64, RAMMiB: 256 * 1024}, vmcfg.Config{MemMaxMiB: 2048, VCPUsMax: 2}, 2048, 2},
		{"detection failure -> old floors", vmcfg.HostCapacity{}, vmcfg.Config{}, 4096, 4},
		{"detection failure nested", vmcfg.HostCapacity{}, vmcfg.Config{Nested: true}, 8192, 4},
	}
	orig := vmcfg.HostCapacityFunc
	t.Cleanup(func() { vmcfg.HostCapacityFunc = orig })
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vmcfg.HostCapacityFunc = func() vmcfg.HostCapacity { return tc.host }
			r := vmcfg.Resolve(tc.cfg)
			if r.MemoryMaxMiB != tc.wantMem {
				t.Errorf("MemoryMaxMiB: got %d, want %d", r.MemoryMaxMiB, tc.wantMem)
			}
			if r.VCPUMax != tc.wantCPUs {
				t.Errorf("VCPUMax: got %d, want %d", r.VCPUMax, tc.wantCPUs)
			}
		})
	}
}

func TestResolveDefaultBootAndCeiling(t *testing.T) {
	tests := []struct {
		name     string
		host     vmcfg.HostCapacity
		cfg      vmcfg.Config
		wantBoot uint32
		wantMax  uint32
	}{
		{"host 12 -> boot 8, max 12", vmcfg.HostCapacity{NCPU: 12}, vmcfg.Config{}, 8, 12},
		{"host 2 -> boot 2, max 2", vmcfg.HostCapacity{NCPU: 2}, vmcfg.Config{}, 2, 2},
		{"host 64 -> boot 8, max 32", vmcfg.HostCapacity{NCPU: 64}, vmcfg.Config{}, 8, 32},
		{"explicit boot 12 on host 12 -> max 12 not 48", vmcfg.HostCapacity{NCPU: 12}, vmcfg.Config{BootVCPUs: 12}, 12, 12},
		{"unknown host -> boot 1, max 4", vmcfg.HostCapacity{}, vmcfg.Config{}, 1, 4},
		{"explicit vcpus-max wins", vmcfg.HostCapacity{NCPU: 12}, vmcfg.Config{VCPUsMax: 20}, 8, 20},
		{"explicit boot above host keeps boot as floor", vmcfg.HostCapacity{NCPU: 4}, vmcfg.Config{BootVCPUs: 6}, 6, 6},
	}
	orig := vmcfg.HostCapacityFunc
	t.Cleanup(func() { vmcfg.HostCapacityFunc = orig })
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vmcfg.HostCapacityFunc = func() vmcfg.HostCapacity { return tc.host }
			r := vmcfg.Resolve(tc.cfg)
			if r.BootVCPUs != tc.wantBoot || r.Bounds.VCPUMin != int32(tc.wantBoot) {
				t.Errorf("boot/VCPUMin: got %d/%d, want %d", r.BootVCPUs, r.Bounds.VCPUMin, tc.wantBoot)
			}
			if r.VCPUMax != tc.wantMax || r.Bounds.VCPUMax != int32(tc.wantMax) {
				t.Errorf("VCPUMax: got %d/%d, want %d", r.VCPUMax, r.Bounds.VCPUMax, tc.wantMax)
			}
		})
	}
}
