// Package daemonclient provides helpers for locating, checking liveness of,
// and auto-starting the nexus workspace daemon from the CLI.
package daemonclient

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// DaemonSecretStore defines the interface for daemon JWT signing secret storage.
type DaemonSecretStore interface {
	// Get retrieves the secret. Returns empty string if not found.
	Get() (string, error)
	// Set stores the secret.
	Set(secret string) error
}

// EnvSecretStore reads/writes secret from environment variable.
// This is the highest priority for e2e/dev overrides.
type EnvSecretStore struct {
	VarName string
}

func (s *EnvSecretStore) Get() (string, error) {
	secret := strings.TrimSpace(os.Getenv(s.VarName))
	return secret, nil
}

func (s *EnvSecretStore) Set(secret string) error {
	return os.Setenv(s.VarName, secret)
}

// KeyringSecretStore stores secret in OS keyring/keychain.
// Uses macOS Keychain on Darwin, secret-tool on Linux.
type KeyringSecretStore struct {
	Service string
	Account string
}

func (s *KeyringSecretStore) Get() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return s.getDarwin()
	case "linux":
		return s.getLinux()
	default:
		return "", fmt.Errorf("keyring not supported on %s", runtime.GOOS)
	}
}

func (s *KeyringSecretStore) Set(secret string) error {
	switch runtime.GOOS {
	case "darwin":
		return s.setDarwin(secret)
	case "linux":
		return s.setLinux(secret)
	default:
		return fmt.Errorf("keyring not supported on %s", runtime.GOOS)
	}
}

func (s *KeyringSecretStore) getDarwin() (string, error) {
	// security find-generic-password -s <service> -a <account> -w
	cmd := exec.Command("security", "find-generic-password",
		"-s", s.Service, "-a", s.Account, "-w")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 44 {
			// Item not found
			return "", nil
		}
		return "", fmt.Errorf("keychain get: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *KeyringSecretStore) setDarwin(secret string) error {
	// First try to update existing item
	cmd := exec.Command("security", "add-generic-password",
		"-s", s.Service, "-a", s.Account, "-w", secret,
		"-U") // -U = update if exists
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain set: %w", err)
	}
	return nil
}

func (s *KeyringSecretStore) getLinux() (string, error) {
	// secret-tool lookup service <service> username <account>
	cmd := exec.Command("secret-tool", "lookup", "service", s.Service, "username", s.Account)
	out, err := cmd.Output()
	if err != nil {
		// secret-tool exits with error if not found
		return "", nil
	}
	return strings.TrimSpace(string(out)), nil
}

func (s *KeyringSecretStore) setLinux(secret string) error {
	// secret-tool store --label="Nexus Daemon Secret" service <service> username <account>
	cmd := exec.Command("secret-tool", "store",
		"--label=Nexus Daemon Secret",
		"service", s.Service,
		"username", s.Account)
	cmd.Stdin = strings.NewReader(secret)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keyring set: %w", err)
	}
	return nil
}

// FileSecretStore stores secret in a file (fallback for headless/CI).
type FileSecretStore struct {
	Path string
}

func (s *FileSecretStore) Get() (string, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (s *FileSecretStore) Set(secret string) error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create secret directory: %w", err)
	}
	return os.WriteFile(s.Path, []byte(secret), 0o600)
}

// ChainedSecretStore tries multiple stores in order.
// First successful Get wins.
// Set writes to all stores that don't error on write.
type ChainedSecretStore struct {
	Stores []DaemonSecretStore
}

func (c *ChainedSecretStore) Get() (string, error) {
	for _, store := range c.Stores {
		secret, err := store.Get()
		if err != nil {
			continue // Skip failing stores
		}
		if secret != "" {
			return secret, nil
		}
	}
	return "", nil
}

func (c *ChainedSecretStore) Set(secret string) error {
	var lastErr error
	success := false
	for _, store := range c.Stores {
		if err := store.Set(secret); err != nil {
			lastErr = err
			// Continue to try other stores
		} else {
			success = true
		}
	}
	if !success && lastErr != nil {
		return lastErr
	}
	return nil
}

// Default keyring service/account names.
const (
	DefaultKeyringService = "nexus-daemon"
	DefaultKeyringAccount = "jwt-secret"
)

// NewDefaultSecretStore creates the default secret store chain:
// 1. NEXUS_DAEMON_TOKEN env var (dev/e2e override)
// 2. OS keyring/keychain (secure production)
// 3. Token file in data directory (fallback)
func NewDefaultSecretStore() DaemonSecretStore {
	dataDir, _ := DefaultDataDir()
	secretPath := filepath.Join(dataDir, "token")

	return &ChainedSecretStore{
		Stores: []DaemonSecretStore{
			&EnvSecretStore{VarName: "NEXUS_DAEMON_TOKEN"},
			&KeyringSecretStore{Service: DefaultKeyringService, Account: DefaultKeyringAccount},
			&FileSecretStore{Path: secretPath},
		},
	}
}

// NewSecureSecretStore creates a secret store that prefers keyring but falls back to file.
// This is used by the daemon to persist secrets securely when possible.
func NewSecureSecretStore() DaemonSecretStore {
	dataDir, _ := DefaultDataDir()
	secretPath := filepath.Join(dataDir, "token")

	return &ChainedSecretStore{
		Stores: []DaemonSecretStore{
			&KeyringSecretStore{Service: DefaultKeyringService, Account: DefaultKeyringAccount},
			&FileSecretStore{Path: secretPath},
		},
	}
}

// ReadDaemonSecretFromStore reads the daemon JWT secret using the default store chain.
func ReadDaemonSecretFromStore() (string, error) {
	store := NewDefaultSecretStore()
	secret, err := store.Get()
	if err != nil {
		return "", fmt.Errorf("read daemon secret: %w", err)
	}
	if secret == "" {
		return "", fmt.Errorf("daemon secret not found")
	}
	return secret, nil
}

// PersistDaemonSecret stores the secret in the secure store chain (keyring preferred, file fallback).
func PersistDaemonSecret(secret string) error {
	store := NewSecureSecretStore()
	return store.Set(secret)
}
