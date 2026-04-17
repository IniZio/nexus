package selection

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/config"
)

func TestRuntimeSetupRunner_FailsFastInNonInteractiveWithoutPasswordlessSudo(t *testing.T) {
	originalGOOS := runtimeSetupGOOS
	originalIsRoot := runtimeSetupIsRootFn
	originalSudoN := runtimeSetupSudoNOKFn
	originalIsTTY := runtimeSetupIsTTYFn
	originalResolveBinary := runtimeSetupResolveBinaryFn
	originalRunCommand := runtimeSetupRunCommandFn
	t.Cleanup(func() {
		runtimeSetupGOOS = originalGOOS
		runtimeSetupIsRootFn = originalIsRoot
		runtimeSetupSudoNOKFn = originalSudoN
		runtimeSetupIsTTYFn = originalIsTTY
		runtimeSetupResolveBinaryFn = originalResolveBinary
		runtimeSetupRunCommandFn = originalRunCommand
	})

	runtimeSetupGOOS = "linux"
	runtimeSetupIsRootFn = func() bool { return false }
	runtimeSetupSudoNOKFn = func() bool { return false }
	runtimeSetupIsTTYFn = func(*os.File) bool { return false }

	resolveCalls := 0
	runtimeSetupResolveBinaryFn = func() (string, error) {
		resolveCalls++
		return "/tmp/nexus", nil
	}

	runCalls := 0
	runtimeSetupRunCommandFn = func(context.Context, string, ...string) ([]byte, error) {
		runCalls++
		return nil, nil
	}

	err := runtimeSetupRunner(context.Background(), "/tmp/repo", "firecracker")
	if err == nil {
		t.Fatal("expected fail-fast error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "non-interactive") {
		t.Fatalf("expected non-interactive fast-fail message, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "manual next steps") {
		t.Fatalf("expected manual next steps in error, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "sudo -E nexus init /tmp/repo") {
		t.Fatalf("expected sudo manual command in error, got %q", err.Error())
	}
	if resolveCalls != 0 {
		t.Fatalf("expected no binary resolution on fast-fail path, got %d", resolveCalls)
	}
	if runCalls != 0 {
		t.Fatalf("expected no command execution on fast-fail path, got %d", runCalls)
	}
}

func TestRuntimeSetupRunner_InteractiveSessionAttemptsSetup(t *testing.T) {
	originalGOOS := runtimeSetupGOOS
	originalIsRoot := runtimeSetupIsRootFn
	originalSudoN := runtimeSetupSudoNOKFn
	originalIsTTY := runtimeSetupIsTTYFn
	originalResolveBinary := runtimeSetupResolveBinaryFn
	originalRunCommand := runtimeSetupRunCommandFn
	t.Cleanup(func() {
		runtimeSetupGOOS = originalGOOS
		runtimeSetupIsRootFn = originalIsRoot
		runtimeSetupSudoNOKFn = originalSudoN
		runtimeSetupIsTTYFn = originalIsTTY
		runtimeSetupResolveBinaryFn = originalResolveBinary
		runtimeSetupRunCommandFn = originalRunCommand
	})

	runtimeSetupGOOS = "linux"
	runtimeSetupIsRootFn = func() bool { return false }
	runtimeSetupSudoNOKFn = func() bool { return false }
	runtimeSetupIsTTYFn = func(*os.File) bool { return true }

	runtimeSetupResolveBinaryFn = func() (string, error) {
		return "/tmp/nexus", nil
	}

	runCalls := 0
	runtimeSetupRunCommandFn = func(context.Context, string, ...string) ([]byte, error) {
		runCalls++
		return nil, nil
	}

	err := runtimeSetupRunner(context.Background(), "/tmp/repo", "firecracker")
	if err != nil {
		t.Fatalf("expected setup attempt to proceed in interactive session, got %v", err)
	}
	if runCalls != 1 {
		t.Fatalf("expected one setup command run, got %d", runCalls)
	}
}

func TestSelectBackend_DarwinDefaultsToProcessWithoutNestedVirt(t *testing.T) {
	original := darwinHasNestedVirtFn
	t.Cleanup(func() {
		darwinHasNestedVirtFn = original
	})
	darwinHasNestedVirtFn = func() bool { return false }

	backend, mode, err := SelectBackend("darwin", nil)
	if err != nil {
		t.Fatalf("unexpected selection error: %v", err)
	}
	if backend != "process" || mode != "process" {
		t.Fatalf("expected process/process, got %s/%s", backend, mode)
	}
}

func TestSelectBackend_DarwinRejectsExplicitVMWithoutNestedVirt(t *testing.T) {
	original := darwinHasNestedVirtFn
	t.Cleanup(func() {
		darwinHasNestedVirtFn = original
	})
	darwinHasNestedVirtFn = func() bool { return false }

	_, _, err := SelectBackend("darwin", &config.WorkspaceConfig{
		Isolation: config.WorkspaceIsolation{Level: "vm"},
	})
	if err == nil {
		t.Fatal("expected explicit vm selection to fail")
	}
	if !strings.Contains(err.Error(), "vm isolation requires nested virtualization") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSelectBackend_DarwinUsesDedicatedLimaWithNestedVirt(t *testing.T) {
	original := darwinHasNestedVirtFn
	t.Cleanup(func() {
		darwinHasNestedVirtFn = original
	})
	darwinHasNestedVirtFn = func() bool { return true }

	backend, mode, err := SelectBackend("darwin", nil)
	if err != nil {
		t.Fatalf("unexpected selection error: %v", err)
	}
	if backend != "lima" || mode != "dedicated" {
		t.Fatalf("expected lima/dedicated, got %s/%s", backend, mode)
	}
}
