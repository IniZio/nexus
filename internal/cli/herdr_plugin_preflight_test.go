package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func TestHerdrPluginCreate_MissingKernel_ErrorNamesInstall(t *testing.T) {
	t.Setenv("NEXUS_KERNEL_PATH", "/nonexistent/vmlinux")

	err := herdrPluginCreate(context.Background(), strings.NewReader(""), &bytes.Buffer{}, nil, t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing kernel, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "nexus kernel install") && !strings.Contains(msg, "NEXUS_KERNEL_PATH") {
		t.Errorf("error should mention kernel install or NEXUS_KERNEL_PATH, got: %s", msg)
	}
}

func TestHerdrPluginCreate_BaseImageMissingUnreachable_ErrorNamesRemediation(t *testing.T) {
	f := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(f, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS_KERNEL_PATH", f)

	orig1 := herdrBaseImageListFn
	orig2 := herdrBaseImageRegistryReachableFn
	t.Cleanup(func() {
		herdrBaseImageListFn = orig1
		herdrBaseImageRegistryReachableFn = orig2
	})

	herdrBaseImageListFn = func(_ context.Context) ([]domain.Image, error) { return nil, nil }
	herdrBaseImageRegistryReachableFn = func(_ string) error { return errors.New("connection refused") }

	err := herdrPluginCreate(context.Background(), strings.NewReader(""), &bytes.Buffer{}, nil, t.TempDir())
	if err == nil {
		t.Fatal("expected error for unreachable registry, got nil")
	}
	if !strings.Contains(err.Error(), herdrDefaultImage) {
		t.Errorf("error should mention the base image ref %q, got: %s", herdrDefaultImage, err.Error())
	}
}

func TestHerdrPluginCreate_BaseImageMissingReachable_ProceedsToCreate(t *testing.T) {
	f := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(f, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS_KERNEL_PATH", f)

	orig1 := herdrBaseImageListFn
	orig2 := herdrBaseImageRegistryReachableFn
	orig3 := herdrExecCommandContext
	t.Cleanup(func() {
		herdrBaseImageListFn = orig1
		herdrBaseImageRegistryReachableFn = orig2
		herdrExecCommandContext = orig3
	})

	herdrBaseImageListFn = func(_ context.Context) ([]domain.Image, error) { return nil, nil }
	herdrBaseImageRegistryReachableFn = func(_ string) error { return nil }
	herdrExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "true")
	}

	err := herdrPluginCreate(context.Background(), strings.NewReader(""), &bytes.Buffer{}, nil, t.TempDir())
	if err != nil && strings.Contains(err.Error(), "registry unreachable") {
		t.Errorf("base image check should not fail when registry is reachable, got: %s", err.Error())
	}
}

func TestHerdrPluginDoctor_PrintsSubstrateChecks(t *testing.T) {
	var buf bytes.Buffer
	if err := herdrPluginDoctor(&buf); err != nil {
		t.Fatalf("herdrPluginDoctor returned error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Capability checks:") {
		t.Errorf("output should contain 'Capability checks:', got:\n%s", out)
	}
	if !strings.Contains(out, "platform") {
		t.Errorf("output should contain 'platform' check, got:\n%s", out)
	}
}
