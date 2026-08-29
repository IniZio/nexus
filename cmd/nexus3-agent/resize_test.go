//go:build linux

package main

// Unit tests for the guest-side auto-resize subsystem (AR-GA).
//
// All tests use /proc and sysfs fixtures (temp dirs/files) and injected exec
// and mount functions so they run without root, without a VM, and without
// real kernel interfaces. Live verification is Wave 4's job (AR-LIVE-MEM,
// AR-LIVE-DC).
//
// Evidence required by the ticket (AR-GA Evidence section):
//   - Sample parsing from /proc fixtures including PSI-absent (Supported=false).
//   - CPU onliner acting on an offline-CPU fixture; no-op on all-online.
//   - disk.grow handler refusing a non-ext4 device.
//   - ZRAM: setup on a normal fixture; second invocation proves idempotence;
//     CONFIG_ZRAM-absent fixture proves best-effort (no boot failure);
//     computed size clamped to [zramMinBytes, zramMaxBytes] (2 GiB, 4 GiB).
//   - /tmp resize: no remount within hysteresis; remount when MemTotal grows;
//     2 GiB cap honoured; live MemTotal used as sizing base (not ceiling).
//   - Always-on: startResizeServices is called unconditionally from main.go;
//     being PID 1 is sufficient.

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/IniZio/nexus3/internal/core/resize"
)

// ── helpers ──────────────────────────────────────────────────────────────────

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setMeminfoPath(t *testing.T, path string) {
	t.Helper()
	orig := sampleMeminfoPath
	t.Cleanup(func() { sampleMeminfoPath = orig })
	sampleMeminfoPath = path
}

func setPSIPaths(t *testing.T, mem, cpu string) {
	t.Helper()
	origMem, origCPU := samplePSIMemPath, samplePSICPUPath
	t.Cleanup(func() { samplePSIMemPath = origMem; samplePSICPUPath = origCPU })
	samplePSIMemPath = mem
	samplePSICPUPath = cpu
}

func setStatfsFunc(t *testing.T, fn func(string, *unix.Statfs_t) error) {
	t.Helper()
	orig := sampleStatfsFunc
	t.Cleanup(func() { sampleStatfsFunc = orig })
	sampleStatfsFunc = fn
}

func setCPUSysPath(t *testing.T, path string) {
	t.Helper()
	orig := sampleCPUSysPath
	t.Cleanup(func() { sampleCPUSysPath = orig })
	sampleCPUSysPath = path
}

func setResizeExec(t *testing.T, fn func(string, ...string) ([]byte, error)) {
	t.Helper()
	orig := resizeExecFunc
	t.Cleanup(func() { resizeExecFunc = orig })
	resizeExecFunc = fn
}

// noopStatfs does nothing so statfs-dependent fields default to zero.
var noopStatfs = func(_ string, _ *unix.Statfs_t) error { return nil }

// ── collectSample: meminfo parsing ───────────────────────────────────────────

func TestCollectSampleMeminfo(t *testing.T) {
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 8388608 kB\nMemFree: 4194304 kB\nMemAvailable: 5242880 kB\n")
	setMeminfoPath(t, mi)
	setPSIPaths(t, filepath.Join(dir, "noexist"), filepath.Join(dir, "noexist"))
	setStatfsFunc(t, noopStatfs)
	setCPUSysPath(t, filepath.Join(dir, "no-cpu"))

	s, err := collectSample(nil)
	if err != nil {
		t.Fatalf("collectSample: %v", err)
	}
	if got, want := s.MemTotalBytes, uint64(8388608*1024); got != want {
		t.Errorf("MemTotalBytes = %d, want %d", got, want)
	}
	if got, want := s.MemAvailableBytes, uint64(5242880*1024); got != want {
		t.Errorf("MemAvailableBytes = %d, want %d", got, want)
	}
	if s.Timestamp.IsZero() {
		t.Error("Timestamp is zero")
	}
}

// ── collectSample: PSI absent → Supported=false, not zero-as-healthy ─────────

func TestCollectSamplePSIAbsent(t *testing.T) {
	// When /proc/pressure/{memory,cpu} are absent, Supported must be false.
	// Zero pressure with Supported=true would look healthy and suppress grows.
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 4096000 kB\nMemAvailable: 2048000 kB\n")
	setMeminfoPath(t, mi)
	setPSIPaths(t, filepath.Join(dir, "absent-mem"), filepath.Join(dir, "absent-cpu"))
	setStatfsFunc(t, noopStatfs)
	setCPUSysPath(t, filepath.Join(dir, "no-cpu"))

	s, err := collectSample(nil)
	if err != nil {
		t.Fatalf("collectSample: %v", err)
	}
	if s.MemPSISupported {
		t.Error("MemPSISupported = true when file absent, want false")
	}
	if s.CPUPSISupported {
		t.Error("CPUPSISupported = true when file absent, want false")
	}
	if s.MemPSISomeAvg10 != 0 {
		t.Errorf("MemPSISomeAvg10 = %v, want 0", s.MemPSISomeAvg10)
	}
}

