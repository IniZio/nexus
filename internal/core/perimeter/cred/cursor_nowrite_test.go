package cred

// Automated regressions for AC-4 (S18): nexus3 never writes to the operator's
// cursor credential file.
//
// The invariant (D-MAC-01 as amended 2026-09-05): nexus3 may READ
// ~/.config/cursor/auth.json for static, non-rotating agents but must NEVER
// write, rename, truncate, or take a refresh grant against it.
//
// Root-safety decision: mtime + content comparison (uid-independent).
//
//   - chmod 0o000 and read-only-dir fixtures are VACUOUS under root
//     (CAP_DAC_OVERRIDE ignores them — see memory/permission-fixtures-vacuous-under-root.md).
//   - Therefore the primary no-write assertion is mtime + content unchanged:
//     any write that does not happen cannot change mtime or file bytes.
//     This assertion holds regardless of the caller's uid.
//   - The read-only-directory fixture (0o555) is kept as a SECONDARY assertion:
//     under non-root it additionally proves no write is required for a successful
//     import; under root the test logs a note that the chmod is not enforced and
//     relies solely on mtime + content.
//   - The tests never skip silently — every root-uid path logs its reasoning.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestImportCursorCredentials_FileUnmodifiedAfterImport is the primary AC-4
// regression (D-MAC-01, uid-independent).
//
// Strategy: capture mtime and content BEFORE calling importCursorCredentialsAt;
// assert both are unchanged AFTER the call.  A write (os.WriteFile, os.Create,
// os.Truncate, os.Rename) that touches auth.json changes mtime on any uid.
//
// Mutation proof (no-write guard):
//
//	Adding `_ = os.WriteFile(path, data, 0o600)` inside importCursorCredentialsAt
//	changes auth.json's mtime, making the mtime assertion fail:
//	    cursor_nowrite_test.go:NNN: auth.json mtime changed after import
//	See verbatim RED output in the commit message.
//
// @verifies AC-4
func TestImportCursorCredentials_FileUnmodifiedAfterImport(t *testing.T) {
	tok := makeSyntheticJWT(t, map[string]any{"exp": float64(1893456000)})
	content, err := json.Marshal(map[string]string{
		"accessToken":  tok,
		"refreshToken": "rt",
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	dir := t.TempDir()
	credDir := filepath.Join(dir, "cursor")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	authPath := filepath.Join(credDir, "auth.json")
	// Write with owner-rw so the import can read it and, critically, so a
	// mutant write-back would also succeed and change the mtime.  The no-write
	// guarantee comes from the mtime+content assertions, not from file permissions.
	if err := os.WriteFile(authPath, content, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Allow a small settling time so the mtime is stable before the import.
	// On most Linux filesystems mtime resolution is 1 s; on tmpfs it is nanosecond.
	// We don't sleep — instead we record "before" strictly after the write settles
	// and check "after" is identical.
	before, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("Stat before: %v", err)
	}
	beforeContent, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile before: %v", err)
	}

	// Run the import.
	store, err := importCursorCredentialsAt(cursorTestProfile(), authPath)
	if err != nil {
		t.Fatalf("importCursorCredentialsAt: %v", err)
	}
	if store == nil || store.AccessToken == "" {
		t.Fatal("unexpected empty AccessToken after import")
	}

	// ── uid-independent no-write assertions ─────────────────────────────────

	after, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("Stat after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("auth.json mtime changed after import: before=%v after=%v — write occurred",
			before.ModTime().Format(time.RFC3339Nano),
			after.ModTime().Format(time.RFC3339Nano))
	}

	afterContent, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile after: %v", err)
	}
	if string(afterContent) != string(beforeContent) {
		t.Errorf("auth.json content changed after import — write occurred")
	}
}

