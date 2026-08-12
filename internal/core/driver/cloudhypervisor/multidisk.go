package cloudhypervisor

// ExtraDisk is a raw ext4 disk image to attach at VM boot as an additional
// virtio-blk device after the rootfs (vda). Devices are assigned in the order
// they appear in Config.ExtraDisks:
//
//	ExtraDisks[0] → /dev/vdb
//	ExtraDisks[1] → /dev/vdc
//	ExtraDisks[2] → /dev/vdd
//	…
//
// Each image must be a raw ext4 image (not qcow2). The driver passes
// image_type=Raw to Cloud Hypervisor so sector-0 writes are not suppressed.
//
// Extra disks are attached at vm.create time (boot-time only). Runtime hotplug
// is not supported; attach all required disks before calling driver.Start.
type ExtraDisk struct {
	// Path is the host filesystem path to the raw ext4 disk image.
	Path string
}
