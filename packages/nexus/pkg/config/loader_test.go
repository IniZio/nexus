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
	data := []byte(`{"version":1,"runtime":{"required":["local"]},"readiness":{"profiles":{"default-services":[{"name":"api","type":"service","serviceName":"api"}]}}}`)
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
	if len(cfg.Lifecycle.OnSetup) != 0 || len(cfg.Lifecycle.OnStart) != 0 || len(cfg.Lifecycle.OnTeardown) != 0 {
		t.Fatalf("expected empty lifecycle hooks, got setup=%v start=%v teardown=%v",
			cfg.Lifecycle.OnSetup, cfg.Lifecycle.OnStart, cfg.Lifecycle.OnTeardown)
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

func TestLoadWorkspaceConfig_DoctorTestsAndRuntime(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".nexus"), 0o755); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"version":1,"runtime":{"required":["firecracker"]},"doctor":{"tests":[{"name":"tooling","command":"bash"}]}}`)
	if err := os.WriteFile(filepath.Join(root, ".nexus", "workspace.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, _, err := LoadWorkspaceConfig(root)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(cfg.Runtime.Required) != 1 || cfg.Runtime.Required[0] != "firecracker" {
		t.Fatalf("expected runtime.required [firecracker], got %v", cfg.Runtime.Required)
	}
	if len(cfg.Doctor.Tests) != 1 {
		t.Fatalf("expected 1 doctor test, got %d", len(cfg.Doctor.Tests))
	}
	if cfg.Doctor.Tests[0].Name != "tooling" {
		t.Fatalf("expected test name 'tooling', got %q", cfg.Doctor.Tests[0].Name)
	}
	if cfg.Doctor.Tests[0].Command != "bash" {
		t.Fatalf("expected test command 'bash', got %q", cfg.Doctor.Tests[0].Command)
	}
}
