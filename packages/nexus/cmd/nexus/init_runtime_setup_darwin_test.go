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
	err := runInitRuntimeBootstrapDarwin(t.TempDir(), "local")
	if err != nil {
		t.Fatalf("expected no error for local runtime, got: %v", err)
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

func TestDarwinBootstrapWritesFirecrackerEnvWhenPrimaryStartSucceeds(t *testing.T) {
	originalLookPath := limactlLookPathFn
	originalOutput := limactlOutputFn
	originalRun := limactlRunFn
	t.Cleanup(func() {
		limactlLookPathFn = originalLookPath
		limactlOutputFn = originalOutput
		limactlRunFn = originalRun
	})

	limactlLookPathFn = func(name string) (string, error) {
		if name == "limactl" {
			return "/usr/local/bin/limactl", nil
		}
		return "", &notFoundError{name: name}
	}

	limactlOutputFn = func(name string, args ...string) ([]byte, error) {
		return []byte("[]"), nil
	}

	runCalls := 0
	limactlRunFn = func(name string, args ...string) error {
		runCalls++
		if name != "limactl" {
			t.Fatalf("expected limactl command, got %q", name)
		}
		if len(args) >= 1 && args[0] == "delete" {
			t.Fatalf("did not expect fallback delete call on primary success")
		}
		return nil
	}

	root := t.TempDir()
	if err := runInitRuntimeBootstrapDarwin(root, "firecracker"); err != nil {
		t.Fatalf("expected successful bootstrap, got: %v", err)
	}

	if runCalls == 0 {
		t.Fatal("expected limactl to be invoked")
	}

	envPath := filepath.Join(root, ".nexus", "run", "nexus-init-env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("expected nexus-init-env file, got: %v", err)
	}
	if !strings.Contains(string(data), "NEXUS_RUNTIME_BACKEND=firecracker") {
		t.Fatalf("expected firecracker backend env, got:\n%s", string(data))
	}
}

func TestDarwinBootstrapFallsBackToLXCWhenPrimaryStartFails(t *testing.T) {
	originalLookPath := limactlLookPathFn
	originalOutput := limactlOutputFn
	originalRun := limactlRunFn
	t.Cleanup(func() {
		limactlLookPathFn = originalLookPath
		limactlOutputFn = originalOutput
		limactlRunFn = originalRun
	})

	limactlLookPathFn = func(name string) (string, error) {
		if name == "limactl" {
			return "/usr/local/bin/limactl", nil
		}
		return "", &notFoundError{name: name}
	}

	limactlOutputFn = func(name string, args ...string) ([]byte, error) {
		return []byte("[]"), nil
	}

	startCalls := 0
	deleteCalled := false
	limactlRunFn = func(name string, args ...string) error {
		if name != "limactl" {
			t.Fatalf("expected limactl command, got %q", name)
		}
		if len(args) == 0 {
			t.Fatalf("expected limactl args")
		}
		switch args[0] {
		case "start":
			startCalls++
			if startCalls == 1 {
				return &notFoundError{name: "nested virtualization unsupported"}
			}
			return nil
		case "delete":
			deleteCalled = true
			return nil
		default:
			return nil
		}
	}

	root := t.TempDir()
	if err := runInitRuntimeBootstrapDarwin(root, "firecracker"); err != nil {
		t.Fatalf("expected fallback bootstrap success, got: %v", err)
	}

	if startCalls != 2 {
		t.Fatalf("expected two limactl start calls (primary + fallback), got %d", startCalls)
	}
	if !deleteCalled {
		t.Fatal("expected limactl delete call before lxc fallback")
	}

	envPath := filepath.Join(root, ".nexus", "run", "nexus-init-env")
	data, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("expected nexus-init-env file, got: %v", err)
	}
	if !strings.Contains(string(data), "NEXUS_RUNTIME_BACKEND=lxc") {
		t.Fatalf("expected lxc backend env after fallback, got:\n%s", string(data))
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

type notFoundError struct {
	name string
}

func (e *notFoundError) Error() string {
	return e.name + " not found"
}

func (e *notFoundError) Unwrap() error {
	return nil
}
