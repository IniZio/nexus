package service_test

// Tests for shadow disk enumeration and classification (spec §4.4).
//
// Requirement traceability:
//   B7-AC1: nexus3 reap reports shadow disks with allocated bytes.
//   B7-AC2: Legacy-format shadow disks classify as orphan unconditionally.
//   B7-AC3: B1-format shadow disks classify as orphan when handle matches
//           no live sandbox; owned when the handle matches a live sandbox.
//   B7-AC4: The liveness gate still applies — a shadow disk whose owner IS
//           live is kept (B7-AC4 "live-owner KEEP" test).

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── helpers ───────────────────────────────────────────────────────────────────

// makeStore returns a FileStore with a single sandbox record whose Handle()
// returns project+"/"+name.
func makeStoreWithSandbox(t *testing.T, project, name string) (store.Store, domain.SandboxID) {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	id := domain.NewSandboxID()
	if err := st.Create(context.Background(), domain.Sandbox{
		ID:      id,
		Name:    name,
		Project: project,
		State:   domain.Stopped,
	}); err != nil {
		t.Fatalf("st.Create: %v", err)
	}
	return st, id
}

// mustWriteShadowDisk creates a shadow disk file with a small amount of real
// data so AllocatedBytes is non-zero, and returns its path.
func mustWriteShadowDisk(t *testing.T, dir, filename string) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, make([]byte, 4096), 0666); err != nil {
		t.Fatalf("write shadow disk %s: %v", path, err)
	}
	return path
}

// ── enumeration tests ─────────────────────────────────────────────────────────

// TestResourceIndex_ShadowDisksEnumerated verifies that both legacy and B1-format
// shadow disks appear in List() output as KindDiskShadow resources without any
// record store access.
//
// @verifies B7-AC1, R1-AC1 (record-free property still holds)
func TestResourceIndex_ShadowDisksEnumerated(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	// Legacy shadow disks (no embedded handle).
	legacyFiles := []string{
		"node_modules.shadow.ext4",
		"dist.shadow.ext4",
	}
	// B1-format shadow disks (embedded safeHandle).
	b1Files := []string{
		"myproj_sandboxA.shadow.node_modules.ext4",
		"myproj_sandboxA.shadow.dist.ext4",
		"other_project_sandboxB.shadow.target.ext4",
	}
	for _, f := range append(legacyFiles, b1Files...) {
		mustTouch(t, filepath.Join(disksDir, f))
	}

	// No store — enumeration must be record-free.
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})
	resources, err := idx.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// Collect shadow disk resources.
	var shadows []service.HostResource
	for _, r := range resources {
		if r.Kind == service.KindDiskShadow {
			shadows = append(shadows, r)
		}
	}

	total := len(legacyFiles) + len(b1Files)
	if got := len(shadows); got != total {
		t.Fatalf("got %d shadow disk resources, want %d", got, total)
	}

	// Legacy: ShadowHandle must be empty.
	legacyCount := 0
	b1Count := 0
	for _, r := range shadows {
		base := filepath.Base(r.Path)
		isLegacy := false
		for _, f := range legacyFiles {
			if base == f {
				isLegacy = true
				break
			}
		}
		if isLegacy {
			if r.ShadowHandle != "" {
				t.Errorf("%s: ShadowHandle = %q, want empty (legacy format)", base, r.ShadowHandle)
			}
			legacyCount++
		} else {
			if r.ShadowHandle == "" {
				t.Errorf("%s: ShadowHandle is empty, want non-empty (B1 format)", base)
			}
			b1Count++
		}
		// OwnerID must be zero for shadow disks.
		if r.OwnerID != (domain.SandboxID{}) {
			t.Errorf("%s: OwnerID is non-zero for shadow disk", base)
		}
	}
	if legacyCount != len(legacyFiles) {
		t.Errorf("legacyCount = %d, want %d", legacyCount, len(legacyFiles))
	}
	if b1Count != len(b1Files) {
		t.Errorf("b1Count = %d, want %d", b1Count, len(b1Files))
	}
}

