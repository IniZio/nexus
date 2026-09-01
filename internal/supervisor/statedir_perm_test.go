package supervisor

// Permission tests for the per-sandbox supervisor state dir (D-HSH-18).
// They drive WriteSpawnSpec — the real function every persistent supervisor
// spawn goes through — not a local mkdir.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

func TestWriteSpawnSpec_StateDirIs0700AndSpecIs0600(t *testing.T) {
	stateDir := DefaultStateDir(t.TempDir(), domain.NewSandboxID())

	if err := WriteSpawnSpec(stateDir, Config{}); err != nil {
		t.Fatalf("WriteSpawnSpec: %v", err)
	}

	di, err := os.Stat(stateDir)
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Fatalf("state dir mode = %04o, want 0700", got)
	}

	fi, err := os.Stat(SpecPath(stateDir))
	if err != nil {
		t.Fatalf("stat spawn.json: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("spawn.json mode = %04o, want 0600", got)
	}
}

// TestWriteSpawnSpec_TightensPreExisting0755StateDir is the migration case:
// the 641 dirs already on disk are 0755, and MkdirAll on an existing dir is a
// no-op. The tightening must happen when a supervisor next takes ownership,
// which is this call.
func TestWriteSpawnSpec_TightensPreExisting0755StateDir(t *testing.T) {
	stateDir := DefaultStateDir(t.TempDir(), domain.NewSandboxID())
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(stateDir, 0o755); err != nil { // defeat umask
		t.Fatalf("chmod: %v", err)
	}
	// A file left behind by the earlier, looser run.
	stale := filepath.Join(stateDir, "supervisor.log")
	if err := os.WriteFile(stale, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("write stale log: %v", err)
	}

	if err := WriteSpawnSpec(stateDir, Config{}); err != nil {
		t.Fatalf("WriteSpawnSpec: %v", err)
	}

	di, err := os.Stat(stateDir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o700 {
		t.Fatalf("pre-existing 0755 state dir left at %04o, want 0700", got)
	}
}
