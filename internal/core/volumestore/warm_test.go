package volumestore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// ── helpers ───────────────────────────────────────────────────────────────────

func newWarmStore(t *testing.T) *VolumeStore {
	t.Helper()
	root := filepath.Join(t.TempDir(), "volumes")
	return New(root)
}

// ficloneSupportedInTempDir probes whether reflink works in t.TempDir().
func ficloneSupportedInTempDir(t *testing.T) bool {
	t.Helper()
	dir := t.TempDir()
	f1, err := os.CreateTemp(dir, "probe-src-*")
	if err != nil {
		return false
	}
	defer os.Remove(f1.Name())
	defer f1.Close()
	if err := f1.Truncate(4096); err != nil {
		return false
	}
	f2, err := os.CreateTemp(dir, "probe-dst-*")
	if err != nil {
		return false
	}
	defer os.Remove(f2.Name())
	defer f2.Close()
	if err := f2.Truncate(4096); err != nil {
		return false
	}
	return reflinkFileFn(f2, f1) == nil
}

// copyClone is an io.Copy-based substitute for tests that don't need real reflink.
func copyClone(dst, src *os.File) error {
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return err
	}
	_, err := io.Copy(dst, src)
	return err
}

// makeTinyWarm writes a minimal warm disk+meta so SeedFromWarm finds a copy.
func makeTinyWarm(t *testing.T, s *VolumeStore, key string, kind WarmKind, sizeBytes int64) {
	t.Helper()
	wDir := s.warmKindDir(key, kind)
	if err := os.MkdirAll(wDir, 0o755); err != nil {
		t.Fatalf("makeTinyWarm mkdir: %v", err)
	}
	diskPath := filepath.Join(wDir, diskFile)
	f, err := os.Create(diskPath)
	if err != nil {
		t.Fatalf("makeTinyWarm create disk: %v", err)
	}
	_ = f.Truncate(sizeBytes)
	f.Close()
	meta := WarmMeta{SourceVolume: "source-vol", PromotedAt: time.Now().UTC(), SizeBytes: sizeBytes}
	raw, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(wDir, metaFile), raw, 0o644); err != nil {
		t.Fatalf("makeTinyWarm write meta: %v", err)
	}
}

// makeVolumeWithDisk writes a volume record and a placeholder disk file.
func makeVolumeWithDisk(t *testing.T, s *VolumeStore, name string, sizeBytes int64) {
	t.Helper()
	dir := s.volDir(name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("makeVolumeWithDisk mkdir: %v", err)
	}
	f, err := os.Create(s.DiskPath(name))
	if err != nil {
		t.Fatalf("makeVolumeWithDisk create disk: %v", err)
	}
	_ = f.Truncate(sizeBytes)
	f.Close()
	rec := &VolumeRecord{Name: name, Kind: KindDisk, SizeBytes: sizeBytes, CreatedAt: time.Now().UTC()}
	if err := s.writeRecord(rec); err != nil {
		t.Fatalf("makeVolumeWithDisk writeRecord: %v", err)
	}
}

// fileBlocks returns the 512-byte block count for path via os.FileInfo.Sys().
func fileBlocks(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	sys, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("os.FileInfo.Sys() not *syscall.Stat_t on this platform")
	}
	return sys.Blocks
}

// ── ProjectKey ────────────────────────────────────────────────────────────────

func TestProjectKey_SamePathSameKey(t *testing.T) {
	a := ProjectKey("/home/user/projects/myrepo/.git")
	b := ProjectKey("/home/user/projects/myrepo/.git")
	if a != b {
		t.Errorf("same path gave different keys: %q vs %q", a, b)
	}
}

func TestProjectKey_SameNameDifferentPathDifferentKey(t *testing.T) {
	a := ProjectKey("/org/team1/myapp/.git")
	b := ProjectKey("/org/team2/myapp/.git")
	if a == b {
		t.Errorf("same name, different path: keys should differ but got %q", a)
	}
}

func TestProjectKey_BareRepo(t *testing.T) {
	k := ProjectKey("/srv/git/myproject.git")
	if k[:9] != "myproject" {
		t.Errorf("expected key to start with 'myproject', got %q", k)
	}
}

