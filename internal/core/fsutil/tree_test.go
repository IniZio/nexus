package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyFileModeOverwriteMkdir(t *testing.T) {
	d := t.TempDir()
	src := filepath.Join(d, "src")
	if err := os.WriteFile(src, []byte("new-content"), 0o711); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(d, "a", "b", "dst")
	if err := CopyFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new-content" {
		t.Fatalf("content %q", b)
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o711 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if err := os.WriteFile(src, []byte("x"), 0o711); err != nil {
		t.Fatal(err)
	}
	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "x" {
		t.Fatalf("overwrite content %q", b)
	}
}

func TestCopyTreeKeepsSymlinksAndModes(t *testing.T) {
	src, dst, outside := t.TempDir(), t.TempDir(), t.TempDir()
	sub := filepath.Join(src, "sub")
	if err := os.Mkdir(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "exec"), []byte("d"), 0o711); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("sub/exec", filepath.Join(src, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := CopyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dst, "sub", "exec")); err != nil || fi.Mode().Perm() != 0o711 {
		t.Fatalf("exec: %v %v", fi, err)
	}
	for name, want := range map[string]string{"link": "sub/exec", "dirlink": outside} {
		got, err := os.Readlink(filepath.Join(dst, name))
		if err != nil || got != want {
			t.Fatalf("%s -> %q, %v; want %q", name, got, err, want)
		}
	}
	if err := CopyTree(filepath.Join(src, "sub", "exec"), dst); err == nil {
		t.Fatal("non-dir src must error")
	}
}
