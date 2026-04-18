package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/config"
)

func TestLoadNodeConfig_Defaults(t *testing.T) {
	cfg, err := config.LoadNodeConfig("/nonexistent/path/node.json")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("expected default version 1, got %d", cfg.Version)
	}
	if cfg.HasExplicitCapabilities() {
		t.Error("expected no explicit capabilities in default config")
	}
}

func TestLoadNodeConfig_Full(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.json")

	raw := map[string]any{
		"version": 1,
		"node": map[string]any{
			"name": "test-node",
			"tags": []string{"builder", "macos"},
		},
		"capabilities": map[string]any{
			"provide": []string{
				"runtime.oci",
				"toolchain.xcodebuild",
				"auth.profile.git",
			},
		},
	}

	data, _ := json.Marshal(raw)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadNodeConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Node.Name != "test-node" {
		t.Errorf("expected node name 'test-node', got %q", cfg.Node.Name)
	}
	if len(cfg.Node.Tags) != 2 {
		t.Errorf("expected 2 tags, got %d", len(cfg.Node.Tags))
	}
	if !cfg.HasExplicitCapabilities() {
		t.Error("expected explicit capabilities")
	}
	if !cfg.ProvidesCapability("runtime.oci") {
		t.Error("expected runtime.oci capability")
	}
	if !cfg.ProvidesCapability("toolchain.xcodebuild") {
		t.Error("expected toolchain.xcodebuild capability")
	}
	if cfg.ProvidesCapability("runtime.firecracker") {
		t.Error("did not expect runtime.firecracker capability")
	}
}

func TestLoadNodeConfig_InvalidVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.json")

	raw := map[string]any{"version": 0}
	data, _ := json.Marshal(raw)
	_ = os.WriteFile(path, data, 0644)

	_, err := config.LoadNodeConfig(path)
	if err == nil {
		t.Fatal("expected error for version < 1")
	}
}

func TestLoadNodeConfig_UnknownField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.json")

	raw := map[string]any{"version": 1, "unknownField": "value"}
	data, _ := json.Marshal(raw)
	_ = os.WriteFile(path, data, 0644)

	_, err := config.LoadNodeConfig(path)
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestNodeConfigPath_NotEmpty(t *testing.T) {
	p := config.NodeConfigPath()
	if p == "" {
		t.Error("NodeConfigPath() returned empty string")
	}
}

func TestLoadNodeConfig_Compatibility_MinimumDaemonVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.json")

	raw := map[string]any{
		"version": 1,
		"compatibility": map[string]any{
			"minimumDaemonVersion": "v0.3.0",
		},
	}
	data, _ := json.Marshal(raw)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadNodeConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Compatibility.MinimumDaemonVersion != "v0.3.0" {
		t.Fatalf("expected minimum daemon version v0.3.0, got %q", cfg.Compatibility.MinimumDaemonVersion)
	}
}

func TestLoadNodeConfig_Compatibility_InvalidMinimumDaemonVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.json")

	raw := map[string]any{
		"version": 1,
		"compatibility": map[string]any{
			"minimumDaemonVersion": "not-semver",
		},
	}
	data, _ := json.Marshal(raw)
	_ = os.WriteFile(path, data, 0o644)

	_, err := config.LoadNodeConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid minimumDaemonVersion")
	}
}

func TestNodeConfigPath_DefaultsToDotNexusWhenXDGConfigHomeMissing(t *testing.T) {
	orig := os.Getenv("XDG_CONFIG_HOME")
	home, homeErr := os.UserHomeDir()
	if homeErr != nil || home == "" {
		t.Skip("home directory unavailable")
	}
	t.Cleanup(func() { _ = os.Setenv("XDG_CONFIG_HOME", orig) })
	_ = os.Unsetenv("XDG_CONFIG_HOME")

	got := config.NodeConfigPath()
	want := filepath.Join(home, ".nexus", "node.json")
	if got != want {
		t.Fatalf("expected default node config path %q, got %q", want, got)
	}
}

func TestNodeDBPath_DefaultsToDotNexusWhenXDGStateHomeMissing(t *testing.T) {
	orig := os.Getenv("XDG_STATE_HOME")
	home, homeErr := os.UserHomeDir()
	if homeErr != nil || home == "" {
		t.Skip("home directory unavailable")
	}
	t.Cleanup(func() { _ = os.Setenv("XDG_STATE_HOME", orig) })
	_ = os.Unsetenv("XDG_STATE_HOME")

	got := config.NodeDBPath()
	want := filepath.Join(home, ".nexus", "node.db")
	if got != want {
		t.Fatalf("expected default node db path %q, got %q", want, got)
	}
}
