package cloudhypervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/resize"
)

// TestDiskIndexMapping verifies the critical disk index → CH id and
// disk index → guest device derivations for a 3-extra-disk topology.
//
// CH auto-assigns disk IDs in attach order (vmDiskConfig has no id field):
//   - rootfs (DiskImagePath)  → _disk0 → /dev/vda
//   - ExtraDisks[0]           → _disk1 → /dev/vdb
//   - ExtraDisks[1]           → _disk2 → /dev/vdc
//   - ExtraDisks[2]           → _disk3 → /dev/vdd
//
// A wrong mapping routes GrowDisk to the wrong filesystem: data loss, not
// a failed build. This test is the primary guard against that class of bug.
func TestDiskIndexMapping(t *testing.T) {
	cases := []struct {
		diskIndex    int
		wantCHID     string
		wantGuestDev string
	}{
		{0, "_disk1", "/dev/vdb"}, // workspace disk in a single-extra-disk setup
		{1, "_disk2", "/dev/vdc"}, // second extra disk
		{2, "_disk3", "/dev/vdd"}, // third extra disk (3-disk topology)
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("index=%d", tc.diskIndex), func(t *testing.T) {
			gotID := diskIndexToCHID(tc.diskIndex)
			if gotID != tc.wantCHID {
				t.Errorf("diskIndexToCHID(%d) = %q, want %q", tc.diskIndex, gotID, tc.wantCHID)
			}
			gotDev := diskIndexToGuestDev(tc.diskIndex)
			if gotDev != tc.wantGuestDev {
				t.Errorf("diskIndexToGuestDev(%d) = %q, want %q", tc.diskIndex, gotDev, tc.wantGuestDev)
			}
		})
	}
}

