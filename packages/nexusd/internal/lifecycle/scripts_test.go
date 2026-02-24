package lifecycle

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewLifecycleScripts(t *testing.T) {
	ls := NewLifecycleScripts("/tmp/test")
	if ls.ProjectPath != "/tmp/test" {
		t.Errorf("ProjectPath = %q, want %q", ls.ProjectPath, "/tmp/test")
	}
}

func TestLifecycleScripts_lifecycleDir(t *testing.T) {
	ls := NewLifecycleScripts("/tmp/test")
	dir := ls.lifecycleDir()
	expected := filepath.Join("/tmp/test", ".nexus", "lifecycle")
	if dir != expected {
		t.Errorf("lifecycleDir() = %q, want %q", dir, expected)
	}
}

func TestLifecycleScripts_scriptPath(t *testing.T) {
	ls := NewLifecycleScripts("/tmp/test")
	path := ls.scriptPath("pre-start.sh")
	expected := filepath.Join("/tmp/test", ".nexus", "lifecycle", "pre-start.sh")
	if path != expected {
		t.Errorf("scriptPath() = %q, want %q", path, expected)
	}
}

func TestLifecycleScripts_scriptExists_NotFound(t *testing.T) {
	ls := NewLifecycleScripts("/tmp/nonexistent")
	if ls.scriptExists("pre-start.sh") {
		t.Error("Expected scriptExists to return false for nonexistent path")
	}
}

func TestLifecycleScripts_scriptExists_IsDir(t *testing.T) {
	tmpDir := t.TempDir()
	lifecycleDir := filepath.Join(tmpDir, ".nexus", "lifecycle")
	if err := os.MkdirAll(lifecycleDir, 0755); err != nil {
		t.Fatalf("Failed to create lifecycle dir: %v", err)
	}

	ls := NewLifecycleScripts(tmpDir)
	if ls.scriptExists("pre-start.sh") {
		t.Error("Expected scriptExists to return false for directory")
	}
}

func TestLifecycleScripts_scriptExists_IsFile(t *testing.T) {
	tmpDir := t.TempDir()
	lifecycleDir := filepath.Join(tmpDir, ".nexus", "lifecycle")
	if err := os.MkdirAll(lifecycleDir, 0755); err != nil {
		t.Fatalf("Failed to create lifecycle dir: %v", err)
	}

	scriptPath := filepath.Join(lifecycleDir, "pre-start.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho hello"), 0755); err != nil {
		t.Fatalf("Failed to create script: %v", err)
	}

	ls := NewLifecycleScripts(tmpDir)
	if !ls.scriptExists("pre-start.sh") {
		t.Error("Expected scriptExists to return true for existing file")
	}
}

func TestLifecycleScripts_RunPreStart_NoScript(t *testing.T) {
	tmpDir := t.TempDir()
	ls := NewLifecycleScripts(tmpDir)

	err := ls.RunPreStart()
	if err != nil {
		t.Errorf("RunPreStart() error = %v", err)
	}
}

func TestLifecycleScripts_RunPostStart_NoScript(t *testing.T) {
	tmpDir := t.TempDir()
	ls := NewLifecycleScripts(tmpDir)

	err := ls.RunPostStart()
	if err != nil {
		t.Errorf("RunPostStart() error = %v", err)
	}
}

func TestLifecycleScripts_RunPreStop_NoScript(t *testing.T) {
	tmpDir := t.TempDir()
	ls := NewLifecycleScripts(tmpDir)

	err := ls.RunPreStop()
	if err != nil {
		t.Errorf("RunPreStop() error = %v", err)
	}
}

func TestLifecycleScripts_RunPostStop_NoScript(t *testing.T) {
	tmpDir := t.TempDir()
	ls := NewLifecycleScripts(tmpDir)

	err := ls.RunPostStop()
	if err != nil {
		t.Errorf("RunPostStop() error = %v", err)
	}
}

func TestLifecycleScripts_RunPreStart_Success(t *testing.T) {
	tmpDir := t.TempDir()
	lifecycleDir := filepath.Join(tmpDir, ".nexus", "lifecycle")
	if err := os.MkdirAll(lifecycleDir, 0755); err != nil {
		t.Fatalf("Failed to create lifecycle dir: %v", err)
	}

	scriptPath := filepath.Join(lifecycleDir, "pre-start.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\nexit 0"), 0755); err != nil {
		t.Fatalf("Failed to create script: %v", err)
	}

	ls := NewLifecycleScripts(tmpDir)
	err := ls.RunPreStart()
	if err != nil {
		t.Errorf("RunPreStart() error = %v", err)
	}
}

func TestLifecycleScripts_RunPreStart_Failure(t *testing.T) {
	tmpDir := t.TempDir()
	lifecycleDir := filepath.Join(tmpDir, ".nexus", "lifecycle")
	if err := os.MkdirAll(lifecycleDir, 0755); err != nil {
		t.Fatalf("Failed to create lifecycle dir: %v", err)
	}

	scriptPath := filepath.Join(lifecycleDir, "pre-start.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\nexit 1"), 0755); err != nil {
		t.Fatalf("Failed to create script: %v", err)
	}

	ls := NewLifecycleScripts(tmpDir)
	err := ls.RunPreStart()
	if err == nil {
		t.Error("Expected error for failing script, got nil")
	}
}

func TestLifecycleScripts_RunPreStart_NotFound(t *testing.T) {
	tmpDir := t.TempDir()
	ls := NewLifecycleScripts(tmpDir)

	err := ls.RunPreStart()
	if err != nil {
		t.Errorf("RunPreStart() error = %v", err)
	}
}

