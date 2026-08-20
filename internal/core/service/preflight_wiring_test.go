package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
)

// recordingPreflight returns a DiskPreflight stub that records what it was
// asked and optionally refuses. It answers the question the count-based
// CheckDiskSpace could not: WHAT is projected, not just how many sandboxes.
type recordingPreflight struct {
	calls     int
	projected int64
	detail    string
	dir       string
	refuse    bool
}

func (r *recordingPreflight) fn(diskDir string, projected int64, detail string) (*DiskPreflightResult, error) {
	r.calls++
	r.dir = diskDir
	r.projected = projected
	r.detail = detail
	if r.refuse {
		return &DiskPreflightResult{ProjectedBytes: projected},
			errors.New("service: insufficient disk space: stub refusal")
	}
	return &DiskPreflightResult{ProjectedBytes: projected}, nil
}

// TestCreateAndBoot_PreflightRefusalStopsBeforeAnyBytes is the load-bearing
// test for TBD-PD-26. It asserts three things at once, because a preflight
// that runs but does not stop the create is worse than none:
//
//  1. the preflight IS called on the create path,
//  2. its refusal aborts CreateAndBoot, and
//  3. nothing was written to diskDir — no .raw copy, no create-intent file.
//
// (3) is what makes this a preflight rather than a post-mortem: the whole
// point is to refuse before a multi-gigabyte cp starts.
func TestCreateAndBoot_PreflightRefusalStopsBeforeAnyBytes(t *testing.T) {
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
	pf := &recordingPreflight{refuse: true}

	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "no-room",
		CreateAndBootOptions{
			Image:         ImageSpec{Digest: string(img.Digest)},
			CacheRoot:     cacheRoot,
			DiskDir:       diskDir,
			DiskPreflight: pf.fn,
		},
	)
	if err == nil {
		t.Fatal("CreateAndBoot succeeded despite a refusing disk preflight")
	}
	if !strings.Contains(err.Error(), "insufficient disk space") {
		t.Errorf("error does not name the cause: %v", err)
	}
	if pf.calls != 1 {
		t.Errorf("preflight called %d times, want exactly 1", pf.calls)
	}

	entries, rErr := os.ReadDir(diskDir)
	if rErr != nil {
		t.Fatalf("ReadDir: %v", rErr)
	}
	for _, e := range entries {
		t.Errorf("preflight refused but %q was still written to diskDir", e.Name())
	}
}

// TestCreateAndBoot_PreflightProjectsSourceArtifactBytes verifies the
// projection is MEASURED FROM THE SOURCE rather than sampled: cowExt4 copies
// the cache artifact, so the artifact's own allocated size bounds the cost
// from above. A projection derived from estimatePerSandbox instead would
// report the 4.57 GiB default here, because a fresh diskDir holds no workspace
// disks to sample — untethered from the image actually being copied.
func TestCreateAndBoot_PreflightProjectsSourceArtifactBytes(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)
	artifactPath := filepath.Join(cacheRoot, img.Digest.Algo(), img.Digest.Hex(), "artifact")
	want := diskAllocatedBytes(artifactPath)

	diskDir := t.TempDir()
	fd := fake.New()
	svc := newTestSvc(t, fd).WithDiskDir(diskDir)
	pf := &recordingPreflight{}

	if _, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "exact",
		CreateAndBootOptions{
			Image:         ImageSpec{Digest: string(img.Digest)},
			CacheRoot:     cacheRoot,
			DiskDir:       diskDir,
			DiskPreflight: pf.fn,
		},
	); err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	if pf.calls != 1 {
		t.Fatalf("preflight called %d times, want 1", pf.calls)
	}
	if pf.projected != want {
		t.Errorf("projected %d bytes, want the source artifact's allocated size %d "+
			"(a sampled estimate would report %d)", pf.projected, want, perSandboxAllocatedBytesDefault)
	}
	if !strings.Contains(pf.detail, "root disk") {
		t.Errorf("detail %q does not name the root disk component", pf.detail)
	}
	if pf.dir != diskDir {
		t.Errorf("preflight checked %q, want the disk dir %q", pf.dir, diskDir)
	}
}