// TestGrowDisk_shrinkRejected verifies SAFETY RULE 1 (grow-only):
//   - targetBytes < currentSize → error returned, file untouched.
//   - targetBytes == currentSize → nil (no-op), file untouched.
func TestGrowDisk_shrinkRejected(t *testing.T) {
	dir := t.TempDir()
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	const origSize = 10 * 1024 * 1024
	diskPath := filepath.Join(dir, "extra0.raw")
	if err := createSizedFile(diskPath, origSize); err != nil {
		t.Fatalf("create backing file: %v", err)
	}
	d.cfg.ExtraDisks = []ExtraDisk{{Path: diskPath}}

	// Fake socket so the running-required check passes.
	fakeSockListener(t, d.socketPath(id), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	r := NewSandboxResizer(d, id, resize.Bounds{}, 512*1024*1024, 1)

	// Shrink: must return error.
	if err := r.GrowDisk(context.Background(), 0, origSize-1); err == nil {
		t.Error("GrowDisk shrink target returned nil; want error")
	}

	// Equal size: must be a no-op (nil error).
	if err := r.GrowDisk(context.Background(), 0, origSize); err != nil {
		t.Errorf("GrowDisk equal-size returned error: %v; want nil (no-op)", err)
	}

	// File must be unchanged.
	fi, _ := os.Stat(diskPath)
	if fi.Size() != origSize {
		t.Errorf("backing file size = %d, want %d (unchanged after no-op/shrink reject)", fi.Size(), origSize)
	}
}

// TestGrowDisk_notRunning verifies SAFETY RULE 2 (running-required): GrowDisk
// rejects the grow and leaves the file untouched when the VMM socket is absent.
func TestGrowDisk_notRunning(t *testing.T) {
	dir := t.TempDir()
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	const origSize = 5 * 1024 * 1024
	diskPath := filepath.Join(dir, "extra0.raw")
	if err := createSizedFile(diskPath, origSize); err != nil {
		t.Fatalf("create backing file: %v", err)
	}
	d.cfg.ExtraDisks = []ExtraDisk{{Path: diskPath}}

	// No socket file: sandbox is not running.
	r := NewSandboxResizer(d, id, resize.Bounds{}, 512*1024*1024, 1)

	if err := r.GrowDisk(context.Background(), 0, origSize*2); err == nil {
		t.Error("GrowDisk with missing socket returned nil; want error")
	}

	fi, _ := os.Stat(diskPath)
	if fi.Size() != origSize {
		t.Errorf("backing file size = %d after not-running rejection; want %d (unchanged)", fi.Size(), origSize)
	}
}

// TestGrowDisk_success verifies the happy path:
//   - backing file is expanded to targetBytes
//   - CH receives PUT /api/v1/vm.resize-disk with chDiskID "_disk1" and correct size
func TestGrowDisk_success(t *testing.T) {
	dir := t.TempDir()
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	const origSize = 5 * 1024 * 1024
	const targetSize = 10 * 1024 * 1024
	diskPath := filepath.Join(dir, "extra0.raw")
	if err := createSizedFile(diskPath, origSize); err != nil {
		t.Fatalf("create backing file: %v", err)
	}
	d.cfg.ExtraDisks = []ExtraDisk{{Path: diskPath}}

	var mu sync.Mutex
	var gotID string
	var gotSize uint64

	mux := http.NewServeMux()
	// The mock intentionally mirrors CH's strictness on the wire format: it
	// decodes into a local struct with hardcoded json:"desired_size" (NOT
	// vmResizeDiskRequest) so that any regression of the client tag back to
	// "size" leaves DesiredSize == 0 and the mock returns HTTP 400 — exactly
	// as real CH does — causing the test to fail on the old wire format.
	mux.HandleFunc("/api/v1/vm.resize-disk", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID          string `json:"id"`
			DesiredSize uint64 `json:"desired_size"` // must match CH VmResizeDisk schema @ v52.0
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.DesiredSize == 0 {
			// desired_size missing or zero — reject exactly as real CH does.
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		gotID = body.ID
		gotSize = body.DesiredSize
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	fakeSockListenerMux(t, d.socketPath(id), mux)

	resizer := NewSandboxResizer(d, id, resize.Bounds{}, 512*1024*1024, 1)
	if err := resizer.GrowDisk(context.Background(), 0, targetSize); err != nil {
		t.Fatalf("GrowDisk: %v", err)
	}

	// Backing file must be expanded.
	fi, _ := os.Stat(diskPath)
	if fi.Size() != targetSize {
		t.Errorf("backing file size = %d, want %d", fi.Size(), targetSize)
	}

	// CH must have received the correct disk ID and size.
	mu.Lock()
	receivedID, receivedSize := gotID, gotSize
	mu.Unlock()

	const wantID = "_disk1" // ExtraDisks[0] → _disk{0+1} = _disk1
	if receivedID != wantID {
		t.Errorf("vm.resize-disk id = %q, want %q", receivedID, wantID)
	}
	if receivedSize != targetSize {
		t.Errorf("vm.resize-disk size = %d, want %d", receivedSize, uint64(targetSize))
	}
}

// TestGrowDisk_atomicRollback verifies SAFETY RULE 4 (atomic-on-failure): when
// vm.resize-disk fails, the backing file is truncated back to its original size.
func TestGrowDisk_atomicRollback(t *testing.T) {
	dir := t.TempDir()
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	const origSize = 5 * 1024 * 1024
	diskPath := filepath.Join(dir, "extra0.raw")
	if err := createSizedFile(diskPath, origSize); err != nil {
		t.Fatalf("create backing file: %v", err)
	}
	d.cfg.ExtraDisks = []ExtraDisk{{Path: diskPath}}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.resize-disk", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError) // simulate CH rejection
	})
	fakeSockListenerMux(t, d.socketPath(id), mux)

	resizer := NewSandboxResizer(d, id, resize.Bounds{}, 512*1024*1024, 1)
	if err := resizer.GrowDisk(context.Background(), 0, origSize*2); err == nil {
		t.Error("GrowDisk with failing CH returned nil; want error")
	}

	// Backing file must be rolled back to original size.
	fi, _ := os.Stat(diskPath)
	if fi.Size() != origSize {
		t.Errorf("backing file size after rollback = %d, want %d (must be original)", fi.Size(), origSize)
	}
}

// TestGrowDisk_sparseAccountsForActual proves the sparse-aware pool check
// (SAFETY RULE 3) uses actual host allocation rather than apparent file size.
//
// A sparse file created with ftruncate has an apparent size (fi.Size()) that
// far exceeds the blocks actually allocated on the host (Stat_t.Blocks * 512).
// The check must use actual bytes because:
//
//   - The guest can fill existing holes without any resize call, silently
//     consuming host blocks up to the current apparent size at any time.
//   - Using (targetBytes - apparentSize) as "needed" severely under-counts
//     the real host commitment; using (targetBytes - actualBytes) accounts for
//     all holes the guest could write into.
//
// The test verifies:
//  1. diskActualBytes returns a value less than fi.Size() for a genuinely
//     sparse file (proves the measurement function works as designed).
//  2. checkFreeSpace does not error when called with targetBytes == apparentSize,
//     proving the function handles sparse inputs without panic or wrong error.
//
// If the underlying filesystem does not support sparse files (e.g. FAT32,
// tmpfs on certain kernels) the test skips rather than failing.
func TestGrowDisk_sparseAccountsForActual(t *testing.T) {
	const apparentSize = 16 * 1024 * 1024 // 16 MiB apparent (fully sparse)
	const writeSize = 4096                // 4 KiB actually written at offset 0

	dir := t.TempDir()
	diskPath := filepath.Join(dir, "sparse.raw")

	// Create a sparse file: truncate to large apparent size, write a small
	// payload at the beginning to force at least one block to be allocated.
	f, err := os.Create(diskPath)
	if err != nil {
		t.Fatalf("create sparse file: %v", err)
	}
	if err := f.Truncate(apparentSize); err != nil {
		f.Close()
		t.Fatalf("truncate to apparent size: %v", err)
	}
	if _, err := f.WriteAt(make([]byte, writeSize), 0); err != nil {
		f.Close()
		t.Fatalf("write at offset 0: %v", err)
	}
	f.Close()

	// --- Part 1: diskActualBytes returns blocks-based allocation, not fi.Size() ---

	actual, err := diskActualBytes(diskPath)
	if err != nil {
		t.Fatalf("diskActualBytes: %v", err)
	}
	fi, err := os.Stat(diskPath)
	if err != nil {
		t.Fatalf("os.Stat: %v", err)
	}
	if fi.Size() != apparentSize {
		t.Fatalf("apparent size = %d B, want %d B", fi.Size(), apparentSize)
	}
	if actual >= fi.Size() {
		// tmpfs and some other filesystems do not create holes; skip rather than fail.
		t.Skipf("filesystem does not support sparse files "+
			"(apparent=%d B, actual=%d B); skipping sparse-aware accounting test",
			fi.Size(), actual)
	}
	t.Logf("sparse file: apparent=%d B, actual=%d B (actual is %.1f%% of apparent)",
		fi.Size(), actual, 100*float64(actual)/float64(fi.Size()))

	// --- Part 2: checkFreeSpace accepts target == apparentSize ---
	// The commitment is (apparentSize - actualBytes) which is less than
	// apparentSize and easily fits on any dev-machine temp partition.
	if err := checkFreeSpace(diskPath, apparentSize); err != nil {
		t.Errorf("checkFreeSpace(target=apparentSize) returned unexpected error: %v", err)
	}
}

// TestResizeMemory verifies that ResizeMemory calls PUT /api/v1/vm.resize with
// the correct desired_ram value and updates CurrentMemoryBytes.
func TestResizeMemory(t *testing.T) {
	dir := t.TempDir()
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	const bootMem int64 = 512 * 1024 * 1024
	const targetMem int64 = 768 * 1024 * 1024

	var mu sync.Mutex
	var gotDesiredRAM uint64
	var gotDesiredVCPUs *uint32
	var gotDesiredBalloon *uint64

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.resize", func(w http.ResponseWriter, r *http.Request) {
		var req vmResizeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		if req.DesiredRAM != nil {
			gotDesiredRAM = *req.DesiredRAM
		}
		gotDesiredVCPUs = req.DesiredVCPUs
		gotDesiredBalloon = req.DesiredBalloon
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	fakeSockListenerMux(t, d.socketPath(id), mux)

	bounds := resize.Bounds{MemMinBytes: bootMem, MemMaxBytes: 1024 * 1024 * 1024}
	resizer := NewSandboxResizer(d, id, bounds, bootMem, 1)

	if got := resizer.CurrentMemoryBytes(); got != bootMem {
		t.Errorf("CurrentMemoryBytes before resize = %d, want %d", got, bootMem)
	}

	got, err := resizer.ResizeMemory(context.Background(), targetMem)
	if err != nil {
		t.Fatalf("ResizeMemory: %v", err)
	}
	if got != targetMem {
		t.Errorf("ResizeMemory returned %d, want %d", got, targetMem)
	}
	if resizer.CurrentMemoryBytes() != targetMem {
		t.Errorf("CurrentMemoryBytes after resize = %d, want %d", resizer.CurrentMemoryBytes(), targetMem)
	}

	mu.Lock()
	ramSent := gotDesiredRAM
	vcpusSent := gotDesiredVCPUs
	balloonSent := gotDesiredBalloon
	mu.Unlock()

	if ramSent != uint64(targetMem) {
		t.Errorf("desired_ram sent = %d, want %d", ramSent, uint64(targetMem))
	}
	// ResizeMemory must NOT touch desired_vcpus or desired_balloon (D-DC-08).
	if vcpusSent != nil {
		t.Errorf("desired_vcpus must be nil in ResizeMemory call; got %v", *vcpusSent)
	}
	if balloonSent != nil {
		t.Errorf("desired_balloon must be nil in ResizeMemory call (balloon is host reclaim only, D-DC-08); got %v", *balloonSent)
	}
}

// TestResizeMemory_clamp verifies that ResizeMemory clamps to Bounds.
func TestResizeMemory_clamp(t *testing.T) {
	dir := t.TempDir()
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	const bootMem int64 = 512 * 1024 * 1024
	const maxMem int64 = 1024 * 1024 * 1024

	var mu sync.Mutex
	var gotRAM uint64

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.resize", func(w http.ResponseWriter, r *http.Request) {
		var req vmResizeRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		if req.DesiredRAM != nil {
			gotRAM = *req.DesiredRAM
		}
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	fakeSockListenerMux(t, d.socketPath(id), mux)

	bounds := resize.Bounds{MemMinBytes: bootMem, MemMaxBytes: maxMem}
	resizer := NewSandboxResizer(d, id, bounds, bootMem, 1)

	// Request above ceiling: should be clamped to maxMem.
	got, err := resizer.ResizeMemory(context.Background(), maxMem*2)
	if err != nil {
		t.Fatalf("ResizeMemory: %v", err)
	}
	if got != maxMem {
		t.Errorf("ResizeMemory with over-ceiling target returned %d, want %d (clamped to MemMaxBytes)", got, maxMem)
	}

	mu.Lock()
	ramSent := gotRAM
	mu.Unlock()

	if ramSent != uint64(maxMem) {
		t.Errorf("desired_ram sent = %d, want %d (clamped)", ramSent, uint64(maxMem))
	}
}

// TestResizeMemory_alignsUnaligned is the driver-side regression guard for the
// CH hotplug-alignment bug. A caller that hands ResizeMemory an unaligned
// target (1091000320 B ≈ 1040.5 MiB — one of the exact values that 500'd on the
// live sandbox) must have it snapped to a memHotplugAlignBytes multiple within
// bounds before the desired_ram reaches CH.
//
// Fails before the fix (desired_ram sent = 1091000320, not a 256 MiB multiple);
// passes after (rounded up to 1342177280 = 1.25 GiB).
func TestResizeMemory_alignsUnaligned(t *testing.T) {
	dir := t.TempDir()
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	const bootMem int64 = 512 * 1024 * 1024
	const maxMem int64 = 4 * 1024 * 1024 * 1024
	const unaligned int64 = 1091000320   // ~1040.5 MiB — a real live 500'ing target
	const wantAligned int64 = 1342177280 // 1.25 GiB, next 256 MiB block up

	var mu sync.Mutex
	var gotRAM uint64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.resize", func(w http.ResponseWriter, r *http.Request) {
		var req vmResizeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		if req.DesiredRAM != nil {
			gotRAM = *req.DesiredRAM
		}
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	fakeSockListenerMux(t, d.socketPath(id), mux)

	bounds := resize.Bounds{MemMinBytes: bootMem, MemMaxBytes: maxMem}
	resizer := NewSandboxResizer(d, id, bounds, bootMem, 1)

	got, err := resizer.ResizeMemory(context.Background(), unaligned)
	if err != nil {
		t.Fatalf("ResizeMemory: %v", err)
	}
	if got%memHotplugAlignBytes != 0 {
		t.Errorf("ResizeMemory returned %d, not a multiple of memHotplugAlignBytes %d", got, memHotplugAlignBytes)
	}
	if got != wantAligned {
		t.Errorf("ResizeMemory returned %d, want %d (unaligned target snapped up to a block multiple)", got, wantAligned)
	}
	if resizer.CurrentMemoryBytes() != wantAligned {
		t.Errorf("CurrentMemoryBytes = %d, want %d", resizer.CurrentMemoryBytes(), wantAligned)
	}

	mu.Lock()
	ramSent := gotRAM
	mu.Unlock()
	if ramSent%uint64(memHotplugAlignBytes) != 0 {
		t.Errorf("desired_ram sent to CH = %d, not a 256 MiB multiple (CH would reject with HTTP 500)", ramSent)
	}
	if ramSent != uint64(wantAligned) {
		t.Errorf("desired_ram sent = %d, want %d", ramSent, uint64(wantAligned))
	}
}

// TestResizeCPU verifies that ResizeCPU calls PUT /api/v1/vm.resize with the
// correct desired_vcpus and updates CurrentVCPUs. Also verifies desired_ram
// and desired_balloon are absent (not other dimensions polluted).
func TestResizeCPU(t *testing.T) {
	dir := t.TempDir()
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	var mu sync.Mutex
	var gotVCPUs uint32
	var gotRAM *uint64
	var gotBalloon *uint64

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.resize", func(w http.ResponseWriter, r *http.Request) {
		var req vmResizeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		if req.DesiredVCPUs != nil {
			gotVCPUs = *req.DesiredVCPUs
		}
		gotRAM = req.DesiredRAM
		gotBalloon = req.DesiredBalloon
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	fakeSockListenerMux(t, d.socketPath(id), mux)

	bounds := resize.Bounds{VCPUMin: 1, VCPUMax: 4}
	resizer := NewSandboxResizer(d, id, bounds, 512*1024*1024, 1)

	if resizer.CurrentVCPUs() != 1 {
		t.Errorf("CurrentVCPUs before resize = %d, want 1", resizer.CurrentVCPUs())
	}

	got, err := resizer.ResizeCPU(context.Background(), 2)
	if err != nil {
		t.Fatalf("ResizeCPU: %v", err)
	}
	if got != 2 {
		t.Errorf("ResizeCPU returned %d, want 2", got)
	}
	if resizer.CurrentVCPUs() != 2 {
		t.Errorf("CurrentVCPUs after resize = %d, want 2", resizer.CurrentVCPUs())
	}

	mu.Lock()
	vcpuSent, ramSent, balloonSent := gotVCPUs, gotRAM, gotBalloon
	mu.Unlock()

	if vcpuSent != 2 {
		t.Errorf("desired_vcpus sent = %d, want 2", vcpuSent)
	}
	if ramSent != nil {
		t.Errorf("desired_ram must be nil in ResizeCPU call; got %v", *ramSent)
	}
	if balloonSent != nil {
		t.Errorf("desired_balloon must be nil in ResizeCPU call; got %v", *balloonSent)
	}
}

// TestMemHotplugCmdline_presentWhenEnabled verifies AR-DRV-AC2: when
// MemoryMaxMiB > 0 the required cmdline tokens are appended. This exercises
// the package-level constants directly without booting a VM.
func TestMemHotplugCmdline_presentWhenEnabled(t *testing.T) {
	cmdline := diskBootCmdline + memHotplugCmdline

	if !strings.Contains(cmdline, "memhp_default_state=online") {
		t.Errorf("cmdline %q missing required token memhp_default_state=online", cmdline)
	}
	if !strings.Contains(cmdline, "memory_hotplug.online_policy=auto-movable") {
		t.Errorf("cmdline %q missing required token memory_hotplug.online_policy=auto-movable", cmdline)
	}
}

// TestMemHotplugCmdline_absentWhenDisabled verifies AR-N-AC1 (negative scope):
// when MemoryMaxMiB == 0 the hotplug tokens do not appear in the cmdline.
func TestMemHotplugCmdline_absentWhenDisabled(t *testing.T) {
	// With MemoryMaxMiB == 0 the driver does NOT append memHotplugCmdline.
	cmdline := diskBootCmdline // no append

	if strings.Contains(cmdline, "memhp_default_state") {
		t.Errorf("cmdline %q contains hotplug token when MemoryMaxMiB=0 (AR-N-AC1 violation)", cmdline)
	}
	if strings.Contains(cmdline, "memory_hotplug") {
		t.Errorf("cmdline %q contains hotplug token when MemoryMaxMiB=0 (AR-N-AC1 violation)", cmdline)
	}
}

// TestNewConfig_validation verifies that New rejects misconfigured
// MemoryMaxMiB and VCPUMax values and accepts valid ones.
func TestNewConfig_validation(t *testing.T) {
	dir := t.TempDir()
	base := Config{
		BinaryPath:   "/usr/bin/true",
		SocketDir:    dir,
		KernelPath:   "/dev/null",
		VCPUs:        2,
		MemoryMiB:    512,
		StartTimeout: 200 * time.Millisecond,
	}

	bad := []struct {
		name string
		cfg  func(Config) Config
	}{
		{"MemoryMaxMiB==MemoryMiB", func(c Config) Config { c.MemoryMaxMiB = 512; return c }},
		{"MemoryMaxMiB<MemoryMiB", func(c Config) Config { c.MemoryMaxMiB = 256; return c }},
		{"VCPUMax==VCPUs", func(c Config) Config { c.VCPUMax = 2; return c }},
		{"VCPUMax<VCPUs", func(c Config) Config { c.VCPUMax = 1; return c }},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg(base)); err == nil {
				t.Errorf("New with %s returned nil error; want error", tc.name)
			}
		})
	}

	good := []struct {
		name string
		cfg  func(Config) Config
	}{
		{"valid MemoryMaxMiB", func(c Config) Config { c.MemoryMaxMiB = 1024; return c }},
		{"valid VCPUMax", func(c Config) Config { c.VCPUMax = 4; return c }},
	}
	for _, tc := range good {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg(base)); err != nil {
				t.Errorf("New with %s returned error: %v", tc.name, err)
			}
		})
	}
}

