package agent

// CacheDiskMount describes a single ecosystem cache disk to be mounted inside
// the builder VM before the build step runs. The device is a virtio-blk block
// device path (e.g. "/dev/vdd"), and MountPath is the canonical in-guest
// directory for that ecosystem's cache (e.g. "/var/lib/buildkit").
type CacheDiskMount struct {
	// Device is the block device path inside the builder VM (e.g. "/dev/vdd").
	Device string

	// MountPath is the target directory inside the builder VM where the device
	// is mounted as ext4. Created with os.MkdirAll if absent.
	MountPath string
}

// BuilderRoleOptions configures [RunBuilderRole].
type BuilderRoleOptions struct {
	// ContextDev is the block device that holds the build context ext4 image
	// (packed by G4 ContextToDisk). Defaults to /dev/vdb when empty.
	ContextDev string

	// ArtifactDev is the block device where the built rootfs ext4 image is
	// written (passed as InGuestBuildOptions.OutputExt4). The block device is
	// written directly — the caller must pre-allocate a raw image of sufficient
	// size. Defaults to /dev/vdc when empty.
	ArtifactDev string

	// ContainerfileRel is the path of the Containerfile relative to the root
	// of the mounted context disk. Defaults to ".nexus/Containerfile".
	ContainerfileRel string

	// BaseRef overrides the OCI base image reference (FROM). When empty the
	// Containerfile's own FROM line determines the base; this field is passed
	// through to [InGuestBuildOptions.BaseRef] for callers that need a
	// programmatic override.
	BaseRef string

	// AgentPath is the host-filesystem path to the nexus3-agent binary that
	// will be baked into the produced rootfs. Defaults to /sbin/nexus3-agent.
	AgentPath string

	// BuildkitdPath overrides the buildkitd binary path inside the VM.
	// Defaults to /usr/local/bin/buildkitd.
	BuildkitdPath string

	// CacheDisks is the ordered list of ecosystem cache disks to mount before
	// the build step. Each entry is mounted read-write as ext4 at its MountPath.
	// Mounting happens after the context disk is mounted but before buildkitd
	// starts, so the buildkit cache disk (conventionally first) is in place
	// before [BuildInGuestImage] checks for a persistent /var/lib/buildkit.
	//
	// The host wires these from builder.BuilderVMSpec.CacheDisks via
	// "nexus3-agent --builder-role --cache-disk=<device>:<mountpath>" args.
	// The order must match the order of ExtraDisks[2+] in the CH driver config.
	CacheDisks []CacheDiskMount
}
