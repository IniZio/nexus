package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// NodeConfig describes the capabilities and identity of a Nexus node (host machine).
// It is stored at an XDG-level config path (e.g. ~/.config/nexus/node.json on Linux,
// It is stored at $XDG_CONFIG_HOME/nexus/node.json (default ~/.config/nexus/node.json) and is separate from workspace
// config which only declares what a workspace requires.
type NodeConfig struct {
	Schema        string            `json:"$schema,omitempty"`
	Version       int               `json:"version"`
	Node          NodeIdentity      `json:"node,omitempty"`
	Capabilities  NodeCapabilities  `json:"capabilities,omitempty"`
	Compatibility NodeCompatibility `json:"compatibility,omitempty"`
}

type NodeCompatibility struct {
	MinimumDaemonVersion string `json:"minimumDaemonVersion,omitempty"`
}

// NodeIdentity holds human-readable metadata about the node.
type NodeIdentity struct {
	// Name is a short human-readable label for this node (e.g. "mac-pro-m2", "linux-builder-01").
	Name string `json:"name,omitempty"`
	// Tags are arbitrary labels for grouping or filtering nodes.
	Tags []string `json:"tags,omitempty"`
}

// NodeCapabilities advertises the capabilities that are available on this node.
// Each entry maps a capability name to a CapabilityAdvertisement describing it.
type NodeCapabilities struct {
	// Provide is the explicit list of capabilities this node advertises as available.
	// Values should match the capability names used in workspace requirements
	// (e.g. "runtime.firecracker", "toolchain.xcodebuild", "auth.profile.git").
	Provide []string `json:"provide,omitempty"`
}

// NodeConfigPath returns the XDG config path for the node config file.
// It respects $XDG_CONFIG_HOME on all platforms.
// If $XDG_CONFIG_HOME is empty, it defaults to ~/.nexus/node.json.
func NodeConfigPath() string {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".nexus", "node.json")
		}
		return filepath.Join(home, ".nexus", "node.json")
	}
	return filepath.Join(configHome, "nexus", "node.json")
}

// NodeDBPath returns the sqlite path for node-level persistent metadata.
// It respects $XDG_STATE_HOME on all platforms.
// If $XDG_STATE_HOME is empty, it defaults to ~/.nexus/node.db.
func NodeDBPath() string {
	if stateHome := os.Getenv("XDG_STATE_HOME"); stateHome != "" {
		return filepath.Join(stateHome, "nexus", "node.db")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".nexus", "node.db")
	}
	return filepath.Join(home, ".nexus", "node.db")
}

// LoadNodeConfig reads and parses the node config from the given path.
// If path is empty, NodeConfigPath() is used. If the file does not exist,
// a default config is returned without error.
func LoadNodeConfig(path string) (*NodeConfig, error) {
	if path == "" {
		path = NodeConfigPath()
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultNodeConfig(), nil
		}
		return nil, fmt.Errorf("failed to read node config %s: %w", path, err)
	}

	var cfg NodeConfig
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse node config %s: %w", path, err)
	}

	if err := cfg.ValidateBasic(); err != nil {
		return nil, fmt.Errorf("invalid node config %s: %w", path, err)
	}

	return &cfg, nil
}

// DefaultNodeConfig returns a minimal valid node config with no explicit capability overrides.
// The daemon will probe capabilities at runtime when no node config is present.
func DefaultNodeConfig() *NodeConfig {
	return &NodeConfig{
		Version: 1,
	}
}

// ValidateBasic performs basic structural validation of the node config.
func (c *NodeConfig) ValidateBasic() error {
	if c.Version < 1 {
		return fmt.Errorf("node config version must be >= 1")
	}
	if v := c.Compatibility.MinimumDaemonVersion; v != "" {
		matched, err := regexp.MatchString(`^v?\d+\.\d+\.\d+$`, v)
		if err != nil {
			return fmt.Errorf("validate minimumDaemonVersion: %w", err)
		}
		if !matched {
			return fmt.Errorf("minimumDaemonVersion must be semver-like (e.g. v0.3.0): %q", v)
		}
	}
	return nil
}

// ProvidesCapability reports whether this node config explicitly advertises the given capability.
// If no capabilities are declared (i.e. the node config is the default), this returns false
// and the daemon falls back to runtime probing.
func (c *NodeConfig) ProvidesCapability(name string) bool {
	for _, cap := range c.Capabilities.Provide {
		if cap == name {
			return true
		}
	}
	return false
}

// HasExplicitCapabilities reports whether the node config has any explicitly declared capabilities.
// When false, the daemon should rely entirely on runtime probing rather than this config.
func (c *NodeConfig) HasExplicitCapabilities() bool {
	return len(c.Capabilities.Provide) > 0
}