// TestBuildCmdline_HotplugPlacement exercises the four cases of buildCmdline
// and is the anti-regression guard for the PID-1 boundary insertion fix.
//
// Reverting buildCmdline to always-append (removing the strings.Index branch)
// causes cases 2 and 4 to fail: the hotplug tokens would land after " --" and
// never reach the kernel.
//
// Cases:
//
//  1. auto-resize off, no " --" boundary → base unchanged (AR-N-AC1)
//  2. auto-resize off, " --" boundary present → base unchanged (AR-N-AC1)
//  3. auto-resize on,  no " --" boundary → hotplug tokens appended at end
//  4. auto-resize on,  " --" boundary present → hotplug tokens inserted BEFORE boundary
func TestBuildCmdline_HotplugPlacement(t *testing.T) {
	// baseNoBoundary is diskBootCmdline: no PID-1 args, no " --".
	baseNoBoundary := diskBootCmdline
	// baseWithBoundary simulates a Cmdline built by the substrate when
	// workspace mounts or other PID-1 args are appended via " --".
	baseWithBoundary := diskBootCmdline + " -- --workspace-mount /work"

	// Case 1: auto-resize off, no boundary → unchanged.
	if got := buildCmdline(baseNoBoundary, 0); got != baseNoBoundary {
		t.Errorf("case 1 (resize-off, no boundary): got %q, want base unchanged", got)
	}

	// Case 2: auto-resize off, boundary present → unchanged.
	if got := buildCmdline(baseWithBoundary, 0); got != baseWithBoundary {
		t.Errorf("case 2 (resize-off, boundary): got %q, want base unchanged", got)
	}

	// Case 3: auto-resize on, no boundary → hotplug tokens appended.
	got3 := buildCmdline(baseNoBoundary, 1024)
	if !strings.Contains(got3, "memhp_default_state=online") {
		t.Errorf("case 3 (resize-on, no boundary): missing memhp_default_state=online in %q", got3)
	}
	if strings.Contains(got3, " --") {
		t.Errorf("case 3 (resize-on, no boundary): unexpected \" --\" in %q", got3)
	}
	if !strings.HasPrefix(got3, baseNoBoundary) {
		t.Errorf("case 3 (resize-on, no boundary): hotplug tokens must be a suffix, not prefix; got %q", got3)
	}

	// Case 4: auto-resize on, " --" boundary present → hotplug inserted BEFORE boundary.
	// This is the critical placement check. Reverting to always-append fails here.
	got4 := buildCmdline(baseWithBoundary, 1024)
	boundaryIdx := strings.Index(got4, " --")
	if boundaryIdx < 0 {
		t.Fatalf("case 4 (resize-on, boundary): PID-1 boundary \" --\" missing from result %q", got4)
	}
	hotplugIdx := strings.Index(got4, "memhp_default_state=online")
	if hotplugIdx < 0 {
		t.Fatalf("case 4 (resize-on, boundary): memhp_default_state=online missing from result %q", got4)
	}
	if hotplugIdx > boundaryIdx {
		t.Errorf("case 4 (resize-on, boundary): hotplug tokens at offset %d are AFTER \" --\" at offset %d; "+
			"kernel will not receive them (revert of insertion fix detected)", hotplugIdx, boundaryIdx)
	}
	// "--auto-resize" is not a recognised PID-1 token and must not appear anywhere.
	if strings.Contains(got4, "--auto-resize") {
		t.Errorf("case 4 (resize-on, boundary): unexpected --auto-resize token found in %q", got4)
	}
}

