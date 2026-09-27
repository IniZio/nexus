package diskfloor_test

import (
	"testing"

	"github.com/IniZio/nexus/internal/core/diskfloor"
)

func TestEnvDiskFloorBytes_set(t *testing.T) {
	t.Setenv("NEXUS_DISK_FLOOR_GIB", "3")
	got := diskfloor.EnvDiskFloorBytes()
	const want int64 = 3 << 30
	if got != want {
		t.Fatalf("EnvDiskFloorBytes()=%d want %d", got, want)
	}
}

func TestEnvDiskFloorBytes_default(t *testing.T) {
	t.Setenv("NEXUS_DISK_FLOOR_GIB", "")
	got := diskfloor.EnvDiskFloorBytes()
	if got != diskfloor.DefaultFreeSpaceFloorBytes {
		t.Fatalf("EnvDiskFloorBytes()=%d want %d", got, diskfloor.DefaultFreeSpaceFloorBytes)
	}
}

func TestEnvDiskFloorBytes_invalid(t *testing.T) {
	t.Setenv("NEXUS_DISK_FLOOR_GIB", "notanumber")
	got := diskfloor.EnvDiskFloorBytes()
	if got != diskfloor.DefaultFreeSpaceFloorBytes {
		t.Fatalf("EnvDiskFloorBytes() with invalid env=%d want default %d", got, diskfloor.DefaultFreeSpaceFloorBytes)
	}
}
