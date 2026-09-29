//go:build !linux

package supervisor

import (
	"os"
	"time"

	"github.com/IniZio/nexus/internal/core/driver/registry"
)

func errSpawnUnsupportedPlatform() error {
	return registry.Unsupported(registry.CloudHypervisor, "detached supervisor spawn")
}

// SpawnConfig mirrors the Linux definition so callers compile cross-platform.
type SpawnConfig struct {
	Config
	Exe          string
	LogPath      string
	ReadyTimeout time.Duration

	AdoptHandoffSock    string
	CacheDiskLeaseFiles []*os.File
	Reacquire           bool
}

// SpawnDetached is unsupported off Linux.
// The *os.File return value (parent-watchdog pipe write end) is always nil
// on non-Linux platforms.
func SpawnDetached(cfg SpawnConfig) (int, *os.File, error) {
	return 0, nil, errSpawnUnsupportedPlatform()
}

// SpawnReacquireDetached is unsupported off Linux.
func SpawnReacquireDetached(cfg SpawnConfig) (int, error) {
	return 0, errSpawnUnsupportedPlatform()
}

// SpawnAdoptDetached is unsupported off Linux.
func SpawnAdoptDetached(cfg SpawnConfig) (int, error) {
	return 0, errSpawnUnsupportedPlatform()
}

// BuildSupervisorArgv is unsupported off Linux; returns nil.
func BuildSupervisorArgv(cfg SpawnConfig) []string { return nil }
