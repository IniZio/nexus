//go:build linux

package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestSpawnDetached_StaleErrFileIgnored is the regression guard for the stale
// supervisor.err defect (D-M4-T2):
//
//	Delete os.Remove(supervisorErrFile) from SpawnDetached → this test fails RED.
//
// supervisor.err written by a previous run must not be attributed to a new
// run's failure. SpawnDetached must remove the file before spawning so any
// subsequent failure message comes from the new run (or the generic
// "process exited before writing pidfile" message) rather than the old cause.
func TestSpawnDetached_StaleErrFileIgnored(t *testing.T) {
	stateDir := t.TempDir()

	// Plant a stale supervisor.err from a fictitious previous run.
	const staleReason = "stale: old failure reason that must not propagate"
	if err := os.WriteFile(filepath.Join(stateDir, supervisorErrFile), []byte(staleReason), 0o644); err != nil {
		t.Fatalf("write stale err file: %v", err)
	}

	// Spawn /bin/true as the "supervisor". It exits immediately without writing
	// supervisor.pid or a new supervisor.err, so SpawnDetached must time out and
	// return an error — but that error must NOT contain the stale reason.
	cfg := SpawnConfig{
		Config: Config{
			SandboxRef: "test-stale-err",
			StoreRoot:  stateDir,
			StateDir:   stateDir,
			CHBin:      "/usr/bin/cloud-hypervisor",
			SocketDir:  stateDir,
			KernelPath: "/nonexistent/vmlinux",
			DiskPath:   "/nonexistent/sb.raw",
		},
		Exe:          "/bin/true",
		ReadyTimeout: 500 * time.Millisecond,
	}

	_, _, err := SpawnDetached(cfg)
	if err == nil {
		t.Fatal("SpawnDetached with /bin/true expected failure (no pidfile written), got nil")
	}
	if strings.Contains(err.Error(), staleReason) {
		t.Errorf("SpawnDetached returned stale error reason — os.Remove(supervisorErrFile) may be missing from SpawnDetached (D-M4-T2 mutation guard)\nstale: %q\nerror: %v", staleReason, err)
	}
}
