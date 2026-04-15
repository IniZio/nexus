package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoader_LoadsWorkspaceJSON(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"version":1}`)
	if err := os.WriteFile(filepath.Join(root, ".nexus", "workspace.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, warnings, err := LoadWorkspaceConfig(root)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	if cfg.Version != 1 {
		t.Fatalf("expected version 1, got %d", cfg.Version)
	}
}

func TestLoader_NoWorkspaceJSON_ReturnsDefaultConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, warnings, err := LoadWorkspaceConfig(root)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Version != 1 {
		t.Fatalf("expected default version 1, got %d", cfg.Version)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
}

func TestLoader_IgnoresLegacyLifecycleWhenWorkspaceMissing(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nexus", "lifecycle.json"), []byte(`{"hooks":{"pre-start":[{"command":"echo"}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, warnings, err := LoadWorkspaceConfig(root)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Version != 1 {
		t.Fatalf("expected default version 1, got %d", cfg.Version)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
}

func TestLoader_EmptyWorkspaceJSONDefaultsVersionToOne(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".nexus", "workspace.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := LoadWorkspaceConfig(root)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if cfg.Version != 1 {
		t.Fatalf("expected version 1, got %d", cfg.Version)
	}
}

func TestLoader_MalformedWorkspaceJSON_ReturnsError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"version":1,"readiness":{"profiles":{"default-services":[`)
	if err := os.WriteFile(filepath.Join(root, ".nexus", "workspace.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := LoadWorkspaceConfig(root)
	if err == nil {
		t.Fatalf("expected error for malformed JSON, got nil")
	}
}

func TestLoader_UnknownFieldInWorkspaceJSON_ReturnsError(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"version":1,"unknownField":"value"}`)
	if err := os.WriteFile(filepath.Join(root, ".nexus", "workspace.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := LoadWorkspaceConfig(root)
	if err == nil {
		t.Fatalf("expected error for unknown field, got nil")
	}
}

func TestLoader_LoadsInternalFeatures(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"version":1,"internalFeatures":{"localDriver":true}}`)
	if err := os.WriteFile(filepath.Join(root, ".nexus", "workspace.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, warnings, err := LoadWorkspaceConfig(root)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", warnings)
	}
	if !cfg.InternalFeatures.LocalDriver {
		t.Fatalf("expected internalFeatures.localDriver=true")
	}
}
