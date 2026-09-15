package portfwd

import (
	"os"
	"path/filepath"
)

// StateFileName is the basename of the file the host-side port-forward
// supervisor writes and every reader (CLI, herdr plugin, laptop client over
// ssh) consumes.
const StateFileName = "forwards.state"

// stateRelDir is the path of the port-forward state directory relative to the
// XDG state home. It is the single source of truth shared by StateDir (Go
// path, used by the supervisor writer) and RemoteStateFileShell (POSIX shell
// expression, used by the laptop client reading the host over ssh).
const stateRelDir = "nexus3/portfwd"

// StateDir returns the canonical directory of the port-forward state file:
// $XDG_STATE_HOME/nexus3/portfwd, falling back to ~/.local/state/nexus3/portfwd.
//
// The resolution is deliberately independent of HERDR_PLUGIN_STATE_DIR: the
// supervisor inherits that variable from the herdr plugin runtime, but a
// remote client reading the file over a plain ssh session does not have it,
// so a writer keyed on it would write where no reader ever looks.
func StateDir() string {
	xdg := os.Getenv("XDG_STATE_HOME")
	if xdg == "" {
		if home, _ := os.UserHomeDir(); home != "" {
			xdg = filepath.Join(home, ".local", "state")
		}
	}
	return filepath.Join(xdg, filepath.FromSlash(stateRelDir))
}

// StateFile returns the canonical path of forwards.state (StateDir joined
// with StateFileName).
func StateFile() string {
	return filepath.Join(StateDir(), StateFileName)
}

// RemoteStateFileShell returns a POSIX shell expression that expands, on the
// remote host, to the same path StateFile returns when evaluated there.
func RemoteStateFileShell() string {
	return "${XDG_STATE_HOME:-$HOME/.local/state}/" + stateRelDir + "/" + StateFileName
}
