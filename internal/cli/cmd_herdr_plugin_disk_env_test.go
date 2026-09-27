package cli

import "testing"

func TestHerdrEnvDiskBytes_usesEnvVar(t *testing.T) {
	t.Setenv("NEXUS_HERDR_DOCKER_DISK_GIB", "2")
	got := herdrEnvDiskBytes("NEXUS_HERDR_DOCKER_DISK_GIB", 20)
	const want int64 = 2 << 30
	if got != want {
		t.Fatalf("herdrEnvDiskBytes=%d want %d", got, want)
	}
}

func TestHerdrEnvDiskBytes_usesDefaultWhenUnset(t *testing.T) {
	t.Setenv("NEXUS_HERDR_DOCKER_DISK_GIB", "")
	got := herdrEnvDiskBytes("NEXUS_HERDR_DOCKER_DISK_GIB", 20)
	const want int64 = 20 << 30
	if got != want {
		t.Fatalf("herdrEnvDiskBytes=%d want %d", got, want)
	}
}
