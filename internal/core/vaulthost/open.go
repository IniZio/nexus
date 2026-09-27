package vaulthost

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/connectors"
	"github.com/IniZio/nexus/internal/core/vault/linkflow"
)

// Open builds a VaultImpl from the default on-disk store.
// Used by both the CLI and the detached supervisor.
func Open() (*vault.VaultImpl, error) {
	dir, err := defaultDir()
	if err != nil {
		return nil, err
	}
	key, err := vault.LoadKey(nil)
	if err != nil {
		return nil, fmt.Errorf("vaulthost: load key: %w", err)
	}
	st, err := vault.NewFileStore(dir, key)
	if err != nil {
		return nil, fmt.Errorf("vaulthost: store: %w", err)
	}
	cfg, _ := defaultAppConfig()
	reg := vault.NewRegistry()
	_ = reg.Register(connectors.NewGitHub(cfg.GitHub.ClientID))
	if cfg.Linear.ClientID != "" {
		redirectURL := fmt.Sprintf("http://localhost:%d", linkflow.LoopbackPort)
		_ = reg.Register(connectors.NewLinear(cfg.Linear.ClientID, cfg.Linear.ClientSecret, redirectURL))
	}
	return vault.NewVaultImpl(st, reg), nil
}

// DefaultDir returns the canonical vault directory: $XDG_DATA_HOME/nexus/vault
// (default ~/.local/share/nexus/vault). Call this instead of duplicating the
// resolution in cmd_vault.go or the supervisor.
func DefaultDir() (string, error) {
	xdg := os.Getenv("XDG_DATA_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("vaulthost: home dir: %w", err)
		}
		xdg = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(xdg, "nexus", "vault"), nil
}

func defaultDir() (string, error) { return DefaultDir() }

func defaultAppConfig() (vault.AppConfig, error) {
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return vault.AppConfig{}, err
		}
		xdg = filepath.Join(home, ".config")
	}
	return vault.LoadAppConfig(filepath.Join(xdg, "nexus"))
}
