package cloudhypervisor

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"

	"github.com/IniZio/nexus/internal/core/domain"
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

	if rem := targetBytes % memHotplugAlignBytes; rem != 0 {
		targetBytes += memHotplugAlignBytes - rem
		if targetBytes > r.bounds.MemMaxBytes {
			targetBytes -= memHotplugAlignBytes
		}
	}

	desiredRAM := uint64(targetBytes)
	c := newClient(r.d.socketPath(r.id))
	if err := c.VMResize(ctx, &desiredRAM, nil, nil); err != nil {
		return r.memBytes.Load(), fmt.Errorf("cloudhypervisor: ResizeMemory %s: %w", r.id, err)
	}

	r.memBytes.Store(targetBytes)
	return targetBytes, nil
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

// GrowDisk expands the host backing file for ExtraDisks[diskIndex] to targetBytes,
// notifies CH via vm.resize-disk, then instructs the guest to run resize2fs over vsock.
// Grow-only; the host leg rolls back only when vm.resize-disk fails. A guest-leg
// failure leaves host file and CH at target and returns an error so the governor
// retries: the next call with the same target skips the host leg and re-sends
// resize2fs, which is idempotent.
func (r *SandboxResizer) GrowDisk(ctx context.Context, diskIndex int, targetBytes int64) error {
	if diskIndex < 0 {
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: diskIndex %d must be >= 0", r.id, diskIndex)
	}
	if diskIndex >= len(r.d.cfg.ExtraDisks) {
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: diskIndex %d out of range (ExtraDisks len %d)",
			r.id, diskIndex, len(r.d.cfg.ExtraDisks))
	}
	if diskIndex > 25 {
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: diskIndex %d exceeds virtio-blk device namespace (max 25)",
			r.id, diskIndex)
	}

	chDiskID := diskIndexToCHID(diskIndex)
	guestDev := diskIndexToGuestDev(diskIndex)
	_ = guestDev

	diskPath := r.d.cfg.ExtraDisks[diskIndex].Path

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

func diskIndexToCHID(diskIndex int) string {
	return fmt.Sprintf("_disk%d", diskIndex+1)
}

func diskIndexToGuestDev(diskIndex int) string {
	return fmt.Sprintf("/dev/vd%c", 'b'+rune(diskIndex))
}
