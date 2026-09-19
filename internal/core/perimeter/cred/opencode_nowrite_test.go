package cred

// nexus may READ ~/.local/share/opencode/auth.json for the static opencode-go
// API key but must NEVER write, rename, truncate, or take a refresh grant
// against it.
//
// The no-write assertion is mtime + content. chmod fixtures are not enforced
// under root (CAP_DAC_OVERRIDE); mtime + content still holds.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestImportOpencodeCredentials_FileUnmodifiedAfterImport(t *testing.T) {
	content := opencodeAuthJSON(t, map[string]any{
		OpencodeGoProviderID: map[string]string{"type": "api", "key": "oc-test-key-not-a-secret"},
	})

	dir := t.TempDir()
	credDir := filepath.Join(dir, "opencode")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	authPath := filepath.Join(credDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	before, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("Stat before: %v", err)
	}
	beforeContent, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile before: %v", err)
	}

	store, err := importOpencodeCredentialsAt(opencodeTestProfile(), authPath)
	if err != nil {
		t.Fatalf("importOpencodeCredentialsAt: %v", err)
	}
	if store == nil || store.AccessToken == "" {
		t.Fatal("unexpected empty AccessToken after import")
	}

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

func TestImportOpencodeCredentials_ReadOnlyDir_Succeeds(t *testing.T) {
	content := opencodeAuthJSON(t, map[string]any{
		OpencodeGoProviderID: map[string]string{"type": "api", "key": "oc-test-key-not-a-secret"},
	})

	dir := t.TempDir()
	credDir := filepath.Join(dir, "opencode")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	authPath := filepath.Join(credDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(content), 0o444); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	before, err := os.Stat(authPath)
	if err != nil {
		t.Fatalf("Stat before: %v", err)
	}
	beforeContent, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile before: %v", err)
	}

	if err := os.Chmod(credDir, 0o555); err != nil {
		t.Fatalf("chmod credDir 0o555: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(credDir, 0o700) })

	if os.Geteuid() == 0 {
		t.Log("running as root: read-only directory (0o555) is not enforced " +
			"(CAP_DAC_OVERRIDE); mtime+content comparison is the no-write guard")
	}

	store, err := importOpencodeCredentialsAt(opencodeTestProfile(), authPath)
	if err != nil {
		t.Fatalf("importOpencodeCredentialsAt with read-only credDir: %v", err)
	}
	if store == nil || store.AccessToken == "" {
		t.Fatal("unexpected empty AccessToken after read-only import")
	}

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

func TestImportOpencodeCredentials_NoRefreshEndpointStored(t *testing.T) {
	dataHome, _ := makeOpencodeDir(t, opencodeAuthJSON(t, map[string]any{
		OpencodeGoProviderID: map[string]string{"type": "api", "key": "oc-test-key-not-a-secret"},
	}))
	t.Setenv("XDG_DATA_HOME", dataHome)

	store, err := ImportOpencodeCredentials(opencodeTestProfile())
	if err != nil {
		t.Fatalf("ImportOpencodeCredentials: %v", err)
	}
	if store.TokenEndpoint != "" {
		t.Errorf("TokenEndpoint = %q; want empty (static API key, no refresh grant)", store.TokenEndpoint)
	}
	if store.ClientID != "" {
		t.Errorf("ClientID = %q; want empty", store.ClientID)
	}
	if store.ClientSecret != "" {
		t.Errorf("ClientSecret = %q; want empty", store.ClientSecret)
	}
}
