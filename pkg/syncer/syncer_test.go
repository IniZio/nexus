package syncer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	// Create a temporary sync config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "sync.yaml")

	configContent := `
strategy: volume
paths:
  - source: ~/.config/test.json
    target: /home/dev/.config/test.json
`
	err := os.WriteFile(configPath, []byte(configContent), 0644)
	require.NoError(t, err)

	cfg, err := LoadConfig(configPath)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, StrategyVolume, cfg.Strategy)
	assert.Len(t, cfg.Paths, 1)
	assert.Equal(t, "~/.config/test.json", cfg.Paths[0].Source)
	assert.Equal(t, "/home/dev/.config/test.json", cfg.Paths[0].Target)
}

func TestExpandPaths(t *testing.T) {
	cfg := &Config{
		Strategy: StrategyVolume,
		Paths: []SyncPath{
			{Source: "~/.config/test", Target: "/home/dev/.config/test"},
			{Source: "/absolute/path", Target: "/home/dev/path"},
		},
	}

	err := cfg.ExpandPaths()
	require.NoError(t, err)

	home, err := os.UserHomeDir()
	require.NoError(t, err)

	assert.Contains(t, cfg.Paths[0].Source, home)
	assert.Equal(t, "/absolute/path", cfg.Paths[1].Source)
}

func TestGetVolumeMounts(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a test file
	testFile := filepath.Join(tmpDir, "test.json")
	err := os.WriteFile(testFile, []byte("{}"), 0644)
	require.NoError(t, err)

	cfg := &Config{
		Strategy: StrategyVolume,
		Paths: []SyncPath{
			{Source: testFile, Target: "/home/dev/test.json"},
			{Source: filepath.Join(tmpDir, "nonexistent"), Target: "/home/dev/nonexistent"},
		},
	}

	mounts := cfg.GetVolumeMounts()
	assert.Len(t, mounts, 1)
	assert.Equal(t, testFile, mounts[0].Source)
	assert.Equal(t, "/home/dev/test.json", mounts[0].Target)
	assert.True(t, mounts[0].ReadOnly)
}

func TestValidate(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		cfg := &Config{
			Paths: []SyncPath{
				{Source: "/local/path", Target: "/remote/path"},
			},
		}
		assert.NoError(t, cfg.Validate())
	})

	t.Run("empty source", func(t *testing.T) {
		cfg := &Config{
			Paths: []SyncPath{
				{Source: "", Target: "/remote/path"},
			},
		}
		assert.Error(t, cfg.Validate())
	})

	t.Run("empty target", func(t *testing.T) {
		cfg := &Config{
			Paths: []SyncPath{
				{Source: "/local/path", Target: ""},
			},
		}
		assert.Error(t, cfg.Validate())
	})
}

func TestValidationError(t *testing.T) {
	err := ErrEmptySource
	assert.Contains(t, err.Error(), "source")
	assert.Contains(t, err.Error(), "empty")

	err = ErrEmptyTarget
	assert.Contains(t, err.Error(), "target")
	assert.Contains(t, err.Error(), "empty")
}