func TestCollectSamplePSIPresent(t *testing.T) {
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 4096000 kB\nMemAvailable: 512000 kB\n")
	psiMem := filepath.Join(dir, "memory")
	writeTestFile(t, psiMem,
		"some avg10=3.50 avg60=1.20 avg300=0.40 total=12345\n"+
			"full avg10=1.25 avg60=0.50 avg300=0.10 total=6789\n")
	psiCPU := filepath.Join(dir, "cpu")
	writeTestFile(t, psiCPU, "some avg10=7.80 avg60=4.30 avg300=2.10 total=99999\n")
	setMeminfoPath(t, mi)
	setPSIPaths(t, psiMem, psiCPU)
	setStatfsFunc(t, noopStatfs)
	setCPUSysPath(t, filepath.Join(dir, "no-cpu"))

	s, err := collectSample(nil)
	if err != nil {
		t.Fatalf("collectSample: %v", err)
	}
	if !s.MemPSISupported {
		t.Error("MemPSISupported = false, want true")
	}
	if s.MemPSISomeAvg10 != 3.50 {
		t.Errorf("MemPSISomeAvg10 = %v, want 3.50", s.MemPSISomeAvg10)
	}
	if s.MemPSIFullAvg10 != 1.25 {
		t.Errorf("MemPSIFullAvg10 = %v, want 1.25", s.MemPSIFullAvg10)
	}
	if !s.CPUPSISupported {
		t.Error("CPUPSISupported = false, want true")
	}
	if s.CPUPSISomeAvg10 != 7.80 {
		t.Errorf("CPUPSISomeAvg10 = %v, want 7.80", s.CPUPSISomeAvg10)
	}
}

func TestCollectSampleDiskStatfs(t *testing.T) {
	// 1000 blocks × 4096 bytes; 200 free → 800 used. Disk at index 3 (/dev/vde).
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 4096000 kB\nMemAvailable: 2000000 kB\n")
	setMeminfoPath(t, mi)
	setPSIPaths(t, filepath.Join(dir, "noexist"), filepath.Join(dir, "noexist"))
	setCPUSysPath(t, filepath.Join(dir, "no-cpu"))
	setStatfsFunc(t, func(_ string, st *unix.Statfs_t) error {
		st.Bsize = 4096
		st.Blocks = 1000
		st.Bfree = 200
		return nil
	})

	disks := []resizableDisk{{Index: 3, MountPath: "/workspace"}}
	s, err := collectSample(disks)
	if err != nil {
		t.Fatalf("collectSample: %v", err)
	}
	// Legacy flat fields mirror the primary disk.
	if got, want := s.DiskTotalBytes, uint64(1000*4096); got != want {
		t.Errorf("DiskTotalBytes = %d, want %d", got, want)
	}
	if got, want := s.DiskUsedBytes, uint64(800*4096); got != want {
		t.Errorf("DiskUsedBytes = %d, want %d", got, want)
	}
	if !s.DiskSupported {
		t.Error("DiskSupported = false, want true")
	}
	// DiskStats must carry a single entry with the correct index.
	if len(s.DiskStats) != 1 {
		t.Fatalf("len(DiskStats) = %d, want 1", len(s.DiskStats))
	}
	if s.DiskStats[0].Index != 3 {
		t.Errorf("DiskStats[0].Index = %d, want 3", s.DiskStats[0].Index)
	}
	if s.DiskStats[0].TotalBytes != uint64(1000*4096) {
		t.Errorf("DiskStats[0].TotalBytes = %d, want %d", s.DiskStats[0].TotalBytes, uint64(1000*4096))
	}
}

// ── CPU onliner ───────────────────────────────────────────────────────────────

