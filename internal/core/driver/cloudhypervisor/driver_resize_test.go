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

func TestDiskIndexMapping(t *testing.T) {
	cases := []struct {
		diskIndex    int
		wantCHID     string
		wantGuestDev string
	}{
		{0, "_disk1", "/dev/vdb"},
		{1, "_disk2", "/dev/vdc"},
		{2, "_disk3", "/dev/vdd"},
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

	fakeSockListener(t, d.socketPath(id), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	r := NewSandboxResizer(d, id, resize.Bounds{}, 512*1024*1024, 1)

	if err := r.GrowDisk(context.Background(), 0, origSize-1); err == nil {
		t.Error("GrowDisk shrink target returned nil; want error")
	}

	if err := r.GrowDisk(context.Background(), 0, origSize); err != nil {
		t.Errorf("GrowDisk equal-size returned error: %v; want nil (no-op)", err)
	}

	fi, _ := os.Stat(diskPath)
	if fi.Size() != origSize {
		t.Errorf("backing file size = %d, want %d (unchanged after no-op/shrink reject)", fi.Size(), origSize)
	}
}

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

	r := NewSandboxResizer(d, id, resize.Bounds{}, 512*1024*1024, 1)

	if err := r.GrowDisk(context.Background(), 0, origSize*2); err == nil {
		t.Error("GrowDisk with missing socket returned nil; want error")
	}

	fi, _ := os.Stat(diskPath)
	if fi.Size() != origSize {
		t.Errorf("backing file size = %d after not-running rejection; want %d (unchanged)", fi.Size(), origSize)
	}
}

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
	mux.HandleFunc("/api/v1/vm.resize-disk", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			ID          string `json:"id"`
			DesiredSize uint64 `json:"desired_size"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if body.DesiredSize == 0 {
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
	resizer.dialGuest = func(ctx context.Context, _ domain.SandboxID, _ uint32) (net.Conn, error) {
		hostConn, guestConn := net.Pipe()
		go func() {
			defer guestConn.Close()
			if _, err := resize.DecodeGrowRequest(guestConn); err != nil {
				return
			}
			_ = resize.EncodeGrowResponse(guestConn, resize.GrowResponse{ResultBytes: targetSize})
		}()
		return hostConn, nil
	}

	if err := resizer.GrowDisk(context.Background(), 0, targetSize); err != nil {
		t.Fatalf("GrowDisk: %v", err)
	}

	fi, _ := os.Stat(diskPath)
	if fi.Size() != targetSize {
		t.Errorf("backing file size = %d, want %d", fi.Size(), targetSize)
	}

	mu.Lock()
	receivedID, receivedSize := gotID, gotSize
	mu.Unlock()

	const wantID = "_disk1"
	if receivedID != wantID {
		t.Errorf("vm.resize-disk id = %q, want %q", receivedID, wantID)
	}
	if receivedSize != targetSize {
		t.Errorf("vm.resize-disk size = %d, want %d", receivedSize, uint64(targetSize))
	}
}

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
		w.WriteHeader(http.StatusInternalServerError)
	})
	fakeSockListenerMux(t, d.socketPath(id), mux)

	resizer := NewSandboxResizer(d, id, resize.Bounds{}, 512*1024*1024, 1)
	if err := resizer.GrowDisk(context.Background(), 0, origSize*2); err == nil {
		t.Error("GrowDisk with failing CH returned nil; want error")
	}

	fi, _ := os.Stat(diskPath)
	if fi.Size() != origSize {
		t.Errorf("backing file size after rollback = %d, want %d (must be original)", fi.Size(), origSize)
	}
}

func TestGrowDisk_sparseAccountsForActual(t *testing.T) {
	const apparentSize = 16 * 1024 * 1024
	const writeSize = 4096

	dir := t.TempDir()
	diskPath := filepath.Join(dir, "sparse.raw")

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
		t.Skipf("filesystem does not support sparse files (apparent=%d B, actual=%d B); skipping", fi.Size(), actual)
	}
	t.Logf("sparse file: apparent=%d B, actual=%d B (actual is %.1f%% of apparent)",
		fi.Size(), actual, 100*float64(actual)/float64(fi.Size()))

	if err := checkFreeSpace(diskPath, apparentSize); err != nil {
		t.Errorf("checkFreeSpace(target=apparentSize) returned unexpected error: %v", err)
	}
}

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
	if vcpusSent != nil {
		t.Errorf("desired_vcpus must be nil in ResizeMemory call; got %v", *vcpusSent)
	}
	if balloonSent != nil {
		t.Errorf("desired_balloon must be nil in ResizeMemory call (D-DC-08); got %v", *balloonSent)
	}
}

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

func TestResizeMemory_alignsUnaligned(t *testing.T) {
	dir := t.TempDir()
	d := newTestDriver(t, dir)
	id := domain.NewSandboxID()

	const bootMem int64 = 512 * 1024 * 1024
	const maxMem int64 = 4 * 1024 * 1024 * 1024
	const unaligned int64 = 1091000320
	const wantAligned int64 = 1342177280

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

func TestMemHotplugCmdline_presentWhenEnabled(t *testing.T) {
	cmdline := diskBootCmdline + memHotplugCmdline

	if !strings.Contains(cmdline, "memhp_default_state=online") {
		t.Errorf("cmdline %q missing memhp_default_state=online (AR-DRV-AC2 violation)", cmdline)
	}
	if !strings.Contains(cmdline, "memory_hotplug.online_policy=auto-movable") {
		t.Errorf("cmdline %q missing memory_hotplug.online_policy=auto-movable (AR-DRV-AC2 violation)", cmdline)
	}
}

func TestMemHotplugCmdline_absentWhenDisabled(t *testing.T) {
	cmdline := diskBootCmdline

	if strings.Contains(cmdline, "memhp_default_state") {
		t.Errorf("cmdline %q contains hotplug token when MemoryMaxMiB=0 (AR-N-AC1 violation)", cmdline)
	}
	if strings.Contains(cmdline, "memory_hotplug") {
		t.Errorf("cmdline %q contains hotplug token when MemoryMaxMiB=0 (AR-N-AC1 violation)", cmdline)
	}
}

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

func TestBuildCmdline_HotplugPlacement(t *testing.T) {
	baseNoBoundary := diskBootCmdline
	baseWithBoundary := diskBootCmdline + " -- --workspace-mount /work"

	if got := buildCmdline(baseNoBoundary, 0); got != baseNoBoundary {
		t.Errorf("case 1 (resize-off, no boundary): got %q, want base unchanged", got)
	}

	if got := buildCmdline(baseWithBoundary, 0); got != baseWithBoundary {
		t.Errorf("case 2 (resize-off, boundary): got %q, want base unchanged", got)
	}

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
	if strings.Contains(got4, "--auto-resize") {
		t.Errorf("case 4 (resize-on, boundary): unexpected --auto-resize token found in %q", got4)
	}
}

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

	fakeSockListener(t, d.socketPath(id), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

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

func TestGrowDisk_guestUnreachable(t *testing.T) {
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

	if err := resizer.GrowDisk(context.Background(), 0, targetSize); err == nil {
		t.Error("GrowDisk returned nil when guest unreachable; want error so governor does not advance lastGrow")
	}

	fi, _ := os.Stat(diskPath)
	if fi.Size() != targetSize {
		t.Errorf("backing file size = %d, want %d (host commit must not be rolled back on guest dial error)", fi.Size(), targetSize)
	}
}

// TestGrowDisk_guestResizeFails verifies that GrowResponse.Error causes GrowDisk to
// return non-nil so the governor's growErrLogged latch fires and lastGrow is not advanced.
// Without the fix GrowDisk returned nil despite the error, causing backing-file/filesystem divergence.
func TestGrowDisk_guestResizeFails(t *testing.T) {
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
		hostConn, guestConn := net.Pipe()
		go func() {
			defer guestConn.Close()
			if _, err := resize.DecodeGrowRequest(guestConn); err != nil {
				return
			}
			_ = resize.EncodeGrowResponse(guestConn, resize.GrowResponse{
				Error: "resize2fs: No such file or directory",
			})
		}()
		return hostConn, nil
	}

	err := resizer.GrowDisk(context.Background(), 0, targetSize)
	if err == nil {
		t.Error("GrowDisk returned nil when guest resize failed; want non-nil error (backing file diverges from guest filesystem)")
	}

	fi, _ := os.Stat(diskPath)
	if fi.Size() != targetSize {
		t.Errorf("backing file size = %d, want %d (host commit must not be rolled back on guest resize error)", fi.Size(), targetSize)
	}
}

func TestBuildVolumePostGrowHooks_NamedVolumeDiskIndexWired(t *testing.T) {
	dir := t.TempDir()

	extraDisks := []ExtraDisk{
		{Path: filepath.Join(dir, "volumes", "myrepo-main-docker", "disk.ext4")},
		{Path: filepath.Join(dir, "disks", "shadow.raw")},
		{Path: filepath.Join(dir, "disks", "ws.raw")},
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

func TestBuildVolumePostGrowHooks_MultipleNamedDisks(t *testing.T) {
	dir := t.TempDir()

	extraDisks := []ExtraDisk{
		{Path: filepath.Join(dir, "volumes", "docker-vol", "disk.ext4")},
		{Path: filepath.Join(dir, "disks", "shadow.raw")},
		{Path: filepath.Join(dir, "volumes", "cache-vol", "disk.ext4")},
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

func hookKeys(hooks map[int]func(context.Context, int64)) []int {
	keys := make([]int, 0, len(hooks))
	for k := range hooks {
		keys = append(keys, k)
	}
	return keys
}

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

func fakeSockListenerMux(t *testing.T, path string, mux *http.ServeMux) {
	t.Helper()
	fakeSockListener(t, path, mux)
}

func createSizedFile(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	f.Close()
	return os.Truncate(path, size)
}
