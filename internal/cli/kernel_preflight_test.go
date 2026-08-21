package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/service"
)

// ── resolveKernelPath unit tests ─────────────────────────────────────────────

func TestResolveKernelPath_EnvSet_FileExists(t *testing.T) {
	f := filepath.Join(t.TempDir(), "vmlinux")
	if err := os.WriteFile(f, []byte("fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS3_KERNEL_PATH", f)

	got, err := resolveKernelPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != f {
		t.Errorf("got %q, want %q", got, f)
	}
}

func TestResolveKernelPath_EnvSet_FileMissing(t *testing.T) {
	t.Setenv("NEXUS3_KERNEL_PATH", "/nonexistent/vmlinux")

	_, err := resolveKernelPath()
	if err == nil {
		t.Fatal("expected error for missing kernel, got nil")
	}
	if !strings.Contains(err.Error(), "NEXUS3_KERNEL_PATH") {
		t.Errorf("error should mention NEXUS3_KERNEL_PATH: %v", err)
	}
}

func TestResolveKernelPath_EnvUnset_NoCandidates(t *testing.T) {
	// Clear NEXUS3_KERNEL_PATH and ensure neither binary-relative nor CWD
	// candidates exist. The function must return an error naming NEXUS3_KERNEL_PATH.
	t.Setenv("NEXUS3_KERNEL_PATH", "")

	_, err := resolveKernelPath()
	if err == nil {
		// Kernel found at binary-relative or CWD path in this environment;
		// skip rather than fail — the path legitimately exists in a dev checkout.
		t.Skip("kernel found at default path; skipping missing-kernel test")
	}
	if !strings.Contains(err.Error(), "NEXUS3_KERNEL_PATH") {
		t.Errorf("error should mention NEXUS3_KERNEL_PATH: %v", err)
	}
}

// ── MCP path preflight ordering ──────────────────────────────────────────────

// TestMCPCreateAndBoot_KernelPreflight_RejectsBeforeExpensiveWork verifies
// that mcpService.CreateAndBoot returns a kernel-path error immediately when
// NEXUS3_KERNEL_PATH points at a non-existent file, not an image-cache or
// CreateAndBoot error.
func TestMCPCreateAndBoot_KernelPreflight_RejectsBeforeExpensiveWork(t *testing.T) {
	t.Setenv("NEXUS3_KERNEL_PATH", "/nonexistent-for-test/vmlinux")

	svc := newTestService(t)
	msvc := &mcpService{Service: svc, cacheRoot: t.TempDir()}

	ctx := context.Background()
	_, err := msvc.CreateAndBoot(ctx, "proj", "box", service.CreateAndBootOptions{})
	if err == nil {
		t.Fatal("expected kernel-preflight error, got nil")
	}
	if !strings.Contains(err.Error(), "NEXUS3_KERNEL_PATH") {
		t.Errorf("error should mention NEXUS3_KERNEL_PATH; got: %v", err)
	}
	// Must NOT mention "image cache" — that would mean the preflight came after
	// the cache open.
	if strings.Contains(err.Error(), "image cache") {
		t.Errorf("kernel preflight fired too late (after image cache): %v", err)
	}
}

// ── herdrPluginCreate preflight ordering ─────────────────────────────────────

// TestHerdrPluginCreate_KernelPreflight_RejectsBeforePrompt verifies that
// herdrPluginCreate returns a kernel-path error before reading from the reader
// (i.e. before any interactive prompting).
func TestHerdrPluginCreate_KernelPreflight_RejectsBeforePrompt(t *testing.T) {
	t.Setenv("NEXUS3_KERNEL_PATH", "/nonexistent-for-test/vmlinux")

	svc := newTestService(t)
	// Use a pipe: if the function reads from r before the preflight fires it
	// will block (write end never written). The test would then deadlock rather
	// than returning an error — which is the observable failure mode.
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pw.Close()
	defer pr.Close()

	ctx := context.Background()
	ferr := herdrPluginCreate(ctx, pr, io.Discard, svc, t.TempDir())
	if ferr == nil {
		t.Fatal("expected kernel-preflight error, got nil")
	}
	if !strings.Contains(ferr.Error(), "NEXUS3_KERNEL_PATH") {
		t.Errorf("error should mention NEXUS3_KERNEL_PATH; got: %v", ferr)
	}
}

// ── herdrPluginLaunch preflight ordering ─────────────────────────────────────

// TestHerdrPluginLaunch_KernelPreflight_RejectsBeforeStoreSetup verifies that
// herdrPluginLaunch returns a kernel-path error before it tries to open the
// store or image cache.
func TestHerdrPluginLaunch_KernelPreflight_RejectsBeforeStoreSetup(t *testing.T) {
	t.Setenv("NEXUS3_KERNEL_PATH", "/nonexistent-for-test/vmlinux")

	ctx := context.Background()
	out, _, _ := capture(false)
	err := herdrPluginLaunch(ctx, "nexus3-base:latest", []string{"echo", "hi"}, false, out)
	if err == nil {
		t.Fatal("expected kernel-preflight error, got nil")
	}
	if !strings.Contains(err.Error(), "NEXUS3_KERNEL_PATH") {
		t.Errorf("error should mention NEXUS3_KERNEL_PATH; got: %v", err)
	}
	// Must NOT mention "store" or "image cache" — that would mean the preflight
	// fired after the store/cache setup.
	if strings.Contains(err.Error(), "resolve store") || strings.Contains(err.Error(), "image cache") {
		t.Errorf("kernel preflight fired too late (after store/cache setup): %v", err)
	}
}

// ── orcaCreate preflight ordering ────────────────────────────────────────────

// TestOrcaCreate_KernelPreflight_RejectsBeforeStoreSetup verifies that
// orcaCreate returns a kernel-path error before it tries to open the store or
// image cache. ORCA_VM_INSTANCE_ID must be set to reach the preflight block
// (the InstanceID guard fires first).
func TestOrcaCreate_KernelPreflight_RejectsBeforeStoreSetup(t *testing.T) {
	t.Setenv("NEXUS3_KERNEL_PATH", "/nonexistent-for-test/vmlinux")
	t.Setenv("ORCA_VM_INSTANCE_ID", "test-instance-b6")

	ctx := context.Background()
	err := orcaCreate(ctx, io.Discard)
	if err == nil {
		t.Fatal("expected kernel-preflight error, got nil")
	}
	if !strings.Contains(err.Error(), "NEXUS3_KERNEL_PATH") {
		t.Errorf("error should mention NEXUS3_KERNEL_PATH; got: %v", err)
	}
	// Must NOT mention "store root" or "image cache" — that would mean the
	// preflight fired after the store/cache setup.
	if strings.Contains(err.Error(), "store root") || strings.Contains(err.Error(), "image cache") {
		t.Errorf("kernel preflight fired too late (after store/cache setup): %v", err)
	}
}

// ── resolveVirtiofsdPath unit tests ──────────────────────────────────────────

func TestResolveVirtiofsdPath_EnvSet_FileExists(t *testing.T) {
	f := filepath.Join(t.TempDir(), "virtiofsd")
	if err := os.WriteFile(f, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEXUS3_VIRTIOFSD_PATH", f)

	got, err := resolveVirtiofsdPath()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != f {
		t.Errorf("got %q, want %q", got, f)
	}
}

func TestResolveVirtiofsdPath_EnvSet_FileMissing(t *testing.T) {
	t.Setenv("NEXUS3_VIRTIOFSD_PATH", "/nonexistent/virtiofsd")

	_, err := resolveVirtiofsdPath()
	if err == nil {
		t.Fatal("expected error for missing virtiofsd, got nil")
	}
	if !strings.Contains(err.Error(), "NEXUS3_VIRTIOFSD_PATH") {
		t.Errorf("error should mention NEXUS3_VIRTIOFSD_PATH: %v", err)
	}
}
