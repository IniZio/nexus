package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGenerateToken_base64URL(t *testing.T) {
	tok, err := generateToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < 43 {
		t.Fatalf("token too short: len=%d want >=43", len(tok))
	}
	if _, err := base64.URLEncoding.DecodeString(tok); err != nil {
		t.Fatalf("not valid base64 URL encoding: %v", err)
	}
}

func TestLoadOrCreateToken_createsAndReuses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode checks are unix-specific")
	}
	dir := t.TempDir()
	first, err := loadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first == "" {
		t.Fatal("empty token")
	}
	path := filepath.Join(dir, "token")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if m := st.Mode().Perm(); m != 0o600 {
		t.Fatalf("token file mode: got %04o want 0600", m)
	}
	second, err := loadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatal("expected same token on second load")
	}
}

func TestLoadOrCreateToken_trimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	_ = os.MkdirAll(dir, 0o700)
	if err := os.WriteFile(path, []byte("  fixed-secret  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tok, err := loadOrCreateToken(dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(tok) != "fixed-secret" {
		t.Fatalf("got %q", tok)
	}
}
