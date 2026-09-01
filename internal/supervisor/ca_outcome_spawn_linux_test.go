//go:build linux

package supervisor

import (
	"os"
	"testing"
	"time"
)

// TestSpawnReacquireDetached_ClearsStaleCAOutcome drives the REAL spawn entry
// point `nexus3 recover` uses and asserts it does not leave a previous
// recovery's CA outcome in place for this run to read.
//
// /bin/true stands in for the supervisor binary — it exits without writing a
// pidfile, so the spawn fails, which is fine: the assertion is about the state
// dir, not the spawn's success. Deleting the ClearCAOutcome call from
// SpawnReacquireDetached makes this RED.
func TestSpawnReacquireDetached_ClearsStaleCAOutcome(t *testing.T) {
	stateDir := t.TempDir()

	// A previous recovery of this same sandbox recovered its CA.
	if err := WriteCAOutcome(stateDir, CAOutcomeRecovered); err != nil {
		t.Fatalf("plant stale outcome: %v", err)
	}

	cfg := SpawnConfig{
		Config: Config{
			SandboxRef: "test-stale-ca-outcome",
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

	if _, err := SpawnReacquireDetached(cfg); err == nil {
		t.Fatal("SpawnReacquireDetached with /bin/true expected failure (no pidfile written), got nil")
	}

	if got := ReadCAOutcome(stateDir); got != CAOutcomeUnknown {
		t.Errorf("a PREVIOUS run's CA outcome (%q) survived into this spawn; "+
			"`recover` would report it as this replacement's answer", got)
	}
	if _, err := os.Stat(CAOutcomePath(stateDir)); !os.IsNotExist(err) {
		t.Errorf("CA outcome file still present after spawn: stat err = %v", err)
	}
}
