//go:build !linux

package supervisor

import (
	"errors"
	"os"
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
// The *os.File return value (parent-watchdog pipe write end) is always nil
// on non-Linux platforms.
func SpawnDetached(cfg SpawnConfig) (int, *os.File, error) {
	return 0, nil, errSpawnUnsupportedPlatform
}

// SpawnReacquireDetached is unsupported off Linux.
func SpawnReacquireDetached(cfg SpawnConfig) (int, error) {
	return 0, errSpawnUnsupportedPlatform
}
