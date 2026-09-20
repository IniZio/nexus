package cloudhypervisor

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/resize"
	"github.com/IniZio/nexus/internal/core/volumestore"
)

const namedVolumeDiskFile = "disk.ext4"

type SandboxResizer struct {
	d      *CHDriver
	id     domain.SandboxID
	bounds resize.Bounds

	dialGuest func(ctx context.Context, id domain.SandboxID, port uint32) (net.Conn, error)

	memBytes      atomic.Int64
	vcpus         atomic.Int32
	postGrowHooks map[int]func(ctx context.Context, targetBytes int64)
}

func NewSandboxResizer(d *CHDriver, id domain.SandboxID, bounds resize.Bounds, bootMemBytes int64, bootVCPUs int32) *SandboxResizer {
	r := &SandboxResizer{d: d, id: id, bounds: bounds, dialGuest: d.DialGuest}
	r.memBytes.Store(bootMemBytes)
	r.vcpus.Store(bootVCPUs)
	r.postGrowHooks = buildVolumePostGrowHooks(d.cfg.ExtraDisks)
	// Eagerly load mem state from the live VM so the correct mode (balloon vs
	// virtio-mem) is known before the first normaliser call. Best-effort: a
	// Debug log on failure leaves state nil so getOrLoadMemState retries later.
	loadCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := d.getOrLoadMemState(loadCtx, id); err != nil {
		slog.Debug("cloudhypervisor: NewSandboxResizer: pre-load mem state (will retry)",
			"sandbox", id, "err", err)
	}
	return r
}

func buildVolumePostGrowHooks(extraDisks []ExtraDisk) map[int]func(context.Context, int64) {
	var hooks map[int]func(context.Context, int64)
	for i, ed := range extraDisks {
		if filepath.Base(ed.Path) != namedVolumeDiskFile {
			continue
		}
		name := filepath.Base(filepath.Dir(ed.Path))
		volRoot := filepath.Dir(filepath.Dir(ed.Path))
		vs := volumestore.New(volRoot)
		idx := i
		if hooks == nil {
			hooks = make(map[int]func(context.Context, int64))
		}
		hooks[idx] = func(ctx context.Context, targetBytes int64) {
			if err := vs.UpdateSizeBytes(ctx, name, targetBytes); err != nil {
				slog.Warn("cloudhypervisor.disk.grow_volume_record_update_failed",
					"diskIndex", idx,
					"volumeName", name,
					"targetBytes", targetBytes,
					"err", err,
				)
			}
		}
	}
	return hooks
}

const memHotplugAlignBytes int64 = 256 * 1024 * 1024

func (r *SandboxResizer) ResizeMemory(ctx context.Context, targetBytes int64) (int64, error) {
	if targetBytes < r.bounds.MemMinBytes {
		targetBytes = r.bounds.MemMinBytes
	}
	if targetBytes > r.bounds.MemMaxBytes {
		targetBytes = r.bounds.MemMaxBytes
	}

	got, err := r.d.ResizeMemory(ctx, r.id, targetBytes)
	if err != nil {
		return r.memBytes.Load(), err
	}
	r.memBytes.Store(got)
	return got, nil
}

func (r *SandboxResizer) CurrentMemoryBytes() int64 {
	return r.memBytes.Load()
}

func (r *SandboxResizer) ResizeCPU(ctx context.Context, targetVCPUs int32) (int32, error) {
	if targetVCPUs < r.bounds.VCPUMin {
		targetVCPUs = r.bounds.VCPUMin
	}
	if targetVCPUs > r.bounds.VCPUMax {
		targetVCPUs = r.bounds.VCPUMax
	}

	desiredVCPUs := uint32(targetVCPUs)
	c := newClient(r.d.socketPath(r.id))
	if err := c.VMResize(ctx, nil, &desiredVCPUs, nil); err != nil {
		return r.vcpus.Load(), fmt.Errorf("cloudhypervisor: ResizeCPU %s: %w", r.id, err)
	}

	r.vcpus.Store(targetVCPUs)
	return targetVCPUs, nil
}