func TestOnlineOfflineCPUs_BringsOfflineOnline(t *testing.T) {
	dir := t.TempDir()
	cpuDir := filepath.Join(dir, "cpu")
	// cpu0: no online file (always online)
	if err := os.MkdirAll(filepath.Join(cpuDir, "cpu0"), 0o755); err != nil {
		t.Fatal(err)
	}
	// cpu1: offline
	writeTestFile(t, filepath.Join(cpuDir, "cpu1", "online"), "0\n")
	// cpu2: online
	writeTestFile(t, filepath.Join(cpuDir, "cpu2", "online"), "1\n")
	// cpufreq: must be ignored by the pattern
	if err := os.MkdirAll(filepath.Join(cpuDir, "cpufreq"), 0o755); err != nil {
		t.Fatal(err)
	}

	setCPUSysPath(t, cpuDir)
	onlineOfflineCPUs()

	got, err := os.ReadFile(filepath.Join(cpuDir, "cpu1", "online"))
	if err != nil {
		t.Fatalf("read cpu1/online: %v", err)
	}
	if strings.TrimSpace(string(got)) != "1" {
		t.Errorf("cpu1/online = %q after onlining, want \"1\"", got)
	}
	got2, _ := os.ReadFile(filepath.Join(cpuDir, "cpu2", "online"))
	if strings.TrimSpace(string(got2)) != "1" {
		t.Errorf("cpu2/online changed from \"1\" (must be untouched)")
	}
}

