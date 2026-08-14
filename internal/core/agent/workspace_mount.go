package agent

// GuestMount describes a single block device → mount-point binding inside the
// guest. The type is intentionally flat: only the guest-visible pieces (device
// path, target directory, filesystem type, and read-only flag) are represented.
// Host-side concerns (disk image path, ExtraDisk metadata) live in the
// host-side WorkspaceMount type and are adapted by a later wave-1 slice.
type GuestMount struct {
	// Device is the guest block device path (e.g. "/dev/vdb", "/dev/vdc").
	// ExtraDisks[0] → /dev/vdb, [1] → /dev/vdc, … in attach order.
	Device string

	// Target is the absolute mount-point directory inside the VM
	// (e.g. "/workspace/myrepo", "/workspace/myrepo/node_modules").
	Target string

	// FSType is the filesystem type passed to mount(2). Defaults to "ext4"
	// for virtio-blk-backed ext4 images (decision D-DC-09).
	FSType string

	// ReadOnly, when true, passes MS_RDONLY to mount(2).
	ReadOnly bool
}
