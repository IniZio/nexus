package cloudhypervisor

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync/atomic"
	"syscall"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/resize"
)

// SandboxResizer implements [resize.MemoryResizer], [resize.CPUResizer], and
// [resize.DiskResizer] for a single running sandbox via the CH REST API.
//
// Disk index resolution (critical — wrong index means resize2fs on the wrong
// filesystem, which is data loss, not a failed build):
//
//	CH auto-assigns disk IDs in attach order (no id field in vmDiskConfig).
//	  rootfs (DiskImagePath)  → _disk0 → /dev/vda
//	  ExtraDisks[0]           → _disk1 → /dev/vdb
//	  ExtraDisks[1]           → _disk2 → /dev/vdc
//	  ExtraDisks[i]           → _disk{i+1} → /dev/vd{chr('b'+i)}
//
// GrowDisk takes a 0-based diskIndex into ExtraDisks and derives both
// the CH disk id and the guest device path by formula, never hardcoded.
// OLD-nexus hardcoded "_disk0" / "/dev/vda" because it had exactly one
// workspace disk; nexus3 has N ExtraDisks, so the derivation is explicit.
//
// Construct with [NewSandboxResizer]. Thread-safe.
type SandboxResizer struct {
	d      *CHDriver
	id     domain.SandboxID
	bounds resize.Bounds

	// dialGuest opens a vsock connection to the sandbox guest at the given
	// port. Defaults to d.DialGuest; overridable in tests.
	dialGuest func(ctx context.Context, id domain.SandboxID, port uint32) (net.Conn, error)

	memBytes atomic.Int64 // current desired memory in bytes; updated on each successful ResizeMemory
	vcpus    atomic.Int32 // current desired vCPU count; updated on each successful ResizeCPU
}

// NewSandboxResizer returns a SandboxResizer for the given sandbox.
// bootMemBytes and bootVCPUs must reflect the values used at vm.create so that
// CurrentMemoryBytes and CurrentVCPUs return correct values before the first
// resize call.
func NewSandboxResizer(d *CHDriver, id domain.SandboxID, bounds resize.Bounds, bootMemBytes int64, bootVCPUs int32) *SandboxResizer {
	r := &SandboxResizer{d: d, id: id, bounds: bounds, dialGuest: d.DialGuest}
	r.memBytes.Store(bootMemBytes)
	r.vcpus.Store(bootVCPUs)
	return r
}

// ResizeMemory adjusts guest RAM to targetBytes via PUT /api/v1/vm.resize and
// returns the new current allocation. targetBytes is clamped to
// [Bounds.MemMinBytes, Bounds.MemMaxBytes]. Calls VMResize(desired_ram) —
// never the balloon path (D-DC-08: balloon is host reclaim only).
//
// Implements [resize.MemoryResizer].
func (r *SandboxResizer) ResizeMemory(ctx context.Context, targetBytes int64) (int64, error) {
	// Clamp to bounds.
	if targetBytes < r.bounds.MemMinBytes {
		targetBytes = r.bounds.MemMinBytes
	}
	if targetBytes > r.bounds.MemMaxBytes {
		targetBytes = r.bounds.MemMaxBytes
	}

	desiredRAM := uint64(targetBytes)
	c := newClient(r.d.socketPath(r.id))
	if err := c.VMResize(ctx, &desiredRAM, nil, nil); err != nil {
		return r.memBytes.Load(), fmt.Errorf("cloudhypervisor: ResizeMemory %s: %w", r.id, err)
	}

	r.memBytes.Store(targetBytes)
	return targetBytes, nil
}

// CurrentMemoryBytes returns the current desired RAM allocation in bytes.
// Reflects the most recent successful ResizeMemory call, or the boot-time
// value if no resize has occurred.
//
// Implements [resize.MemoryResizer].
func (r *SandboxResizer) CurrentMemoryBytes() int64 {
	return r.memBytes.Load()
}

