package update

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type Paths struct {
	Root      string
	StateFile string
	LockFile  string
	StagedDir string
	BackupDir string
}

func ResolvePaths() (Paths, error) {
	root, err := resolveStateRoot()
	if err != nil {
		return Paths{}, err
	}
	return Paths{
		Root:      root,
		StateFile: filepath.Join(root, "state.json"),
		LockFile:  filepath.Join(root, "update.lock"),
		StagedDir: filepath.Join(root, "staged"),
		BackupDir: filepath.Join(root, "previous"),
	}, nil
}

func resolveStateRoot() (string, error) {
	if xdg := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); xdg != "" {
		return filepath.Join(xdg, "nexus", "update"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, ".local", "state", "nexus", "update"), nil
	}
	return filepath.Join(home, ".local", "state", "nexus", "update"), nil
}
