package credsbundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildFromHomeIncludesExistingFiles(t *testing.T) {
	home := t.TempDir()
	credFile := filepath.Join(home, ".codex", "auth.json")
	if err := os.MkdirAll(filepath.Dir(credFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credFile, []byte(`{"token":"test"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	encoded, err := BuildFromHome(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if encoded == "" {
		t.Fatal("expected non-empty bundle")
	}

	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("not valid base64: %v", err)
	}
	gr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("not gzip: %v", err)
	}
	tr := tar.NewReader(gr)
	found := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar error: %v", err)
		}
		if hdr.Name == ".codex/auth.json" {
			found = true
		}
	}
	if !found {
		t.Fatal("bundle missing .codex/auth.json")
	}
}

func TestBuildFromHomeEmptyWhenNoFiles(t *testing.T) {
	home := t.TempDir()
	encoded, err := BuildFromHome(home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if encoded != "" {
		t.Fatalf("expected empty bundle for empty home, got non-empty")
	}
}