func (r *SandboxResizer) CurrentVCPUs() int32 {
	return r.vcpus.Load()
}

func (r *SandboxResizer) rootDiskPath() string {
	if r.d.cfg.DiskImagePath != "" {
		return r.d.cfg.DiskImagePath
	}
	if r.d.cfg.DiskDir != "" {
		return filepath.Join(r.d.cfg.DiskDir, r.id.String()+".raw")
	}
	return ""
}

func (r *SandboxResizer) diskIndexToPathAndCHID(diskIndex int) (diskPath, chDiskID string, err error) {
	if diskIndex == resize.RootDiskIndex {
		p := r.rootDiskPath()
		if p == "" {
			return "", "", fmt.Errorf("root disk path unknown (neither DiskImagePath nor DiskDir is set)")
		}
		return p, "_disk0", nil
	}
	if diskIndex < 0 {
		return "", "", fmt.Errorf("invalid diskIndex %d", diskIndex)
	}
	if diskIndex >= len(r.d.cfg.ExtraDisks) {
		return "", "", fmt.Errorf("diskIndex %d out of range (ExtraDisks len %d)", diskIndex, len(r.d.cfg.ExtraDisks))
	}
	if diskIndex > 25 {
		return "", "", fmt.Errorf("diskIndex %d exceeds virtio-blk device namespace (max 25)", diskIndex)
	}
	return r.d.cfg.ExtraDisks[diskIndex].Path, diskIndexToCHID(diskIndex), nil
}

// GrowDisk expands the host backing file for the disk at diskIndex to targetBytes,
// notifies CH via vm.resize-disk, then instructs the guest to run resize2fs over vsock.
// Pass resize.RootDiskIndex to grow the root disk (/dev/vda). Grow-only; the host
// leg rolls back only when vm.resize-disk fails. A guest-leg failure leaves host
// file and CH at target and returns an error so the governor retries: the next call
// with the same target skips the host leg and re-sends resize2fs, which is idempotent.
func (r *SandboxResizer) GrowDisk(ctx context.Context, diskIndex int, targetBytes int64) error {
	diskPath, chDiskID, resolveErr := r.diskIndexToPathAndCHID(diskIndex)
	if resolveErr != nil {
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: %w", r.id, resolveErr)
	}

	guestDev := diskIndexToGuestDev(diskIndex)
	_ = guestDev

	if _, err := os.Stat(r.d.socketPath(r.id)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("cloudhypervisor: GrowDisk %s: sandbox is not running (socket missing)", r.id)
		}
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: check VMM socket: %w", r.id, err)
	}

	fi, err := os.Stat(diskPath)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: stat %s: %w", r.id, diskPath, err)
	}
	currentSize := fi.Size()
	hostGrown := targetBytes > currentSize
	if !hostGrown {
		targetBytes = currentSize
	}

	if hostGrown {
		if err := checkFreeSpace(diskPath, targetBytes); err != nil {
			return fmt.Errorf("cloudhypervisor: GrowDisk %s: %w", r.id, err)
		}

		if err := os.Truncate(diskPath, targetBytes); err != nil {
			return fmt.Errorf("cloudhypervisor: GrowDisk %s: expand backing file: %w", r.id, err)
		}

		c := newClient(r.d.socketPath(r.id))
		if err := c.VMResizeDisk(ctx, chDiskID, uint64(targetBytes)); err != nil {
			if rollbackErr := os.Truncate(diskPath, currentSize); rollbackErr != nil {
				return fmt.Errorf("cloudhypervisor: GrowDisk %s: vm.resize-disk failed (%w); rollback truncate also failed (%v) — disk state is UNKNOWN",
					r.id, err, rollbackErr)
			}
			return fmt.Errorf("cloudhypervisor: GrowDisk %s: vm.resize-disk: %w", r.id, err)
		}
	}

	if conn, dialErr := r.dialGuest(ctx, r.id, resize.TelemetryVsockPort); dialErr != nil {
		slog.Warn("cloudhypervisor.disk.grow_guest_unreachable",
			"sandbox", r.id,
			"diskIndex", diskIndex,
			"targetBytes", targetBytes,
			"err", dialErr,
		)
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: disk %d: guest unreachable: %w", r.id, diskIndex, dialErr)
	} else if growErr := r.sendGrowToGuest(conn, diskIndex, targetBytes); growErr != nil {
		slog.Warn("cloudhypervisor.disk.grow_guest_failed",
			"sandbox", r.id,
			"diskIndex", diskIndex,
			"targetBytes", targetBytes,
			"err", growErr,
		)
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: disk %d: guest resize failed: %w", r.id, diskIndex, growErr)
	}

	if fn, ok := r.postGrowHooks[diskIndex]; ok {
		fn(ctx, targetBytes)
	}
	return nil
}

