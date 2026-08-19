package agent

// GuestMount describes a single block device → mount-point binding inside the
// guest. The type is intentionally flat: only the guest-visible pieces (device
// path, target directory, filesystem type, and read-only flag) are represented.
// Host-side concerns (disk image path, ExtraDisk metadata) live in the
// host-side WorkspaceMount type and are adapted by a later wave-1 slice.
type GuestMount struct {
	// Device is the mount source passed to mount(2). Its meaning depends on FSType:
	//   - virtio-blk (ext4): guest block device path derived from ExtraDisks attach
	//     order (e.g. "/dev/vdb", "/dev/vdc"; ExtraDisks[0] → /dev/vdb, [1] → /dev/vdc).
	//   - virtiofs (D-PD-53): the virtiofs tag string configured on the host, passed
	//     as the device argument to mount(2) (e.g. "workspace-tag"). Not a /dev path.
	Device string

	// Target is the absolute mount-point directory inside the VM
	// (e.g. "/workspace/myrepo", "/workspace/myrepo/node_modules").
	Target string

	// FSType is the filesystem type passed to mount(2). Two values are supported:
	//   - "ext4": virtio-blk-backed ext4 images (decision D-DC-09). Default for
	//     workspace and shadow disks.
	//   - "virtiofs": VirtioFS shared-directory mounts (decision D-PD-53). Used for
	//     named volume mounts; Device holds the virtiofs tag, not a /dev path.
	FSType string

	// ReadOnly, when true, passes MS_RDONLY to mount(2).
	ReadOnly bool

	// IsWorkspace marks exactly one mount as the primary workspace disk — the
	// disk whose capacity is reported by disk-telemetry (statfs) and managed by
	// the disk governor. Shadow mounts (node_modules, .next, …) leave this false.
	// The agent selects the telemetry target by this marker, never by position or
	// ReadOnly inference.
	IsWorkspace bool
}