// ResizeCPU sets the desired online vCPU count to targetVCPUs via PUT
// /api/v1/vm.resize and returns the new count. targetVCPUs is clamped to
// [Bounds.VCPUMin, Bounds.VCPUMax].
//
// Implements [resize.CPUResizer].
func (r *SandboxResizer) ResizeCPU(ctx context.Context, targetVCPUs int32) (int32, error) {
	// Clamp to bounds.
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

// CurrentVCPUs returns the current desired vCPU count. Reflects the most
// recent successful ResizeCPU call, or the boot-time value if no resize has
// occurred.
//
// Implements [resize.CPUResizer].
func (r *SandboxResizer) CurrentVCPUs() int32 {
	return r.vcpus.Load()
}

// GrowDisk expands the host backing file for ExtraDisks[diskIndex] to
// targetBytes and notifies CH via PUT /api/v1/vm.resize-disk.
//
// # Disk index → CH id → guest device derivation
//
//	diskIndex 0 → CH "_disk1" → guest "/dev/vdb"
//	diskIndex 1 → CH "_disk2" → guest "/dev/vdc"
//	diskIndex i → CH "_disk{i+1}" → guest "/dev/vd{chr('b'+i)}"
//
// The rootfs (DiskImagePath) occupies "_disk0"/"/dev/vda" and is never a
// valid target for GrowDisk. The formula is explicit and never hardcoded so
// that a multi-disk topology routes each grow to the correct device.
//
// # Safety rules (ported from OLD CHANGESET 2026-06-19-driver-contract-lifecycle-hardening §2.8)
//
//  1. Grow-only: targetBytes == currentSize → no-op (nil). targetBytes <
//     currentSize → error (never shrink a live filesystem).
//  2. Running-required: the VMM API socket must be reachable. GrowDisk checks
//     socket existence before touching the backing file, so a not-running
//     sandbox cannot corrupt the disk image.
//  3. Pool-free-space: (targetBytes − actualBytes) <= hostFree, where
//     actualBytes = Stat_t.Blocks * 512 (sparse-aware actual host allocation).
//     Checked via syscall.Stat + syscall.Statfs on the backing file path.
//  4. Atomic-on-failure: if vm.resize-disk fails after the backing file was
//     expanded, the file is truncated back to its original size so the VM and
//     host-file remain in sync.
//
// The guest must run resize2fs separately via a GrowRequest wire command (AR-GA
// owns that path). GrowDisk returns as soon as CH has acknowledged the new size.
//
// Implements [resize.DiskResizer].
func (r *SandboxResizer) GrowDisk(ctx context.Context, diskIndex int, targetBytes int64) error {
	if diskIndex < 0 {
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: diskIndex %d must be >= 0", r.id, diskIndex)
	}
	if diskIndex >= len(r.d.cfg.ExtraDisks) {
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: diskIndex %d out of range (ExtraDisks len %d)",
			r.id, diskIndex, len(r.d.cfg.ExtraDisks))
	}

	// Guard against overflowing the virtio-blk vd* device namespace.
	// 'b'+25 = 'z'; beyond that the guest device path would be malformed.
	if diskIndex > 25 {
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: diskIndex %d exceeds virtio-blk device namespace (max 25)",
			r.id, diskIndex)
	}

	// Derive CH disk id and guest device path from diskIndex using the
	// canonical package-level helpers. Rootfs (DiskImagePath) occupies _disk0
	// and /dev/vda; ExtraDisks[i] starts at _disk1 / /dev/vdb.
	chDiskID := diskIndexToCHID(diskIndex)
	guestDev := diskIndexToGuestDev(diskIndex)

	_ = guestDev // guestDev is carried in the GrowRequest.DiskIndex derivation on the guest; not used directly on the host

	diskPath := r.d.cfg.ExtraDisks[diskIndex].Path

	// SAFETY RULE 2 (running-required): verify the VMM socket exists before
	// touching the backing file. A missing socket means no VM is running and
	// any truncate would corrupt the image without a recipient to notify.
	if _, err := os.Stat(r.d.socketPath(r.id)); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("cloudhypervisor: GrowDisk %s: sandbox is not running (socket missing)", r.id)
		}
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: check VMM socket: %w", r.id, err)
	}

	// SAFETY RULE 1 (grow-only): stat the backing file for current size.
	fi, err := os.Stat(diskPath)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: stat %s: %w", r.id, diskPath, err)
	}
	currentSize := fi.Size()
	if targetBytes <= currentSize {
		if targetBytes == currentSize {
			return nil // equal-size is a no-op, not an error
		}
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: cannot shrink disk (current %d B, target %d B)",
			r.id, currentSize, targetBytes)
	}
	// SAFETY RULE 3 (pool-free-space): sparse-aware host pool check.
	// checkFreeSpace computes commitment as (targetBytes - actualBlocks*512)
	// rather than (targetBytes - apparentSize); see checkFreeSpace for limits.
	if err := checkFreeSpace(diskPath, targetBytes); err != nil {
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: %w", r.id, err)
	}

	// SAFETY RULE 4 (atomic-on-failure): expand the backing file first, then
	// notify CH. If CH rejects the new size, truncate the file back to original.
	if err := os.Truncate(diskPath, targetBytes); err != nil {
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: expand backing file: %w", r.id, err)
	}

	c := newClient(r.d.socketPath(r.id))
	if err := c.VMResizeDisk(ctx, chDiskID, uint64(targetBytes)); err != nil {
		// Roll back the file expansion so disk image and VMM stay in sync.
		if rollbackErr := os.Truncate(diskPath, currentSize); rollbackErr != nil {
			// Log the rollback failure alongside the CH error; the caller must
			// treat the disk state as unknown and should not issue resize2fs.
			return fmt.Errorf("cloudhypervisor: GrowDisk %s: vm.resize-disk failed (%w); rollback truncate also failed (%v) — disk state is UNKNOWN",
				r.id, err, rollbackErr)
		}
		return fmt.Errorf("cloudhypervisor: GrowDisk %s: vm.resize-disk: %w", r.id, err)
	}

	// Instruct the guest agent to grow the filesystem (resize2fs) over vsock
	// port TelemetryVsockPort (3002). This is best-effort: a dial failure or a
	// guest-side resize2fs error does NOT fail GrowDisk — the host backing file
	// is already expanded and CH has acknowledged the new size. A missed
	// resize2fs leaves free space unavailable inside the guest until the guest
	// agent reconnects or the sandbox restarts, but it does not corrupt data.
	// Both failure modes are logged with enough context to diagnose from logs alone.
	if conn, dialErr := r.dialGuest(ctx, r.id, resize.TelemetryVsockPort); dialErr != nil {
		slog.Warn("cloudhypervisor.disk.grow_guest_unreachable",
			"sandbox", r.id,
			"diskIndex", diskIndex,
			"targetBytes", targetBytes,
			"err", dialErr,
		)
	} else if growErr := r.sendGrowToGuest(conn, diskIndex, targetBytes); growErr != nil {
		slog.Warn("cloudhypervisor.disk.grow_guest_failed",
			"sandbox", r.id,
			"diskIndex", diskIndex,
			"targetBytes", targetBytes,
			"err", growErr,
		)
	}
	return nil
}

