package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// userMountConfigFile is the top-level YAML wrapper. Only the agent_mounts key
// is consumed; unknown keys are silently ignored (yaml.v3 default).
type userMountConfigFile struct {
	AgentMounts UserMountConfig `yaml:"agent_mounts"`
}

// UserMountConfig is the parsed agent_mounts section of ~/.config/nexus3/config.yaml.
// All fields are optional; a zero value means "no user overrides, use defaults."
type UserMountConfig struct {
	// DisableDefaults suppresses the built-in defaultUserMounts table. When true
	// only the user's own Mounts entries are used.
	DisableDefaults bool `yaml:"disable_defaults"`

	// Mounts is the user's additional (or overriding) mount entries. Each entry
	// with the same guest path as a default row replaces that default.
	Mounts []UserMountEntry `yaml:"mounts"`

	// Symlinks declares guest-side symlinks the guest seed should create on first
	// boot. Targets are guest paths — the symlink is created entirely in-guest.
	// Built-in defaults contribute no symlinks (they are host-specific).
	// Use for a marketplace/plugin registered at a host path outside the mounted
	// trees; prefer re-pointing the registration at an already-mounted path.
	// Example:
	//   link:   /root/.config/some-tool/plugins/foo
	//   target: /root/.local/share/foo
	Symlinks []UserSymlinkEntry `yaml:"symlinks"`
}

// UserMountEntry is one user-configured host→guest mount.
// Host supports ~ and $HOME expansion against the operator's home dir.
// Overlay semantics match UserMount.Overlay.
// RO is reserved for future use; all mounts are read-only for now.
//
// SECURITY: never add secret dirs (~/.ssh, ~/.config/gh, ~/.claude.json,
// ~/.claude/.credentials.json). The config is the user's responsibility, but
// note that anything listed here becomes visible to the in-guest agent.
type UserMountEntry struct {
	Host    string `yaml:"host"`
	Guest   string `yaml:"guest"`
	Overlay bool   `yaml:"overlay"`
	RO      *bool  `yaml:"ro"` // reserved; ignored (all mounts RO)
}

// UserSymlinkEntry is one guest-side symlink to create.
// Both Link and Target are in-guest paths; no host path is involved.
type UserSymlinkEntry struct {
	Link   string `yaml:"link"`   // guest path to create (the symlink)
	Target string `yaml:"target"` // guest path it should point to
}

// LoadUserMountConfig reads ~/.config/nexus3/config.yaml (respecting
// $XDG_CONFIG_HOME) and returns the parsed agent_mounts section.
//
// Absent file → zero UserMountConfig, nil error (caller uses defaults as-is).
// Malformed YAML → zero UserMountConfig, non-nil error (caller logs + falls
// back to defaults — sandbox create must never fail due to a config parse error).
//
// hostHome is used both for ~ expansion in Host fields and as the fallback base
// for the config dir when $XDG_CONFIG_HOME is unset.
func LoadUserMountConfig(hostHome string) (UserMountConfig, error) {
	// Derive config dir: XDG_CONFIG_HOME if set, else <hostHome>/.config.
	// This makes the function testable without touching real env vars.
	configBase := os.Getenv("XDG_CONFIG_HOME")
	if configBase == "" {
		configBase = filepath.Join(hostHome, ".config")
	}
	path := filepath.Join(configBase, "nexus3", "config.yaml")

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return UserMountConfig{}, nil // no config → pure defaults
	}
	if err != nil {
		return UserMountConfig{}, fmt.Errorf("usermount: read %s: %w", path, err)
	}

	var f userMountConfigFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return UserMountConfig{}, fmt.Errorf("usermount: parse %s: %w", path, err)
	}

	// Expand ~ and $HOME in Host fields against the operator's home dir.
	cfg := f.AgentMounts
	for i := range cfg.Mounts {
		cfg.Mounts[i].Host = expandHome(cfg.Mounts[i].Host, hostHome)
	}
	return cfg, nil
}

// expandHome expands a leading ~ or $HOME in path against home.
// Other occurrences of $HOME within the string are also replaced.
func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return home + path[1:] // home + "/" + rest
	}
	return strings.ReplaceAll(path, "$HOME", home)
}