func TestProjectKey_SlugCharset(t *testing.T) {
	k := ProjectKey("/home/user/My Awesome Repo 2!/.git")
	for _, c := range k {
		valid := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_' || c == '-'
		if !valid {
			t.Errorf("key contains invalid char %q: %s", c, k)
		}
	}
}

func TestProjectKey_EmptyBaseFallback(t *testing.T) {
	k := ProjectKey("/.git")
	if len(k) == 0 {
		t.Error("empty key for root .git")
	}
	if k == "-" {
		t.Error("key should not be just a dash")
	}
}

func TestProjectKey_HashSuffix12Hex(t *testing.T) {
	k := ProjectKey("/some/path/repo/.git")
	parts := k[len(k)-12:]
	for _, c := range parts {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("suffix not 12 hex chars in %q", k)
		}
	}
}

// ── SeedFromWarm ──────────────────────────────────────────────────────────────

func TestSeedFromWarm_NoWarmCopy(t *testing.T) {
	s := newWarmStore(t)
	seeded, err := s.SeedFromWarm(context.Background(), "vol1", "proj-aabbccddeeff", WarmKindDocker, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seeded {
		t.Error("expected seeded=false when no warm copy exists")
	}
}

func TestSeedFromWarm_ExistingVolumeUntouched(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	makeTinyWarm(t, s, key, WarmKindDocker, 4096)

	dir := s.volDir("vol1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rec := &VolumeRecord{Name: "vol1", Kind: KindDisk, SizeBytes: 4096, CreatedAt: time.Now().UTC()}
	if err := s.writeRecord(rec); err != nil {
		t.Fatalf("writeRecord: %v", err)
	}

	old := reflinkFileFn
	called := false
	reflinkFileFn = func(dst, src *os.File) error { called = true; return nil }
	defer func() { reflinkFileFn = old }()

	seeded, err := s.SeedFromWarm(context.Background(), "vol1", key, WarmKindDocker, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seeded {
		t.Error("expected seeded=false for existing volume")
	}
	if called {
		t.Error("reflinkFileFn must not be called for existing volume")
	}
}

func TestSeedFromWarm_ClonesAndWritesRecord(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	const warmSize int64 = 4096

	makeTinyWarm(t, s, key, WarmKindGoCache, warmSize)

	old := reflinkFileFn
	reflinkFileFn = copyClone
	defer func() { reflinkFileFn = old }()

	seeded, err := s.SeedFromWarm(context.Background(), "vol-seed", key, WarmKindGoCache, warmSize)
	if err != nil {
		t.Fatalf("SeedFromWarm: %v", err)
	}
	if !seeded {
		t.Fatal("expected seeded=true")
	}

	rec, err := s.Get("vol-seed")
	if err != nil {
		t.Fatalf("Get after seed: %v", err)
	}
	if rec.Kind != KindDisk {
		t.Errorf("expected kind=disk, got %s", rec.Kind)
	}
	if rec.SizeBytes == 0 {
		t.Error("SizeBytes should be non-zero")
	}
	if _, err := os.Stat(s.DiskPath("vol-seed")); err != nil {
		t.Errorf("disk.ext4 not found: %v", err)
	}
}

func TestSeedFromWarm_CloneFailure_NoPartialFiles(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	makeTinyWarm(t, s, key, WarmKindGoPath, 4096)

	oldR := reflinkFileFn
	oldS := sparseCopyFileFn
	reflinkFileFn = func(dst, src *os.File) error { return errors.New("injected clone failure") }
	sparseCopyFileFn = func(_ context.Context, dst, src *os.File) error { return errors.New("injected sparse failure") }
	defer func() { reflinkFileFn = oldR; sparseCopyFileFn = oldS }()

	seeded, err := s.SeedFromWarm(context.Background(), "vol-fail", key, WarmKindGoPath, 0)
	if err == nil {
		t.Fatal("expected error from clone failure")
	}
	if seeded {
		t.Error("expected seeded=false on failure")
	}
	if _, statErr := os.Stat(s.DiskPath("vol-fail")); statErr == nil {
		t.Error("partial disk.ext4 left after clone failure")
	}
	if _, statErr := os.Stat(s.metaPath("vol-fail")); statErr == nil {
		t.Error("partial meta.json left after clone failure")
	}

	seeded2, err2 := s.SeedFromWarm(context.Background(), "vol-fail", key, WarmKindGoPath, 0)
	if seeded2 {
		t.Error("second seed attempt should also fail")
	}
	if err2 == nil {
		t.Error("second seed attempt should still error with injected failure")
	}
}

func TestSeedFromWarm_UnknownKindError(t *testing.T) {
	s := newWarmStore(t)
	_, err := s.SeedFromWarm(context.Background(), "v", "proj-aabbccddeeff", WarmKind("agentcfg"), 0)
	if err == nil {
		t.Error("expected error for unknown warm kind")
	}
}

// TestSeedFromWarm_ReflinkUnsupported_FallsBackToSparseCopy verifies that when
// FICLONE is unsupported, SeedFromWarm falls back to sparse copy, content is
// identical, and the copy is reasonably sparse (Blocks ≤ source Blocks * 2 + 8).
func TestSeedFromWarm_ReflinkUnsupported_FallsBackToSparseCopy(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	const warmSize int64 = 1 << 20 // 1 MiB

	// Create warm disk with a small data region and the rest as holes.
	wDir := s.warmKindDir(key, WarmKindDocker)
	if err := os.MkdirAll(wDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	diskPath := filepath.Join(wDir, diskFile)
	f, err := os.Create(diskPath)
	if err != nil {
		t.Fatalf("create disk: %v", err)
	}
	if err := f.Truncate(warmSize); err != nil {
		f.Close()
		t.Fatalf("truncate: %v", err)
	}
	data := bytes.Repeat([]byte{0xAB}, 4096)
	if _, err := f.WriteAt(data, 0); err != nil {
		f.Close()
		t.Fatalf("write data: %v", err)
	}
	f.Close()
	meta := WarmMeta{SourceVolume: "source-vol", PromotedAt: time.Now().UTC(), SizeBytes: warmSize}
	raw, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(wDir, metaFile), raw, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	srcBlocks := fileBlocks(t, diskPath)

	old := reflinkFileFn
	reflinkFileFn = func(dst, src *os.File) error {
		return fmt.Errorf("%w: injected", ErrReflinkUnsupported)
	}
	defer func() { reflinkFileFn = old }()

	seeded, err := s.SeedFromWarm(context.Background(), "vol-sparse", key, WarmKindDocker, warmSize)
	if err != nil {
		t.Fatalf("SeedFromWarm with sparse fallback: %v", err)
	}
	if !seeded {
		t.Fatal("expected seeded=true")
	}

	dstDisk := s.DiskPath("vol-sparse")
	dstBlocks := fileBlocks(t, dstDisk)
	if dstBlocks > srcBlocks*2+8 {
		t.Errorf("copy not sparse: src blocks=%d dst blocks=%d", srcBlocks, dstBlocks)
	}

	srcData, err := os.ReadFile(diskPath)
	if err != nil {
		t.Fatalf("read src: %v", err)
	}
	dstData, err := os.ReadFile(dstDisk)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if !bytes.Equal(srcData, dstData) {
		t.Error("sparse copy content mismatch")
	}
}

// TestSeedFromWarm_SparseCopyFailure_NoPartialFiles verifies that a sparse copy
// failure leaves no partial files and subsequent Create can succeed.
func TestSeedFromWarm_SparseCopyFailure_NoPartialFiles(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	makeTinyWarm(t, s, key, WarmKindDocker, 4096)

	oldR := reflinkFileFn
	oldS := sparseCopyFileFn
	reflinkFileFn = func(dst, src *os.File) error {
		return fmt.Errorf("%w: injected", ErrReflinkUnsupported)
	}
	sparseCopyFileFn = func(_ context.Context, dst, src *os.File) error {
		return errors.New("injected sparse copy failure")
	}
	defer func() { reflinkFileFn = oldR; sparseCopyFileFn = oldS }()

	seeded, err := s.SeedFromWarm(context.Background(), "vol-sparsefail", key, WarmKindDocker, 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if seeded {
		t.Error("expected seeded=false")
	}
	if _, statErr := os.Stat(s.DiskPath("vol-sparsefail")); statErr == nil {
		t.Error("partial disk.ext4 left after sparse copy failure")
	}
	if _, err := s.Get("vol-sparsefail"); err == nil {
		t.Error("volume should not be registered after copy failure")
	}
}

// ── PromoteToWarm ─────────────────────────────────────────────────────────────

func TestPromoteToWarm_Basic(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	makeVolumeWithDisk(t, s, "vol-src", 4096)

	old := reflinkFileFn
	reflinkFileFn = copyClone
	defer func() { reflinkFileFn = old }()

	if err := s.PromoteToWarm(context.Background(), "vol-src", key, WarmKindDocker); err != nil {
		t.Fatalf("PromoteToWarm: %v", err)
	}
	if _, err := os.Stat(s.WarmDiskPath(key, WarmKindDocker)); err != nil {
		t.Errorf("warm disk not found: %v", err)
	}
	dirEntries, _ := os.ReadDir(s.warmKindDir(key, WarmKindDocker))
	for _, e := range dirEntries {
		n := e.Name()
		if n != diskFile && n != metaFile {
			t.Errorf("unexpected file after promote: %s", n)
		}
	}
	entries, _ := s.ListWarm()
	if len(entries) != 1 || entries[0].Meta.SourceVolume != "vol-src" {
		t.Errorf("unexpected ListWarm result: %+v", entries)
	}
}

func TestPromoteToWarm_ReplacesOldCopy(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	makeVolumeWithDisk(t, s, "vol-a", 4096)
	makeVolumeWithDisk(t, s, "vol-b", 4096)

	old := reflinkFileFn
	reflinkFileFn = copyClone
	defer func() { reflinkFileFn = old }()

	if err := s.PromoteToWarm(context.Background(), "vol-a", key, WarmKindDocker); err != nil {
		t.Fatalf("first promote: %v", err)
	}
	if err := s.PromoteToWarm(context.Background(), "vol-b", key, WarmKindDocker); err != nil {
		t.Fatalf("second promote: %v", err)
	}
	entries, _ := s.ListWarm()
	if len(entries) != 1 {
		t.Fatalf("expected 1 warm entry, got %d", len(entries))
	}
	if entries[0].Meta.SourceVolume != "vol-b" {
		t.Errorf("expected source=vol-b, got %s", entries[0].Meta.SourceVolume)
	}
}

func TestPromoteToWarm_RefusesAttachedVolume(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	makeVolumeWithDisk(t, s, "vol-att", 4096)
	rec, _ := s.readRecord("vol-att")
	rec.Attachments = []VolumeAttachment{{SandboxID: "sb1", AttachedAt: time.Now().UTC()}}
	if err := s.writeRecord(rec); err != nil {
		t.Fatalf("writeRecord: %v", err)
	}
	if err := s.PromoteToWarm(context.Background(), "vol-att", key, WarmKindDocker); err == nil {
		t.Error("expected error for attached volume")
	}
}

func TestPromoteToWarm_CloneFailureLeavesOldCopyIntact(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	makeVolumeWithDisk(t, s, "vol-orig", 4096)
	makeVolumeWithDisk(t, s, "vol-new", 4096)

	old := reflinkFileFn
	reflinkFileFn = copyClone
	if err := s.PromoteToWarm(context.Background(), "vol-orig", key, WarmKindDocker); err != nil {
		t.Fatalf("first promote: %v", err)
	}

	reflinkFileFn = func(dst, src *os.File) error { return errors.New("injected clone failure") }
	defer func() { reflinkFileFn = old }()

	if err := s.PromoteToWarm(context.Background(), "vol-new", key, WarmKindDocker); err == nil {
		t.Fatal("expected error from clone failure")
	}

	entries, _ := s.ListWarm()
	if len(entries) != 1 || entries[0].Meta.SourceVolume != "vol-orig" {
		t.Errorf("old warm copy corrupted: %+v", entries)
	}
	dirEntries, _ := os.ReadDir(s.warmKindDir(key, WarmKindDocker))
	for _, e := range dirEntries {
		n := e.Name()
		if n != diskFile && n != metaFile {
			t.Errorf("unexpected file after failed promote: %s", n)
		}
	}
}

func TestPromoteToWarm_RefusesNonDiskVolume(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	dir := s.volDir("vol-dir")
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	rec := &VolumeRecord{Name: "vol-dir", Kind: KindDir, CreatedAt: time.Now().UTC()}
	if err := s.writeRecord(rec); err != nil {
		t.Fatalf("writeRecord: %v", err)
	}
	if err := s.PromoteToWarm(context.Background(), "vol-dir", key, WarmKindDocker); err == nil {
		t.Error("expected error for non-disk kind")
	}
}

// TestPromoteToWarm_FallsBackToSparseCopy verifies promote uses sparse copy when reflink unsupported.
func TestPromoteToWarm_FallsBackToSparseCopy(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	makeVolumeWithDisk(t, s, "vol-spromote", 4096)

	oldR := reflinkFileFn
	reflinkFileFn = func(dst, src *os.File) error {
		return fmt.Errorf("%w: injected", ErrReflinkUnsupported)
	}
	defer func() { reflinkFileFn = oldR }()

	if err := s.PromoteToWarm(context.Background(), "vol-spromote", key, WarmKindDocker); err != nil {
		t.Fatalf("PromoteToWarm with sparse fallback: %v", err)
	}
	if _, err := os.Stat(s.WarmDiskPath(key, WarmKindDocker)); err != nil {
		t.Errorf("warm disk not found after sparse promote: %v", err)
	}
}

// TestPromoteToWarm_SparseCopyFailureLeavesOldCopyIntact verifies that if both
// reflink and sparse copy fail, the previously promoted warm copy is untouched.
func TestPromoteToWarm_SparseCopyFailureLeavesOldCopyIntact(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	makeVolumeWithDisk(t, s, "vol-orig2", 4096)
	makeVolumeWithDisk(t, s, "vol-new2", 4096)

	oldR := reflinkFileFn
	oldS := sparseCopyFileFn
	reflinkFileFn = copyClone
	if err := s.PromoteToWarm(context.Background(), "vol-orig2", key, WarmKindGoCache); err != nil {
		t.Fatalf("first promote: %v", err)
	}

	reflinkFileFn = func(dst, src *os.File) error {
		return fmt.Errorf("%w: injected", ErrReflinkUnsupported)
	}
	sparseCopyFileFn = func(_ context.Context, dst, src *os.File) error {
		return errors.New("injected sparse failure")
	}
	defer func() { reflinkFileFn = oldR; sparseCopyFileFn = oldS }()

	if err := s.PromoteToWarm(context.Background(), "vol-new2", key, WarmKindGoCache); err == nil {
		t.Fatal("expected error")
	}

	entries, _ := s.ListWarm()
	if len(entries) != 1 || entries[0].Meta.SourceVolume != "vol-orig2" {
		t.Errorf("old warm copy corrupted after sparse failure: %+v", entries)
	}
}

// ── ListWarm / RemoveWarm ─────────────────────────────────────────────────────

func TestListWarm_Empty(t *testing.T) {
	s := newWarmStore(t)
	entries, err := s.ListWarm()
	if err != nil {
		t.Fatalf("ListWarm: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty list, got %d", len(entries))
	}
}

func TestListWarm_SkipsUnreadable(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	wDir := s.warmKindDir(key, WarmKindDocker)
	if err := os.MkdirAll(wDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wDir, metaFile), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write bad meta: %v", err)
	}
	entries, err := s.ListWarm()
	if err != nil {
		t.Fatalf("ListWarm: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries (skip unreadable), got %d", len(entries))
	}
}

func TestRemoveWarm_ByKey(t *testing.T) {
	s := newWarmStore(t)
	k1 := "proj-aabbccddeeff"
	k2 := "proj-112233445566"
	makeTinyWarm(t, s, k1, WarmKindDocker, 4096)
	makeTinyWarm(t, s, k2, WarmKindDocker, 4096)

	removed, err := s.RemoveWarm(k1)
	if err != nil {
		t.Fatalf("RemoveWarm: %v", err)
	}
	if len(removed) != 1 || removed[0].ProjectKey != k1 {
		t.Errorf("unexpected removed: %+v", removed)
	}
	remaining, _ := s.ListWarm()
	if len(remaining) != 1 || remaining[0].ProjectKey != k2 {
		t.Errorf("unexpected remaining: %+v", remaining)
	}
}

func TestRemoveWarm_All(t *testing.T) {
	s := newWarmStore(t)
	makeTinyWarm(t, s, "proj-aabbccddeeff", WarmKindDocker, 4096)
	makeTinyWarm(t, s, "proj-112233445566", WarmKindGoCache, 4096)

	removed, err := s.RemoveWarm("")
	if err != nil {
		t.Fatalf("RemoveWarm: %v", err)
	}
	if len(removed) != 2 {
		t.Errorf("expected 2 removed, got %d", len(removed))
	}
	remaining, _ := s.ListWarm()
	if len(remaining) != 0 {
		t.Errorf("expected 0 remaining, got %d", len(remaining))
	}
}

// ── Real FICLONE integration ──────────────────────────────────────────────────

func TestFicloneProbe(t *testing.T) {
	supported := ficloneSupportedInTempDir(t)
	t.Logf("FICLONE supported in t.TempDir(): %v", supported)
}

func TestSeedFromWarm_RealReflink(t *testing.T) {
	if !ficloneSupportedInTempDir(t) {
		t.Skip("FICLONE not supported on this filesystem")
	}
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	makeTinyWarm(t, s, key, WarmKindGoPath, 4096)

	seeded, err := s.SeedFromWarm(context.Background(), "vol-reflink", key, WarmKindGoPath, 4096)
	if err != nil {
		t.Fatalf("SeedFromWarm (real reflink): %v", err)
	}
	if !seeded {
		t.Error("expected seeded=true with real reflink")
	}
}

func TestPromoteToWarm_RealReflink(t *testing.T) {
	if !ficloneSupportedInTempDir(t) {
		t.Skip("FICLONE not supported on this filesystem")
	}
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	makeVolumeWithDisk(t, s, "vol-promote", 4096)

	if err := s.PromoteToWarm(context.Background(), "vol-promote", key, WarmKindDocker); err != nil {
		t.Fatalf("PromoteToWarm (real reflink): %v", err)
	}
	if _, err := os.Stat(s.WarmDiskPath(key, WarmKindDocker)); err != nil {
		t.Errorf("warm disk not found after real promote: %v", err)
	}
}

func TestSeedFromWarm_InsufficientSpace(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	makeTinyWarm(t, s, key, WarmKindDocker, 4096)

	orig := DiskStatfs
	t.Cleanup(func() { DiskStatfs = orig })
	DiskStatfs = func(string) (int64, error) { return 0, nil }

	seeded, err := s.SeedFromWarm(context.Background(), "vol-nospace", key, WarmKindDocker, 4096)
	if err == nil {
		t.Fatal("expected error for insufficient space")
	}
	if seeded {
		t.Error("expected seeded=false on insufficient space")
	}
}

func TestPromoteToWarm_InsufficientSpace(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	makeVolumeWithDisk(t, s, "vol-nospace", 4096)

	orig := DiskStatfs
	t.Cleanup(func() { DiskStatfs = orig })
	DiskStatfs = func(string) (int64, error) { return 0, nil }

	if err := s.PromoteToWarm(context.Background(), "vol-nospace", key, WarmKindDocker); err == nil {
		t.Fatal("expected error for insufficient space")
	}
}

func TestSeedFromWarm_GrowPath(t *testing.T) {
	if !ShrinkToolsAvailable() || !Mke2fsAvailable() {
		t.Skip("e2fsprogs not on PATH")
	}
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	const smallSize int64 = 8 * 1024 * 1024
	const bigSize int64 = 16 * 1024 * 1024

	wDir := s.warmKindDir(key, WarmKindDocker)
	if err := os.MkdirAll(wDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	diskPath := filepath.Join(wDir, diskFile)
	if err := preallocateFile(diskPath, smallSize); err != nil {
		t.Fatalf("preallocate: %v", err)
	}
	if err := formatExt4(context.Background(), diskPath); err != nil {
		t.Fatalf("formatExt4: %v", err)
	}
	meta := WarmMeta{SourceVolume: "source-vol", PromotedAt: time.Now().UTC(), SizeBytes: smallSize}
	raw, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(wDir, metaFile), raw, 0o644); err != nil {
		t.Fatalf("write meta: %v", err)
	}

	seeded, err := s.SeedFromWarm(context.Background(), "vol-grow", key, WarmKindDocker, bigSize)
	if err != nil {
		t.Fatalf("SeedFromWarm grow: %v", err)
	}
	if !seeded {
		t.Fatal("expected seeded=true")
	}
	fi, err := os.Stat(s.DiskPath("vol-grow"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Size() != bigSize {
		t.Errorf("expected size %d, got %d", bigSize, fi.Size())
	}
}

func TestPromoteToWarm_StaleCleanup(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	makeVolumeWithDisk(t, s, "vol-stale", 4096)

	wDir := s.warmKindDir(key, WarmKindDocker)
	if err := os.MkdirAll(wDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	staleDisk := filepath.Join(wDir, "disk.ext4.tmp-1-1")
	staleMeta := filepath.Join(wDir, "meta.json.tmp-1-2")
	for _, f := range []string{staleDisk, staleMeta} {
		if err := os.WriteFile(f, []byte("stale"), 0o644); err != nil {
			t.Fatalf("write stale tmp: %v", err)
		}
		old2h := time.Now().Add(-2 * time.Hour)
		_ = os.Chtimes(f, old2h, old2h)
	}
	recentTmp := filepath.Join(wDir, "disk.ext4.tmp-2-2")
	if err := os.WriteFile(recentTmp, []byte("recent"), 0o644); err != nil {
		t.Fatalf("write recent tmp: %v", err)
	}

	oldR := reflinkFileFn
	oldSp := sparseCopyFileFn
	reflinkFileFn = func(dst, src *os.File) error {
		return fmt.Errorf("%w: injected", ErrReflinkUnsupported)
	}
	sparseCopyFileFn = func(_ context.Context, dst, src *os.File) error { return fmt.Errorf("injected") }
	defer func() { reflinkFileFn = oldR; sparseCopyFileFn = oldSp }()

	_ = s.PromoteToWarm(context.Background(), "vol-stale", key, WarmKindDocker)

	for _, f := range []string{staleDisk, staleMeta} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("stale tmp file should be removed: %s", f)
		}
	}
	if _, err := os.Stat(recentTmp); err != nil {
		t.Errorf("recent tmp file should survive: %v", err)
	}
}

func TestSeedFromWarm_CancelledCtx(t *testing.T) {
	s := newWarmStore(t)
	key := "proj-aabbccddeeff"
	makeTinyWarm(t, s, key, WarmKindDocker, 4096)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	oldR := reflinkFileFn
	reflinkFileFn = func(dst, src *os.File) error {
		return fmt.Errorf("%w: injected", ErrReflinkUnsupported)
	}
	defer func() { reflinkFileFn = oldR }()

	seeded, err := s.SeedFromWarm(ctx, "vol-cancel", key, WarmKindDocker, 4096)
	if err == nil {
		t.Fatal("expected error from cancelled ctx")
	}
	if seeded {
		t.Error("expected seeded=false on cancelled ctx")
	}
	if _, statErr := os.Stat(s.DiskPath("vol-cancel")); statErr == nil {
		t.Error("partial disk.ext4 must not exist after ctx cancel")
	}
}

// ── Benchmark ─────────────────────────────────────────────────────────────────

// TestWarmBench measures PromoteToWarm + SeedFromWarm on a 20 GiB sparse disk
// with ~4 GiB of actual data on the real volumes filesystem (not tmpfs).
// Run with: NEXUS_WARM_BENCH=1 make test GOTEST_PKGS=./internal/core/volumestore/ GOTEST_ARGS='-run TestWarmBench -v'
func TestWarmBench(t *testing.T) {
	if os.Getenv("NEXUS_WARM_BENCH") != "1" {
		t.Skip("set NEXUS_WARM_BENCH=1 to run")
	}
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("HOME not set")
	}
	scratchRoot := filepath.Join(home, ".local/state/nexus/volumes-scratch-fwr1")
	t.Cleanup(func() {
		if err := os.RemoveAll(scratchRoot); err != nil {
			t.Logf("cleanup scratchRoot: %v", err)
		} else {
			t.Log("scratchRoot removed")
		}
	})

	s := New(scratchRoot)
	const totalSize int64 = 20 << 30 // 20 GiB
	const chunkSize = 512 << 20      // 512 MiB per chunk, 8 chunks = 4 GiB data

	volName := "bench-src"
	if err := os.MkdirAll(s.volDir(volName), 0o755); err != nil {
		t.Fatal(err)
	}
	diskPath := s.DiskPath(volName)

	t.Log("creating 20 GiB sparse disk with ~4 GiB data...")
	f, err := os.Create(diskPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(totalSize); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if mke2fsBin, err := exec.LookPath("mke2fs"); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = exec.CommandContext(ctx, mke2fsBin, "-F", "-t", "ext4", "-E", "nodiscard", diskPath).Run()
		t.Log("mke2fs applied")
	}
	buf := make([]byte, 1<<20) // 1 MiB write buffer
	for i := range buf {
		buf[i] = byte(i) | 0x01
	}
	for chunk := int64(0); chunk < 8; chunk++ {
		offset := (totalSize / 9) * (chunk + 1)
		for written := int64(0); written < chunkSize; written += int64(len(buf)) {
			if _, err := f.WriteAt(buf, offset+written); err != nil {
				f.Close()
				t.Fatalf("write chunk %d: %v", chunk, err)
			}
		}
	}
	f.Close()
	t.Log("disk written")


	rec := &VolumeRecord{Name: volName, Kind: KindDisk, SizeBytes: totalSize, CreatedAt: time.Now().UTC()}
	if err := s.writeRecord(rec); err != nil {
		t.Fatal(err)
	}

	// Detect copy method by probing the scratch filesystem.
	method := "sparse-copy"
	{
		probeDir := filepath.Join(scratchRoot, ".bench-probe")
		_ = os.MkdirAll(probeDir, 0o755)
		f1, _ := os.CreateTemp(probeDir, "src")
		_ = f1.Truncate(4096)
		f2, _ := os.CreateTemp(probeDir, "dst")
		_ = f2.Truncate(4096)
		if reflinkFileFn(f2, f1) == nil {
			method = "reflink"
		}
		f1.Close()
		f2.Close()
		_ = os.RemoveAll(probeDir)
	}

	key := "bench-project-aabbccddeeff"

	t0 := time.Now()
	if err := s.PromoteToWarm(context.Background(), volName, key, WarmKindDocker); err != nil {
		t.Fatalf("PromoteToWarm: %v", err)
	}
	promoteElapsed := time.Since(t0)

	t0 = time.Now()
	seeded, err := s.SeedFromWarm(context.Background(), "bench-dst", key, WarmKindDocker, totalSize)
	seedElapsed := time.Since(t0)
	if err != nil {
		t.Fatalf("SeedFromWarm: %v", err)
	}
	if !seeded {
		t.Fatal("expected seeded=true")
	}

	t.Logf("allocated: src=%d MiB seeded=%d MiB", fileBlocks(t, diskPath)*512>>20, fileBlocks(t, s.DiskPath("bench-dst"))*512>>20)
	t.Logf("method: %s", method)
	t.Logf("PromoteToWarm elapsed: %v", promoteElapsed)
	t.Logf("SeedFromWarm elapsed: %v", seedElapsed)
}
