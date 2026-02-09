package plugins

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Lockfile represents the nexus.lock file structure
type Lockfile struct {
	Plugins map[string]LockedPlugin `json:"plugins"`
}

// LockedPlugin represents a plugin entry in the lockfile
type LockedPlugin struct {
	Version string `json:"version"`
	Source  string `json:"source,omitempty"`
}

// ReadLockfile reads and parses the nexus.lock file from the given path
func ReadLockfile(path string) (*Lockfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var lockfile Lockfile
	if err := json.Unmarshal(data, &lockfile); err != nil {
		return nil, fmt.Errorf("failed to parse lockfile: %w", err)
	}

	return &lockfile, nil
}

// ReadLockfileFromWorkspace reads the nexus.lock file from a workspace directory
func ReadLockfileFromWorkspace(workspacePath string) (*Lockfile, error) {
	lockfilePath := filepath.Join(workspacePath, "nexus.lock")
	return ReadLockfile(lockfilePath)
}

// DiscoverPlugins discovers remote plugins from a lockfile
func DiscoverPlugins(lockfile *Lockfile) ([]Plugin, error) {
	if lockfile == nil || len(lockfile.Plugins) == 0 {
		return nil, nil
	}

	var plugins []Plugin
	for name, locked := range lockfile.Plugins {
		plugin := Plugin{
			Name:    name,
			Version: locked.Version,
		}

		// Extract repository from source URL if present
		if locked.Source != "" {
			plugin.Repository = locked.Source
		}

		plugins = append(plugins, plugin)
	}

	return plugins, nil
}

// LoadPluginFromLockfile loads a specific plugin by name from the lockfile
func LoadPluginFromLockfile(name string, lockfile *Lockfile) (*Plugin, error) {
	if lockfile == nil {
		return nil, fmt.Errorf("lockfile is nil")
	}

	locked, exists := lockfile.Plugins[name]
	if !exists {
		return nil, fmt.Errorf("plugin %s not found in lockfile", name)
	}

	plugin := &Plugin{
		Name:    name,
		Version: locked.Version,
	}

	if locked.Source != "" {
		plugin.Repository = locked.Source
	}

	return plugin, nil
}

// GetPluginNames returns all plugin names from the lockfile
func GetPluginNames(lockfile *Lockfile) []string {
	if lockfile == nil {
		return nil
	}

	names := make([]string, 0, len(lockfile.Plugins))
	for name := range lockfile.Plugins {
		names = append(names, name)
	}

	return names
}

// HasPlugin checks if a plugin exists in the lockfile
func HasPlugin(lockfile *Lockfile, name string) bool {
	if lockfile == nil {
		return false
	}

	_, exists := lockfile.Plugins[name]
	return exists
}
