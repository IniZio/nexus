package main

// Platform-agnostic helpers for deriving the list of resizable (index, mount)
// pairs that the telemetry producer collects per-disk stats for.
//
// Index convention: resize.RootDiskIndex (-1) for the root disk (/dev/vda),
// or 0-based into ExtraDisks for extra disks (/dev/vdb=0, /dev/vdc=1, etc.).
// Matches the host DiskAxis and Config.ResizableDiskIndices index space.

import (
	"fmt"
	"strings"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/resize"
)

// resizableDisk pairs a disk index with its guest mount path for statfs().
// Index is resize.RootDiskIndex (-1) for root, or 0-based into ExtraDisks.
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
	if len(device) != 8 || !strings.HasPrefix(device, "/dev/vd") {
		return 0, false
	}
	letter := device[7]
	if letter == 'a' {
		return resize.RootDiskIndex, true
	}
	if letter < 'b' || letter > 'z' {
		return 0, false
	}
	return int(letter - 'b'), true
}

// resizableDisksFromWorkspaceMounts builds the telemetry disk list for a
// normal sandbox agent from the parsed --workspace-mount= arguments.
//
// Included mounts: those where (IsWorkspace || Resizable) is true AND the
// device is a recognisable /dev/vd* block device path.  Virtiofs mounts
// (device is a tag, not a /dev path) are silently skipped: their index cannot
// be derived from the tag string, and virtiofs mounts are never resizable via
// resize2fs.  Plain shadow mounts (IsWorkspace=false AND Resizable=false) are
// also skipped — they are never governor-managed.
//
// Two roles are represented in the result:
//   - IsWorkspace=true: the primary workspace disk (one per sandbox).
//   - Resizable=true:   named-volume kind=disk mounts (e.g. /var/lib/docker)
//     whose governor axis was registered by the host via ResizableDiskIndices.
//
// Both roles report a DiskSample at their respective ExtraDisks index so the
// host DiskAxis for that index finds a matching entry in Sample.DiskStats.
func resizableDisksFromWorkspaceMounts(mounts []agent.GuestMount) []resizableDisk {
	var out []resizableDisk
	for _, m := range mounts {
		if !m.IsWorkspace && !m.Resizable {
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

// sandboxResizableDisks derives the normal-sandbox telemetry disk list from the
// parsed --workspace-mount= args and returns it together with a one-line
// console diagnostic.
//
// The list is derived whether or not a workspace mount exists: herdr worktree
// sandboxes mount /workspace over virtiofs (IsWorkspace=false) yet still carry
// Resizable=true named-volume disks (/var/lib/docker, /root/.cache, ...) whose
// governor axes the host registered via ResizableDiskIndices. Gating the list on
// the workspace mount silently dropped those volumes and disabled disk
// governance for every such sandbox.
//
// Returns an error only when more than one mount claims IsWorkspace=true.
func sandboxResizableDisks(mounts []agent.GuestMount) ([]resizableDisk, string, error) {
	wsMount, hasWS, err := selectWorkspaceMount(mounts)
	if err != nil {
		return nil, "", err
	}
	disks := resizableDisksFromWorkspaceMounts(mounts)
	var b strings.Builder
	switch {
	case len(disks) == 0 && hasWS:
		fmt.Fprintf(&b, "nexus-agent: auto-resize: workspace mount %q: cannot derive disk index from device %q; disk telemetry disabled\n", wsMount.Target, wsMount.Device)
	case len(disks) == 0:
		fmt.Fprintf(&b, "nexus-agent: auto-resize: no workspace or resizable block mount in %d mount(s); extra-disk telemetry skipped\n", len(mounts))
	default:
		fmt.Fprintf(&b, "nexus-agent: auto-resize: disk telemetry: %d disk(s) (workspace mount: %t) at index(es):", len(disks), hasWS)
		for _, d := range disks {
			fmt.Fprintf(&b, " [%d]%s", d.Index, d.MountPath)
		}
		b.WriteString("\n")
	}
	return disks, b.String(), nil
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
	if len(wsDisks) == 0 && len(cacheDisks) > 0 {
		return resizableDisksFromCacheDisks(cacheDisks)
	}
	result := make([]resizableDisk, 0, len(wsDisks)+1)
	result = append(result, resizableDisk{Index: resize.RootDiskIndex, MountPath: "/"})
	result = append(result, wsDisks...)
	return result
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
