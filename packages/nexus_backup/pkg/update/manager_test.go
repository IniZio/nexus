package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadArtifactAndChecksum(t *testing.T) {
	payload := []byte("test-binary")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/nexus-darwin-arm64" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	tempDir := t.TempDir()
	path, err := downloadArtifact(context.Background(), tempDir, server.URL, BinaryArtifact{
		Name:   "nexus-darwin-arm64",
		SHA256: sha256Bytes(payload),
	})
	if err != nil {
		t.Fatalf("download artifact: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged artifact: %v", err)
	}
	if string(data) != string(payload) {
		t.Fatalf("unexpected staged artifact payload")
	}
}

func TestReplaceBinary(t *testing.T) {
	tempDir := t.TempDir()
	source := filepath.Join(tempDir, "source")
	destination := filepath.Join(tempDir, "destination")
	if err := os.WriteFile(source, []byte("new-data"), 0o755); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.WriteFile(destination, []byte("old-data"), 0o755); err != nil {
		t.Fatalf("write destination: %v", err)
	}
	if err := replaceBinary(source, destination); err != nil {
		t.Fatalf("replace binary: %v", err)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(data) != "new-data" {
		t.Fatalf("expected destination to be replaced")
	}
}
