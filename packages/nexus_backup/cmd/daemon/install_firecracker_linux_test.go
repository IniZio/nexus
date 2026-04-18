//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaybeInstallFirecracker_WritesEmbeddedBinary(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "firecracker")

	origInstallPath := firecrackerInstallPath
	firecrackerInstallPath = dest
	t.Cleanup(func() { firecrackerInstallPath = origInstallPath })

	origEmbedded := embeddedFirecracker
	embeddedFirecracker = []byte("fake-firecracker-binary")
	t.Cleanup(func() { embeddedFirecracker = origEmbedded })

	if err := maybeInstallFirecracker(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("output file not written: %v", err)
	}
	if string(got) != "fake-firecracker-binary" {
		t.Fatalf("unexpected content: %q", string(got))
	}

	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat failed: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("expected binary to be executable, got %v", info.Mode())
	}
}

func TestMaybeInstallFirecracker_IdempotentWhenAlreadyPresent(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "firecracker")

	origInstallPath := firecrackerInstallPath
	firecrackerInstallPath = dest
	t.Cleanup(func() { firecrackerInstallPath = origInstallPath })

	origEmbedded := embeddedFirecracker
	embeddedFirecracker = []byte("fake-firecracker-binary")
	t.Cleanup(func() { embeddedFirecracker = origEmbedded })

	// Pre-create the file
	if err := os.WriteFile(dest, []byte("existing"), 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if err := maybeInstallFirecracker(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should not have overwritten it
	got, _ := os.ReadFile(dest)
	if string(got) != "existing" {
		t.Fatalf("expected existing file to be preserved, got %q", string(got))
	}
}

func TestMaybeInstallFirecracker_ErrorWhenEmbeddedEmpty(t *testing.T) {
	origEmbedded := embeddedFirecracker
	embeddedFirecracker = nil
	t.Cleanup(func() { embeddedFirecracker = origEmbedded })

	dir := t.TempDir()
	dest := filepath.Join(dir, "firecracker")
	origInstallPath := firecrackerInstallPath
	firecrackerInstallPath = dest
	t.Cleanup(func() { firecrackerInstallPath = origInstallPath })

	err := maybeInstallFirecracker()
	if err == nil {
		t.Fatal("expected error for empty embedded binary, got nil")
	}
}

func TestMaybeInstallFirecracker_ErrorWhenCannotWrite(t *testing.T) {
	origEmbedded := embeddedFirecracker
	embeddedFirecracker = []byte("fake-firecracker-binary")
	t.Cleanup(func() { embeddedFirecracker = origEmbedded })

	// Point to a path where the parent dir doesn't exist and can't be created
	origInstallPath := firecrackerInstallPath
	firecrackerInstallPath = "/nonexistent-dir-for-test/firecracker"
	t.Cleanup(func() { firecrackerInstallPath = origInstallPath })

	err := maybeInstallFirecracker()
	if err == nil {
		t.Fatal("expected error when write fails, got nil")
	}
}

func TestRunServer_FailsIfFirecrackerInstallFails(t *testing.T) {
	origInstallPath := firecrackerInstallPath
	firecrackerInstallPath = "/nonexistent-dir-for-test/firecracker"
	t.Cleanup(func() { firecrackerInstallPath = origInstallPath })

	origEmbedded := embeddedFirecracker
	embeddedFirecracker = []byte("fake-firecracker-binary")
	t.Cleanup(func() { embeddedFirecracker = origEmbedded })

	err := runServer(0, t.TempDir(), "test-token")
	if err == nil {
		t.Fatal("expected error when firecracker install fails, got nil")
	}
	if !strings.Contains(err.Error(), "install firecracker") {
		t.Fatalf("expected 'install firecracker' in error, got: %v", err)
	}
}
