package vault

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

const keySize = 32

const keyringService = "nexus-vault"
const keyringUser = "key"

// KeyringFn is injectable so tests never touch the real OS keyring.
type KeyringFn func(service, user string) (string, error)

// LoadKey returns the 32-byte vault encryption key.
// Sources checked in order:
//  1. $CREDENTIALS_DIRECTORY/<KeyCredentialName>
//  2. OS keyring (injectable via kr; nil uses the real keyring)
//  3. $XDG_CONFIG_HOME/nexus/vault.key — refused if mode > 0600; generated on first use
func LoadKey(kr KeyringFn) ([]byte, error) {
	if dir := os.Getenv("CREDENTIALS_DIRECTORY"); dir != "" {
		path := filepath.Join(dir, KeyCredentialName)
		if data, err := os.ReadFile(path); err == nil && len(data) >= keySize {
			return data[:keySize], nil
		}
	}

	fn := kr
	if fn == nil {
		fn = func(svc, user string) (string, error) {
			return keyring.Get(svc, user)
		}
	}
	if val, err := fn(keyringService, keyringUser); err == nil && len(val) >= keySize {
		return []byte(val[:keySize]), nil
	}

	return loadOrCreateKeyFile()
}

func keyFilePath() (string, error) {
	configHome := os.Getenv("XDG_CONFIG_HOME")
	if configHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "nexus", "vault.key"), nil
}

func loadOrCreateKeyFile() ([]byte, error) {
	path, err := keyFilePath()
	if err != nil {
		return nil, fmt.Errorf("vault: key file path: %w", err)
	}

	info, statErr := os.Stat(path)
	if statErr == nil {
		if info.Mode().Perm()&^os.FileMode(0600) != 0 {
			return nil, fmt.Errorf("vault: key file %s has unsafe mode %v", path, info.Mode().Perm())
		}
		return os.ReadFile(path)
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("vault: stat key file: %w", statErr)
	}

	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("vault: generate key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("vault: mkdir for key file: %w", err)
	}
	if err := os.WriteFile(path, key, 0600); err != nil {
		return nil, fmt.Errorf("vault: write key file: %w", err)
	}
	return key, nil
}
