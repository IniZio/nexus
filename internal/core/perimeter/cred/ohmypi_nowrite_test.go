package cred

// Automated regressions: nexus never writes to the operator's oh-my-pi
// credential vault.
//
// The vault is ~/.omp/agent/agent.db (or PI_CODING_AGENT_DIR/agent.db). Import
// may read the SQLite header and must then refuse — it must not write, rename,
// truncate, or pick a provider row.
//
// Root-safety: mtime + content comparison (uid-independent). chmod fixtures
// are vacuous under root (CAP_DAC_OVERRIDE).

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeOhMyPiVault(t *testing.T, dir string) (path string, content []byte) {
	t.Helper()
	content = append([]byte("SQLite format 3\x00"), []byte("synthetic-vault-bytes")...)
	path = filepath.Join(dir, "agent.db")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path, content
}

// TestImportOhMyPiCredentials_FileUnmodifiedAfterImport is the no-write guard.
//
// Mutation proof: adding `_ = os.WriteFile(path, data, 0o600)` inside
// importOhMyPiCredentialsAt changes agent.db's mtime and this assertion fails.
func TestImportOhMyPiCredentials_FileUnmodifiedAfterImport(t *testing.T) {
	dir := t.TempDir()
	authPath, content := writeOhMyPiVault(t, dir)

	before, err := os.Stat(authPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeContent, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}

	store, err := importOhMyPiCredentialsAt(ohMyPiTestProfile(), authPath)
	if store != nil {
		t.Fatal("import returned a store; vault must be refused")
	}
	if !errors.Is(err, ErrOhMyPiVaultNotImportable) {
		t.Fatalf("importOhMyPiCredentialsAt: %v", err)
	}
	if string(beforeContent) != string(content) {
		t.Fatal("fixture content mismatch before import")
	}

	after, err := os.Stat(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("agent.db mtime changed after import: before=%v after=%v — write occurred",
			before.ModTime().Format(time.RFC3339Nano),
			after.ModTime().Format(time.RFC3339Nano))
	}
	afterContent, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterContent) != string(beforeContent) {
		t.Error("agent.db content changed after import — write occurred")
	}
}

// TestImportOhMyPiCredentials_ReadOnlyDir_RefusesWithoutWrite proves a
// read-only credential directory does not force a write. Under root the
// chmod is not enforced; mtime + content remain the guard.
func TestImportOhMyPiCredentials_ReadOnlyDir_RefusesWithoutWrite(t *testing.T) {
	dir := t.TempDir()
	authPath, _ := writeOhMyPiVault(t, dir)
	if err := os.Chmod(authPath, 0o444); err != nil {
		t.Fatal(err)
	}

	before, err := os.Stat(authPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeContent, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if os.Geteuid() == 0 {
		t.Log("running as root: read-only directory (0o555) is not enforced " +
			"(CAP_DAC_OVERRIDE); mtime+content comparison is the no-write guard")
	}

	store, err := importOhMyPiCredentialsAt(ohMyPiTestProfile(), authPath)
	if store != nil {
		t.Fatal("import returned a store from a read-only vault")
	}
	if !errors.Is(err, ErrOhMyPiVaultNotImportable) {
		t.Fatalf("importOhMyPiCredentialsAt with read-only credDir: %v", err)
	}

	after, err := os.Stat(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("agent.db mtime changed after import: before=%v after=%v — write occurred",
			before.ModTime().Format(time.RFC3339Nano),
			after.ModTime().Format(time.RFC3339Nano))
	}
	afterContent, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterContent) != string(beforeContent) {
		t.Error("agent.db content changed after import — write occurred")
	}
}