// TestCreateAndBoot_ForceSkipsPreflight verifies --force reaches the check.
// Without an override a btrfs/xfs host could not create at all once the
// projection exceeded free space, even though the reflink copy is free.
func TestCreateAndBoot_ForceSkipsPreflight(t *testing.T) {
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
	pf := &recordingPreflight{refuse: true}

	if _, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe,
		"proj", "forced",
		CreateAndBootOptions{
			Image:          ImageSpec{Digest: string(img.Digest)},
			CacheRoot:      cacheRoot,
			DiskDir:        diskDir,
			DiskPreflight:  pf.fn,
			ForceDiskSpace: true,
		},
	); err != nil {
		t.Fatalf("CreateAndBoot with --force: %v", err)
	}
	if pf.calls != 0 {
		t.Errorf("preflight called %d times under --force, want 0", pf.calls)
	}
}

// TestProjectForkBytes_MultipliesWholeParentFootprint pins the fork
// projection. Fork copies EVERY parent disk once per child, so a 2-way fork of
// a parent holding a root disk plus a workspace disk must project twice their
// combined footprint — not twice the root alone, and not one sandwich of the
// sampled workspace estimate.
func TestProjectForkBytes_MultipliesWholeParentFootprint(t *testing.T) {
	diskDir := t.TempDir()
	parent := "sb-06G1KWRT0000000000000000"

	write := func(name string, size int) {
		if err := os.WriteFile(filepath.Join(diskDir, name), make([]byte, size), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write(parent+".raw", 64*1024)
	write(parent+"-workspace.ext4", 32*1024)
	// Metadata, not copied by fork — must not inflate the projection.
	write(parent+".create-intent.json", 8*1024)
	// A different sandbox's disk — must not be counted at all.
	write("sb-06G1KWRT1111111111111111.raw", 128*1024)

	perParent := diskAllocatedBytes(filepath.Join(diskDir, parent+".raw")) +
		diskAllocatedBytes(filepath.Join(diskDir, parent+"-workspace.ext4"))

	projected, detail := ProjectForkBytes(diskDir, parent, 2)
	if projected != 2*perParent {
		t.Errorf("projected %d, want %d (2 × parent footprint %d)", projected, 2*perParent, perParent)
	}
	if !strings.Contains(detail, "2 child(ren)") {
		t.Errorf("detail %q does not name the child count", detail)
	}
}

// TestProjectCreateBytes_RootfsProjectsNothing verifies the --rootfs path is
// not charged. Booting an image in place makes no copy, so charging it a
// default estimate would refuse creates that allocate zero bytes.
func TestProjectCreateBytes_RootfsProjectsNothing(t *testing.T) {
	projected, detail := ProjectCreateBytes(t.TempDir(), "", false)
	if projected != 0 {
		t.Errorf("projected %d bytes for a --rootfs create with no workspace, want 0", projected)
	}
	if detail != "" {
		t.Errorf("detail = %q, want empty", detail)
	}
}

// TestCheckDiskSpaceBytes_FreshHostDoesNotRefuse is a regression test for a
// defect the TBD-PD-26 wiring exposed: the old code fell back exactly ONE
// level when diskDir did not exist. On a machine that has never run nexus3
// neither <root>/disks nor <root> exists, so statfs ran against a missing
// directory, the check failed closed, and every first-ever create was refused
// with "cannot stat free space". The fix walks up to the nearest existing
// ancestor, which reports the same filesystem.
func TestCheckDiskSpaceBytes_FreshHostDoesNotRefuse(t *testing.T) {
	// Three levels deep, none of which exist — the fresh-host shape.
	missing := filepath.Join(t.TempDir(), "nexus3", "state", "disks")

	r, err := CheckDiskSpaceBytes(missing, 1024, "1 KiB")
	if err != nil {
		t.Fatalf("refused a 1 KiB projection on an empty host: %v", err)
	}
	if r.FreeBytes <= 0 {
		t.Errorf("FreeBytes = %d, want the containing filesystem's free space", r.FreeBytes)
	}
}

// TestCheckDiskSpaceBytes_FailsClosedWhenUnmeasurable pins the other half:
// when free space genuinely cannot be read, the check must refuse rather than
// wave the create through. The ancestor walk must not turn a real statfs
// failure into a pass.
func TestCheckDiskSpaceBytes_FailsClosedWhenUnmeasurable(t *testing.T) {
	orig := DiskStatfs
	t.Cleanup(func() { DiskStatfs = orig })
	DiskStatfs = func(string) (int64, error) { return 0, errors.New("statfs exploded") }

	if _, err := CheckDiskSpaceBytes(t.TempDir(), 1024, "1 KiB"); err == nil {
		t.Fatal("passed despite being unable to measure free space; must fail closed")
	}
}
