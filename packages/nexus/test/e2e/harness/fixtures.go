//go:build e2e

package harness

import (
	"os"
	"path/filepath"
	"testing"
)

func TempDB(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nexus-e2e-db-*")
	if err != nil {
		t.Fatalf("TempDB: mkdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "nexus.db")
}

func TempSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nexus-e2e-sock-*")
	if err != nil {
		t.Fatalf("TempSocket: mkdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "nexusd.sock")
}

func TempWorkdir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nexus-e2e-workdir-*")
	if err != nil {
		t.Fatalf("TempWorkdir: mkdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
