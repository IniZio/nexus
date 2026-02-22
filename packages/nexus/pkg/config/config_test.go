package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultConfigDir(t *testing.T) {
	dir := DefaultConfigDir()
	assert.NotEmpty(t, dir)
	assert.Contains(t, dir, ".nexus")
}

func TestDefaultConfigPath(t *testing.T) {
	path := DefaultConfigPath()
	assert.NotEmpty(t, path)
	assert.Contains(t, path, "config.yaml")
}

func TestLoadConfig_NotFound(t *testing.T) {
	// Test with non-existent file returns default config
	cfg, err := LoadConfig("/nonexistent/config.yaml")
	require.NoError(t, err)
	require.NotNil(t, cfg)
	
	assert.Equal(t, "mutagen", cfg.Sync.Provider)
	assert.Equal(t, "two-way-safe", cfg.Sync.Mode)
}

func TestLoadConfig_InvalidPath(t *testing.T) {
	_, err := LoadConfig("/invalid/../../etc/config.yaml")
	assert.Error(t, err)
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	
	err := os.WriteFile(configPath, []byte("invalid: yaml: content: ["), 0644)
	require.NoError(t, err)
	
	_, err = LoadConfig(configPath)
	assert.Error(t, err)
}

func TestLoadConfig_Valid(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	
	yamlContent := `
sync:
  provider: mutagen
  mode: one-way
  exclude:
    - node_modules
    - .git
    - "*.log"
`
	err := os.WriteFile(configPath, []byte(yamlContent), 0644)
	require.NoError(t, err)
	
	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	
	assert.Equal(t, "mutagen", cfg.Sync.Provider)
	assert.Equal(t, "one-way", cfg.Sync.Mode)
	assert.Len(t, cfg.Sync.Exclude, 3)
	assert.Contains(t, cfg.Sync.Exclude, "node_modules")
}

func TestLoadConfig_EmptyMode(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	
	yamlContent := `
sync:
  provider: mutagen
`
	err := os.WriteFile(configPath, []byte(yamlContent), 0644)
	require.NoError(t, err)
	
	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	
	assert.Equal(t, "two-way-safe", cfg.Sync.Mode)
}

func TestLoadConfig_EmptyExclude(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	
	yamlContent := `
sync:
  provider: mutagen
  mode: two-way-safe
`
	err := os.WriteFile(configPath, []byte(yamlContent), 0644)
	require.NoError(t, err)
	
	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	
	assert.NotNil(t, cfg.Sync.Exclude)
}

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	require.NotNil(t, cfg)
	
	assert.Equal(t, "mutagen", cfg.Sync.Provider)
	assert.Equal(t, "two-way-safe", cfg.Sync.Mode)
	assert.NotEmpty(t, cfg.Sync.Exclude)
}

func TestSyncConfig_ToSyncConfig(t *testing.T) {
	syncCfg := SyncConfig{
		Provider: "mutagen",
		Mode:     "one-way",
		Exclude:  []string{"node_modules", ".git"},
	}
	
	result := syncCfg.ToSyncConfig()
	require.NotNil(t, result)
	
	assert.Equal(t, "one-way", result.Mode)
	assert.Equal(t, []string{"node_modules", ".git"}, result.Exclude)
}
