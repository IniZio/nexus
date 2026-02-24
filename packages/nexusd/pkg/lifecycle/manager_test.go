package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestNewManager_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if m.config != nil {
		t.Errorf("Expected nil config when no lifecycle.json exists, got %v", m.config)
	}
}

func TestNewManager_WithConfig(t *testing.T) {
	tmpDir := t.TempDir()

	nexusDir := filepath.Join(tmpDir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		t.Fatalf("Failed to create .nexus dir: %v", err)
	}

	config := LifecycleConfig{
		Version: "1",
		Hooks: Hooks{
			PreStart: []Hook{
				{Name: "test-hook", Command: "echo", Args: []string{"hello"}},
			},
		},
	}

	configData, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	configPath := filepath.Join(nexusDir, "lifecycle.json")
	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if m.config == nil {
		t.Fatal("Expected config to be loaded")
	}

	if m.config.Version != "1" {
		t.Errorf("Version = %q, want %q", m.config.Version, "1")
	}

	if len(m.config.Hooks.PreStart) != 1 {
		t.Errorf("PreStart hooks = %d, want %d", len(m.config.Hooks.PreStart), 1)
	}
}

func TestNewManager_MalformedConfig(t *testing.T) {
	tmpDir := t.TempDir()

	nexusDir := filepath.Join(tmpDir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		t.Fatalf("Failed to create .nexus dir: %v", err)
	}

	configPath := filepath.Join(nexusDir, "lifecycle.json")
	if err := os.WriteFile(configPath, []byte("invalid json"), 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager() returns nil error even on malformed config: %v", err)
	}

	if m.config != nil {
		t.Error("Expected nil config for malformed JSON")
	}
}

func TestManager_RunPreStart_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	err = m.RunPreStart()
	if err != nil {
		t.Errorf("RunPreStart() error = %v", err)
	}
}

func TestManager_RunPostStart_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	err = m.RunPostStart()
	if err != nil {
		t.Errorf("RunPostStart() error = %v", err)
	}
}

func TestManager_RunPreStop_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	err = m.RunPreStop()
	if err != nil {
		t.Errorf("RunPreStop() error = %v", err)
	}
}

func TestManager_RunPostStop_NoConfig(t *testing.T) {
	tmpDir := t.TempDir()

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	err = m.RunPostStop()
	if err != nil {
		t.Errorf("RunPostStop() error = %v", err)
	}
}

func TestManager_RunHook_Success(t *testing.T) {
	tmpDir := t.TempDir()

	nexusDir := filepath.Join(tmpDir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		t.Fatalf("Failed to create .nexus dir: %v", err)
	}

	config := LifecycleConfig{
		Version: "1",
		Hooks: Hooks{
			PreStart: []Hook{
				{Name: "echo-test", Command: "echo", Args: []string{"hello"}},
			},
		},
	}

	configData, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	configPath := filepath.Join(nexusDir, "lifecycle.json")
	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	err = m.RunPreStart()
	if err != nil {
		t.Errorf("RunPreStart() error = %v", err)
	}
}

func TestManager_RunHook_NotFound(t *testing.T) {
	tmpDir := t.TempDir()

	nexusDir := filepath.Join(tmpDir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		t.Fatalf("Failed to create .nexus dir: %v", err)
	}

	config := LifecycleConfig{
		Version: "1",
		Hooks: Hooks{
			PreStart: []Hook{
				{Name: "nonexistent", Command: "/nonexistent/command", Args: []string{}},
			},
		},
	}

	configData, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	configPath := filepath.Join(nexusDir, "lifecycle.json")
	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	err = m.RunPreStart()
	if err == nil {
		t.Error("Expected error for nonexistent command, got nil")
	}
}

