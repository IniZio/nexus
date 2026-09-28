package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAutoFetch_MissingKernel_DownloadsAndInstalls verifies that resolveKernelPathWithClient
// auto-downloads the kernel when none is found locally.
func TestAutoFetch_MissingKernel_DownloadsAndInstalls(t *testing.T) {
	kernelBytes := []byte("pinned-kernel-bytes")
	h := sha256.Sum256(kernelBytes)
	hash := hex.EncodeToString(h[:])

	orig := kernelPins
	kernelPins = fakeKernelPins(hash)
	t.Cleanup(func() { kernelPins = orig })

	xdgDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdgDir)
	t.Setenv("NEXUS_KERNEL_PATH", "")

	fetched := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched = true
		w.Write(kernelBytes) //nolint:errcheck
	}))
	defer srv.Close()
	t.Setenv("NEXUS_RELEASE_BASE_URL", srv.URL)

	// Use a temp cwd with no kernel so cwd-relative search fails.
	t.Chdir(t.TempDir())

	got, err := resolveKernelPathWithClient(context.Background(), srv.Client())
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if !fetched {
		t.Error("expected HTTP fetch but none occurred")
	}

	want := filepath.Join(xdgDir, "nexus", "images", "kernel", "vmlinux-x86_64")
	if got != want {
		t.Errorf("path: got %q, want %q", got, want)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("installed file missing: %v", err)
	}
}

// TestAutoFetch_SHAMismatch_ErrorNothingWritten verifies that a bad download
// leaves no file and returns an error.
func TestAutoFetch_SHAMismatch_ErrorNothingWritten(t *testing.T) {
	wrongSHA := strings.Repeat("b", 64)

	orig := kernelPins
	kernelPins = fakeKernelPins(wrongSHA) // pin says wrong hash
	t.Cleanup(func() { kernelPins = orig })

	xdgDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdgDir)
	t.Setenv("NEXUS_KERNEL_PATH", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("different-bytes")) //nolint:errcheck
	}))
	defer srv.Close()
	t.Setenv("NEXUS_RELEASE_BASE_URL", srv.URL)
	t.Chdir(t.TempDir())

	_, err := resolveKernelPathWithClient(context.Background(), srv.Client())
	if err == nil {
		t.Fatal("expected sha mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Errorf("error should mention sha256 mismatch: %v", err)
	}

	dest := filepath.Join(xdgDir, "nexus", "images", "kernel", "vmlinux-x86_64")
	if _, statErr := os.Stat(dest); statErr == nil {
		t.Error("file should not be written on sha mismatch")
	}
	tmps, _ := filepath.Glob(filepath.Join(filepath.Dir(dest), "vmlinux-*.tmp"))
	if len(tmps) > 0 {
		t.Errorf("temp file(s) not cleaned up: %v", tmps)
	}
}

// TestAutoFetch_Offline_ErrorMentionsInstall verifies offline failure message.
func TestAutoFetch_Offline_ErrorMentionsInstall(t *testing.T) {
	kernelBytes := []byte("kernel")
	h := sha256.Sum256(kernelBytes)
	orig := kernelPins
	kernelPins = fakeKernelPins(hex.EncodeToString(h[:]))
	t.Cleanup(func() { kernelPins = orig })

	xdgDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdgDir)
	t.Setenv("NEXUS_KERNEL_PATH", "")

	// Point to a closed server to simulate offline.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	t.Setenv("NEXUS_RELEASE_BASE_URL", srv.URL)
	t.Chdir(t.TempDir())

	_, err := resolveKernelPathWithClient(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for offline, got nil")
	}
	if !strings.Contains(err.Error(), "nexus kernel install") {
		t.Errorf("error should mention `nexus kernel install`: %v", err)
	}
}

// TestAutoFetch_ExistingKernel_NoNetwork verifies that a locally present kernel
// is returned without any HTTP request.
func TestAutoFetch_ExistingKernel_NoNetwork(t *testing.T) {
	xdgDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", xdgDir)
	t.Setenv("NEXUS_KERNEL_PATH", "")

	want := filepath.Join(xdgDir, "nexus", "images", "kernel", "vmlinux-x86_64")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("existing-kernel"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())

	fetched := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched = true
	}))
	defer srv.Close()
	t.Setenv("NEXUS_RELEASE_BASE_URL", srv.URL)

	got, err := resolveKernelPathWithClient(context.Background(), srv.Client())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if fetched {
		t.Error("HTTP request made despite existing kernel")
	}
}
