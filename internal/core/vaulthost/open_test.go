package vaulthost

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConnectorRegistryResolvesGitHub(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg, err := DefaultConnectorRegistry()
	if err != nil {
		t.Fatalf("DefaultConnectorRegistry: %v", err)
	}
	if _, err := reg.Get("github"); err != nil {
		t.Fatalf("registry.Get(\"github\"): %v", err)
	}
}

func TestVaultDirUsesXDGDataHome(t *testing.T) {
	t.Run("respects XDG_DATA_HOME override", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("XDG_DATA_HOME", tmp)
		t.Setenv("XDG_CONFIG_HOME", "/should-not-be-used")

		dir, err := DefaultDir()
		if err != nil {
			t.Fatalf("DefaultDir: %v", err)
		}
		want := filepath.Join(tmp, "nexus", "vault")
		if dir != want {
			t.Errorf("DefaultDir = %q, want %q", dir, want)
		}
		if strings.Contains(dir, "/should-not-be-used") {
			t.Errorf("DefaultDir must not use XDG_CONFIG_HOME; got %q", dir)
		}
	})

	t.Run("defaults to ~/.local/share when XDG_DATA_HOME unset", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "")

		dir, err := DefaultDir()
		if err != nil {
			t.Fatalf("DefaultDir: %v", err)
		}
		home, _ := os.UserHomeDir()
		want := filepath.Join(home, ".local", "share", "nexus", "vault")
		if dir != want {
			t.Errorf("DefaultDir = %q, want %q", dir, want)
		}
	})

	t.Run("never contains .config", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "")
		dir, err := DefaultDir()
		if err != nil {
			t.Fatalf("DefaultDir: %v", err)
		}
		if strings.Contains(dir, ".config") {
			t.Errorf("DefaultDir must not use .config; got %q", dir)
		}
	})
}