func TestManager_RunHook_PermissionDenied(t *testing.T) {
	tmpDir := t.TempDir()

	scriptPath := filepath.Join(tmpDir, "no-permission.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\nexit 1"), 0000); err != nil {
		t.Fatalf("Failed to create script: %v", err)
	}

	nexusDir := filepath.Join(tmpDir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		t.Fatalf("Failed to create .nexus dir: %v", err)
	}

	config := LifecycleConfig{
		Version: "1",
		Hooks: Hooks{
			PreStart: []Hook{
				{Name: "permission-test", Command: scriptPath},
			},
		},
	}

	configData, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	configPath := filepath.Join(nexusDir, "lifecycle.json")
	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	err = m.RunPreStart()
	if err == nil {
		t.Error("Expected error for permission denied, got nil")
	}
}

func TestManager_RunHook_WithEnv(t *testing.T) {
	tmpDir := t.TempDir()

	nexusDir := filepath.Join(tmpDir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		t.Fatalf("Failed to create .nexus dir: %v", err)
	}

	config := LifecycleConfig{
		Version: "1",
		Hooks: Hooks{
			PreStart: []Hook{
				{
					Name:    "env-test",
					Command: "env",
					Env:     map[string]string{"TEST_VAR": "test_value"},
				},
			},
		},
	}

	configData, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	configPath := filepath.Join(nexusDir, "lifecycle.json")
	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	err = m.RunPreStart()
	if err != nil {
		t.Errorf("RunPreStart() error = %v", err)
	}
}

func TestManager_RunHook_WithTimeout(t *testing.T) {
	tmpDir := t.TempDir()

	sleepScript := filepath.Join(tmpDir, "sleep.sh")
	if err := os.WriteFile(sleepScript, []byte("#!/bin/bash\nsleep 10"), 0755); err != nil {
		t.Fatalf("Failed to create script: %v", err)
	}

	nexusDir := filepath.Join(tmpDir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		t.Fatalf("Failed to create .nexus dir: %v", err)
	}

	config := LifecycleConfig{
		Version: "1",
		Hooks: Hooks{
			PreStart: []Hook{
				{
					Name:    "timeout-test",
					Command: sleepScript,
					Timeout: 1,
				},
			},
		},
	}

	configData, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	configPath := filepath.Join(nexusDir, "lifecycle.json")
	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	err = m.RunPreStart()
	if err == nil {
		t.Error("Expected error for timeout, got nil")
	}
}

func TestManager_RunMultipleHooks(t *testing.T) {
	tmpDir := t.TempDir()

	nexusDir := filepath.Join(tmpDir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		t.Fatalf("Failed to create .nexus dir: %v", err)
	}

	config := LifecycleConfig{
		Version: "1",
		Hooks: Hooks{
			PreStart: []Hook{
				{Name: "hook1", Command: "echo", Args: []string{"first"}},
				{Name: "hook2", Command: "echo", Args: []string{"second"}},
			},
		},
	}

	configData, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	configPath := filepath.Join(nexusDir, "lifecycle.json")
	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	err = m.RunPreStart()
	if err != nil {
		t.Errorf("RunPreStart() error = %v", err)
	}
}

func TestManager_RunHook_FailsOnFirstError(t *testing.T) {
	tmpDir := t.TempDir()

	nexusDir := filepath.Join(tmpDir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		t.Fatalf("Failed to create .nexus dir: %v", err)
	}

	config := LifecycleConfig{
		Version: "1",
		Hooks: Hooks{
			PreStart: []Hook{
				{Name: "failing-hook", Command: "false"},
				{Name: "should-not-run", Command: "echo", Args: []string{"this should not run"}},
			},
		},
	}

	configData, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("Failed to marshal config: %v", err)
	}

	configPath := filepath.Join(nexusDir, "lifecycle.json")
	if err := os.WriteFile(configPath, configData, 0644); err != nil {
		t.Fatalf("Failed to write config: %v", err)
	}

	m, err := NewManager(tmpDir)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	err = m.RunPreStart()
	if err == nil {
		t.Error("Expected error when first hook fails, got nil")
	}
}
