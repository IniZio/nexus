package cred

// ImportCodexCredentials refuses before touching the filesystem. A write
// would mutate the operator's live Codex login (refresh tokens rotate).
// The guard is "the directory we pointed at is unchanged", which holds
// even when the function does not consult CODEX_HOME.

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestImportCodexCredentials_DoesNotTouchAuthFile(t *testing.T) {
	dir := t.TempDir()
	authPath := filepath.Join(dir, "auth.json")
	const body = `{"auth_mode":"chatgpt","tokens":{"access_token":"a","refresh_token":"r"}}`
	if err := os.WriteFile(authPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(authPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", dir)

	store, err := ImportCodexCredentials(CodexProfile)
	if err == nil || store != nil {
		t.Fatalf("ImportCodexCredentials = (%v, %v), want error and nil store", store, err)
	}

	after, err := os.Stat(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("auth.json mtime changed: before=%s after=%s",
			before.ModTime().Format(time.RFC3339Nano),
			after.ModTime().Format(time.RFC3339Nano))
	}
	got, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Errorf("auth.json content changed: %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "auth.json" {
		t.Errorf("CODEX_HOME entries = %v, want only the original auth.json", entries)
	}
}
