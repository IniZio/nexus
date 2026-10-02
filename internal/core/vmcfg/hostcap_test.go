package vmcfg_test

import (
	"os"
	"testing"

	"github.com/IniZio/nexus/internal/core/vmcfg"
)

// TestMain pins a small host so the pre-existing floor assertions stay
// host-independent.
func TestMain(m *testing.M) {
	vmcfg.HostCapacityFunc = func() vmcfg.HostCapacity { return vmcfg.HostCapacity{NCPU: 2, RAMMiB: 4096} }
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
		{"small host 2cpu/4GiB -> floors", vmcfg.HostCapacity{NCPU: 2, RAMMiB: 4096}, vmcfg.Config{}, 4096, 4},
		{"12cpu/30GiB", vmcfg.HostCapacity{NCPU: 12, RAMMiB: 30 * 1024}, vmcfg.Config{}, 7680, 12},
		{"64cpu/256GiB clamps mem to 8192", vmcfg.HostCapacity{NCPU: 64, RAMMiB: 256 * 1024}, vmcfg.Config{}, 8192, 64},
		{"nested on small host >= 8192", vmcfg.HostCapacity{NCPU: 2, RAMMiB: 4096}, vmcfg.Config{Nested: true}, 8192, 4},
		{"nested on big host", vmcfg.HostCapacity{NCPU: 64, RAMMiB: 256 * 1024}, vmcfg.Config{Nested: true}, 8192, 64},
		{"4x boot wins over host", vmcfg.HostCapacity{NCPU: 2, RAMMiB: 4096}, vmcfg.Config{BootMemMiB: 4096, BootVCPUs: 8}, 16384, 32},
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
