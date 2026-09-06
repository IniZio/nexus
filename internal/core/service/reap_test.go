package service_test

// Tests for ResourceIndex and Reap.
//
// Requirement traceability:
//   R1-AC1: ResourceIndex.List() never consults the record store.
//   R1-AC2: Reaper classifies ALL filesystem resources, not just those with records.
//   N-AC2:  A resource with a live record is NOT deleted even with --apply.
//   N-AC3:  Dry-run (apply=false) never deletes any file.

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// createSparseFile creates a sparse file with the given apparent size. Only the
// last block is physically allocated; the interior is a hole. On most Linux
// filesystems the allocated block count is 0 or 1 (a few KB), not apparentSize.
func createSparseFile(t *testing.T, path string, apparentSize int64) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("createSparseFile: create %s: %v", path, err)
	}
	defer f.Close()
	_, err = f.Seek(apparentSize-1, io.SeekStart)
	if err != nil {
		t.Fatalf("createSparseFile: seek: %v", err)
	}
	_, err = f.Write([]byte{0})
	if err != nil {
		t.Fatalf("createSparseFile: write: %v", err)
	}
}

// mustMkdir creates a directory or fails the test.
func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

// mustTouch creates an empty file or fails the test.
func mustTouch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte{}, 0600); err != nil {
		t.Fatalf("touch %s: %v", path, err)
	}
}

// mustWriteFile writes data to a file or fails the test.
func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// newEmptyStore creates a FileStore backed by a temporary directory with no
// sandbox records. The store satisfies store.Store.
func newEmptyStore(t *testing.T) store.Store {
	t.Helper()
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	return st
}

// TestResourceIndex_RecordFree verifies that ResourceIndex.List() enumerates
// host resources purely from the filesystem without consulting any record store.
//
// @verifies R1-AC1
func TestResourceIndex_RecordFree(t *testing.T) {
	stateRoot := t.TempDir()
	sockDir := t.TempDir()

	disksDir := filepath.Join(stateRoot, "disks")
	bsDir := filepath.Join(stateRoot, "builder-supervisors")
	mustMkdir(t, disksDir)
	mustMkdir(t, bsDir)

	id1 := domain.NewSandboxID()
	id2 := domain.NewSandboxID()
	id3 := domain.NewSandboxID()
	id4 := domain.NewSandboxID()

	// 3 disk files
	mustTouch(t, filepath.Join(disksDir, id1.String()+".raw"))
	mustTouch(t, filepath.Join(disksDir, id2.String()+".raw"))
	mustTouch(t, filepath.Join(disksDir, id2.String()+"-workspace.ext4"))

	// 1 builder-supervisor directory
	mustMkdir(t, filepath.Join(bsDir, id3.String()))

	// 3 socket files
	mustTouch(t, filepath.Join(sockDir, id4.String()+".sock"))
	mustTouch(t, filepath.Join(sockDir, id4.String()+".vsock"))
	mustTouch(t, filepath.Join(sockDir, id4.String()+".iid"))

	// No FileStore created — the index must NOT need one.
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})
	resources, err := idx.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// 7 resources: 3 disks + 1 builder-supervisor + 3 sockets (.sock/.vsock/.iid)
	if got, want := len(resources), 7; got != want {
		t.Fatalf("List returned %d resources, want %d", got, want)
	}

	// Build a set of (kind, ownerID) pairs for assertion.
	type pair struct {
		kind service.ResourceKind
		id   domain.SandboxID
	}
	got := make(map[pair]bool, len(resources))
	for _, r := range resources {
		got[pair{r.Kind, r.OwnerID}] = true
	}

	expect := []pair{
		{service.KindDiskRaw, id1},
		{service.KindDiskRaw, id2},
		{service.KindDiskWorkspace, id2},
		{service.KindBuilderSupervisor, id3},
		{service.KindSocketAPI, id4},
		{service.KindSocketVSock, id4},
		{service.KindSocketIID, id4},
	}
	for _, p := range expect {
		if !got[p] {
			t.Errorf("missing resource kind=%s ownerID=%s", p.kind, p.id)
		}
	}
}