// sendGrowToGuest sends a GrowRequest over conn and reads the GrowResponse.
// conn is always closed when this function returns. A non-nil error is
// returned so the caller can log it; GrowDisk never propagates it to its
// own caller because the host-side grow is already committed.
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

// diskActualBytes returns the number of bytes currently allocated on the host
// filesystem for the file at path. For sparse files this is less than the
// file's logical (apparent) size returned by os.FileInfo.Size.
//
// Uses syscall.Stat_t.Blocks which is always in 512-byte units (POSIX),
// independent of the filesystem block size reported by Statfs.Bsize. Blocks is
// int64 on both linux and darwin; no build-tag split is required.
func diskActualBytes(path string) (int64, error) {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Blocks) * 512, nil
}

// checkFreeSpace verifies that the host filesystem can accommodate growing the
// sparse backing file at diskPath to targetBytes.
//
// # Sparse-aware accounting
//
// The check uses actual host allocation (Stat_t.Blocks * 512) rather than
// apparent file size (fi.Size()). For a sparse ext4 image the two diverge
// widely: a 10 GiB logical file may occupy only a few hundred MiB on the host.
// The check asserts:
//
//	(targetBytes − actualBytes) <= hostFree
//
// This is more conservative than delta-against-apparent-size because it
// accounts for all the holes a guest could fill both before and after the grow,
// not just the incremental bytes added by this resize call.
//
// # Known limits (document here so host-full incidents are debuggable)
//
//   - TOCTOU: another process can fill the host between this Stat+Statfs call
//     and the subsequent ftruncate. There is no filesystem-level atomic
//     check+grow primitive; a near-zero margin is a soft warning, not a
//     guarantee.
//
//   - Cross-VM contention: multiple sandboxes on the same host partition each
//     check the same free-space budget independently with no cross-process lock.
//     N concurrent grows can each observe the full free space and collectively
//     over-commit. Operators running dense VM pools should monitor host block
//     usage (df -i + du --apparent-size vs du) directly, not rely solely on
//     this check.
//
//   - st.Blocks counts 512-byte POSIX allocation units, not Statfs.Bsize
//     blocks. Both sides are converted to bytes explicitly to avoid unit mixing.
func checkFreeSpace(diskPath string, targetBytes int64) error {
	dir := diskPath
	// Trim to directory component.
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

	// Sparse-aware: measure actual host allocation in bytes.
	actualBytes, err := diskActualBytes(diskPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", diskPath, err)
	}

	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return fmt.Errorf("statfs %s: %w", dir, err)
	}
	// Bavail (not Bfree) counts blocks available to unprivileged users;
	// reserve the same margin the kernel does for the ext4 reserved block %.
	// Both Bavail and Bsize are converted to int64 explicitly: Bsize is uint32
	// on darwin and int64 on linux, so a direct multiplication without a cast
	// fails to compile on one or the other (same class as the st.Dev issue
	// that Half A fixed with _other.go build-tag stubs).
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

// diskIndexToCHID returns the CH-assigned disk identifier for the given
// 0-based ExtraDisks index. This function is the canonical formula used by
// GrowDisk; it is also tested directly in driver_resize_test.go.
//
//	diskIndex 0 → "_disk1"   (rootfs occupies "_disk0")
//	diskIndex 1 → "_disk2"
//	diskIndex i → "_disk{i+1}"
func diskIndexToCHID(diskIndex int) string {
	return fmt.Sprintf("_disk%d", diskIndex+1)
}

// diskIndexToGuestDev returns the guest virtio-blk device path for the given
// 0-based ExtraDisks index.
//
//	diskIndex 0 → "/dev/vdb"
//	diskIndex 1 → "/dev/vdc"
//	diskIndex i → "/dev/vd{chr('b'+i)}"
func diskIndexToGuestDev(diskIndex int) string {
	return fmt.Sprintf("/dev/vd%c", 'b'+rune(diskIndex))
}