// TestGrowDisk_sendsGrowRequestToGuest verifies that after a successful
// vm.resize-disk, GrowDisk dials the guest over vsock and sends a GrowRequest
// carrying the correct DiskIndex and TargetBytes.
//
// Break-and-restore proof: comment out the dialGuest call in GrowDisk and
// the test fails with "timed out: GrowDisk did not send GrowRequest to guest".
func TestGrowDisk_sendsGrowRequestToGuest(t *testing.T) {
	dir := t.TempDir()
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	const origSize = 5 * 1024 * 1024
	const targetSize = 10 * 1024 * 1024
	diskPath := filepath.Join(dir, "extra0.raw")
	if err := createSizedFile(diskPath, origSize); err != nil {
		t.Fatalf("create backing file: %v", err)
	}
	d.cfg.ExtraDisks = []ExtraDisk{{Path: diskPath}}

	// Fake CH HTTP: accept vm.resize-disk unconditionally.
	fakeSockListener(t, d.socketPath(id), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	// Fake vsock: host side returned from dialGuest; guest side serves the wire protocol.
	hostConn, guestConn := net.Pipe()

	var gotReq resize.GrowRequest
	var guestDecodeErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer guestConn.Close()
		gotReq, guestDecodeErr = resize.DecodeGrowRequest(guestConn)
		if guestDecodeErr != nil {
			return
		}
		_ = resize.EncodeGrowResponse(guestConn, resize.GrowResponse{ResultBytes: int64(targetSize)})
	}()

	resizer := NewSandboxResizer(d, id, resize.Bounds{}, 512*1024*1024, 1)
	resizer.dialGuest = func(ctx context.Context, _ domain.SandboxID, _ uint32) (net.Conn, error) {
		return hostConn, nil
	}

	if err := resizer.GrowDisk(context.Background(), 0, targetSize); err != nil {
		t.Fatalf("GrowDisk: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out: GrowDisk did not send GrowRequest to guest")
	}

	if guestDecodeErr != nil {
		t.Fatalf("guest DecodeGrowRequest: %v", guestDecodeErr)
	}
	if gotReq.DiskIndex != 0 {
		t.Errorf("GrowRequest.DiskIndex = %d, want 0 (ExtraDisks[0])", gotReq.DiskIndex)
	}
	if gotReq.TargetBytes != targetSize {
		t.Errorf("GrowRequest.TargetBytes = %d, want %d", gotReq.TargetBytes, targetSize)
	}
}

