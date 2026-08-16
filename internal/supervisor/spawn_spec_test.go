package supervisor

import (
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/resize"
)

func TestWriteReadSpawnSpec_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := Config{
		SandboxRef:         "sb-test",
		StoreRoot:          "/store",
		StateDir:           "/tmp/old",
		CHBin:              "/usr/bin/cloud-hypervisor",
		SocketDir:          "/run/nexus3",
		KernelPath:         "/boot/vmlinux",
		DiskPath:           "/data/sb.raw",
		ExtraDisks:         []string{"/data/ws.ext4"},
		MemoryMiB:          2048,
		BootVCPUs:          2,
		HasWorkspaceDisk:   true,
		WorkspaceDiskIndex: 0,
		WorkspaceGuestPath: "/workspace/proj", // GIT-SEED (D-PD-29)
		GovBounds: resize.Bounds{
			MemMinBytes:  512 << 20,
			MemMaxBytes:  4096 << 20,
			VCPUMin:      1,
			VCPUMax:      4,
			DiskMaxBytes: 100 << 30,
		},
		Cmdline:      "root=/dev/vda",
		Ephemeral:    true,
		ParentPipeFD: 3,
	}
	if err := WriteSpawnSpec(dir, in); err != nil {
		t.Fatalf("WriteSpawnSpec: %v", err)
	}
	got, err := ReadSpawnSpec(dir)
	if err != nil {
		t.Fatalf("ReadSpawnSpec: %v", err)
	}
	if got.Ephemeral || got.ParentPipeFD != 0 {
		t.Errorf("persisted watchdog leaked: ephemeral=%v fd=%d", got.Ephemeral, got.ParentPipeFD)
	}
	if got.StateDir != dir {
		t.Errorf("StateDir = %q, want %q", got.StateDir, dir)
	}
	if got.DiskPath != in.DiskPath || got.MemoryMiB != 2048 || !got.HasWorkspaceDisk {
		t.Errorf("got %+v", got)
	}
	if got.WorkspaceGuestPath != in.WorkspaceGuestPath {
		t.Errorf("WorkspaceGuestPath = %q, want %q", got.WorkspaceGuestPath, in.WorkspaceGuestPath)
	}
	if got.GovBounds.MemMaxBytes != in.GovBounds.MemMaxBytes {
		t.Errorf("GovBounds = %+v", got.GovBounds)
	}
}

func TestDefaultStateDir_KeyedBySandbox(t *testing.T) {
	var id domain.SandboxID
	id[0] = 0x26
	got := DefaultStateDir("/store", id)
	if filepath.Dir(got) != "/store/supervisors" {
		t.Errorf("dir = %q", got)
	}
	if filepath.Base(got) != id.String() {
		t.Errorf("base = %q, want %q", filepath.Base(got), id.String())
	}
}
