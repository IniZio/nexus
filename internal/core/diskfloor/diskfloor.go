// Package diskfloor holds the host free-space floor shared by every allocator
// that writes into the nexus state directory.
//
// It is a leaf package (no internal imports) so that both internal/core/service
// (image GC, DiskUsage, BuildPreflight) and internal/core/volumestore (named
// volume preallocation) can import the same value: service imports volumestore,
// so the constant cannot live in service without an import cycle.
package diskfloor

import (
	"os"
	"strconv"
)

// DefaultFreeSpaceFloorGiB is the minimum free disk space (in GiB) that must
// remain on the filesystem backing the nexus state directory after an
// allocation. Builds run GC first when free space is below it; named-volume
// preallocation refuses outright when the allocation would breach it.
// Configurable for GC via ImageGCConfig.FreeSpaceFloorGiB.
const DefaultFreeSpaceFloorGiB = 15

const DefaultFreeSpaceFloorBytes int64 = DefaultFreeSpaceFloorGiB << 30

// EnvDiskFloorBytes returns the floor derived from NEXUS_DISK_FLOOR_GIB, or
// DefaultFreeSpaceFloorBytes when the variable is absent or invalid.
func EnvDiskFloorBytes() int64 {
	if v := os.Getenv("NEXUS_DISK_FLOOR_GIB"); v != "" {
		if gib, err := strconv.ParseInt(v, 10, 64); err == nil && gib >= 0 {
			return gib << 30
		}
	}
	return DefaultFreeSpaceFloorBytes
}