func TestLifecycleScripts_RunPreStart_PermissionDenied(t *testing.T) {
	tmpDir := t.TempDir()
	lifecycleDir := filepath.Join(tmpDir, ".nexus", "lifecycle")
	if err := os.MkdirAll(lifecycleDir, 0755); err != nil {
		t.Fatalf("Failed to create lifecycle dir: %v", err)
	}

	scriptPath := filepath.Join(lifecycleDir, "pre-start.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\nexit 0"), 0000); err != nil {
		t.Fatalf("Failed to create script: %v", err)
	}

	ls := NewLifecycleScripts(tmpDir)
	err := ls.RunPreStart()
	if err == nil {
		t.Error("Expected error for permission denied, got nil")
	}
}

func TestLifecycleScripts_RunHealthCheck_NoScript(t *testing.T) {
	tmpDir := t.TempDir()
	ls := NewLifecycleScripts(tmpDir)

	ok, err := ls.RunHealthCheck()
	if err != nil {
		t.Errorf("RunHealthCheck() error = %v", err)
	}
	if ok {
		t.Error("Expected ok = false when no health check script exists")
	}
}

func TestLifecycleScripts_RunHealthCheck_Success(t *testing.T) {
	tmpDir := t.TempDir()
	lifecycleDir := filepath.Join(tmpDir, ".nexus", "lifecycle")
	if err := os.MkdirAll(lifecycleDir, 0755); err != nil {
		t.Fatalf("Failed to create lifecycle dir: %v", err)
	}

	scriptPath := filepath.Join(lifecycleDir, "health-check.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\nexit 0"), 0755); err != nil {
		t.Fatalf("Failed to create script: %v", err)
	}

	ls := NewLifecycleScripts(tmpDir)
	ok, err := ls.RunHealthCheck()
	if err != nil {
		t.Errorf("RunHealthCheck() error = %v", err)
	}
	if !ok {
		t.Error("Expected ok = true for successful health check")
	}
}

func TestLifecycleScripts_RunHealthCheck_Failure(t *testing.T) {
	tmpDir := t.TempDir()
	lifecycleDir := filepath.Join(tmpDir, ".nexus", "lifecycle")
	if err := os.MkdirAll(lifecycleDir, 0755); err != nil {
		t.Fatalf("Failed to create lifecycle dir: %v", err)
	}

	scriptPath := filepath.Join(lifecycleDir, "health-check.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\nexit 1"), 0755); err != nil {
		t.Fatalf("Failed to create script: %v", err)
	}

	ls := NewLifecycleScripts(tmpDir)
	_, err := ls.RunHealthCheck()
	if err == nil {
		t.Error("Expected error for failing health check, got nil")
	}
}

func TestLifecycleScripts_RunHealthCheck_Timeout(t *testing.T) {
	tmpDir := t.TempDir()
	lifecycleDir := filepath.Join(tmpDir, ".nexus", "lifecycle")
	if err := os.MkdirAll(lifecycleDir, 0755); err != nil {
		t.Fatalf("Failed to create lifecycle dir: %v", err)
	}

	scriptPath := filepath.Join(lifecycleDir, "health-check.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\nsleep 30"), 0755); err != nil {
		t.Fatalf("Failed to create script: %v", err)
	}

	ls := NewLifecycleScripts(tmpDir)
	_, err := ls.RunHealthCheck()
	if err == nil {
		t.Error("Expected error for timeout, got nil")
	}
}

func TestLifecycleScripts_HasLifecycleScripts_NoDir(t *testing.T) {
	tmpDir := t.TempDir()
	ls := NewLifecycleScripts(tmpDir)

	if ls.HasLifecycleScripts() {
		t.Error("Expected false when lifecycle dir does not exist")
	}
}

func TestLifecycleScripts_HasLifecycleScripts_DirExists(t *testing.T) {
	tmpDir := t.TempDir()
	lifecycleDir := filepath.Join(tmpDir, ".nexus", "lifecycle")
	if err := os.MkdirAll(lifecycleDir, 0755); err != nil {
		t.Fatalf("Failed to create lifecycle dir: %v", err)
	}

	ls := NewLifecycleScripts(tmpDir)
	if !ls.HasLifecycleScripts() {
		t.Error("Expected true when lifecycle dir exists")
	}
}

func TestLifecycleScripts_AllLifecycleScripts(t *testing.T) {
	tmpDir := t.TempDir()
	lifecycleDir := filepath.Join(tmpDir, ".nexus", "lifecycle")
	if err := os.MkdirAll(lifecycleDir, 0755); err != nil {
		t.Fatalf("Failed to create lifecycle dir: %v", err)
	}

	scripts := []string{"pre-start.sh", "post-start.sh", "pre-stop.sh", "post-stop.sh"}
	for _, script := range scripts {
		scriptPath := filepath.Join(lifecycleDir, script)
		if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\nexit 0"), 0755); err != nil {
			t.Fatalf("Failed to create script %s: %v", script, err)
		}
	}

	ls := NewLifecycleScripts(tmpDir)

	if err := ls.RunPreStart(); err != nil {
		t.Errorf("RunPreStart() error = %v", err)
	}
	if err := ls.RunPostStart(); err != nil {
		t.Errorf("RunPostStart() error = %v", err)
	}
	if err := ls.RunPreStop(); err != nil {
		t.Errorf("RunPreStop() error = %v", err)
	}
	if err := ls.RunPostStop(); err != nil {
		t.Errorf("RunPostStop() error = %v", err)
	}
}
