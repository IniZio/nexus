package supervisor

import (
	"os"
	"slices"
	"testing"
)

// A spawn.json written before net-mode plumbing was removed still carries
// "NetMode"; it must decode and respawn without a --net-mode argument.
func TestLegacySpawnSpecNetModeIgnored(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"SandboxRef":"sb-x","NetMode":"tap"}`
	if err := os.WriteFile(SpecPath(dir), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := ReadSpawnSpec(dir)
	if err != nil {
		t.Fatalf("ReadSpawnSpec: %v", err)
	}
	if cfg.SandboxRef != "sb-x" {
		t.Fatalf("SandboxRef = %q, want sb-x", cfg.SandboxRef)
	}
	if args := BuildSupervisorArgv(SpawnConfig{Config: cfg}); slices.Contains(args, "--net-mode") {
		t.Fatalf("--net-mode present in argv %v", args)
	}
}
