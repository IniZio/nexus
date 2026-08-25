package main

// Platform-agnostic helpers for deriving the list of resizable (index, mount)
// pairs that the telemetry producer collects per-disk stats for.
//
// Index convention: 0-based into the VM's ExtraDisks, matching the
// /dev/vd{b+index} attachment order used by handleDiskGrow and GrowRequest.
// vdb=0, vdc=1, vdd=2, etc.  This is the SAME index space as the host
// DiskAxis and Config.ResizableDiskIndices, so a DiskSample.Index==i is read
// by the host DiskAxis for ExtraDisks[i].

import (
	"strings"

	"github.com/IniZio/nexus3/internal/core/agent"
)

// resizableDisk pairs a 0-based ExtraDisks index with the guest mount path
// whose statfs() gives the disk's used/total bytes. Index follows the
// /dev/vd{b+index} convention so the host DiskAxis can correlate telemetry
// samples back to the right ExtraDisks entry without any additional mapping.
type resizableDisk struct {
	Index     int
	MountPath string
}

// diskIndexFromDevice derives the 0-based ExtraDisks index from a guest block
// device path of the form /dev/vdX (one letter): /dev/vdb→0, /dev/vdc→1, etc.
// Returns (index, true) for a valid /dev/vd* path whose letter is in ['b','z'].
// Returns (0, false) for virtiofs tags, paths shorter or longer than "/dev/vdX",
// or letters outside the valid range.
func diskIndexFromDevice(device string) (int, bool) {
	// "/dev/vdX" is exactly 8 bytes: /dev/vd = 7, plus one letter.
	if len(device) != 8 || !strings.HasPrefix(device, "/dev/vd") {
		return 0, false
	}
	letter := device[7]
	if letter < 'b' || letter > 'z' {
		return 0, false
	}
	return int(letter - 'b'), true
}

// resizableDisksFromWorkspaceMounts builds the telemetry disk list for a
// normal sandbox agent from the parsed --workspace-mount= arguments.
//
// Only mounts where IsWorkspace=true AND the device is a recognisable
// /dev/vd* block device path are included.  Virtiofs mounts (device is a tag,
// not a /dev path) are silently skipped: their index cannot be derived from the
// tag string, and virtiofs mounts are never resizable via resize2fs.  Shadow
// mounts (IsWorkspace=false) are also skipped.
//
// For the normal sandbox a single entry is expected, with the index derived
// from the workspace disk's device letter (e.g. /dev/vdf → index 4).
func resizableDisksFromWorkspaceMounts(mounts []agent.GuestMount) []resizableDisk {
	var out []resizableDisk
	for _, m := range mounts {
		if !m.IsWorkspace {
			continue
		}
		idx, ok := diskIndexFromDevice(m.Device)
		if !ok {
			// virtiofs tag or unrecognised device: index cannot be derived.
			// Skipping avoids reporting telemetry with a wrong index, which
			// would cause the host DiskAxis to grow the wrong disk.
			continue
		}
		out = append(out, resizableDisk{Index: idx, MountPath: m.Target})
	}
	return out
}

// selectResizableDisks returns the resizableDisk list appropriate for the agent
// mode. Three cases:
//
//  1. isBuilderRole=true (builder-role child): derive from --cache-disk= args.
//  2. isBuilderRole=false, wsDisks non-empty (normal sandbox PID-1): use wsDisks.
//  3. isBuilderRole=false, wsDisks empty, cacheDisks non-empty (PID-1 in builder VM):
//     fall back to cache disks. This happens when the builder VM's kernel cmdline
//     carries --cache-disk= args for PID-1 telemetry but no --workspace-mount= args
//     (builder VMs have no workspace disk). See builder_supervisor_driver.go:Start.
//
// wsDisks is passed in rather than re-derived here so normal-mode logic
// (including its logging) is not duplicated.
//
// This is the SOLE place that chooses between the disk lists; extracted for
// unit testability.
func selectResizableDisks(isBuilderRole bool, cacheDisks []agent.CacheDiskMount, wsDisks []resizableDisk) []resizableDisk {
	if isBuilderRole {
		return resizableDisksFromCacheDisks(cacheDisks)
	}
	// Case 3: PID-1 in a builder VM — no workspace disks, but cache disks were
	// passed on the kernel cmdline so PID-1 can report them via telemetry.
	if len(wsDisks) == 0 && len(cacheDisks) > 0 {
		return resizableDisksFromCacheDisks(cacheDisks)
	}
	return wsDisks
}

// resizableDisksFromCacheDisks builds the telemetry disk list for the builder
// VM agent from the parsed --cache-disk= arguments. Device letters are mapped
// to ExtraDisks indices via diskIndexFromDevice: in the builder VM layout
// (vdb=context, vdc=artifact, vdd+=cache disks), /dev/vdd → index 2.
// Entries with unrecognised device paths are silently dropped.
func resizableDisksFromCacheDisks(cacheDisks []agent.CacheDiskMount) []resizableDisk {
	out := make([]resizableDisk, 0, len(cacheDisks))
	for _, cd := range cacheDisks {
		idx, ok := diskIndexFromDevice(cd.Device)
		if !ok {
			continue
		}
		out = append(out, resizableDisk{Index: idx, MountPath: cd.MountPath})
	}
	return out
}
