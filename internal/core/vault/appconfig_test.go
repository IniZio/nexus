package vault

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppConfigMissingClientIDRefuses(t *testing.T) {
	cfg := AppConfig{}
	_, err := cfg.LinearClientID()
	if err == nil {
		t.Fatal("expected error for missing Linear client_id; got nil")
	}
	if !strings.Contains(err.Error(), "client_id") {
		t.Errorf("error should mention client_id; got: %v", err)
	}
}

func TestLoadAppConfig_AbsentFile_ReturnsZero(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadAppConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error for absent file: %v", err)
	}
	if cfg.GitHub.ClientID != "" || cfg.Linear.ClientID != "" {
		t.Error("zero AppConfig expected for absent file")
	}
}

func TestLoadAppConfig_ParsesFields(t *testing.T) {
	dir := t.TempDir()
	data := `{"github":{"client_id":"gh-123"},"linear":{"client_id":"lin-456","client_secret":"sec"}}`
	if err := os.WriteFile(filepath.Join(dir, "vault-app.json"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadAppConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GitHub.ClientID != "gh-123" {
		t.Errorf("github.client_id = %q, want gh-123", cfg.GitHub.ClientID)
	}
	id, err := cfg.LinearClientID()
	if err != nil {
		t.Fatalf("LinearClientID: %v", err)
	}
	if id != "lin-456" {
		t.Errorf("linear.client_id = %q, want lin-456", id)
	}
}
