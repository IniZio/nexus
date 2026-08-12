package builder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestContextToDisk_DockerignoreExclusion verifies that ContextToDisk honours
// .dockerignore: excluded paths must be absent from the ext4 image while
// non-excluded paths remain present.
func TestContextToDisk_DockerignoreExclusion(t *testing.T) {
	skipIfInGuest(t)
	if !Mke2fsAvailable() {
		t.Skip("mke2fs not available; skipping contextdisk dockerignore test")
	}
	if !debugfsAvailable() {
		t.Skip("debugfs not available; skipping contextdisk dockerignore test")
	}

	ctx := context.Background()
	contextDir := t.TempDir()

	// Write a file that should be kept.
	if err := os.WriteFile(filepath.Join(contextDir, "keep.txt"), []byte("kept\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a file inside the ignored subtree.
	if err := os.MkdirAll(filepath.Join(contextDir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "node_modules", "big.bin"), []byte("this must be excluded\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write a nested file also inside the ignored subtree.
	if err := os.MkdirAll(filepath.Join(contextDir, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "node_modules", "pkg", "index.js"), []byte("excluded too\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Write .dockerignore excluding node_modules.
	if err := os.WriteFile(filepath.Join(contextDir, ".dockerignore"), []byte("node_modules\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outExt4 := filepath.Join(t.TempDir(), "context.ext4")
	if err := ContextToDisk(ctx, contextDir, outExt4); err != nil {
		t.Fatalf("ContextToDisk: %v", err)
	}

	// Confirm image was created.
	fi, err := os.Stat(outExt4)
	if err != nil {
		t.Fatalf("stat ext4: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatal("context ext4 is empty")
	}

	// keep.txt must be present.
	out, err := exec.CommandContext(ctx, "debugfs", "-R", "cat /keep.txt", outExt4).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs cat /keep.txt: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "kept") {
		t.Errorf("keep.txt content mismatch: got %q", string(out))
	}

	// node_modules/big.bin must be absent.
	lsOut, _ := exec.CommandContext(ctx, "debugfs", "-R", "ls /node_modules", outExt4).CombinedOutput()
	if strings.Contains(string(lsOut), "big.bin") {
		t.Errorf("node_modules/big.bin should be excluded but was found in ext4")
	}
	// Also verify the directory itself is absent (debugfs ls on a missing dir
	// exits non-zero; the output should not list the directory).
	lsRootOut, _ := exec.CommandContext(ctx, "debugfs", "-R", "ls /", outExt4).CombinedOutput()
	if strings.Contains(string(lsRootOut), "node_modules") {
		t.Errorf("node_modules directory should be excluded but appears in ext4 root listing")
	}

	t.Logf("ext4 size: %d bytes", fi.Size())
	t.Logf("root listing: %s", string(lsRootOut))
}

// TestContextToDisk_NoDockerignore verifies that ContextToDisk packs all files
// when no .dockerignore is present (existing behaviour is preserved).
func TestContextToDisk_NoDockerignore(t *testing.T) {
	skipIfInGuest(t)
	if !Mke2fsAvailable() {
		t.Skip("mke2fs not available; skipping contextdisk no-dockerignore test")
	}
	if !debugfsAvailable() {
		t.Skip("debugfs not available; skipping contextdisk no-dockerignore test")
	}

	ctx := context.Background()
	contextDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(contextDir, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	outExt4 := filepath.Join(t.TempDir(), "context.ext4")
	if err := ContextToDisk(ctx, contextDir, outExt4); err != nil {
		t.Fatalf("ContextToDisk (no dockerignore): %v", err)
	}

	out, err := exec.CommandContext(ctx, "debugfs", "-R", "cat /hello.txt", outExt4).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs cat /hello.txt: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "hello") {
		t.Errorf("hello.txt content mismatch: got %q", string(out))
	}
}