// TestResourceIndex_RecordFree_ShadowDisks is the architectural guard for
// shadow disk enumeration.  It verifies that KindDiskShadow resources are
// discovered by List() WITHOUT any record store instance — the same record-
// free property that TestResourceIndex_RecordFree establishes for ULID-keyed
// resources.
//
// Shadow disks are the only kind whose *classification* requires record access
// (to map a safeHandle to a live sandbox).  That access belongs in the
// classification phase (Reap), not in enumeration (List).  This test guards
// that seam: if enumeration is ever made record-dependent, no store instance
// exists here to consult, so the test would fail with a nil-dereference or
// wrong count — not silently pass.
//
// Mutation proof: temporarily removing the KindDiskShadow case from List()
// (or gating it on a non-existent store) causes this test to fail with:
//
//	List returned N resources, want N+2 (shadow disks missing)
//
// or a specific missing-resource error for each shadow file.
//
// @verifies R1-AC1 for KindDiskShadow
func TestResourceIndex_RecordFree_ShadowDisks(t *testing.T) {
	stateRoot := t.TempDir()
	sockDir := t.TempDir()

	disksDir := filepath.Join(stateRoot, "disks")
	bsDir := filepath.Join(stateRoot, "builder-supervisors")
	mustMkdir(t, disksDir)
	mustMkdir(t, bsDir)

	id1 := domain.NewSandboxID()
	id2 := domain.NewSandboxID()
	id3 := domain.NewSandboxID()
	id4 := domain.NewSandboxID()

	// Same 7 ULID-keyed resources as TestResourceIndex_RecordFree.
	mustTouch(t, filepath.Join(disksDir, id1.String()+".raw"))
	mustTouch(t, filepath.Join(disksDir, id2.String()+".raw"))
	mustTouch(t, filepath.Join(disksDir, id2.String()+"-workspace.ext4"))
	mustMkdir(t, filepath.Join(bsDir, id3.String()))
	mustTouch(t, filepath.Join(sockDir, id4.String()+".sock"))
	mustTouch(t, filepath.Join(sockDir, id4.String()+".vsock"))
	mustTouch(t, filepath.Join(sockDir, id4.String()+".iid"))

	// Two shadow disk files — one B1-format (embedded safeHandle), one legacy.
	const b1Handle = "testproj_shadow-guard-b1"
	b1Path := filepath.Join(disksDir, b1Handle+".shadow.node_modules.ext4")
	legacyPath := filepath.Join(disksDir, "node_modules.shadow.ext4")
	mustTouch(t, b1Path)
	mustTouch(t, legacyPath)

	// No FileStore created — enumeration MUST NOT need one.
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})
	resources, err := idx.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	// 9 resources: 7 ULID-keyed + 1 B1 shadow + 1 legacy shadow.
	if got, want := len(resources), 9; got != want {
		t.Fatalf("List returned %d resources, want %d\n(if shadow disks are missing, enumeration became record-dependent)", got, want)
	}

	// Locate the two shadow disk resources.
	var b1Entry, legacyEntry *service.HostResource
	for i := range resources {
		r := &resources[i]
		if r.Kind != service.KindDiskShadow {
			continue
		}
		switch r.Path {
		case b1Path:
			b1Entry = r
		case legacyPath:
			legacyEntry = r
		}
	}

	if b1Entry == nil {
		t.Fatalf("B1 shadow disk %s not found in List() output (enumeration is record-dependent?)", b1Path)
	}
	if legacyEntry == nil {
		t.Fatalf("legacy shadow disk %s not found in List() output (enumeration is record-dependent?)", legacyPath)
	}

	// B1-format: ShadowHandle must carry the embedded handle.
	if got, want := b1Entry.ShadowHandle, b1Handle; got != want {
		t.Errorf("B1 shadow disk ShadowHandle = %q, want %q", got, want)
	}
	// B1-format: OwnerID must be zero (no ULID).
	if b1Entry.OwnerID != (domain.SandboxID{}) {
		t.Errorf("B1 shadow disk OwnerID is non-zero: %s", b1Entry.OwnerID)
	}

	// Legacy format: ShadowHandle must be empty (no embedded handle).
	if got := legacyEntry.ShadowHandle; got != "" {
		t.Errorf("legacy shadow disk ShadowHandle = %q, want empty", got)
	}
}

