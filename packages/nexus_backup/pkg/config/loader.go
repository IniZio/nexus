package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func LoadWorkspaceConfig(root string) (WorkspaceConfig, []string, error) {
	workspacePath := filepath.Join(root, ".nexus", "workspace.json")
	legacyPath := filepath.Join(root, ".nexus", "lifecycle.json")

	workspaceData, wsErr := os.ReadFile(workspacePath)
	legacyData, legacyErr := os.ReadFile(legacyPath)

	warnings := []string{}

	if wsErr == nil {
		var cfg WorkspaceConfig
		dec := json.NewDecoder(bytes.NewReader(workspaceData))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			return WorkspaceConfig{}, warnings, fmt.Errorf("failed to parse %s: %w", workspacePath, err)
		}
		if err := cfg.ValidateBasic(); err != nil {
			return WorkspaceConfig{}, warnings, fmt.Errorf("invalid %s: %w", workspacePath, err)
		}
		if cfg.Version == 0 {
			cfg.Version = 1
		}
		return cfg, warnings, nil
	}
	_ = legacyData
	_ = legacyErr

	return WorkspaceConfig{Version: 1}, warnings, nil
}