func (r *SandboxResizer) sendGrowToGuest(conn net.Conn, diskIndex int, targetBytes int64) error {
	defer conn.Close()
	req := resize.GrowRequest{DiskIndex: diskIndex, TargetBytes: targetBytes}
	if err := resize.EncodeGrowRequest(conn, req); err != nil {
		return fmt.Errorf("cloudhypervisor: sendGrowToGuest %s: encode: %w", r.id, err)
	}
	resp, err := resize.DecodeGrowResponse(conn)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: sendGrowToGuest %s: decode response: %w", r.id, err)
	}
	if resp.Error != "" {
		return fmt.Errorf("cloudhypervisor: sendGrowToGuest %s: guest resize2fs: %s", r.id, resp.Error)
	}
	return nil
}

func diskActualBytes(path string) (int64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Blocks) * 512, nil
}

func checkFreeSpace(diskPath string, targetBytes int64) error {
	dir := diskPath
	for i := len(dir) - 1; i >= 0; i-- {
		if dir[i] == '/' {
			if i == 0 {
				dir = "/"
			} else {
				dir = dir[:i]
			}
			break
		}
	}

	actualBytes, err := diskActualBytes(diskPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", diskPath, err)
	}

	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return fmt.Errorf("statfs %s: %w", dir, err)
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	needed := targetBytes - actualBytes
	if needed < 0 {
		needed = 0
	}
	if free < needed {
		return fmt.Errorf("host pool free space insufficient: actual alloc %d B, target %d B, need %d B more, have %d B free on %s",
			actualBytes, targetBytes, needed, free, dir)
	}
	return nil
}

type DriftStatus = driver.DriftStatus

const (
	DriftOK        = driver.DriftOK
	DriftSuspect   = driver.DriftSuspect
	DriftCorrected = driver.DriftCorrected
)

const (
	driftMinSamples = 3
	driftMinWindow  = 15 * time.Second
)

type vmMemState struct {
	mode     driver.MemoryMode
	bootMiB  uint32
	totalMiB uint32
	balloon  atomic.Uint32

	mu             sync.Mutex
	clockFn        func() time.Time
	driftFirstSeen time.Time
	driftCount     int
}

func (s *vmMemState) now() time.Time {
	if s.clockFn != nil {
		return s.clockFn()
	}
	return time.Now()
}

func (d *CHDriver) storeMemState(id domain.SandboxID, bootMiB, totalMiB, initBalloonMiB uint32) {
	st := &vmMemState{bootMiB: bootMiB, totalMiB: totalMiB}
	if totalMiB > bootMiB {
		st.mode = driver.MemoryModeBalloon
	} else {
		st.mode = driver.MemoryModeFlat
	}
	st.balloon.Store(initBalloonMiB)
	d.memMu.Lock()
	d.memState[id] = st
	d.memMu.Unlock()
}

func (d *CHDriver) clearMemState(id domain.SandboxID) {
	d.memMu.Lock()
	delete(d.memState, id)
	d.memMu.Unlock()
}

func (d *CHDriver) loadMemStateBestEffort(id domain.SandboxID) *vmMemState {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, err := d.getOrLoadMemState(ctx, id)
	if err != nil {
		slog.Debug("cloudhypervisor: lazy load mem state", "sandbox", id, "err", err)
		return nil
	}
	return st
}

