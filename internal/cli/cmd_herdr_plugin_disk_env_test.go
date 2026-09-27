package cli

import "testing"

func TestHerdrDockerDiskSizeBytes_usesEnvVar(t *testing.T) {
	t.Setenv("NEXUS_HERDR_DOCKER_DISK_GIB", "2")
	got := herdrDockerDiskSizeBytes()
	const want int64 = 2 << 30
	if got != want {
		t.Fatalf("herdrDockerDiskSizeBytes=%d want %d", got, want)
	}
}

func TestHerdrDockerDiskSizeBytes_usesDefaultWhenUnset(t *testing.T) {
	t.Setenv("NEXUS_HERDR_DOCKER_DISK_GIB", "")
	got := herdrDockerDiskSizeBytes()
	const want int64 = 20 << 30
	if got != want {
		t.Fatalf("herdrDockerDiskSizeBytes default=%d want %d", got, want)
	}
}

func TestHerdrDockerDiskSizeBytes_zeroFallsBack(t *testing.T) {
	t.Setenv("NEXUS_HERDR_DOCKER_DISK_GIB", "0")
	got := herdrDockerDiskSizeBytes()
	const want int64 = 20 << 30
	if got != want {
		t.Fatalf("herdrDockerDiskSizeBytes zero=%d want default %d", got, want)
	}
}

func TestHerdrDockerDiskSizeBytes_negativeFallsBack(t *testing.T) {
	t.Setenv("NEXUS_HERDR_DOCKER_DISK_GIB", "-5")
	got := herdrDockerDiskSizeBytes()
	const want int64 = 20 << 30
	if got != want {
		t.Fatalf("herdrDockerDiskSizeBytes negative=%d want default %d", got, want)
	}
}

func TestHerdrDockerDiskSizeBytes_garbageFallsBack(t *testing.T) {
	t.Setenv("NEXUS_HERDR_DOCKER_DISK_GIB", "notanumber")
	got := herdrDockerDiskSizeBytes()
	const want int64 = 20 << 30
	if got != want {
		t.Fatalf("herdrDockerDiskSizeBytes garbage=%d want default %d", got, want)
	}
}

func TestHerdrGoCacheDiskSizeBytes_usesEnvVar(t *testing.T) {
	t.Setenv("NEXUS_HERDR_GOCACHE_DISK_GIB", "3")
	got := herdrGoCacheDiskSizeBytes()
	const want int64 = 3 << 30
	if got != want {
		t.Fatalf("herdrGoCacheDiskSizeBytes=%d want %d", got, want)
	}
}

func TestHerdrGoPathDiskSizeBytes_usesEnvVar(t *testing.T) {
	t.Setenv("NEXUS_HERDR_GOPATH_DISK_GIB", "4")
	got := herdrGoPathDiskSizeBytes()
	const want int64 = 4 << 30
	if got != want {
		t.Fatalf("herdrGoPathDiskSizeBytes=%d want %d", got, want)
	}
}

// TestHerdrDiskSizeBytes_lazyRead confirms the functions read the env at call
// time (not at package init), so t.Setenv is effective.
func TestHerdrDiskSizeBytes_lazyRead(t *testing.T) {
	t.Setenv("NEXUS_HERDR_DOCKER_DISK_GIB", "6")
	got := herdrDockerDiskSizeBytes()
	const want int64 = 6 << 30
	if got != want {
		t.Fatalf("herdrDockerDiskSizeBytes lazy=%d want %d", got, want)
	}
}