func TestOnlineOfflineCPUs_AllOnlineIsNoOp(t *testing.T) {
	dir := t.TempDir()
	cpuDir := filepath.Join(dir, "cpu")
	if err := os.MkdirAll(filepath.Join(cpuDir, "cpu0"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(cpuDir, "cpu1", "online"), "1\n")

	setCPUSysPath(t, cpuDir)
	onlineOfflineCPUs() // must not error or panic

	got, _ := os.ReadFile(filepath.Join(cpuDir, "cpu1", "online"))
	if strings.TrimSpace(string(got)) != "1" {
		t.Errorf("cpu1/online changed from \"1\" (must be untouched), got %q", got)
	}
}

func TestReadVCPUs(t *testing.T) {
	dir := t.TempDir()
	cpuDir := filepath.Join(dir, "cpu")
	_ = os.MkdirAll(filepath.Join(cpuDir, "cpu0"), 0o755)            // no online file → always online
	writeTestFile(t, filepath.Join(cpuDir, "cpu1", "online"), "1\n") // online
	writeTestFile(t, filepath.Join(cpuDir, "cpu2", "online"), "0\n") // offline
	_ = os.MkdirAll(filepath.Join(cpuDir, "cpuidle"), 0o755)         // must be ignored

	count, online := readVCPUs(cpuDir)
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
	if online != 2 { // cpu0 implicit + cpu1 explicit
		t.Errorf("online = %d, want 2", online)
	}
}

// ── handleDiskGrow ────────────────────────────────────────────────────────────

func TestHandleDiskGrow_RefusesNonExt4(t *testing.T) {
	setResizeExec(t, func(name string, args ...string) ([]byte, error) {
		if name == "blkid" {
			return []byte("xfs\n"), nil
		}
		return nil, nil
	})
	resp := handleDiskGrow(resize.GrowRequest{DiskIndex: 0, TargetBytes: 10 << 30})
	if resp.Error == "" {
		t.Fatal("expected error for non-ext4, got none")
	}
	if !strings.Contains(resp.Error, "ext4") {
		t.Errorf("error %q does not mention ext4", resp.Error)
	}
	if resp.ResultBytes != 0 {
		t.Errorf("ResultBytes = %d, want 0 on error", resp.ResultBytes)
	}
}

func TestHandleDiskGrow_IndexToDevice(t *testing.T) {
	// DiskIndex 0 → /dev/vdb, 1 → /dev/vdc, 2 → /dev/vdd.
	for _, tc := range []struct {
		index  int
		device string
	}{
		{0, "/dev/vdb"},
		{1, "/dev/vdc"},
		{2, "/dev/vdd"},
	} {
		tc := tc
		t.Run(tc.device, func(t *testing.T) {
			var gotDevice string
			setResizeExec(t, func(name string, args ...string) ([]byte, error) {
				switch name {
				case "blkid":
					for _, a := range args {
						if strings.HasPrefix(a, "/dev/") {
							gotDevice = a
						}
					}
					return []byte("ext4\n"), nil
				case "resize2fs":
					return []byte("The filesystem on " + gotDevice + " is now 655360 (4k) blocks long.\n"), nil
				}
				return nil, nil
			})
			handleDiskGrow(resize.GrowRequest{DiskIndex: tc.index, TargetBytes: 10 << 20})
			if gotDevice != tc.device {
				t.Errorf("index %d: device = %q, want %q", tc.index, gotDevice, tc.device)
			}
		})
	}
}

func TestHandleDiskGrow_Resize2fsSuccess(t *testing.T) {
	setResizeExec(t, func(name string, _ ...string) ([]byte, error) {
		if name == "blkid" {
			return []byte("ext4\n"), nil
		}
		return []byte("The filesystem on /dev/vdb is now 655360 (4k) blocks long.\n"), nil
	})
	resp := handleDiskGrow(resize.GrowRequest{DiskIndex: 0, TargetBytes: 655360 * 4096})
	if resp.Error != "" {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if want := int64(655360 * 4096); resp.ResultBytes != want {
		t.Errorf("ResultBytes = %d, want %d", resp.ResultBytes, want)
	}
}

func TestHandleDiskGrow_InvalidIndex(t *testing.T) {
	resp := handleDiskGrow(resize.GrowRequest{DiskIndex: 26})
	if resp.Error == "" {
		t.Error("expected error for out-of-range index, got none")
	}
}

// ── ZRAM swap ─────────────────────────────────────────────────────────────────

// zramFixture wires injectable vars for ZRAM tests and returns a cleanup func.
type zramFixture struct {
	meminfoFile string
	swapsFile   string
	calledNames []string
	sizeArg     string
	swapActive  bool
	ctlExists   bool
}

func (f *zramFixture) install(t *testing.T) {
	t.Helper()
	origCtl := zramControlExistsFunc
	origActive := zramSwapActiveFunc
	origMeminfo := zramMeminfoPath
	origSwaps := zramProcSwapsPath
	origExec := zramExecFunc
	origWrite := zramWriteFileFunc
	t.Cleanup(func() {
		zramControlExistsFunc = origCtl
		zramSwapActiveFunc = origActive
		zramMeminfoPath = origMeminfo
		zramProcSwapsPath = origSwaps
		zramExecFunc = origExec
		zramWriteFileFunc = origWrite
	})
	active := f.swapActive
	ctl := f.ctlExists
	zramControlExistsFunc = func() bool { return ctl }
	zramSwapActiveFunc = func() bool { return active }
	zramMeminfoPath = f.meminfoFile
	zramProcSwapsPath = f.swapsFile
	names := &f.calledNames
	sizeArg := &f.sizeArg
	zramExecFunc = func(name string, args ...string) ([]byte, error) {
		*names = append(*names, name)
		if name == "zramctl" {
			for i, a := range args {
				if a == "--size" && i+1 < len(args) {
					*sizeArg = args[i+1]
				}
			}
			return []byte("/dev/zram0\n"), nil
		}
		return nil, nil
	}
	zramWriteFileFunc = func(string, []byte, os.FileMode) error { return nil }
}

func (f *zramFixture) called(name string) bool {
	for _, n := range f.calledNames {
		if n == name {
			return true
		}
	}
	return false
}

func TestSetupZRAMSwap_Normal(t *testing.T) {
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	sw := filepath.Join(dir, "swaps")
	// 4 GiB MemTotal → half = 2 GiB, within [1 GiB, 4 GiB] → 2 GiB.
	writeTestFile(t, mi, "MemTotal: 4194304 kB\nMemAvailable: 4000000 kB\n")
	writeTestFile(t, sw, "Filename\t\t\tType\t\tSize\t\tUsed\t\tPriority\n")

	fix := &zramFixture{meminfoFile: mi, swapsFile: sw, ctlExists: true, swapActive: false}
	fix.install(t)
	setupZRAMSwap(nil)

	if !fix.called("mkswap") {
		t.Error("mkswap not called")
	}
	if !fix.called("swapon") {
		t.Error("swapon not called")
	}
	if fix.sizeArg != "2147483648" { // 2 GiB
		t.Errorf("ZRAM size = %s, want 2147483648 (2 GiB)", fix.sizeArg)
	}
}

func TestSetupZRAMSwap_Idempotent(t *testing.T) {
	// swap already active → zramctl/mkswap/swapon must not be called.
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 4194304 kB\nMemAvailable: 4000000 kB\n")

	fix := &zramFixture{meminfoFile: mi, swapsFile: mi, ctlExists: true, swapActive: true}
	fix.install(t)
	setupZRAMSwap(nil)

	if fix.called("zramctl") {
		t.Error("zramctl called when swap already active (must be idempotent)")
	}
}

func TestSetupZRAMSwap_AbsentKernelContinues(t *testing.T) {
	// /sys/class/zram-control absent (CONFIG_ZRAM=n) → no panic, no mkswap.
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 4194304 kB\nMemAvailable: 4000000 kB\n")

	fix := &zramFixture{meminfoFile: mi, ctlExists: false}
	fix.install(t)
	setupZRAMSwap(nil) // must not panic or call Fatal

	if fix.called("mkswap") {
		t.Error("mkswap called when CONFIG_ZRAM absent")
	}
}

func TestZRAMSizeClamp_BelowFloor(t *testing.T) {
	// MemTotal = 1 GiB → half = 512 MiB → clamped to zramMinBytes (2 GiB,
	// raised in db4bc61 to match safeMemFloorBytes).
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 1048576 kB\nMemAvailable: 900000 kB\n")

	fix := &zramFixture{meminfoFile: mi, ctlExists: true, swapActive: false}
	fix.install(t)
	setupZRAMSwap(nil)

	want := strconv.FormatInt(zramMinBytes, 10)
	if fix.sizeArg != want {
		t.Errorf("ZRAM size = %s, want %s (zramMinBytes floor)", fix.sizeArg, want)
	}
}

func TestZRAMSizeClamp_AboveCap(t *testing.T) {
	// MemTotal = 16 GiB → half = 8 GiB → clamped to zramMaxBytes (4 GiB).
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 16777216 kB\nMemAvailable: 16000000 kB\n")

	fix := &zramFixture{meminfoFile: mi, ctlExists: true, swapActive: false}
	fix.install(t)
	setupZRAMSwap(nil)

	if fix.sizeArg != "4294967296" { // 4 GiB
		t.Errorf("ZRAM size = %s, want 4294967296 (4 GiB cap)", fix.sizeArg)
	}
}

// ── /tmp tmpfs resizer ────────────────────────────────────────────────────────

func setTmpFixture(t *testing.T, meminfoFile string, statfsFn func(string, *unix.Statfs_t) error, mountFn func(string, string, string, uintptr, string) error) {
	t.Helper()
	origMi := tmpMeminfoPath
	origSf := tmpStatfsFunc
	origMt := tmpMountFunc
	t.Cleanup(func() {
		tmpMeminfoPath = origMi
		tmpStatfsFunc = origSf
		tmpMountFunc = origMt
	})
	tmpMeminfoPath = meminfoFile
	tmpStatfsFunc = statfsFn
	tmpMountFunc = mountFn
}

// tmpfsStatfs returns a Statfs_t that looks like tmpfs with the given size.
func tmpfsStatfs(capBytes uint64) func(string, *unix.Statfs_t) error {
	return func(_ string, st *unix.Statfs_t) error {
		st.Type = tmpfsMagic
		st.Bsize = 4096
		st.Blocks = capBytes / 4096
		st.Bfree = st.Blocks
		return nil
	}
}

func TestComputeTmpTargetBytes_Cap2GiB(t *testing.T) {
	// 4 GiB MemTotal → 50% = 2 GiB → hits the absolute cap.
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 4194304 kB\nMemAvailable: 4000000 kB\n")
	orig := tmpMeminfoPath
	t.Cleanup(func() { tmpMeminfoPath = orig })
	tmpMeminfoPath = mi

	got := computeTmpTargetBytes()
	if got != uint64(2<<30) {
		t.Errorf("computeTmpTargetBytes = %d, want %d (2 GiB cap)", got, uint64(2<<30))
	}
}

func TestComputeTmpTargetBytes_LiveMemTotal(t *testing.T) {
	// 2 GiB MemTotal → 50% = 1 GiB, below cap.
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 2097152 kB\nMemAvailable: 2000000 kB\n")
	orig := tmpMeminfoPath
	t.Cleanup(func() { tmpMeminfoPath = orig })
	tmpMeminfoPath = mi

	got := computeTmpTargetBytes()
	if got != uint64(1<<30) {
		t.Errorf("computeTmpTargetBytes = %d, want %d (1 GiB)", got, uint64(1<<30))
	}
}

// TestComputeTmpSizeBytes exercises the pure sizing formula across floor, cap,
// and proportional cases without touching /proc/meminfo.
func TestComputeTmpSizeBytes(t *testing.T) {
	const (
		gib = uint64(1 << 30)
		mib = uint64(1 << 20)
	)
	cases := []struct {
		name    string
		totalKB uint64
		wantB   uint64
		note    string
	}{
		{
			name:    "512MiB_guest_floor_wins",
			totalKB: 512 * 1024,
			// 50% of 512 MiB = 256 MiB < 1 GiB floor → floor wins.
			wantB: 1 * gib,
			note:  "floor: 1 GiB (tmpfs sized not preallocated — no real cost on 512 MiB guest)",
		},
		{
			name:    "8GiB_guest_cap_wins",
			totalKB: 8 * 1024 * 1024,
			// 50% of 8 GiB = 4 GiB > 2 GiB cap → cap wins.
			wantB: 2 * gib,
			note:  "cap: 2 GiB",
		},
		{
			name:    "3GiB_guest_proportional",
			totalKB: 3 * 1024 * 1024,
			// 50% of 3 GiB = 1.5 GiB; between floor and cap.
			wantB: (1536 * mib / mib) * mib, // 1536 MiB = 1.5 GiB, already MiB-aligned
			note:  "proportional: 1.5 GiB",
		},
		{
			name:    "zero_totalKB_returns_zero",
			totalKB: 0,
			wantB:   0,
			note:    "unavailable MemTotal sentinel",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := computeTmpSizeBytes(tc.totalKB)
			if got != tc.wantB {
				t.Errorf("computeTmpSizeBytes(%d kB) = %d bytes (%d MiB), want %d bytes (%d MiB) [%s]",
					tc.totalKB, got, got>>20, tc.wantB, tc.wantB>>20, tc.note)
			}
		})
	}
}

func TestResizeTmpfsOnce_SkipsUnderHysteresis(t *testing.T) {
	// current cap = target = 1 GiB → delta = 0 < 64 MiB → no remount.
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 2097152 kB\nMemAvailable: 2000000 kB\n")

	var remounted bool
	setTmpFixture(t, mi,
		tmpfsStatfs(1<<30), // 1 GiB current cap (= target)
		func(string, string, string, uintptr, string) error {
			remounted = true
			return nil
		},
	)

	if err := resizeTmpfsOnce(nil); err != nil {
		t.Fatalf("resizeTmpfsOnce: %v", err)
	}
	if remounted {
		t.Error("remounted when delta < hysteresis band (64 MiB)")
	}
}

func TestResizeTmpfsOnce_RemountsWhenMemGrows(t *testing.T) {
	// current cap = 512 MiB, target = 1 GiB → delta = 512 MiB > 64 MiB → remount.
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 2097152 kB\nMemAvailable: 2000000 kB\n")

	var mountOpts string
	setTmpFixture(t, mi,
		tmpfsStatfs(512<<20), // 512 MiB current cap
		func(_, _, _ string, _ uintptr, opts string) error {
			mountOpts = opts
			return nil
		},
	)

	if err := resizeTmpfsOnce(nil); err != nil {
		t.Fatalf("resizeTmpfsOnce: %v", err)
	}
	// Remount opts must contain the 1 GiB target in bytes.
	if !strings.Contains(mountOpts, "1073741824") {
		t.Errorf("mount opts %q do not contain 1 GiB target (1073741824)", mountOpts)
	}
}

func TestResizeTmpfsOnce_CapAt2GiB(t *testing.T) {
	// 8 GiB MemTotal → target capped at 2 GiB, not 4 GiB.
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 8388608 kB\nMemAvailable: 8000000 kB\n")

	var mountOpts string
	setTmpFixture(t, mi,
		tmpfsStatfs(0), // zero current cap → always triggers remount
		func(_, _, _ string, _ uintptr, opts string) error {
			mountOpts = opts
			return nil
		},
	)

	if err := resizeTmpfsOnce(nil); err != nil {
		t.Fatalf("resizeTmpfsOnce: %v", err)
	}
	// 2 GiB = 2147483648 bytes.
	if !strings.Contains(mountOpts, "2147483648") {
		t.Errorf("mount opts %q do not show 2 GiB cap (2147483648)", mountOpts)
	}
}

func TestResizeTmpfsOnce_NonTmpfsIsNoOp(t *testing.T) {
	// /tmp on ext4 (not tmpfs) → no remount.
	var remounted bool
	orig := tmpStatfsFunc
	origMt := tmpMountFunc
	t.Cleanup(func() { tmpStatfsFunc = orig; tmpMountFunc = origMt })
	tmpStatfsFunc = func(_ string, st *unix.Statfs_t) error {
		st.Type = 0xef53 // EXT4_SUPER_MAGIC
		return nil
	}
	tmpMountFunc = func(string, string, string, uintptr, string) error {
		remounted = true
		return nil
	}

	if err := resizeTmpfsOnce(nil); err != nil {
		t.Fatalf("resizeTmpfsOnce: %v", err)
	}
	if remounted {
		t.Error("remounted when /tmp is not a tmpfs")
	}
}

// ── wire roundtrip ────────────────────────────────────────────────────────────

func TestHandleResizeConn_SampleRoundtrip(t *testing.T) {
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 4096000 kB\nMemAvailable: 2048000 kB\n")
	setMeminfoPath(t, mi)
	setPSIPaths(t, filepath.Join(dir, "noexist"), filepath.Join(dir, "noexist"))
	setStatfsFunc(t, noopStatfs)
	setCPUSysPath(t, filepath.Join(dir, "no-cpu"))

	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })

	go handleResizeConn(nil, server, nil)

	if err := resize.EncodeSampleRequest(client); err != nil {
		t.Fatalf("EncodeSampleRequest: %v", err)
	}
	resp, err := resize.DecodeSampleResponse(client)
	if err != nil {
		t.Fatalf("DecodeSampleResponse: %v", err)
	}
	if resp.Sample.MemTotalBytes == 0 {
		t.Error("MemTotalBytes = 0")
	}
	if resp.Sample.Timestamp.IsZero() {
		t.Error("Timestamp is zero")
	}
}

