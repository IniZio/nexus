// Package diskfloor holds the host free-space floor shared by every allocator
// that writes into the nexus state directory.
//
// It is a leaf package (no internal imports) so that both internal/core/service
// (image GC, DiskUsage, BuildPreflight) and internal/core/volumestore (named
// volume preallocation) can import the same value: service imports volumestore,
// so the constant cannot live in service without an import cycle.
package diskfloor

import (
	"log/slog"
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

// EnvGiBBytes reads an integer GiB value from the named environment variable.
// It returns defaultGiB<<30 when the variable is unset, empty, non-integer,
// zero, or negative, logging a warning in the latter two cases.
// Both herdr volume size knobs (NEXUS_HERDR_*_DISK_GIB) and the disk floor
// knob (NEXUS_DISK_FLOOR_GIB) delegate to this function so validation is
// consistent: values must be positive integers.
func EnvGiBBytes(key string, defaultGiB int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return defaultGiB << 30
	}
	gib, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		slog.Warn("diskfloor: ignoring non-integer env var, using default", "key", key, "value", v, "defaultGiB", defaultGiB)
		return defaultGiB << 30
	}
	if gib <= 0 {
		slog.Warn("diskfloor: ignoring non-positive env var, using default", "key", key, "value", v, "defaultGiB", defaultGiB)
		return defaultGiB << 30
	}
	return gib << 30
}

// EnvDiskFloorBytes returns the floor derived from NEXUS_DISK_FLOOR_GIB, or
// DefaultFreeSpaceFloorBytes when the variable is absent or invalid.
func EnvDiskFloorBytes() int64 {
	return EnvGiBBytes("NEXUS_DISK_FLOOR_GIB", DefaultFreeSpaceFloorGiB)
}
