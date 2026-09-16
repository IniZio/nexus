// Package diskfloor holds the host free-space floor shared by every allocator
// that writes into the nexus3 state directory.
//
// It is a leaf package (no internal imports) so that both internal/core/service
// (image GC, DiskUsage, BuildPreflight) and internal/core/volumestore (named
// volume preallocation) can import the same value: service imports volumestore,
// so the constant cannot live in service without an import cycle.
package diskfloor

// DefaultFreeSpaceFloorGiB is the minimum free disk space (in GiB) that must
// remain on the filesystem backing the nexus3 state directory after an
// allocation. Builds run GC first when free space is below it; named-volume
// preallocation refuses outright when the allocation would breach it.
// Configurable for GC via ImageGCConfig.FreeSpaceFloorGiB.
const DefaultFreeSpaceFloorGiB = 15

const DefaultFreeSpaceFloorBytes int64 = DefaultFreeSpaceFloorGiB << 30
