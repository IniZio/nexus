package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	ws, err := NewWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("NewWorkspace failed: %v", err)
	}
	if ws == nil {
		t.Fatal("Expected workspace, got nil")
	}
	if ws.ID() == "" {
		t.Error("Expected non-empty ID")
	}
	if ws.Path() != tmpDir {
		t.Errorf("Expected path %s, got %s", tmpDir, ws.Path())
	}
}

func TestSecurePath(t *testing.T) {
	tmpDir := t.TempDir()
	ws, err := NewWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("NewWorkspace failed: %v", err)
	}

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"empty returns workspace path", "", tmpDir, false},
		{"dot returns workspace path", ".", tmpDir, false},
		{"relative file", "file.txt", filepath.Join(tmpDir, "file.txt"), false},
		{"relative nested file", "dir/file.txt", filepath.Join(tmpDir, "dir/file.txt"), false},
		{"absolute path rejected", "/etc/passwd", "", true},
		{"path traversal rejected", "../etc/passwd", "", true},
		{"path traversal with double dots", "foo/../../../etc/passwd", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ws.SecurePath(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("Expected error, got nil")
				}
			} else {
				if err != nil {
					t.Fatalf("Unexpected error: %v", err)
				}
				if got != tt.want {
					t.Errorf("SecurePath(%q) = %q, want %q", tt.input, got, tt.want)
				}
			}
		})
	}
}

func TestExists(t *testing.T) {
	tmpDir := t.TempDir()
	ws, err := NewWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("NewWorkspace failed: %v", err)
	}

	if !ws.Exists() {
		t.Error("Expected workspace to exist")
	}
}

func TestExistsDeleted(t *testing.T) {
	tmpDir := t.TempDir()
	ws, err := NewWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("NewWorkspace failed: %v", err)
	}

	os.RemoveAll(tmpDir)

	if ws.Exists() {
		t.Error("Expected workspace to not exist after deletion")
	}
}

func TestIsValidSubPath(t *testing.T) {
	tmpDir := t.TempDir()
	ws, err := NewWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("NewWorkspace failed: %v", err)
	}

	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"valid file", "file.txt", true},
		{"valid nested", "dir/file.txt", true},
		{"valid nested deep", "a/b/c/file.txt", true},
		{"path traversal out", "../file.txt", false},
		{"absolute path - BUG: currently allows", "/etc/passwd", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ws.IsValidSubPath(tt.input)
			if got != tt.want {
				t.Errorf("IsValidSubPath(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestCreatedAt(t *testing.T) {
	tmpDir := t.TempDir()
	ws, err := NewWorkspace(tmpDir)
	if err != nil {
		t.Fatalf("NewWorkspace failed: %v", err)
	}

	if ws.CreatedAt().IsZero() {
		t.Error("Expected non-zero CreatedAt")
	}
}