// TestResourceIndex_SkipsNonULID verifies that files whose names do not parse
// as a SandboxID are silently skipped by List().
func TestResourceIndex_SkipsNonULID(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	// Non-ULID files — must be skipped.
	mustTouch(t, filepath.Join(disksDir, "some-random-file.raw"))
	mustTouch(t, filepath.Join(disksDir, "not-a-sandbox.ext4"))

	// One valid disk — must be returned.
	id := domain.NewSandboxID()
	mustTouch(t, filepath.Join(disksDir, id.String()+".raw"))

	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(), // empty socket dir
	})
	resources, err := idx.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if got, want := len(resources), 1; got != want {
		t.Fatalf("List returned %d resources, want %d; got: %v", got, want, resources)
	}
	if resources[0].OwnerID != id {
		t.Errorf("got ownerID=%s, want %s", resources[0].OwnerID, id)
	}
	if resources[0].Kind != service.KindDiskRaw {
		t.Errorf("got kind=%s, want KindDiskRaw", resources[0].Kind)
	}
}

// TestReap_SyntheticP13Fixture is the primary correctness test for the reaper.
//
// It creates a P13-style state: 829 sparse ("tiny") orphan disks and 5 large
// orphan disks, with NO records in the store. The reaper MUST classify all 834
// resources as orphans. This test would fail if List() were record-driven (a
// store-driven index would return 0 resources because the store is empty).
//
// @verifies R1-AC2
func TestReap_SyntheticP13Fixture(t *testing.T) {
	const (
		tinyCount    = 829
		largeCount   = 5
		totalCount   = tinyCount + largeCount
		largeSize    = 1024 * 1024 // 1 MiB — real allocation
		tinyApparent = 100 * 1024 * 1024
	)

	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	// Sparse ("tiny") disks — huge apparent size, ~0 allocated blocks.
	for i := 0; i < tinyCount; i++ {
		id := domain.NewSandboxID()
		createSparseFile(t, filepath.Join(disksDir, id.String()+".raw"), tinyApparent)
	}

	// Large disks — 1 MiB of real data, non-zero allocated blocks.
	largeIDs := make([]domain.SandboxID, largeCount)
	for i := 0; i < largeCount; i++ {
		id := domain.NewSandboxID()
		largeIDs[i] = id
		mustWriteFile(t, filepath.Join(disksDir, id.String()+".raw"), make([]byte, largeSize))
	}

	// Store backed by a DIFFERENT tempdir — no records at all.
	st := newEmptyStore(t)

	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(), // empty; no sockets
	})

	// Inject an empty synthetic /proc dir (no numeric subdirectories) so the
	// proc scan returns procScanDead for every ULID. Without this, a process
	// that exits between ReadDir and Open causes ENOENT → procScanAmbiguous →
	// resource incorrectly classified as live, making the test flaky.
	emptyProc := t.TempDir()

	ctx := context.Background()
	report, err := service.Reap(ctx, st, idx, false /*dry-run*/, service.ReapOptions{ProcDir: emptyProc})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	// All 834 disk resources must appear.
	if got, want := len(report.Entries), totalCount; got != want {
		t.Fatalf("len(report.Entries) = %d, want %d", got, want)
	}

	// Every entry must be an orphan — none have a record in the store.
	orphanCount := 0
	for i, e := range report.Entries {
		if e.Status != service.ReapStatusOrphan {
			t.Errorf("entries[%d] status = %q, want ReapStatusOrphan (path=%s)", i, e.Status, e.Resource.Path)
		} else {
			orphanCount++
		}
	}
	if orphanCount != totalCount {
		t.Errorf("orphanCount = %d, want %d", orphanCount, totalCount)
	}

	// Large files contribute non-zero ReclaimableBytes.
	if report.ReclaimableBytes <= 0 {
		t.Errorf("ReclaimableBytes = %d, want > 0 (large files should contribute)", report.ReclaimableBytes)
	}

	// Dry-run: nothing deleted.
	if got := len(report.Deleted); got != 0 {
		t.Errorf("len(report.Deleted) = %d, want 0 (dry-run must not delete)", got)
	}

	// Sanity: the 5 large files must register meaningful allocated bytes.
	// Build a set for fast lookup.
	largePaths := make(map[string]bool, largeCount)
	for _, id := range largeIDs {
		largePaths[filepath.Join(disksDir, id.String()+".raw")] = true
	}
	largeWithAlloc := 0
	for _, e := range report.Entries {
		if largePaths[e.Resource.Path] && e.AllocatedBytes > 0 {
			largeWithAlloc++
		}
	}
	if largeWithAlloc != largeCount {
		t.Errorf("%d large files have AllocatedBytes > 0, want %d", largeWithAlloc, largeCount)
	}
}

