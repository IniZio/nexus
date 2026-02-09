package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	RuntimeDirName = ".nexus-runtime"
	ConfigDirName  = ".nexus"
)

// GetProjectRoot returns the project root directory (where .nexus/ should be)
func GetProjectRoot() string {
	if root := os.Getenv("NEXUS_PROJECT_ROOT"); root != "" {
		return root
	}
	cwd, _ := os.Getwd()
	return cwd
}

// GetDataDir returns the data directory for runtime files (databases, etc)
func GetDataDir(projectRoot string) string {
	if dir := os.Getenv("NEXUS_DATA_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(projectRoot, RuntimeDirName, "data")
}

// GetStateDir returns the state directory for runtime state (PIDs, worktrees, etc)
func GetStateDir(projectRoot string) string {
	if dir := os.Getenv("NEXUS_STATE_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(projectRoot, RuntimeDirName, "state")
}

// GetLogsDir returns the logs directory
func GetLogsDir(projectRoot string) string {
	if dir := os.Getenv("NEXUS_LOGS_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(projectRoot, RuntimeDirName, "logs")
}

// GetCacheDir returns the cache directory (user-local or system-wide)
func GetCacheDir(projectRoot string) string {
	if dir := os.Getenv("NEXUS_CACHE_DIR"); dir != "" {
		return dir
	}

	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".cache", "nexus")
	}

	return filepath.Join(projectRoot, RuntimeDirName, "cache")
}

// GetConfigDir returns the config directory (always .nexus/)
func GetConfigDir(projectRoot string) string {
	return filepath.Join(projectRoot, ConfigDirName)
}

// GetDatabasePath returns the full path to the SQLite database
func GetDatabasePath(projectRoot string) string {
	return filepath.Join(GetDataDir(projectRoot), "nexus.db")
}

// GetPIDFilePath returns the full path to the server PID file
func GetPIDFilePath(projectRoot string) string {
	return filepath.Join(GetStateDir(projectRoot), "server.pid")
}

// GetServerLockFilePath returns the full path to the server lock file
func GetServerLockFilePath(projectRoot string) string {
	return filepath.Join(GetStateDir(projectRoot), "server.lock")
}

// GetWorktreesDir returns the directory for workspace state
func GetWorktreesDir(projectRoot string) string {
	return filepath.Join(GetStateDir(projectRoot), "worktrees")
}

// GetServerLogPath returns the full path to the server log file
func GetServerLogPath(projectRoot string) string {
	return filepath.Join(GetLogsDir(projectRoot), "server.log")
}

// GetLogsArchiveDir returns the directory for archived logs
func GetLogsArchiveDir(projectRoot string) string {
	return filepath.Join(GetLogsDir(projectRoot), "archive")
}

// GetSSHCacheDir returns the directory for SSH key cache
func GetSSHCacheDir(projectRoot string) string {
	return filepath.Join(GetCacheDir(projectRoot), "ssh")
}

// GetTemplatesCacheDir returns the directory for compiled templates cache
func GetTemplatesCacheDir(projectRoot string) string {
	return filepath.Join(GetCacheDir(projectRoot), "templates")
}

// GetPluginsCacheDir returns the directory for plugin cache
func GetPluginsCacheDir(projectRoot string) string {
	return filepath.Join(GetCacheDir(projectRoot), "plugins")
}

// EnsureDir creates a directory if it doesn't exist, with proper error handling
func EnsureDir(path string) error {
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", path, err)
	}
	return nil
}

// EnsureDirs creates multiple directories
func EnsureDirs(paths ...string) error {
	for _, path := range paths {
		if err := EnsureDir(path); err != nil {
			return err
		}
	}
	return nil
}

// EnsureAllRuntimeDirs creates the complete runtime directory structure
func EnsureAllRuntimeDirs(projectRoot string) error {
	dirs := []string{
		GetDataDir(projectRoot),
		GetStateDir(projectRoot),
		GetWorktreesDir(projectRoot),
		GetLogsDir(projectRoot),
		GetLogsArchiveDir(projectRoot),
		GetCacheDir(projectRoot),
		GetSSHCacheDir(projectRoot),
		GetTemplatesCacheDir(projectRoot),
		GetPluginsCacheDir(projectRoot),
	}
	return EnsureDirs(dirs...)
}

// EnsureConfigDirs creates the config directory structure
func EnsureConfigDirs(projectRoot string) error {
	configDir := GetConfigDir(projectRoot)
	dirs := []string{
		configDir,
		filepath.Join(configDir, "agents"),
		filepath.Join(configDir, "templates"),
		filepath.Join(configDir, "hooks"),
		filepath.Join(configDir, "plugins"),
		filepath.Join(configDir, "remotes"),
	}
	return EnsureDirs(dirs...)
}
