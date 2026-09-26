package vault_test

import (
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/core/vault"
)

func TestKeySourceOrder(t *testing.T) {
	t.Run("credentials_directory_wins", func(t *testing.T) {
		dir := t.TempDir()
		want := make([]byte, 32)
		rand.Read(want)
		credPath := filepath.Join(dir, vault.KeyCredentialName)
		os.WriteFile(credPath, want, 0600)
		t.Setenv("CREDENTIALS_DIRECTORY", dir)

		neverCalled := func(_, _ string) (string, error) {
			return "", errors.New("keyring should not be called")
		}
		got, err := vault.LoadKey(neverCalled)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Error("key from CREDENTIALS_DIRECTORY does not match")
		}
	})

	t.Run("keyring_wins_without_credentials_directory", func(t *testing.T) {
		t.Setenv("CREDENTIALS_DIRECTORY", "")
		want := make([]byte, 32)
		rand.Read(want)
		keyringFn := func(_, _ string) (string, error) {
			return string(want), nil
		}
		got, err := vault.LoadKey(keyringFn)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Error("key from keyring does not match")
		}
	})

	t.Run("falls_back_to_key_file", func(t *testing.T) {
		t.Setenv("CREDENTIALS_DIRECTORY", "")
		configDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", configDir)

		failKeyring := func(_, _ string) (string, error) {
			return "", errors.New("no keyring")
		}
		got, err := vault.LoadKey(failKeyring)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 32 {
			t.Errorf("key length %d, want 32", len(got))
		}
		keyPath := filepath.Join(configDir, "nexus", "vault.key")
		if _, err := os.Stat(keyPath); err != nil {
			t.Errorf("key file not created: %v", err)
		}
	})
}

func TestKeyFileRejectsWideMode(t *testing.T) {
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	configDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configDir)

	keyDir := filepath.Join(configDir, "nexus")
	os.MkdirAll(keyDir, 0700)
	keyPath := filepath.Join(keyDir, "vault.key")
	key := make([]byte, 32)
	rand.Read(key)
	os.WriteFile(keyPath, key, 0644)

	failKeyring := func(_, _ string) (string, error) {
		return "", errors.New("no keyring")
	}
	_, err := vault.LoadKey(failKeyring)
	if err == nil {
		t.Fatal("expected error for wide-mode key file")
	}
}
