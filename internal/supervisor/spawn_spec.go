package supervisor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// SpecFile is the durable spawn.json name written next to supervisor.pid.
const SpecFile = "spawn.json"

// DefaultStateDir is the durable per-sandbox supervisor directory.
// Unlike orca's /tmp state dir this survives reboot so start/stop can
// re-own the broker the same way the supervisor re-owns the VM.
func DefaultStateDir(storeRoot string, id domain.SandboxID) string {
	return filepath.Join(storeRoot, "supervisors", id.String())
}

// SpecPath returns <stateDir>/spawn.json.
func SpecPath(stateDir string) string {
	return filepath.Join(stateDir, SpecFile)
}

// WriteSpawnSpec persists cfg so a later `sandbox start` can SpawnDetached
// without the original CLI process. ParentPipeFD is zeroed — persisted
// supervisors are never ephemeral watchdogs.
func WriteSpawnSpec(stateDir string, cfg Config) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return fmt.Errorf("supervisor: mkdir state dir %s: %w", stateDir, err)
	}
	cfg.ParentPipeFD = 0
	cfg.Ephemeral = false
	cfg.StateDir = stateDir
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("supervisor: marshal spawn spec: %w", err)
	}
	path := SpecPath(stateDir)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("supervisor: write spawn spec %s: %w", path, err)
	}
	return nil
}

// ReadSpawnSpec loads a previously written spawn.json.
func ReadSpawnSpec(stateDir string) (Config, error) {
	path := SpecPath(stateDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("supervisor: read spawn spec %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("supervisor: decode spawn spec %s: %w", path, err)
	}
	if cfg.StateDir == "" {
		cfg.StateDir = stateDir
	}
	return cfg, nil
}