func (d *CHDriver) getOrLoadMemState(ctx context.Context, id domain.SandboxID) (*vmMemState, error) {
	d.memMu.Lock()
	st := d.memState[id]
	d.memMu.Unlock()
	if st != nil {
		return st, nil
	}
	c := newClient(d.socketPath(id))
	info, _, err := c.VMInfoFull(ctx)
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: load mem state for %s: %w", id, err)
	}
	if info == nil {
		return nil, fmt.Errorf("cloudhypervisor: load mem state for %s: VM absent", id)
	}
	st = adoptMemState(info)
	d.memMu.Lock()
	if existing := d.memState[id]; existing != nil {
		d.memMu.Unlock()
		return existing, nil
	}
	d.memState[id] = st
	d.memMu.Unlock()
	return st, nil
}

func adoptMemState(info *vmInfoResponse) *vmMemState {
	st := &vmMemState{}
	if info.Config == nil || info.Config.Memory == nil {
		st.mode = driver.MemoryModeFlat
		return st
	}
	mem := info.Config.Memory
	sizeMiB := uint32(mem.SizeBytes / (1024 * 1024)) //nolint:gosec

	if info.Config.Balloon != nil && info.Config.Balloon.SizeBytes > 0 {
		// Balloon: memory.size is the ceiling; balloon inflates within it.
		balloonMiB := uint32(info.Config.Balloon.SizeBytes / (1024 * 1024)) //nolint:gosec
		st.mode = driver.MemoryModeBalloon
		st.totalMiB = sizeMiB
		st.bootMiB = sizeMiB - balloonMiB
		st.balloon.Store(balloonMiB)
	} else if mem.HotplugSize > 0 {
		// VirtioMem: memory.size = boot RAM; hotplug_size = extra capacity.
		hotplugMiB := uint32(mem.HotplugSize / (1024 * 1024)) //nolint:gosec
		st.mode = driver.MemoryModeVirtioMem
		st.bootMiB = sizeMiB
		st.totalMiB = sizeMiB + hotplugMiB
	} else {
		st.mode = driver.MemoryModeFlat
		st.bootMiB = sizeMiB
		st.totalMiB = sizeMiB
	}
	return st
}

func (d *CHDriver) MemoryMode(id domain.SandboxID) driver.MemoryMode {
	d.memMu.Lock()
	st := d.memState[id]
	d.memMu.Unlock()
	if st == nil {
		st = d.loadMemStateBestEffort(id)
	}
	if st == nil {
		return driver.MemoryModeFlat
	}
	return st.mode
}

func (d *CHDriver) BalloonBytes(id domain.SandboxID) int64 {
	d.memMu.Lock()
	st := d.memState[id]
	d.memMu.Unlock()
	if st == nil {
		st = d.loadMemStateBestEffort(id)
	}
	if st == nil || st.mode != driver.MemoryModeBalloon {
		return 0
	}
	return int64(st.balloon.Load()) * 1024 * 1024
}

func (d *CHDriver) ObserveSample(id domain.SandboxID, memTotal, memAvail uint64) (driver.DriftStatus, uint32) {
	d.memMu.Lock()
	st := d.memState[id]
	d.memMu.Unlock()
	if st == nil {
		st = d.loadMemStateBestEffort(id)
	}
	if st == nil || st.mode != driver.MemoryModeBalloon {
		return driver.DriftOK, 0
	}
	return st.observeSample(memTotal, memAvail)
}

