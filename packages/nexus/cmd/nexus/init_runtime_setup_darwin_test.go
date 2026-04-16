//go:build darwin

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinBootstrapReturnsInstallInstructionsWhenLimactlMissing(t *testing.T) {
	originalLookPath := limactlLookPathFn
	t.Cleanup(func() { limactlLookPathFn = originalLookPath })

	limactlLookPathFn = func(name string) (string, error) {
		if name == "limactl" {
			return "", &notFoundError{name: name}
		}
		if name == "brew" {
			return "/usr/local/bin/brew", nil
		}
		return "", &notFoundError{name: name}
	}

	brewCalled := false
	originalRun := limactlRunFn
	limactlRunFn = func(name string, args ...string) error {
		if name == "brew" && len(args) > 0 && args[0] == "install" && args[1] == "lima" {
			brewCalled = true
		}
		return nil
	}
	t.Cleanup(func() { limactlRunFn = originalRun })

	err := runInitRuntimeBootstrapDarwin(t.TempDir(), "firecracker")
	if err == nil {
		t.Fatal("expected missing limactl error")
	}
	if !strings.Contains(err.Error(), "brew install lima") {
		t.Fatalf("expected brew instruction, got: %v", err)
	}
	if !brewCalled {
		t.Fatal("expected brew install lima to be called")
	}
}

func TestDarwinBootstrapIsNoOpForNonFirecrackerRuntime(t *testing.T) {
	err := runInitRuntimeBootstrapDarwin(t.TempDir(), "process")
	if err != nil {
		t.Fatalf("expected no error for process runtime, got: %v", err)
	}
}

func TestEnsurePersistentLimaInstanceSkipsStartWhenInstanceExists(t *testing.T) {
	originalOutput := limactlOutputFn
	originalRun := limactlRunFn
	t.Cleanup(func() {
		limactlOutputFn = originalOutput
		limactlRunFn = originalRun
	})

	limactlOutputFn = func(name string, args ...string) ([]byte, error) {
		return []byte(`[{"name":"nexus-firecracker"}]`), nil
	}

	runCalled := false
	limactlRunFn = func(name string, args ...string) error {
		runCalled = true
		return nil
	}

	if err := ensurePersistentLimaInstance("nexus-firecracker", "/tmp/firecracker.yaml"); err != nil {
		t.Fatalf("expected no error when instance already exists, got: %v", err)
	}
	if runCalled {
		t.Fatal("expected limactl start not to be called when instance already exists")
	}
}

func TestEnsurePersistentLimaInstanceStartsWhenMissing(t *testing.T) {
	originalOutput := limactlOutputFn
	originalRun := limactlRunFn
	t.Cleanup(func() {
		limactlOutputFn = originalOutput
		limactlRunFn = originalRun
	})

	limactlOutputFn = func(name string, args ...string) ([]byte, error) {
		return []byte("[]"), nil
	}

	runCalled := false
	limactlRunFn = func(name string, args ...string) error {
		runCalled = true
		if name != "limactl" {
			t.Fatalf("expected limactl command, got %q", name)
		}
		if len(args) < 4 || args[0] != "start" || args[1] != "--name" || args[2] != "nexus-firecracker" || args[3] != "/tmp/firecracker.yaml" {
			t.Fatalf("unexpected limactl args: %v", args)
		}
		return nil
	}

	if err := ensurePersistentLimaInstance("nexus-firecracker", "/tmp/firecracker.yaml"); err != nil {
		t.Fatalf("expected no error when starting missing instance, got: %v", err)
	}
	if !runCalled {
		t.Fatal("expected limactl start to be called when instance is missing")
	}
}

func TestBootstrapFirecrackerExecContextDarwinFailsWhenWorkspaceNotReady(t *testing.T) {
	originalLookPath := limactlLookPathFn
	originalRun := limactlRunFn
	originalCheck := runLimaCheckCommandFn
	t.Cleanup(func() {
		limactlLookPathFn = originalLookPath
		limactlRunFn = originalRun
		runLimaCheckCommandFn = originalCheck
		doctorLimaInstanceName = ""
	})

	limactlLookPathFn = func(name string) (string, error) {
		if name == "limactl" {
			return "/usr/local/bin/limactl", nil
		}
		return "", &notFoundError{name: name}
	}

	limactlRunFn = func(name string, args ...string) error {
		return nil
	}

	runLimaCheckCommandFn = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
		return "", &notFoundError{name: "workspace"}
	}

	err := bootstrapFirecrackerExecContextDarwin(t.TempDir(), doctorExecContext{backend: "firecracker"})
	if err == nil {
		t.Fatal("expected workspace readiness failure")
	}
	if !strings.Contains(err.Error(), "workspace readiness") {
		t.Fatalf("expected workspace readiness error, got: %v", err)
	}
}

func TestDarwinBootstrapReturnsErrorWhenLimaStartFails(t *testing.T) {
	originalLookPath := limactlLookPathFn
	originalRun := limactlRunFn
	originalOutput := limactlOutputFn
	t.Cleanup(func() {
		limactlLookPathFn = originalLookPath
		limactlRunFn = originalRun
		limactlOutputFn = originalOutput
	})

	projectRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectRoot, ".nexus"), 0o755); err != nil {
		t.Fatalf("create .nexus: %v", err)
	}

	limactlLookPathFn = func(name string) (string, error) {
		if name == "limactl" || name == "brew" {
			return "/opt/homebrew/bin/" + name, nil
		}
		return "", &notFoundError{name: name}
	}

	limactlRunFn = func(name string, args ...string) error {
		if name == "limactl" && len(args) > 0 && args[0] == "start" {
			return &notFoundError{name: "nested virtualization unsupported"}
		}
		return nil
	}

	limactlOutputFn = func(name string, args ...string) ([]byte, error) {
		return []byte("[]"), nil
	}

	err := runInitRuntimeBootstrapDarwin(projectRoot, "firecracker")
	if err == nil {
		t.Fatal("expected bootstrap error when limactl start fails")
	}
	if !strings.Contains(err.Error(), "firecracker runtime setup failed on darwin") {
		t.Fatalf("expected wrapped firecracker setup error, got: %v", err)
	}

	envPath := filepath.Join(projectRoot, ".nexus", "run", "nexus-init-env")
	if _, statErr := os.Stat(envPath); !os.IsNotExist(statErr) {
		t.Fatalf("did not expect nexus-init-env to be written on bootstrap failure")
	}
}

type notFoundError struct {
	name string
}

func (e *notFoundError) Error() string {
	return e.name + " not found"
}

func (e *notFoundError) Unwrap() error {
	return nil
}

func TestPatchLimaTemplateUID(t *testing.T) {
	dir := t.TempDir()
	templatePath := filepath.Join(dir, "lima.yaml")
	if err := os.WriteFile(templatePath, []byte("vmType: vz\n"), 0644); err != nil {
		t.Fatalf("write template: %v", err)
	}

	if err := patchLimaTemplateUID(templatePath, 501); err != nil {
		t.Fatalf("patchLimaTemplateUID: %v", err)
	}

	content, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	if !strings.Contains(string(content), "uid: 501") {
		t.Fatalf("expected uid: 501 in template, got:\n%s", content)
	}
}
