//go:build linux

package main

import (
	"testing"

	"github.com/IniZio/nexus3/internal/core/resize"
	"github.com/IniZio/nexus3/internal/supervisor"
)

// TestParseSupervisorFlags_RoundTrip pins the flag→struct glue: every
// forwarded SpawnConfig field must survive argv parsing into Config.
//
// Regression guard for 2026-08-16: an edit adding WorkspaceGuestPath to the
// Config literal silently dropped ExtraDisks and WorkspaceDiskIndex. The
// supervisor then attached ONLY the rootfs; every workspace sandbox's guest
// panicked at boot ("mount /dev/vdf … no such file") because its extra disks
// never reached vm.create. A round-trip test catches a dropped field at unit
// speed instead of after a 4-minute live boot.
func TestParseSupervisorFlags_RoundTrip(t *testing.T) {
	in := supervisor.Config{
		SandboxRef:         "sb-roundtrip",
		StoreRoot:          "/store",
		StateDir:           "/state",
		CHBin:              "/usr/bin/cloud-hypervisor",
		SocketDir:          "/run/nexus3",
		KernelPath:         "/boot/vmlinux",
		DiskPath:           "/data/sb.raw",
		CredsFile:          "/creds.json",
		MemoryMiB:          2048,
		BootVCPUs:          2,
		HasWorkspaceDisk:   true,
		WorkspaceDiskIndex: 4,
		WorkspaceGuestPath: "/workspace/proj",
		ExtraDisks:         []string{"/d1.ext4", "/d2.ext4", "/d3.ext4", "/d4.ext4", "/d5.ext4"},
		GovBounds: resize.Bounds{
			MemMinBytes:  512 << 20,
			MemMaxBytes:  4096 << 20,
			VCPUMin:      1,
			VCPUMax:      4,
			DiskMaxBytes: 100 << 30,
		},
		Cmdline: "root=/dev/vda rw init=/sbin/nexus3-agent console=ttyS0 -- --workspace-mount=/dev/vdf:/workspace/proj:ext4:false:true",
	}

	// BuildSupervisorArgv includes the leading HiddenSubcommand token; the
	// CLI dispatch strips it before runSupervisorMain (os.Args[2:]).
	argv := supervisor.BuildSupervisorArgv(supervisor.SpawnConfig{Config: in})[1:]
	got, err := parseSupervisorFlags(argv)
	if err != nil {
		t.Fatalf("parseSupervisorFlags: %v", err)
	}

	if got.SandboxRef != in.SandboxRef || got.DiskPath != in.DiskPath || got.Cmdline != in.Cmdline {
		t.Errorf("identity fields lost: %+v", got)
	}
	if len(got.ExtraDisks) != len(in.ExtraDisks) {
		t.Fatalf("ExtraDisks round-trip: got %d disks (%v), want %d — dropped fields attach only the rootfs and panic workspace guests",
			len(got.ExtraDisks), got.ExtraDisks, len(in.ExtraDisks))
	}
	for i, p := range in.ExtraDisks {
		if got.ExtraDisks[i] != p {
			t.Errorf("ExtraDisks[%d] = %q, want %q", i, got.ExtraDisks[i], p)
		}
	}
	if got.WorkspaceDiskIndex != in.WorkspaceDiskIndex || !got.HasWorkspaceDisk {
		t.Errorf("workspace disk index lost: got idx=%d has=%v, want idx=%d has=true",
			got.WorkspaceDiskIndex, got.HasWorkspaceDisk, in.WorkspaceDiskIndex)
	}
	if got.WorkspaceGuestPath != in.WorkspaceGuestPath {
		t.Errorf("WorkspaceGuestPath = %q, want %q", got.WorkspaceGuestPath, in.WorkspaceGuestPath)
	}
	if got.MemoryMiB != in.MemoryMiB || got.BootVCPUs != in.BootVCPUs || got.CredsFile != in.CredsFile {
		t.Errorf("boot config lost: %+v", got)
	}
	if got.GovBounds != in.GovBounds {
		t.Errorf("GovBounds = %+v, want %+v", got.GovBounds, in.GovBounds)
	}
}

// TestParseSupervisorFlags_MissingRequired asserts the required-flag guard
// fires with the first missing flag name.
func TestParseSupervisorFlags_MissingRequired(t *testing.T) {
	_, err := parseSupervisorFlags([]string{"--sandbox-ref", "sb-x"})
	if err == nil || err.Error() != "supervisor: --store-root is required" {
		t.Fatalf("err = %v, want --store-root required", err)
	}
}
