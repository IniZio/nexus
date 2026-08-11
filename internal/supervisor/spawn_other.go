//go:build !linux

package supervisor

import (
	"errors"
	"time"
)

// errSpawnUnsupportedPlatform: the detached supervisor re-execs the nexus3
// binary to own a local microVM, which only runs on Linux. The nexus3 client on
// other platforms drives a Linux host via `nexus3 orca ... --remote` and never
// spawns a local supervisor.
var errSpawnUnsupportedPlatform = errors.New("supervisor: detached spawn is only supported on Linux (host-only); use --remote")

// SpawnConfig mirrors the Linux definition so callers compile cross-platform.
type SpawnConfig struct {
	Config
	Exe          string
	LogPath      string
	ReadyTimeout time.Duration
}

// SpawnDetached is unsupported off Linux.
func SpawnDetached(cfg SpawnConfig) (int, error) {
	return 0, errSpawnUnsupportedPlatform
}