func (s *vmMemState) observeSample(memTotal, memAvail uint64) (driver.DriftStatus, uint32) {
	const mib = 1024 * 1024
	if memTotal < mib {
		return driver.DriftOK, s.balloon.Load()
	}
	var usedMiB uint32
	if memTotal > memAvail {
		usedMiB = uint32((memTotal - memAvail) / mib) //nolint:gosec
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.balloon.Load()
	if cur <= usedMiB {
		s.driftFirstSeen = time.Time{}
		s.driftCount = 0
		return driver.DriftOK, cur
	}
	now := s.now()
	if s.driftCount == 0 {
		s.driftFirstSeen = now
	}
	s.driftCount++
	if s.driftCount >= driftMinSamples && now.Sub(s.driftFirstSeen) >= driftMinWindow {
		s.balloon.Store(usedMiB)
		s.driftFirstSeen = time.Time{}
		s.driftCount = 0
		return driver.DriftCorrected, usedMiB
	}
	if s.driftCount == 1 {
		return driver.DriftSuspect, cur
	}
	return driver.DriftOK, cur
}

func (d *CHDriver) ResizeMemory(ctx context.Context, id domain.SandboxID, targetBytes int64) (int64, error) {
	st, err := d.getOrLoadMemState(ctx, id)
	if err != nil {
		return 0, err
	}
	switch st.mode {
	case driver.MemoryModeBalloon:
		return d.resizeMemoryBalloon(ctx, id, st, targetBytes)
	case driver.MemoryModeVirtioMem:
		return d.resizeMemoryVirtioMem(ctx, id, st, targetBytes)
	default:
		return 0, fmt.Errorf("cloudhypervisor: ResizeMemory %s: mode flat does not support resize", id)
	}
}

func (d *CHDriver) resizeMemoryBalloon(ctx context.Context, id domain.SandboxID, st *vmMemState, targetBytes int64) (int64, error) {
	targetMiB := uint32(targetBytes / (1024 * 1024)) //nolint:gosec
	if targetMiB < st.bootMiB {
		targetMiB = st.bootMiB
	}
	if targetMiB > st.totalMiB {
		targetMiB = st.totalMiB
	}
	newBalloonMiB := st.totalMiB - targetMiB
	if max := st.totalMiB - st.bootMiB; newBalloonMiB > max {
		newBalloonMiB = max
	}
	if err := d.ResizeBalloon(ctx, id, newBalloonMiB); err != nil {
		return int64(st.totalMiB-st.balloon.Load()) * 1024 * 1024,
			fmt.Errorf("cloudhypervisor: ResizeMemory %s: %w", id, err)
	}
	st.mu.Lock()
	st.balloon.Store(newBalloonMiB)
	st.driftFirstSeen = time.Time{}
	st.driftCount = 0
	st.mu.Unlock()
	return int64(st.totalMiB-newBalloonMiB) * 1024 * 1024, nil
}

func (d *CHDriver) resizeMemoryVirtioMem(ctx context.Context, id domain.SandboxID, st *vmMemState, targetBytes int64) (int64, error) {
	const align = int64(memHotplugAlignBytes)
	if rem := targetBytes % align; rem != 0 {
		targetBytes += align - rem
		if targetBytes > int64(st.totalMiB)*1024*1024 {
			targetBytes -= align
		}
	}
	desiredRAM := uint64(targetBytes) //nolint:gosec
	c := newClient(d.socketPath(id))
	if err := c.VMResize(ctx, &desiredRAM, nil, nil); err != nil {
		return 0, fmt.Errorf("cloudhypervisor: ResizeMemory %s: %w", id, err)
	}
	return targetBytes, nil
}

func (d *CHDriver) CurrentMemoryBytes(id domain.SandboxID) int64 {
	d.memMu.Lock()
	st := d.memState[id]
	d.memMu.Unlock()
	if st == nil {
		st = d.loadMemStateBestEffort(id)
	}
	if st == nil {
		return 0
	}
	switch st.mode {
	case driver.MemoryModeBalloon:
		return int64(st.totalMiB-st.balloon.Load()) * 1024 * 1024
	case driver.MemoryModeFlat:
		return int64(st.bootMiB) * 1024 * 1024
	default:
		return 0
	}
}

func diskIndexToCHID(diskIndex int) string {
	return fmt.Sprintf("_disk%d", diskIndex+1)
}

func diskIndexToGuestDev(diskIndex int) string {
	return fmt.Sprintf("/dev/vd%c", 'b'+rune(diskIndex))
}