// TestReap_N_AC2_RecordProtectsResource verifies that a resource whose ULID has
// a record in the store is NOT deleted even when apply=true is passed.
//
// @verifies N-AC2
func TestReap_N_AC2_RecordProtectsResource(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	id := domain.NewSandboxID()
	diskPath := filepath.Join(disksDir, id.String()+".raw")
	mustWriteFile(t, diskPath, make([]byte, 1024)) // 1 KiB

	// Insert a record for this sandbox.
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	if createErr := st.Create(context.Background(), domain.Sandbox{
		ID:      id,
		Name:    "test",
		Project: "proj",
		State:   domain.Stopped,
	}); createErr != nil {
		t.Fatalf("st.Create: %v", createErr)
	}

	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	ctx := context.Background()
	report, err := service.Reap(ctx, st, idx, true /*apply*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	if got := len(report.Entries); got != 1 {
		t.Fatalf("len(report.Entries) = %d, want 1", got)
	}
	if got, want := report.Entries[0].Status, service.ReapStatusOwned; got != want {
		t.Errorf("entry status = %q, want %q", got, want)
	}

	// The file must still exist.
	if _, statErr := os.Stat(diskPath); statErr != nil {
		t.Errorf("disk file deleted despite having a live record: %v", statErr)
	}

	if got := len(report.Deleted); got != 0 {
		t.Errorf("len(report.Deleted) = %d, want 0", got)
	}
}

// TestReap_N_AC3_DryRunDeletesNothing verifies that calling Reap with apply=false
// never removes files, even when resources are classified as orphans.
//
// @verifies N-AC3
func TestReap_N_AC3_DryRunDeletesNothing(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	paths := make([]string, 3)
	for i := range paths {
		id := domain.NewSandboxID()
		p := filepath.Join(disksDir, id.String()+".raw")
		mustTouch(t, p)
		paths[i] = p
	}

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

	if got, want := len(report.Entries), 3; got != want {
		t.Fatalf("len(report.Entries) = %d, want %d", got, want)
	}
	for i, e := range report.Entries {
		if e.Status != service.ReapStatusOrphan {
			t.Errorf("entries[%d] status = %q, want ReapStatusOrphan", i, e.Status)
		}
	}

	// All 3 files must still exist.
	for _, p := range paths {
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("file %s was deleted by dry-run: %v", p, statErr)
		}
	}

	if got := len(report.Deleted); got != 0 {
		t.Errorf("len(report.Deleted) = %d, want 0 (dry-run must not delete)", got)
	}
}

// TestReap_ApplyDeletesOrphans verifies that Reap with apply=true removes orphan
// files and reports them in report.Deleted.
func TestReap_ApplyDeletesOrphans(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	paths := make([]string, 3)
	for i := range paths {
		id := domain.NewSandboxID()
		p := filepath.Join(disksDir, id.String()+".raw")
		mustTouch(t, p)
		paths[i] = p
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

	if got, want := len(report.Entries), 3; got != want {
		t.Fatalf("len(report.Entries) = %d, want %d", got, want)
	}
	for i, e := range report.Entries {
		if e.Status != service.ReapStatusOrphan {
			t.Errorf("entries[%d] status = %q, want ReapStatusOrphan", i, e.Status)
		}
	}

	// All 3 files must be gone.
	for _, p := range paths {
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Errorf("orphan file %s not deleted after apply=true", p)
		}
	}

	if got, want := len(report.Deleted), 3; got != want {
		t.Errorf("len(report.Deleted) = %d, want %d", got, want)
	}
}

// TestReap_LiveUni5OrphanReported verifies that the known uni5 orphan disk on
// this host appears in the reap report as an orphan and is NOT deleted (dry-run).
//
// The test is skipped when the orphan file is absent — it is a best-effort
// live integration probe, not a hermetic unit test.
//
// @verifies N-AC3
func TestReap_LiveUni5OrphanReported(t *testing.T) {
	const uni5ID = "sb-06FZZX7V8XZM12YE7VTR7T8168"

	root, err := store.DefaultRoot()
	if err != nil {
		t.Skip("cannot determine state root:", err)
	}
	diskPath := filepath.Join(root, "disks", uni5ID+".raw")
	if _, statErr := os.Stat(diskPath); os.IsNotExist(statErr) {
		t.Skip("uni5 orphan disk not present on this host")
	}

	st, err := store.NewFileStore(root)
	if err != nil {
		t.Fatalf("NewFileStore(%s): %v", root, err)
	}

	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: root,
		// SocketDir left empty → uses XDG_RUNTIME_DIR or TMPDIR-derived default.
	})

	ctx := context.Background()
	report, err := service.Reap(ctx, st, idx, false /*dry-run*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	// Find the uni5 orphan in the report.
	var found *service.ReapEntry
	for i := range report.Entries {
		if report.Entries[i].Resource.Path == diskPath {
			found = &report.Entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("uni5 orphan disk %s not found in report.Entries", diskPath)
	}
	// uni5's record exists (state=running, stale) so R1 correctly classifies it
	// as ReapStatusOwned — reclaiming stale records is R2's job.
	// R1 must report it (it appears in Entries) but must NOT reclaim it.
	if found.Status != service.ReapStatusOwned {
		t.Errorf("uni5 entry status = %q, want ReapStatusOwned (record exists; R1 must keep it)", found.Status)
	}
	// Allocated bytes must be reported as stat(2).Blocks*512, not apparent size.
	// uni5 has ~120 MiB allocated but 4 GiB apparent; confirm we avoid the P13 illusion.
	const gib4 = int64(4) * 1024 * 1024 * 1024
	if found.AllocatedBytes >= gib4 {
		t.Errorf("uni5 AllocatedBytes = %d >= 4 GiB apparent: reporting apparent size, not allocated (P13 illusion)", found.AllocatedBytes)
	}

	// Dry-run: disk must still exist (N-AC3).
	if _, statErr := os.Stat(diskPath); statErr != nil {
		t.Errorf("uni5 disk was deleted by dry-run: %v", statErr)
	}

	if got := len(report.Deleted); got != 0 {
		t.Errorf("len(report.Deleted) = %d after dry-run, want 0", got)
	}
}

// TestResourceIndex_EnumeratesCreateIntent verifies that create-intent files
// are enumerated by ResourceIndex.List() as KindCreateIntent resources.
//
// @verifies R1-AC1 (intent files are real host resources that must be visible)
func TestResourceIndex_EnumeratesCreateIntent(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	id := domain.NewSandboxID()
	intentFile := filepath.Join(disksDir, id.String()+".create-intent.json")
	mustWriteFile(t, intentFile, []byte(`{"id":"`+id.String()+`"}`))

	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})
	resources, err := idx.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var found *service.HostResource
	for i := range resources {
		if resources[i].Kind == service.KindCreateIntent && resources[i].OwnerID == id {
			found = &resources[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("create-intent file not found in List() output; resources: %v", resources)
	}
	if found.Path != intentFile {
		t.Errorf("wrong path: got %s, want %s", found.Path, intentFile)
	}
}

// TestReap_ConcurrentCreateInFlight verifies N-AC2 for the create-in-flight
// scenario: a create started but not committed yet has an intent file on disk
// and no store record. If a live process for that ULID is found, the reaper
// must NOT report it as an orphan — even with --apply.
//
// A synthetic /proc dir with one PID entry containing the ULID stands in for
// the real mid-create process.
//
// @verifies N-AC2 (live create process = liveness check positive = KEEP)
func TestReap_ConcurrentCreateInFlight(t *testing.T) {
	// Set up state root with an intent file (no record in store).
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	id := domain.NewSandboxID()
	intentFile := filepath.Join(disksDir, id.String()+".create-intent.json")
	mustWriteFile(t, intentFile, []byte(`{"id":"`+id.String()+`","disk_copy_path":"`+
		filepath.Join(disksDir, id.String()+".raw")+`"}`))
	// The .raw file itself is not yet materialized — create is in flight.

	// Synthetic /proc dir: one PID whose cmdline contains the ULID (simulates
	// the create process passing --api-socket containing the ULID).
	procDir := t.TempDir()
	pidDir := filepath.Join(procDir, "42000")
	if err := os.MkdirAll(pidDir, 0700); err != nil {
		t.Fatal(err)
	}
	cmdline := "nexus3\x00create\x00--file\x00rootfs.ext4\x00--id\x00" + id.String() + "\x00"
	if err := os.WriteFile(filepath.Join(pidDir, "cmdline"), []byte(cmdline), 0600); err != nil {
		t.Fatal(err)
	}

	st := newEmptyStore(t) // no records — create hasn't committed yet
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	ctx := context.Background()
	// Use ProcDir override to inject synthetic /proc.
	report, err := service.Reap(ctx, st, idx, true /*apply*/, service.ReapOptions{ProcDir: procDir})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	// The intent file must appear in the report but must NOT be an orphan.
	var found *service.ReapEntry
	for i := range report.Entries {
		if report.Entries[i].Resource.Path == intentFile {
			found = &report.Entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("intent file not found in report at all")
	}
	// Must be LIVE (process found), not ORPHAN.
	if found.Status == service.ReapStatusOrphan {
		t.Errorf("in-flight create classified as orphan — would destroy a running create (N-AC2 violation)")
	}
	if found.Status != service.ReapStatusLive {
		t.Errorf("expected ReapStatusLive for in-flight create, got %s", found.Status)
	}
	// --apply must not have deleted the intent file.
	if _, statErr := os.Stat(intentFile); statErr != nil {
		t.Errorf("intent file was deleted despite live process (N-AC2 violation): %v", statErr)
	}
	if len(report.Deleted) != 0 {
		t.Errorf("Deleted should be empty for in-flight create, got %v", report.Deleted)
	}
}

// TestSocketPathForID_AllSocketKindsUseRealPath verifies that socketPathForID
// returns res.Path for every socket kind — not a fabricated .sock path.
//
// Mutation proof: reverting socketPathForID to fabricate a .sock path for
// non-API kinds causes this test to fail with:
//
//	kind KindSocketVSock: got /state/sockets/<id>.sock, want /state/sockets/<id>.vsock
//	kind KindSocketIID: got /state/sockets/<id>.sock, want /state/sockets/<id>.iid
//
// @verifies TBD-PD-23
func TestSocketPathForID_AllSocketKindsUseRealPath(t *testing.T) {
	id := domain.NewSandboxID()
	sockDir := "/state/sockets"

	cases := []struct {
		kind    service.ResourceKind
		suffix  string
	}{
		{service.KindSocketAPI, ".sock"},
		{service.KindSocketVSock, ".vsock"},
		{service.KindSocketIID, ".iid"},
	}

	for _, tc := range cases {
		realPath := filepath.Join(sockDir, id.String()+tc.suffix)
		res := service.HostResource{
			Kind:    tc.kind,
			Path:    realPath,
			OwnerID: id,
		}
		got := service.SocketPathForID(res, sockDir)
		if got != realPath {
			t.Errorf("kind %s: got %s, want %s", tc.kind, got, realPath)
		}
		// Confirm the returned path carries the correct extension, NOT .sock.
		if tc.kind != service.KindSocketAPI {
			fabricated := filepath.Join(sockDir, id.String()+".sock")
			if got == fabricated {
				t.Errorf("kind %s: returned fabricated .sock path %s instead of real path %s", tc.kind, got, realPath)
			}
		}
	}
}
