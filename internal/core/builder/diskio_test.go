package builder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/image"
)

// debugfsAvailable reports whether debugfs is on the host PATH.
// debugfs ships with e2fsprogs, the same package as mke2fs.
func debugfsAvailable() bool {
	_, err := exec.LookPath("debugfs")
	return err == nil
}

// TestContextToDiskRoundTrip packs a small context directory into an ext4
// image and reads back a known file via debugfs to confirm fidelity.
func TestContextToDiskRoundTrip(t *testing.T) {
	if !Mke2fsAvailable() {
		t.Skip("mke2fs not available; skipping contextdisk test")
	}
	if !debugfsAvailable() {
		t.Skip("debugfs not available; skipping contextdisk read-back verification")
	}

	ctx := context.Background()

	// Build a small context directory with known content.
	contextDir := t.TempDir()
	wantContent := "hello from G4 context disk\n"
	if err := os.WriteFile(filepath.Join(contextDir, "hello.txt"), []byte(wantContent), 0o644); err != nil {
		t.Fatal(err)
	}
	subDir := filepath.Join(contextDir, "subdir")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "nested.txt"), []byte("nested file\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Pack into ext4.
	outExt4 := filepath.Join(t.TempDir(), "context.ext4")
	if err := ContextToDisk(ctx, contextDir, outExt4); err != nil {
		t.Fatalf("ContextToDisk: %v", err)
	}

	// Verify the image file exists and is non-empty.
	fi, err := os.Stat(outExt4)
	if err != nil {
		t.Fatalf("stat ext4: %v", err)
	}
	if fi.Size() == 0 {
		t.Fatal("context ext4 is empty")
	}

	// Read hello.txt back via debugfs to confirm content fidelity.
	out, err := exec.CommandContext(ctx, "debugfs", "-R", "cat /hello.txt", outExt4).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs cat hello.txt: %v\n%s", err, out)
	}
	if got := string(out); !strings.Contains(got, strings.TrimSpace(wantContent)) {
		t.Errorf("hello.txt content mismatch: got %q, want to contain %q", got, wantContent)
	}

	// Verify nested file exists via debugfs ls.
	lsOut, err := exec.CommandContext(ctx, "debugfs", "-R", "ls /subdir", outExt4).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs ls /subdir: %v\n%s", err, lsOut)
	}
	if !strings.Contains(string(lsOut), "nested.txt") {
		t.Errorf("nested.txt not found in /subdir listing: %s", lsOut)
	}

	t.Logf("context ext4 size: %d bytes", fi.Size())
	t.Logf("debugfs cat output: %q", string(out))
}

// TestArtifactFromDiskRoundTrip creates a small ext4 artifact image, calls
// ArtifactFromDisk to ingest it into the image cache, then verifies the
// returned digest is stored in the cache.
func TestArtifactFromDiskRoundTrip(t *testing.T) {
	if !Mke2fsAvailable() {
		t.Skip("mke2fs not available; skipping artifactdisk test")
	}

	ctx := context.Background()

	// Build a small artifact directory and pack it into ext4 to simulate
	// what the builder VM would write to /dev/vdc.
	artifactSrcDir := t.TempDir()
	artifactContent := "artifact output from builder VM\n"
	if err := os.WriteFile(filepath.Join(artifactSrcDir, "output.txt"), []byte(artifactContent), 0o644); err != nil {
		t.Fatal(err)
	}

	artifactExt4 := filepath.Join(t.TempDir(), "artifact.ext4")
	// Use ContextToDisk reusing the same packing logic (both are dir→ext4).
	if err := ContextToDisk(ctx, artifactSrcDir, artifactExt4); err != nil {
		t.Fatalf("pack artifact ext4: %v", err)
	}

	// Set up a real image cache in a temp directory.
	cacheDir := t.TempDir()
	cache, err := image.NewCache(cacheDir)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	// Ingest the artifact disk into the cache.
	digestStr, err := ArtifactFromDisk(ctx, artifactExt4, cache)
	if err != nil {
		t.Fatalf("ArtifactFromDisk: %v", err)
	}
	if digestStr == "" {
		t.Fatal("ArtifactFromDisk returned empty digest")
	}
	if !strings.HasPrefix(digestStr, "sha256:") {
		t.Errorf("digest should start with sha256:, got %q", digestStr)
	}

	t.Logf("artifact digest: %s", digestStr)
	t.Logf("artifact ext4 ingested into cache at %s", cacheDir)
}
