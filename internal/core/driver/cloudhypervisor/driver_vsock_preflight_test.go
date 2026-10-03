package cloudhypervisor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func TestRemoveStaleVsock(t *testing.T) {
	dir := t.TempDir()
	d := &CHDriver{cfg: Config{SocketDir: dir}}
	id := domain.NewSandboxID()
	other := domain.NewSandboxID()

	stale := []string{d.vsockPath(id), d.vsockGuestPortPath(id, 1024), d.vsockGuestPortPath(id, 5)}
	keep := []string{d.vsockPath(other), d.vsockGuestPortPath(other, 1024), d.socketPath(id)}
	for _, p := range append(append([]string{}, stale...), keep...) {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d.removeStaleVsock(id)
	for _, p := range stale {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s not removed", filepath.Base(p))
		}
	}
	for _, p := range keep {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s wrongly removed", filepath.Base(p))
		}
	}
}
