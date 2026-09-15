package portfwd

import (
	"os"
	"path/filepath"
)

const StateFileName = "forwards.state" // basename consumed by supervisor, CLI, herdr plugin, laptop ssh client
const stateRelDir = "nexus3/portfwd"   // relative to XDG_STATE_HOME; single source of truth for StateDir and RemoteStateFileShell

// StateDir returns $XDG_STATE_HOME/nexus3/portfwd; not HERDR_PLUGIN_STATE_DIR (remote ssh clients lack it).
func StateDir() string {
	xdg := os.Getenv("XDG_STATE_HOME")
	if xdg == "" {
		if home, _ := os.UserHomeDir(); home != "" {
			xdg = filepath.Join(home, ".local", "state")
		}
	}
	return filepath.Join(xdg, filepath.FromSlash(stateRelDir))
}

func StateFile() string {
	return filepath.Join(StateDir(), StateFileName)
}

func RemoteStateFileShell() string {
	return "${XDG_STATE_HOME:-$HOME/.local/state}/" + stateRelDir + "/" + StateFileName
}