func TestHandleResizeConn_GrowRoundtrip(t *testing.T) {
	setResizeExec(t, func(name string, _ ...string) ([]byte, error) {
		if name == "blkid" {
			return []byte("ext4\n"), nil
		}
		return []byte("The filesystem on /dev/vdb is now 655360 (4k) blocks long.\n"), nil
	})

	server, client := net.Pipe()
	t.Cleanup(func() { server.Close(); client.Close() })

	go handleResizeConn(nil, server, nil)

	req := resize.GrowRequest{DiskIndex: 0, TargetBytes: 655360 * 4096}
	if err := resize.EncodeGrowRequest(client, req); err != nil {
		t.Fatalf("EncodeGrowRequest: %v", err)
	}
	resp, err := resize.DecodeGrowResponse(client)
	if err != nil {
		t.Fatalf("DecodeGrowResponse: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("GrowResponse.Error = %q", resp.Error)
	}
	if resp.ResultBytes == 0 {
		t.Error("ResultBytes = 0")
	}
}

// ── readMultiDiskStats ────────────────────────────────────────────────────────

func TestReadMultiDiskStats_MultiMount(t *testing.T) {
	// Two disks at indices 2 and 4; statfs returns distinct block counts.
	callCount := 0
	setStatfsFunc(t, func(path string, st *unix.Statfs_t) error {
		callCount++
		st.Bsize = 4096
		switch path {
		case "/mnt/disk2":
			st.Blocks = 500
			st.Bfree = 100 // used = 400
		case "/mnt/disk4":
			st.Blocks = 800
			st.Bfree = 300 // used = 500
		}
		return nil
	})

	disks := []resizableDisk{
		{Index: 2, MountPath: "/mnt/disk2"},
		{Index: 4, MountPath: "/mnt/disk4"},
	}
	got := readMultiDiskStats(disks)

	if len(got) != 2 {
		t.Fatalf("len(DiskStats) = %d, want 2", len(got))
	}
	if got[0].Index != 2 {
		t.Errorf("DiskStats[0].Index = %d, want 2", got[0].Index)
	}
	if got[0].TotalBytes != 500*4096 {
		t.Errorf("DiskStats[0].TotalBytes = %d, want %d", got[0].TotalBytes, uint64(500*4096))
	}
	if got[0].UsedBytes != 400*4096 {
		t.Errorf("DiskStats[0].UsedBytes = %d, want %d", got[0].UsedBytes, uint64(400*4096))
	}
	if !got[0].Supported {
		t.Error("DiskStats[0].Supported = false, want true")
	}
	if got[1].Index != 4 {
		t.Errorf("DiskStats[1].Index = %d, want 4", got[1].Index)
	}
	if got[1].TotalBytes != 800*4096 {
		t.Errorf("DiskStats[1].TotalBytes = %d, want %d", got[1].TotalBytes, uint64(800*4096))
	}
	if got[1].UsedBytes != 500*4096 {
		t.Errorf("DiskStats[1].UsedBytes = %d, want %d", got[1].UsedBytes, uint64(500*4096))
	}
	if callCount != 2 {
		t.Errorf("statfs called %d times, want 2", callCount)
	}
}

func TestReadMultiDiskStats_StatfsFailureSetsSupportedFalse(t *testing.T) {
	// First disk statfs fails; second succeeds. Supported must be false/true.
	setStatfsFunc(t, func(path string, st *unix.Statfs_t) error {
		if path == "/mnt/bad" {
			return fmt.Errorf("statfs: no such file or directory")
		}
		st.Bsize = 4096
		st.Blocks = 100
		st.Bfree = 20
		return nil
	})

	disks := []resizableDisk{
		{Index: 0, MountPath: "/mnt/bad"},
		{Index: 1, MountPath: "/mnt/ok"},
	}
	got := readMultiDiskStats(disks)

	if len(got) != 2 {
		t.Fatalf("len(DiskStats) = %d, want 2", len(got))
	}
	if got[0].Supported {
		t.Error("DiskStats[0].Supported = true after statfs failure, want false")
	}
	if got[0].TotalBytes != 0 || got[0].UsedBytes != 0 {
		t.Errorf("DiskStats[0]: want zero bytes on failure, got used=%d total=%d", got[0].UsedBytes, got[0].TotalBytes)
	}
	if !got[1].Supported {
		t.Error("DiskStats[1].Supported = false, want true")
	}
	if got[1].TotalBytes != 100*4096 {
		t.Errorf("DiskStats[1].TotalBytes = %d, want %d", got[1].TotalBytes, uint64(100*4096))
	}
}

func TestCollectSampleDiskStats_LegacyMirrorsPrimary(t *testing.T) {
	// Two disks; legacy flat fields must mirror only the FIRST (primary) entry.
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 4096000 kB\nMemAvailable: 2000000 kB\n")
	setMeminfoPath(t, mi)
	setPSIPaths(t, filepath.Join(dir, "noexist"), filepath.Join(dir, "noexist"))
	setCPUSysPath(t, filepath.Join(dir, "no-cpu"))

	// statfs: primary at /mnt/ws → 1000 blocks/200 free; secondary at /mnt/cache → 500/100.
	setStatfsFunc(t, func(path string, st *unix.Statfs_t) error {
		st.Bsize = 4096
		switch path {
		case "/mnt/ws":
			st.Blocks = 1000
			st.Bfree = 200
		case "/mnt/cache":
			st.Blocks = 500
			st.Bfree = 100
		}
		return nil
	})

	disks := []resizableDisk{
		{Index: 0, MountPath: "/mnt/ws"},    // primary
		{Index: 2, MountPath: "/mnt/cache"}, // secondary (builder cache)
	}
	s, err := collectSample(disks)
	if err != nil {
		t.Fatalf("collectSample: %v", err)
	}

	// Legacy fields mirror the primary disk (index 0).
	if got, want := s.DiskTotalBytes, uint64(1000*4096); got != want {
		t.Errorf("DiskTotalBytes = %d, want %d (primary)", got, want)
	}
	if got, want := s.DiskUsedBytes, uint64(800*4096); got != want {
		t.Errorf("DiskUsedBytes = %d, want %d (primary)", got, want)
	}
	if !s.DiskSupported {
		t.Error("DiskSupported = false, want true (primary)")
	}

	// DiskStats must have two entries with correct indices and values.
	if len(s.DiskStats) != 2 {
		t.Fatalf("len(DiskStats) = %d, want 2", len(s.DiskStats))
	}
	if s.DiskStats[0].Index != 0 || s.DiskStats[1].Index != 2 {
		t.Errorf("DiskStats indices = [%d, %d], want [0, 2]", s.DiskStats[0].Index, s.DiskStats[1].Index)
	}
	if s.DiskStats[1].TotalBytes != 500*4096 {
		t.Errorf("DiskStats[1].TotalBytes = %d, want %d", s.DiskStats[1].TotalBytes, uint64(500*4096))
	}
}

func TestCollectSampleDiskStats_EmptyDisksYieldsNilDiskStats(t *testing.T) {
	// No resizable disks → DiskStats must be nil (omitempty in JSON).
	dir := t.TempDir()
	mi := filepath.Join(dir, "meminfo")
	writeTestFile(t, mi, "MemTotal: 4096000 kB\nMemAvailable: 2000000 kB\n")
	setMeminfoPath(t, mi)
	setPSIPaths(t, filepath.Join(dir, "noexist"), filepath.Join(dir, "noexist"))
	setCPUSysPath(t, filepath.Join(dir, "no-cpu"))
	setStatfsFunc(t, noopStatfs)

	s, err := collectSample(nil)
	if err != nil {
		t.Fatalf("collectSample: %v", err)
	}
	if s.DiskStats != nil {
		t.Errorf("DiskStats = %v, want nil for empty disk list", s.DiskStats)
	}
	if s.DiskSupported {
		t.Error("DiskSupported = true, want false when no disks")
	}
}
