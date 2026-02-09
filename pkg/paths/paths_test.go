package paths

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetDataDir(t *testing.T) {
	projectRoot := "/tmp/nexus-test"

	t.Run("default_path", func(t *testing.T) {
		os.Unsetenv("NEXUS_DATA_DIR")
		dir := GetDataDir(projectRoot)
		assert.Equal(t, filepath.Join(projectRoot, ".nexus-runtime", "data"), dir)
	})

	t.Run("env_override", func(t *testing.T) {
		t.Setenv("NEXUS_DATA_DIR", "/var/lib/nexus/data")
		dir := GetDataDir(projectRoot)
		assert.Equal(t, "/var/lib/nexus/data", dir)
	})
}

func TestGetStateDir(t *testing.T) {
	projectRoot := "/tmp/nexus-test"

	t.Run("default_path", func(t *testing.T) {
		os.Unsetenv("NEXUS_STATE_DIR")
		dir := GetStateDir(projectRoot)
		assert.Equal(t, filepath.Join(projectRoot, ".nexus-runtime", "state"), dir)
	})

	t.Run("env_override", func(t *testing.T) {
		t.Setenv("NEXUS_STATE_DIR", "/var/lib/nexus/state")
		dir := GetStateDir(projectRoot)
		assert.Equal(t, "/var/lib/nexus/state", dir)
	})
}

func TestGetLogsDir(t *testing.T) {
	projectRoot := "/tmp/nexus-test"

	t.Run("default_path", func(t *testing.T) {
		os.Unsetenv("NEXUS_LOGS_DIR")
		dir := GetLogsDir(projectRoot)
		assert.Equal(t, filepath.Join(projectRoot, ".nexus-runtime", "logs"), dir)
	})

	t.Run("env_override", func(t *testing.T) {
		t.Setenv("NEXUS_LOGS_DIR", "/var/log/nexus")
		dir := GetLogsDir(projectRoot)
		assert.Equal(t, "/var/log/nexus", dir)
	})
}

func TestGetCacheDir(t *testing.T) {
	projectRoot := "/tmp/nexus-test"

	t.Run("env_override", func(t *testing.T) {
		t.Setenv("NEXUS_CACHE_DIR", "/var/cache/nexus")
		dir := GetCacheDir(projectRoot)
		assert.Equal(t, "/var/cache/nexus", dir)
	})

	t.Run("fallback_to_home", func(t *testing.T) {
		os.Unsetenv("NEXUS_CACHE_DIR")
		dir := GetCacheDir(projectRoot)
		home, _ := os.UserHomeDir()
		assert.Equal(t, filepath.Join(home, ".cache", "nexus"), dir)
	})
}

func TestGetConfigDir(t *testing.T) {
	projectRoot := "/tmp/nexus-test"
	dir := GetConfigDir(projectRoot)
	assert.Equal(t, filepath.Join(projectRoot, ".nexus"), dir)
}

func TestGetDatabasePath(t *testing.T) {
	projectRoot := "/tmp/nexus-test"
	os.Unsetenv("NEXUS_DATA_DIR")
	path := GetDatabasePath(projectRoot)
	expected := filepath.Join(projectRoot, ".nexus-runtime", "data", "nexus.db")
	assert.Equal(t, expected, path)
}

func TestGetPIDFilePath(t *testing.T) {
	projectRoot := "/tmp/nexus-test"
	os.Unsetenv("NEXUS_STATE_DIR")
	path := GetPIDFilePath(projectRoot)
	expected := filepath.Join(projectRoot, ".nexus-runtime", "state", "server.pid")
	assert.Equal(t, expected, path)
}

func TestGetWorktreesDir(t *testing.T) {
	projectRoot := "/tmp/nexus-test"
	os.Unsetenv("NEXUS_STATE_DIR")
	dir := GetWorktreesDir(projectRoot)
	expected := filepath.Join(projectRoot, ".nexus-runtime", "state", "worktrees")
	assert.Equal(t, expected, dir)
}

func TestGetServerLogPath(t *testing.T) {
	projectRoot := "/tmp/nexus-test"
	os.Unsetenv("NEXUS_LOGS_DIR")
	path := GetServerLogPath(projectRoot)
	expected := filepath.Join(projectRoot, ".nexus-runtime", "logs", "server.log")
	assert.Equal(t, expected, path)
}

func TestEnsureDir(t *testing.T) {
	tmpDir := t.TempDir()
	testDir := filepath.Join(tmpDir, "test", "nested", "dir")

	err := EnsureDir(testDir)
	require.NoError(t, err)

	_, err = os.Stat(testDir)
	require.NoError(t, err)
}

func TestEnsureDirs(t *testing.T) {
	tmpDir := t.TempDir()
	paths := []string{
		filepath.Join(tmpDir, "dir1"),
		filepath.Join(tmpDir, "dir2"),
		filepath.Join(tmpDir, "dir3"),
	}

	err := EnsureDirs(paths...)
	require.NoError(t, err)

	for _, path := range paths {
		_, err := os.Stat(path)
		assert.NoError(t, err)
	}
}

func TestEnsureAllRuntimeDirs(t *testing.T) {
	tmpDir := t.TempDir()

	err := EnsureAllRuntimeDirs(tmpDir)
	require.NoError(t, err)

	expectedDirs := []string{
		GetDataDir(tmpDir),
		GetStateDir(tmpDir),
		GetWorktreesDir(tmpDir),
		GetLogsDir(tmpDir),
		GetLogsArchiveDir(tmpDir),
		GetCacheDir(tmpDir),
	}

	for _, dir := range expectedDirs {
		_, err := os.Stat(dir)
		assert.NoError(t, err, "expected directory to exist: %s", dir)
	}
}

func TestEnsureConfigDirs(t *testing.T) {
	tmpDir := t.TempDir()

	err := EnsureConfigDirs(tmpDir)
	require.NoError(t, err)

	expectedDirs := []string{
		GetConfigDir(tmpDir),
		filepath.Join(GetConfigDir(tmpDir), "agents"),
		filepath.Join(GetConfigDir(tmpDir), "templates"),
		filepath.Join(GetConfigDir(tmpDir), "hooks"),
		filepath.Join(GetConfigDir(tmpDir), "plugins"),
		filepath.Join(GetConfigDir(tmpDir), "remotes"),
	}

	for _, dir := range expectedDirs {
		_, err := os.Stat(dir)
		assert.NoError(t, err, "expected directory to exist: %s", dir)
	}
}

func TestEnvOverridesProduction(t *testing.T) {
	projectRoot := "/tmp/nexus-test"

	t.Setenv("NEXUS_DATA_DIR", "/var/lib/nexus/data")
	t.Setenv("NEXUS_STATE_DIR", "/var/lib/nexus/state")
	t.Setenv("NEXUS_LOGS_DIR", "/var/log/nexus")
	t.Setenv("NEXUS_CACHE_DIR", "/var/cache/nexus")

	assert.Equal(t, "/var/lib/nexus/data", GetDataDir(projectRoot))
	assert.Equal(t, "/var/lib/nexus/state", GetStateDir(projectRoot))
	assert.Equal(t, "/var/log/nexus", GetLogsDir(projectRoot))
	assert.Equal(t, "/var/cache/nexus", GetCacheDir(projectRoot))
}
