package service

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
)

// TestCreateAndBoot_CacheArtifactUnchanged verifies that booting a sandbox
// does not modify the digest-addressed cache artifact. The bytes read from
// the cache artifact before and after CreateAndBoot must be identical.
func TestCreateAndBoot_CacheArtifactUnchanged(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	// Record the cache artifact bytes before boot.
	artifactPath := filepath.Join(cacheRoot, img.Digest.Algo(), img.Digest.Hex(), "artifact")
	beforeBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read cache artifact before boot: %v", err)
	}

	diskDir := t.TempDir()
	fd := fake.New()
	svc := newTestSvc(t, fd).WithDiskDir(diskDir)

	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "cow-unchanged",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
			DiskDir:   diskDir,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	// Cache artifact bytes must be identical after boot.
	afterBytes, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("read cache artifact after boot: %v", err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) {
		t.Errorf("cache artifact was mutated: len before=%d, after=%d", len(beforeBytes), len(afterBytes))
	}
}

// TestCreateAndBoot_DriverGetsCopyPath verifies two seam properties:
//
//  1. The path handed to the DriverFactory is the per-sandbox copy, NOT the
//     cache artifact path.
//  2. The copy file exists at <diskDir>/<id>.raw after CreateAndBoot.
func TestCreateAndBoot_DriverGetsCopyPath(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	artifactPath := filepath.Join(cacheRoot, img.Digest.Algo(), img.Digest.Hex(), "artifact")
	diskDir := t.TempDir()

	// capturingFactory records the ext4Path argument for inspection.
	var capturedPath string
	capturingFactory := func(ext4Path string, _ []ExtraDisk) (driver.Driver, error) {
		capturedPath = ext4Path
		return fake.New(), nil
	}

	fd := fake.New()
	svc := newTestSvc(t, fd).WithDiskDir(diskDir)

	sb, err := CreateAndBoot(ctx, svc, cache, capturingFactory, noopProbe,
		"proj", "cow-seam",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
			DiskDir:   diskDir,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	// The path handed to the factory must NOT be the cache artifact.
	if capturedPath == artifactPath {
		t.Errorf("driver received cache artifact path %q; want per-sandbox copy", capturedPath)
	}

	// The path must be the per-sandbox copy inside diskDir.
	expectedCopy := filepath.Join(diskDir, sb.ID.String()+".raw")
	if capturedPath != expectedCopy {
		t.Errorf("driver received %q, want %q", capturedPath, expectedCopy)
	}

	// The copy file must exist on disk.
	if _, err := os.Stat(expectedCopy); err != nil {
		t.Errorf("per-sandbox disk copy missing at %q: %v", expectedCopy, err)
	}
}

// TestRemove_ReapsDiskCopy verifies that Service.Remove deletes the
// per-sandbox ext4 disk copy that CreateAndBoot created.
func TestRemove_ReapsDiskCopy(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	diskDir := t.TempDir()
	fd := fake.New()
	svc := newTestSvc(t, fd).WithDiskDir(diskDir)

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "cow-reap",
		CreateAndBootOptions{
			Image:     ImageSpec{Digest: string(img.Digest)},
			CacheRoot: cacheRoot,
			DiskDir:   diskDir,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	copyPath := filepath.Join(diskDir, sb.ID.String()+".raw")

	// Assert: copy exists after create (positive assertion — ensures the test
	// is not vacuously passing on a never-created file).
	if _, err := os.Stat(copyPath); err != nil {
		t.Fatalf("disk copy missing after create at %q: %v", copyPath, err)
	}

	// Remove the sandbox.
	if err := svc.Remove(ctx, sb.ID.String()); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Assert: copy is gone after remove.
	if _, err := os.Stat(copyPath); !os.IsNotExist(err) {
		t.Errorf("disk copy still present after remove at %q (err=%v)", copyPath, err)
	}
}

// TestCreateAndBoot_RootfsPathNoCopy verifies that when ImageSpec.RootfsPath
// is set (--rootfs dev convenience), no disk copy is created.
func TestCreateAndBoot_RootfsPathNoCopy(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}

	diskDir := t.TempDir()

	// capturingFactory records the ext4Path argument.
	var capturedPath string
	capturingFactory := func(ext4Path string, _ []ExtraDisk) (driver.Driver, error) {
		capturedPath = ext4Path
		return fake.New(), nil
	}

	fd := fake.New()
	svc := newTestSvc(t, fd).WithDiskDir(diskDir)

	rootfsPath := "/fake/dev.ext4"
	_, err = CreateAndBoot(ctx, svc, cache, capturingFactory, noopProbe,
		"proj", "cow-rootfs",
		CreateAndBootOptions{
			Image:     ImageSpec{RootfsPath: rootfsPath},
			CacheRoot: cacheRoot,
			DiskDir:   diskDir,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	// Driver must receive the original rootfs path unchanged — no copy.
	if capturedPath != rootfsPath {
		t.Errorf("driver received %q; want original rootfs path %q (no copy should be made)", capturedPath, rootfsPath)
	}

	// diskDir must be empty — no copy was created.
	entries, err := os.ReadDir(diskDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("diskDir has %d entries after --rootfs boot; want 0 (no copy)", len(entries))
	}
}
