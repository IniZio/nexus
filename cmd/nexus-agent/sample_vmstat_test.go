//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

func setVmstatPath(t *testing.T, path string) {
	t.Helper()
	orig := sampleVmstatPath
	t.Cleanup(func() { sampleVmstatPath = orig })
	sampleVmstatPath = path
}

func setSwapSources(t *testing.T, swapsPath, blockSysDir string) {
	t.Helper()
	origSwaps, origBlock := sampleSwapsPath, sampleBlockSysDir
	t.Cleanup(func() { sampleSwapsPath, sampleBlockSysDir = origSwaps, origBlock })
	sampleSwapsPath, sampleBlockSysDir = swapsPath, blockSysDir
}

const swapsZram = "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n" +
	"/dev/zram0                              partition\t2097148\t\t0\t\t100\n"

// zram0 stat captured live from a guest after a forced swap event: 175 read
// I/Os, 3328 read sectors (= 416 pages).
const zramStatAfterSwap = "     175        0     3328        4    62825        0   502600       36        0       40       40        0        0        0        0        0        0\n"

func TestReadVmstatPswpin(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		missing bool
		want    uint64
	}{
		{
			name: "present",
			content: "nr_free_pages 12345\n" +
				"pswpout 99\n" +
				"pswpin 4242\n" +
				"pgpgin 1\n",
			want: 4242,
		},
		{name: "zero", content: "pswpin 0\npswpout 0\n", want: 0},
		{name: "missing_key", content: "nr_free_pages 12345\npswpout 7\n", want: 0},
		{name: "unparsable", content: "pswpin abc\n", want: 0},
		{name: "missing_file", missing: true, want: 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "vmstat")
			if !tc.missing {
				writeTestFile(t, path, tc.content)
			}
			if got := readVmstatPswpin(path); got != tc.want {
				t.Errorf("readVmstatPswpin = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSwapBlockDevice(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		missing bool
		want    string
	}{
		{name: "zram", content: swapsZram, want: "zram0"},
		{name: "partition", content: "Filename Type Size Used Priority\n/dev/vda2 partition 1048572 0 -2\n", want: "vda2"},
		{name: "file_skipped", content: "Filename Type Size Used Priority\n/swapfile file 1048572 0 -2\n", want: ""},
		{name: "file_then_partition", content: "Filename Type Size Used Priority\n/swapfile file 1 0 -2\n/dev/zram0 partition 2 0 100\n", want: "zram0"},
		{name: "header_only", content: "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n", want: ""},
		{name: "missing_file", missing: true, want: ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "swaps")
			if !tc.missing {
				writeTestFile(t, path, tc.content)
			}
			if got := swapBlockDevice(path); got != tc.want {
				t.Errorf("swapBlockDevice = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadBlockReadSectors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
		missing bool
		want    uint64
		ok      bool
	}{
		{name: "live_zram", content: zramStatAfterSwap, want: 3328, ok: true},
		{name: "short", content: "1 2\n", ok: false},
		{name: "unparsable", content: "1 2 abc 4\n", ok: false},
		{name: "missing_file", missing: true, ok: false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "stat")
			if !tc.missing {
				writeTestFile(t, path, tc.content)
			}
			got, ok := readBlockReadSectors(path)
			if got != tc.want || ok != tc.ok {
				t.Errorf("readBlockReadSectors = (%d, %v), want (%d, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestReadSwapInPages(t *testing.T) {
	t.Parallel()
	type fixture struct {
		swaps    string
		zramStat string
		vmstat   string
	}
	cases := []struct {
		name string
		fx   fixture
		want uint64
	}{
		{
			name: "zram_stat_preferred_over_pswpin",
			fx:   fixture{swaps: swapsZram, zramStat: zramStatAfterSwap, vmstat: "pswpin 999\n"},
			want: 3328 / sectorsPerPage,
		},
		{
			name: "no_block_swap_falls_back_to_pswpin",
			fx:   fixture{swaps: "Filename Type Size Used Priority\n/swapfile file 1 0 -2\n", vmstat: "pswpin 999\n"},
			want: 999,
		},
		{
			name: "swap_device_without_stat_falls_back_to_pswpin",
			fx:   fixture{swaps: swapsZram, vmstat: "pswpin 999\n"},
			want: 999,
		},
		{
			name: "shipped_kernel_no_counters",
			fx:   fixture{swaps: swapsZram, zramStat: zramStatAfterSwap, vmstat: "nr_free_pages 1\nnr_swapcached 0\n"},
			want: 416,
		},
		{
			name: "nothing_available",
			fx:   fixture{},
			want: 0,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			swaps := filepath.Join(dir, "swaps")
			blockDir := filepath.Join(dir, "block")
			vmstat := filepath.Join(dir, "vmstat")
			if tc.fx.swaps != "" {
				writeTestFile(t, swaps, tc.fx.swaps)
			}
			if tc.fx.zramStat != "" {
				if err := os.MkdirAll(filepath.Join(blockDir, "zram0"), 0o755); err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, filepath.Join(blockDir, "zram0", "stat"), tc.fx.zramStat)
			}
			if tc.fx.vmstat != "" {
				writeTestFile(t, vmstat, tc.fx.vmstat)
			}
			if got := readSwapInPages(swaps, blockDir, vmstat); got != tc.want {
				t.Errorf("readSwapInPages = %d, want %d", got, tc.want)
			}
		})
	}
}

// collectSample must carry the swap-in counter into Sample.SwapInPages from
// the zram block stat, and a guest with none of the sources must still yield
// a sample (SwapInPages = 0, no error).
func TestCollectSampleSwapInPages(t *testing.T) {
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 8388608 kB\nMemFree: 4194304 kB\nMemAvailable: 5242880 kB\n")
	setMeminfoPath(t, mi)
	setPSIPaths(t, filepath.Join(dir, "noexist"), filepath.Join(dir, "noexist"))
	setStatfsFunc(t, noopStatfs)
	setCPUSysPath(t, filepath.Join(dir, "no-cpu"))

	swaps := filepath.Join(dir, "swaps")
	writeTestFile(t, swaps, swapsZram)
	blockDir := filepath.Join(dir, "block")
	if err := os.MkdirAll(filepath.Join(blockDir, "zram0"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(blockDir, "zram0", "stat"), zramStatAfterSwap)
	setSwapSources(t, swaps, blockDir)
	setVmstatPath(t, filepath.Join(dir, "no-vmstat"))

	s, err := collectSample(nil)
	if err != nil {
		t.Fatalf("collectSample: %v", err)
	}
	if s.SwapInPages != 416 {
		t.Fatalf("SwapInPages = %d, want 416", s.SwapInPages)
	}

	setSwapSources(t, filepath.Join(dir, "no-swaps"), filepath.Join(dir, "no-block"))
	vm := filepath.Join(dir, "vmstat")
	writeTestFile(t, vm, "pswpin 777\npswpout 5\n")
	setVmstatPath(t, vm)
	s, err = collectSample(nil)
	if err != nil {
		t.Fatalf("collectSample: %v", err)
	}
	if s.SwapInPages != 777 {
		t.Fatalf("SwapInPages via pswpin fallback = %d, want 777", s.SwapInPages)
	}

	setVmstatPath(t, filepath.Join(dir, "no-vmstat"))
	s, err = collectSample(nil)
	if err != nil {
		t.Fatalf("collectSample without any source: %v", err)
	}
	if s.SwapInPages != 0 {
		t.Fatalf("SwapInPages without any source = %d, want 0", s.SwapInPages)
	}
}
