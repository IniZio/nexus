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

// EnvGiBBytes tests

const testKey = "NEXUS_TEST_DISK_GIB"
const testDefault int64 = 5

func TestEnvGiBBytes_valid(t *testing.T) {
	t.Setenv(testKey, "7")
	got := diskfloor.EnvGiBBytes(testKey, testDefault)
	const want int64 = 7 << 30
	if got != want {
		t.Fatalf("EnvGiBBytes=%d want %d", got, want)
	}
}

func TestEnvGiBBytes_unset(t *testing.T) {
	t.Setenv(testKey, "")
	got := diskfloor.EnvGiBBytes(testKey, testDefault)
	want := testDefault << 30
	if got != want {
		t.Fatalf("EnvGiBBytes unset=%d want %d", got, want)
	}
}

func TestEnvGiBBytes_zero(t *testing.T) {
	t.Setenv(testKey, "0")
	got := diskfloor.EnvGiBBytes(testKey, testDefault)
	want := testDefault << 30
	if got != want {
		t.Fatalf("EnvGiBBytes zero=%d want default %d", got, want)
	}
}

func TestEnvGiBBytes_negative(t *testing.T) {
	t.Setenv(testKey, "-3")
	got := diskfloor.EnvGiBBytes(testKey, testDefault)
	want := testDefault << 30
	if got != want {
		t.Fatalf("EnvGiBBytes negative=%d want default %d", got, want)
	}
}

func TestEnvGiBBytes_garbage(t *testing.T) {
	t.Setenv(testKey, "notanumber")
	got := diskfloor.EnvGiBBytes(testKey, testDefault)
	want := testDefault << 30
	if got != want {
		t.Fatalf("EnvGiBBytes garbage=%d want default %d", got, want)
	}
}

func TestEnvGiBBytes_lazyRead(t *testing.T) {
	// Verify that EnvGiBBytes reads the env at call time, not at package init.
	t.Setenv(testKey, "12")
	got := diskfloor.EnvGiBBytes(testKey, testDefault)
	const want int64 = 12 << 30
	if got != want {
		t.Fatalf("EnvGiBBytes lazy=%d want %d", got, want)
	}
}