// TestResourceIndex_RecordFree_UnaffectedByShadowEnumeration verifies that
// adding shadow disk enumeration does NOT change the record-free property for
// non-shadow resources. This is a regression guard on TestResourceIndex_RecordFree.
//
// @verifies R1-AC1
func TestResourceIndex_RecordFree_UnaffectedByShadowEnumeration(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	// Add a shadow disk alongside ordinary ULID-keyed disks.
	id := domain.NewSandboxID()
	mustTouch(t, filepath.Join(disksDir, id.String()+".raw"))
	mustTouch(t, filepath.Join(disksDir, "node_modules.shadow.ext4")) // legacy shadow

	// No store — must not be needed.
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})
	resources, err := idx.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Expect 2: the raw disk + the shadow disk.
	if got, want := len(resources), 2; got != want {
		t.Fatalf("List returned %d resources, want %d: %v", got, want, resources)
	}
}

// ── classification tests ──────────────────────────────────────────────────────

// TestReap_ShadowDisk_LegacyIsOrphan verifies that legacy-format shadow disks
// (*.shadow.ext4, no embedded handle) are always classified as orphans.
//
// @verifies B7-AC2
func TestReap_ShadowDisk_LegacyIsOrphan(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	legacyPaths := []string{
		mustWriteShadowDisk(t, disksDir, "node_modules.shadow.ext4"),
		mustWriteShadowDisk(t, disksDir, "dist.shadow.ext4"),
		mustWriteShadowDisk(t, disksDir, "target.shadow.ext4"),
		mustWriteShadowDisk(t, disksDir, ".next.shadow.ext4"),
	}

	// Store has no records — ensures ownership comes from disk name, not store.
	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	ctx := context.Background()
	report, err := service.Reap(ctx, st, idx, false /*dry-run*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	orphanPaths := make(map[string]bool)
	for _, e := range report.Entries {
		if e.Status == service.ReapStatusOrphan && e.Resource.Kind == service.KindDiskShadow {
			orphanPaths[e.Resource.Path] = true
		}
	}

	for _, p := range legacyPaths {
		if !orphanPaths[p] {
			t.Errorf("legacy shadow disk %s not classified as orphan", filepath.Base(p))
		}
	}

	if report.ReclaimableBytes <= 0 {
		t.Errorf("ReclaimableBytes = %d, want > 0 (shadow disks have data)", report.ReclaimableBytes)
	}
}

// TestReap_ShadowDisk_B1NoMatchIsOrphan verifies that B1-format shadow disks
// whose safeHandle matches no live sandbox are classified as orphans.
//
// @verifies B7-AC3
func TestReap_ShadowDisk_B1NoMatchIsOrphan(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	// B1 disks for a handle "gone/project" → safeHandle "gone_project".
	diskPaths := []string{
		mustWriteShadowDisk(t, disksDir, "gone_project.shadow.node_modules.ext4"),
		mustWriteShadowDisk(t, disksDir, "gone_project.shadow.dist.ext4"),
	}

	// Store is empty — no sandbox with handle "gone/project".
	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	ctx := context.Background()
	report, err := service.Reap(ctx, st, idx, false /*dry-run*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	orphanPaths := make(map[string]bool)
	for _, e := range report.Entries {
		if e.Status == service.ReapStatusOrphan {
			orphanPaths[e.Resource.Path] = true
		}
	}
	for _, p := range diskPaths {
		if !orphanPaths[p] {
			t.Errorf("B1 shadow disk %s should be orphan when no matching handle", filepath.Base(p))
		}
	}
}

// TestReap_ShadowDisk_LiveOwnerKept verifies that a B1-format shadow disk
// whose safeHandle matches a live sandbox record is NOT classified as an orphan
// and is NOT deleted even when apply=true.
//
// This is the "live-owner KEEP" acceptance test required by the B7 spec.
//
// @verifies B7-AC4
func TestReap_ShadowDisk_LiveOwnerKept(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	// The sandbox has handle "myproj/live-sandbox" →
	// safeHandle "myproj_live-sandbox".
	project := "myproj"
	name := "live-sandbox"
	st, _ := makeStoreWithSandbox(t, project, name)

	safeHandle := "myproj_live-sandbox"
	ownedPaths := []string{
		mustWriteShadowDisk(t, disksDir, safeHandle+".shadow.node_modules.ext4"),
		mustWriteShadowDisk(t, disksDir, safeHandle+".shadow.dist.ext4"),
	}
	// Also create an orphan disk from a different handle to confirm orphan
	// classification still works for unmatched B1 disks in the same directory.
	orphanPath := mustWriteShadowDisk(t, disksDir, "dead_handle.shadow.node_modules.ext4")

	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	ctx := context.Background()
	// apply=true to prove owned disks survive deletion.
	report, err := service.Reap(ctx, st, idx, true /*apply*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	byPath := make(map[string]service.ReapEntry, len(report.Entries))
	for _, e := range report.Entries {
		byPath[e.Resource.Path] = e
	}

	// Owned disks must be ReapStatusOwned and must still exist on disk.
	for _, p := range ownedPaths {
		e, ok := byPath[p]
		if !ok {
			t.Fatalf("owned shadow disk %s missing from report", filepath.Base(p))
		}
		if e.Status != service.ReapStatusOwned {
			t.Errorf("%s: status = %q, want ReapStatusOwned", filepath.Base(p), e.Status)
		}
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("owned shadow disk %s was deleted (liveness gate violation): %v", filepath.Base(p), statErr)
		}
	}

	// Orphan disk must be classified and deleted.
	e, ok := byPath[orphanPath]
	if !ok {
		t.Fatalf("orphan shadow disk %s missing from report", filepath.Base(orphanPath))
	}
	if e.Status != service.ReapStatusOrphan {
		t.Errorf("dead_handle shadow disk: status = %q, want ReapStatusOrphan", e.Status)
	}
	if _, statErr := os.Stat(orphanPath); !os.IsNotExist(statErr) {
		t.Errorf("orphan shadow disk should have been deleted, stat err: %v", statErr)
	}

	// Only the orphan should be in Deleted.
	if len(report.Deleted) != 1 || report.Deleted[0] != orphanPath {
		t.Errorf("Deleted = %v, want [%s]", report.Deleted, orphanPath)
	}
}

// TestReap_ShadowDisk_AllocatedBytes verifies that AllocatedBytes for shadow
// disks is reported as stat(2).Blocks*512, not apparent size (P-2 / P13
// illusion guard).
//
// @verifies B7-AC1 (allocated bytes, not apparent)
func TestReap_ShadowDisk_AllocatedBytes(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	// Sparse file — large apparent size, tiny allocated.
	const apparent = int64(10 * 1024 * 1024 * 1024) // 10 GiB apparent
	path := filepath.Join(disksDir, "node_modules.shadow.ext4")
	createSparseFile(t, path, apparent)

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	ctx := context.Background()
	report, err := service.Reap(ctx, st, idx, false /*dry-run*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	if len(report.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(report.Entries))
	}
	e := report.Entries[0]
	// Allocated must be much less than apparent.
	if e.AllocatedBytes >= apparent {
		t.Errorf("AllocatedBytes = %d >= apparent %d: reporting apparent size (P13 illusion)", e.AllocatedBytes, apparent)
	}
}

// TestReap_ShadowDisk_ApplyDeletesOrphans verifies that --apply removes orphan
// shadow disks and reports them in Deleted.
//
// @verifies B7-AC1
func TestReap_ShadowDisk_ApplyDeletesOrphans(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	paths := []string{
		mustWriteShadowDisk(t, disksDir, "node_modules.shadow.ext4"),        // legacy
		mustWriteShadowDisk(t, disksDir, "orphan_handle.shadow.dist.ext4"),  // B1, no record
	}

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	ctx := context.Background()
	report, err := service.Reap(ctx, st, idx, true /*apply*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	if got, want := len(report.Deleted), len(paths); got != want {
		t.Errorf("Deleted count = %d, want %d", got, want)
	}
	for _, p := range paths {
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Errorf("orphan shadow disk %s not deleted after apply=true", filepath.Base(p))
		}
	}
}
