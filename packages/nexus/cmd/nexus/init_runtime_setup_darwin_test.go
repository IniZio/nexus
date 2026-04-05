//go:build darwin

package main

import (
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

type notFoundError struct {
	name string
}

func (e *notFoundError) Error() string {
	return e.name + " not found"
}

func (e *notFoundError) Unwrap() error {
	return nil
}
