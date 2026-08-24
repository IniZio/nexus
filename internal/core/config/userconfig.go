package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// LoadUserGlobal reads the user-global nexus3 config from
// $XDG_CONFIG_HOME/nexus3/config.yaml (falling back to ~/.config/nexus3/config.yaml).
//
// An absent file returns a zero Config and nil error — callers treat absence
// as "no overrides". A malformed file returns a non-nil error; the caller logs
// and continues (sandbox create must not fail on a bad user config).
//
// The same version requirement and unknown-key strictness as the repo-level
// Load applies — the file must contain version: 1.
func LoadUserGlobal() (Config, error) {
	dir, err := userConfigDir()
	if err != nil {
		return Config{}, fmt.Errorf("user config: resolve dir: %w", err)
	}
	path := filepath.Join(dir, "nexus3", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, nil // absent is fine
		}
		return Config{}, fmt.Errorf("user config: read %s: %w", path, err)
	}
	cfg, err := parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("user config: parse %s: %w", path, err)
	}
	return cfg, nil
}

// userConfigDir returns the XDG_CONFIG_HOME directory, falling back to
// ~/.config when the env var is unset or empty.
func userConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config"), nil
}
