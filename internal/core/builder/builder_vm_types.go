package builder

// CacheDiskSpec describes a persistent per-ecosystem cache disk that survives
// across builder VM runs. Each ecosystem (npm, pip, cargo, etc.) gets its own
// raw ext4 image on the host, mounted read-write into the guest at a canonical
// path (e.g. /root/.npm for the npm ecosystem).
//
// G3/G5 populate and consume CacheDiskSpec values; G0 defines the type so
// parallel slices share a stable signature.
type CacheDiskSpec struct {
	// EcosystemKey is a stable identifier for the package ecosystem (e.g.
	// "npm", "pip", "cargo", "maven"). Used as the disk image filename stem
	// when the image store allocates or looks up the persistent image.
	EcosystemKey string

	// ImagePath is the host filesystem path to the persistent raw ext4 image
	// for this ecosystem's cache. The image is created on first use.
	ImagePath string

	// MountPath is the canonical guest mount point for this cache disk
	// (e.g. "/root/.npm", "/root/.cache/pip"). The builder VM init mounts
	// the ext4 image here before invoking the build step.
	MountPath string

	// Subpaths optionally restricts guest access to specific subdirectories
	// of MountPath. When empty, the full mount point is exposed. Reserved
	// for future use by G5 (bind-mount narrowing for security isolation).
	Subpaths []string
}

// BuilderVMSpec describes an ephemeral builder VM to be launched for a single
// build invocation. It composes the rootfs disk with optional context and
// artifact disks and a set of ecosystem cache disks into an ordered boot-time
// virtio-blk device list.
//
// Device assignment (guest sees):
//
//	vda — RootfsDiskPath  (always present; read-only in production, rw for dev)
//	vdb — ContextDiskPath (optional; build context packed by G2)
//	vdc — ArtifactDiskPath (optional; written by the build step, harvested by G5)
//	vdd+ — CacheDisks in order (zero or more ecosystem cache disks)
//
// G3 wires BuilderVMSpec → the CH driver; G5 harvests ArtifactDiskPath after
// the VM terminates.
type BuilderVMSpec struct {
	// RootfsDiskPath is the host path to the builder rootfs raw ext4 image
	// (vda). Produced by the builder package and cached by content digest.
	RootfsDiskPath string

	// ContextDiskPath is the host path to a raw ext4 image containing the
	// repository build context (vdb). Packed by G2 from a sparse copy of the
	// workspace. Optional: empty string means no context disk is attached.
	ContextDiskPath string

	// ArtifactDiskPath is the host path to a raw ext4 image where the build
	// step writes its output artifacts (vdc). Harvested by G5 after VM exit.
	// Optional: empty string means no artifact disk is attached.
	ArtifactDiskPath string

	// CacheDisks are per-ecosystem cache disks attached after vdc (vdd+).
	// G3 selects the relevant ecosystems from the Containerfile's FROM chain.
	CacheDisks []CacheDiskSpec

	// VCPUs is the number of virtual CPUs for the builder VM. Defaults used
	// by G3 when zero.
	VCPUs uint8

	// MemoryMiB is the guest memory in mebibytes. Defaults used by G3 when zero.
	MemoryMiB uint16
}
