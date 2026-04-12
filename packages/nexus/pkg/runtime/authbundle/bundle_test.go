package authbundle

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"archive/tar"
)

func TestIncludeBundledFile(t *testing.T) {
	tests := []struct {
		rel  string
		want bool
	}{
		{".config/opencode/session.json", true},
		{".config/opencode/secret.pem", false},
		{".config/codex/auth.json", true},
		{".codex/foo.yaml", true},
		{".config/openai/key.json", true},
		{".claude/settings.json", true},
		{".claude/CLAUDE.md", true},
		{".claude/claude.md", true},
		{".claude/projects/foo/x.json", false},
		{".gitconfig", true},
		{".git-credentials", true},
		{".config/git/config", true},
		{".config/git/credentials", true},
		{".config/opencode/cache.bin", false},
		{"other/x.json", false},
	}
	for _, tt := range tests {
		if got := includeBundledFile(tt.rel); got != tt.want {
			t.Errorf("includeBundledFile(%q) = %v, want %v", tt.rel, got, tt.want)
		}
	}
}

func TestBuildFromHomeSkipsNonRegistryFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	mkdir := func(p string) {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mkdir(filepath.Join(home, ".config", "opencode"))
	if err := os.WriteFile(filepath.Join(home, ".config", "opencode", "session.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "opencode", "noise.bin"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	b64, err := BuildFromHome()
	if err != nil {
		t.Fatal(err)
	}
	if b64 == "" {
		t.Fatal("expected bundle with session.json only")
	}

	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	gzr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	joined := strings.Join(names, "\n")
	if !strings.Contains(joined, ".config/opencode/session.json") {
		t.Fatalf("expected session.json in archive, got %q", joined)
	}
	if strings.Contains(joined, "noise.bin") {
		t.Fatalf("did not want noise.bin in archive, got %q", joined)
	}
}

func TestBuildFromHomeIncludesGitConfigAndCredentials(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	mkdir := func(p string) {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mkdir(filepath.Join(home, ".config", "git"))

	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[user]\n\tname = test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".git-credentials"), []byte("https://u:t@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "git", "config"), []byte("[credential]\n\thelper = store\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "git", "credentials"), []byte("https://u:t@example.com\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	b64, err := BuildFromHome()
	if err != nil {
		t.Fatal(err)
	}
	if b64 == "" {
		t.Fatal("expected bundle with git config and credentials")
	}

	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	gzr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}

	joined := strings.Join(names, "\n")
	if !strings.Contains(joined, ".gitconfig") {
		t.Fatalf("expected .gitconfig in archive, got %q", joined)
	}
	if !strings.Contains(joined, ".git-credentials") {
		t.Fatalf("expected .git-credentials in archive, got %q", joined)
	}
	if !strings.Contains(joined, ".config/git/config") {
		t.Fatalf("expected .config/git/config in archive, got %q", joined)
	}
	if !strings.Contains(joined, ".config/git/credentials") {
		t.Fatalf("expected .config/git/credentials in archive, got %q", joined)
	}
}
