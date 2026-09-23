//go:build linux

package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureHostHomeSymlink_SkipConditions(t *testing.T) {
	t.Parallel()

	t.Run("empty string is no-op", func(t *testing.T) {
		if err := EnsureHostHomeSymlink(""); err != nil {
			t.Errorf("want nil, got %v", err)
		}
	})

	t.Run("/root is no-op", func(t *testing.T) {
		if err := EnsureHostHomeSymlink("/root"); err != nil {
			t.Errorf("want nil, got %v", err)
		}
	})

	t.Run("relative path is no-op", func(t *testing.T) {
		if err := EnsureHostHomeSymlink("home/alice"); err != nil {
			t.Errorf("want nil, got %v", err)
		}
	})

	t.Run("path with .. is no-op", func(t *testing.T) {
		if err := EnsureHostHomeSymlink("/home/../alice"); err != nil {
			t.Errorf("want nil, got %v", err)
		}
	})
}

func TestEnsureHostHomeSymlink_DoesNotExist(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	target := filepath.Join(base, "home", "alice")

	if err := EnsureHostHomeSymlink(target); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("lstat after creation: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected symlink, got %v", fi.Mode())
	}
	dest, err := os.Readlink(target)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if dest != "/root" {
		t.Errorf("symlink target = %q, want /root", dest)
	}
}

func TestEnsureHostHomeSymlink_AlreadySymlink(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	target := filepath.Join(base, "home", "alice")

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/some/other/path", target); err != nil {
		t.Fatal(err)
	}

	if err := EnsureHostHomeSymlink(target); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should leave the existing symlink untouched.
	dest, err := os.Readlink(target)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if dest != "/some/other/path" {
		t.Errorf("symlink was changed: got %q, want /some/other/path", dest)
	}
}

func TestEnsureHostHomeSymlink_EmptyRealDir(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	target := filepath.Join(base, "home", "alice")

	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := EnsureHostHomeSymlink(target); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("lstat after creation: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("expected symlink replacing empty dir, got %v", fi.Mode())
	}
	dest, err := os.Readlink(target)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if dest != "/root" {
		t.Errorf("symlink target = %q, want /root", dest)
	}
}

func TestEnsureHostHomeSymlink_NonEmptyRealDir(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	target := filepath.Join(base, "home", "alice")

	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	// Put a file in the dir to make it non-empty.
	if err := os.WriteFile(filepath.Join(target, "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := EnsureHostHomeSymlink(target); err != nil {
		t.Fatalf("unexpected error (should be no-op): %v", err)
	}

	// Should remain a real directory — not converted to a symlink.
	fi, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("lstat: %v", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Errorf("non-empty dir must not be replaced by symlink")
	}
	if !fi.IsDir() {
		t.Errorf("expected directory to remain, got %v", fi.Mode())
	}
}
