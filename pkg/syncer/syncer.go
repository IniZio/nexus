// Package syncer provides local-to-workspace configuration synchronization.
// It enables syncing local agent configurations (like Claude Desktop, Cursor rules,
// OpenCode config, VSCode settings, etc.) into workspaces.
package syncer

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config defines what to sync from local machine to workspace
type Config struct {
	// Paths defines individual file/directory syncs
	Paths []SyncPath `yaml:"paths"`
	// Strategy defines how to perform the sync
	Strategy SyncStrategy `yaml:"strategy"`
}

// SyncPath defines a single sync operation
type SyncPath struct {
	// Source is the local path (supports ~ expansion)
	Source string `yaml:"source"`
	// Target is the absolute path inside the workspace
	Target string `yaml:"target"`
	// Optional: Pattern to filter files (glob format)
	Pattern string `yaml:"pattern,omitempty"`
	// Optional: Exclude patterns (glob format, comma-separated)
	Exclude string `yaml:"exclude,omitempty"`
}

// SyncStrategy defines how to perform synchronization
type SyncStrategy string

const (
	// StrategyVolume mounts local paths as volumes (best for containers)
	StrategyVolume SyncStrategy = "volume"
	// StrategyCopy copies files on workspace start
	StrategyCopy SyncStrategy = "copy"
	// StrategyRsync uses rsync for efficient syncing
	StrategyRsync SyncStrategy = "rsync"
)

// VolumeMount represents a volume mount for a provider
type VolumeMount struct {
	Source    string // Local path (absolute)
	Target    string // Container path (absolute)
	ReadOnly  bool   `yaml:"read_only"`
	MountType string // "bind" or "volume"
}

// LoadConfig loads sync configuration from a file
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}

	// Apply defaults
	if cfg.Strategy == "" {
		cfg.Strategy = StrategyVolume
	}

	return &cfg, nil
}

// ExpandPaths expands ~ in source paths to the user's home directory
func (c *Config) ExpandPaths() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	for i := range c.Paths {
		c.Paths[i].Source = expandHome(c.Paths[i].Source, home)
	}

	return nil
}

func expandHome(path, home string) string {
	if len(path) > 1 && path[0] == '~' {
		return filepath.Join(home, path[1:])
	}
	return path
}

// GetVolumeMounts returns volume mounts for the sync configuration
// These can be passed to the provider when creating a workspace
func (c *Config) GetVolumeMounts() []VolumeMount {
	var mounts []VolumeMount

	for _, sp := range c.Paths {
		// Check if source exists
		info, err := os.Stat(sp.Source)
		if err != nil {
			// Skip if source doesn't exist
			continue
		}

		mount := VolumeMount{
			Source:    sp.Source,
			Target:    sp.Target,
			ReadOnly:  true,
			MountType: "bind",
		}

		_ = info // info used for future type checking (dir vs file)

		mounts = append(mounts, mount)
	}

	return mounts
}

// Validate validates the sync configuration
func (c *Config) Validate() error {
	for _, sp := range c.Paths {
		if sp.Source == "" {
			return ErrEmptySource
		}
		if sp.Target == "" {
			return ErrEmptyTarget
		}
	}
	return nil
}

// Common errors
var (
	ErrEmptySource = &ValidationError{Field: "source", Message: "source path cannot be empty"}
	ErrEmptyTarget = &ValidationError{Field: "target", Message: "target path cannot be empty"}
)

// ValidationError represents a configuration validation error
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Message
}

