package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const configPath = "~/.nexus/telemetry_config.json"

type TelemetryConfig struct {
	Enabled       bool `json:"enabled"`
	Anonymize     bool `json:"anonymize"`
	RetentionDays int  `json:"retention_days"`
}

func LoadTelemetryConfig() TelemetryConfig {
	config := TelemetryConfig{
		Enabled:       true,
		Anonymize:     true,
		RetentionDays: 30,
	}

	expandedPath := expandPath(configPath)
	data, err := os.ReadFile(expandedPath)
	if err != nil {
		return config
	}

	json.Unmarshal(data, &config)
	return config
}

func SaveTelemetryConfig(config TelemetryConfig) error {
	expandedPath := expandPath(configPath)
	dir := filepath.Dir(expandedPath)
	os.MkdirAll(dir, 0755)

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(expandedPath, data, 0644)
}

func expandPath(path string) string {
	if path[:2] == "~/" {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}