// TestGrowDisk_guestUnreachable_bestEffort verifies that a failed vsock dial
// (guest unreachable) does NOT fail GrowDisk. The host-side file expansion
// and CH notification are already committed; the guest phase is best-effort.
func TestGrowDisk_guestUnreachable_bestEffort(t *testing.T) {
	dir := t.TempDir()
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	const origSize = 5 * 1024 * 1024
	const targetSize = 10 * 1024 * 1024
	diskPath := filepath.Join(dir, "extra0.raw")
	if err := createSizedFile(diskPath, origSize); err != nil {
		t.Fatalf("create backing file: %v", err)
	}
	d.cfg.ExtraDisks = []ExtraDisk{{Path: diskPath}}

	fakeSockListener(t, d.socketPath(id), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	resizer := NewSandboxResizer(d, id, resize.Bounds{}, 512*1024*1024, 1)
	resizer.dialGuest = func(ctx context.Context, _ domain.SandboxID, _ uint32) (net.Conn, error) {
		return nil, fmt.Errorf("vsock: guest unreachable (injected)")
	}

	// GrowDisk must return nil even when the guest is unreachable.
	if err := resizer.GrowDisk(context.Background(), 0, targetSize); err != nil {
		t.Errorf("GrowDisk returned error when guest unreachable; want nil (best-effort): %v", err)
	}

	// Backing file must still be expanded.
	fi, _ := os.Stat(diskPath)
	if fi.Size() != targetSize {
		t.Errorf("backing file size = %d, want %d (must be expanded despite guest unreachable)", fi.Size(), targetSize)
	}
}

// --- buildVolumePostGrowHooks tests ------------------------------------------

// TestBuildVolumePostGrowHooks_NamedVolumeDiskIndexWired verifies that
// buildVolumePostGrowHooks registers a hook at the correct 0-based ExtraDisks
// index for a named-volume disk.ext4 path and that non-volume disks get no hook.
//
// MUTATION PROOF (index wiring): if the loop in buildVolumePostGrowHooks uses
// the wrong index (e.g. always 0), this test fails for the non-zero-index case.
// If the disk.ext4 filename check is removed, the workspace disk path also gets
// a hook, causing len(hooks) == 2 and the second assertion to fail.
func TestBuildVolumePostGrowHooks_NamedVolumeDiskIndexWired(t *testing.T) {
	dir := t.TempDir()

	// ExtraDisks layout: [named-vol at index 0, shadow at index 1, workspace at index 2]
	extraDisks := []ExtraDisk{
		{Path: filepath.Join(dir, "volumes", "myrepo-main-docker", "disk.ext4")},
		{Path: filepath.Join(dir, "disks", "shadow.raw")}, // shadow disk: not a volume
		{Path: filepath.Join(dir, "disks", "ws.raw")},     // workspace disk: not a volume
	}

	hooks := buildVolumePostGrowHooks(extraDisks)

	if len(hooks) != 1 {
		t.Fatalf("expected 1 hook (named-vol only), got %d: hooks for indices %v",
			len(hooks), hookKeys(hooks))
	}
	if _, ok := hooks[0]; !ok {
		t.Errorf("hook missing for index 0 (named-vol disk); registered indices: %v", hookKeys(hooks))
	}
	if _, ok := hooks[1]; ok {
		t.Errorf("unexpected hook at index 1 (shadow disk should not have a hook)")
	}
}

// TestBuildVolumePostGrowHooks_MultipleNamedDisks verifies that two named-volume
// disks in ExtraDisks[0] and ExtraDisks[2] each get their own hook with the
// correct index, while a shadow disk at ExtraDisks[1] gets none.
func TestBuildVolumePostGrowHooks_MultipleNamedDisks(t *testing.T) {
	dir := t.TempDir()

	extraDisks := []ExtraDisk{
		{Path: filepath.Join(dir, "volumes", "docker-vol", "disk.ext4")},   // index 0 → named
		{Path: filepath.Join(dir, "disks", "shadow.raw")},                   // index 1 → shadow
		{Path: filepath.Join(dir, "volumes", "cache-vol", "disk.ext4")},    // index 2 → named
	}

	hooks := buildVolumePostGrowHooks(extraDisks)

	if len(hooks) != 2 {
		t.Fatalf("expected 2 hooks, got %d: %v", len(hooks), hookKeys(hooks))
	}
	if _, ok := hooks[0]; !ok {
		t.Errorf("hook missing at index 0 (docker-vol)")
	}
	if _, ok := hooks[2]; !ok {
		t.Errorf("hook missing at index 2 (cache-vol)")
	}
	if _, ok := hooks[1]; ok {
		t.Errorf("unexpected hook at index 1 (shadow disk)")
	}
}

// hookKeys returns the sorted indices of entries in hooks, for test diagnostics.
func hookKeys(hooks map[int]func(context.Context, int64)) []int {
	keys := make([]int, 0, len(hooks))
	for k := range hooks {
		keys = append(keys, k)
	}
	return keys
}

// --- helpers -----------------------------------------------------------------

// fakeSockListener starts an httptest server on a Unix socket at path,
// serving all requests with handler. The server is stopped at test cleanup.
func fakeSockListener(t *testing.T, path string, handler http.Handler) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen unix %s: %v", path, err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
}

// fakeSockListenerMux starts an httptest server on a Unix socket using a mux.
func fakeSockListenerMux(t *testing.T, path string, mux *http.ServeMux) {
	t.Helper()
	fakeSockListener(t, path, mux)
}

// createSizedFile creates a new file at path and sets its size to size bytes
// using os.Truncate. This is the host-side equivalent of what
// substrate.CreateSparseFile does for disk images.
func createSizedFile(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	f.Close()
	return os.Truncate(path, size)
}