// TestImportCursorCredentials_ReadOnlyDir_Succeeds proves that the cursor import
// completes successfully when the credential directory is read-only (0o555).
//
// A 0o555 directory allows reading existing files (exec bit present for path
// traversal) but denies creating new files, renaming, or truncating — any write
// to the directory would return EACCES under non-root.
//
// Root-safety: under root, CAP_DAC_OVERRIDE bypasses the chmod.  The test:
//   - Always asserts mtime + content unchanged (uid-independent guard).
//   - Under non-root: additionally verifies that a read-only directory does not
//     prevent a successful import (proving the import path needs no write).
//   - Under root: logs that the read-only fixture was not enforced, relying
//     solely on mtime + content.  The test does NOT skip — the mtime + content
//     assertion still provides coverage under root.
//
// @verifies AC-4
func TestImportCursorCredentials_ReadOnlyDir_Succeeds(t *testing.T) {
	tok := makeSyntheticJWT(t, map[string]any{"exp": float64(1893456000)})
	content, err := json.Marshal(map[string]string{
		"accessToken":  tok,
		"refreshToken": "rt",
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	dir := t.TempDir()
	credDir := filepath.Join(dir, "cursor")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	authPath := filepath.Join(credDir, "auth.json")
	if err := os.WriteFile(authPath, content, 0o444); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Capture before state.
	before, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("Stat before: %v", err)
	}
	beforeContent, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile before: %v", err)
	}

	// Make the directory read+exec only (no write).  Restore to 0o700 on
	// cleanup so t.TempDir removal succeeds.
	if err := os.Chmod(credDir, 0o555); err != nil {
		t.Fatalf("chmod credDir 0o555: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(credDir, 0o700) })

	if os.Geteuid() == 0 {
		t.Log("running as root: read-only directory (0o555) is not enforced " +
			"(CAP_DAC_OVERRIDE); mtime+content comparison is the no-write guard")
	}

	// Import must succeed even with the directory read-only.
	store, err := importCursorCredentialsAt(cursorTestProfile(), authPath)
	if err != nil {
		t.Fatalf("importCursorCredentialsAt with read-only credDir: %v "+
			"(any error here proves the import path attempts a write or create "+
			"inside the credential directory)", err)
	}
	if store == nil || store.AccessToken == "" {
		t.Fatal("unexpected empty AccessToken after read-only import")
	}

	// uid-independent no-write assertions.
	after, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("Stat after: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("auth.json mtime changed after import: before=%v after=%v — write occurred",
			before.ModTime().Format(time.RFC3339Nano),
			after.ModTime().Format(time.RFC3339Nano))
	}
	afterContent, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile after: %v", err)
	}
	if string(afterContent) != string(beforeContent) {
		t.Errorf("auth.json content changed after import — write occurred")
	}
}

// TestImportCursorCredentials_NoRefreshEndpointStored is the AC-4 "no refresh
// grant" regression (D-MAC-09): importing a cursor credential must produce a
// DedicatedCredStore with no TokenEndpoint, ClientID, or ClientSecret.
//
// An empty TokenEndpoint means the host never POSTs to a token endpoint on the
// operator's behalf — the credential is treated as a static JWT.
//
// Mutation proof:
//
//	Wiring a TokenEndpoint value (e.g. "https://auth.cursor.sh/token") inside
//	importCursorCredentialsAt makes this test fail:
//	    cursor_nowrite_test.go:NNN: TokenEndpoint = "..."; want empty (static JWT)
//	See verbatim RED output in the commit message.
//
// @verifies AC-4
func TestImportCursorCredentials_NoRefreshEndpointStored(t *testing.T) {
	tok := makeSyntheticJWT(t, map[string]any{"exp": float64(1893456000)})
	xdgBase, _ := makeCursorDir(t, cursorAuthJSON(t, tok, "rt"))
	t.Setenv("XDG_CONFIG_HOME", xdgBase)

	store, err := ImportCursorCredentials(cursorTestProfile())
	if err != nil {
		t.Fatalf("ImportCursorCredentials: %v", err)
	}

	if store.TokenEndpoint != "" {
		t.Errorf("TokenEndpoint = %q; want empty (static JWT, D-MAC-09: no refresh grant)", store.TokenEndpoint)
	}
	if store.ClientID != "" {
		t.Errorf("ClientID = %q; want empty", store.ClientID)
	}
	if store.ClientSecret != "" {
		t.Errorf("ClientSecret = %q; want empty", store.ClientSecret)
	}
}

// cursorAuthJSON marshals a cursor auth.json fixture to a string.
// Helper used only in this file; makeCursorDir (cursor_test.go) writes the result.
func cursorAuthJSON(t *testing.T, accessToken, refreshToken string) string {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"accessToken":  accessToken,
		"refreshToken": refreshToken,
	})
	if err != nil {
		t.Fatalf("cursorAuthJSON marshal: %v", err)
	}
	return string(b)
}
