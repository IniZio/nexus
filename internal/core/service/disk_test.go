package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/store"
)

// TestReapDiskCopy_RemovesAllThreeArtifacts verifies that ReapDiskCopy removes:
//   - the sandbox disk copy (.raw)
//   - the workspace disk (-workspace.ext4) — gap-3 fix
//   - the create-intent marker (.create-intent.json) — if present
func TestReapDiskCopy_RemovesAllThreeArtifacts(t *testing.T) {
	dir := t.TempDir()
	id := domain.NewSandboxID()

	// Create all three files to simulate a fully-materialised sandbox whose
	// record has been deleted (normal remove path).
	rawPath := filepath.Join(dir, id.String()+".raw")
	wsPath := filepath.Join(dir, id.String()+"-workspace.ext4")
	intentPath := filepath.Join(dir, id.String()+".create-intent.json")

	for _, p := range []string{rawPath, wsPath, intentPath} {
		if err := os.WriteFile(p, []byte("placeholder"), 0o600); err != nil {
			t.Fatalf("create placeholder %s: %v", p, err)
		}
	}

	if err := ReapDiskCopy(dir, id); err != nil {
		t.Fatalf("ReapDiskCopy: %v", err)
	}

	for _, p := range []string{rawPath, wsPath, intentPath} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s to be removed, got err=%v", filepath.Base(p), err)
		}
	}
}

// TestReapDiskCopy_IdempotentWhenFilesAbsent verifies that ReapDiskCopy is a
// no-op when none of the three files exist (missing file is not an error).
func TestReapDiskCopy_IdempotentWhenFilesAbsent(t *testing.T) {
	dir := t.TempDir()
	id := domain.NewSandboxID()
	if err := ReapDiskCopy(dir, id); err != nil {
		t.Fatalf("ReapDiskCopy on absent files: %v", err)
	}
}

// TestReapDiskCopy_WorkspaceDiskOnly_NoRaw verifies that the workspace disk is
// still reaped when no .raw file exists (e.g. --rootfs sandbox with workspace).
func TestReapDiskCopy_WorkspaceDiskOnly_NoRaw(t *testing.T) {
	dir := t.TempDir()
	id := domain.NewSandboxID()
	wsPath := filepath.Join(dir, id.String()+"-workspace.ext4")
	if err := os.WriteFile(wsPath, []byte("ws"), 0o600); err != nil {
		t.Fatalf("create workspace disk: %v", err)
	}

	if err := ReapDiskCopy(dir, id); err != nil {
		t.Fatalf("ReapDiskCopy: %v", err)
	}
	if _, err := os.Stat(wsPath); !os.IsNotExist(err) {
		t.Errorf("workspace disk still exists after ReapDiskCopy")
	}
}

// ── ReapShadowDisks ──────────────────────────────────────────────────────────

// TestReapShadowDisks_RemovesAllForHandle verifies that ReapShadowDisks
// removes every shadow disk image whose filename begins with the safeHandle
// of the given sandbox handle.
func TestReapShadowDisks_RemovesAllForHandle(t *testing.T) {
	dir := t.TempDir()
	handle := "example-app/b1-proof-a"

	// Create two shadow disks for this handle plus one for a different handle.
	own := []string{
		"example-app_b1-proof-a.shadow.node_modules.ext4",
		"example-app_b1-proof-a.shadow.dist.ext4",
	}
	other := "example-app_b1-proof-b.shadow.node_modules.ext4"
	for _, f := range append(own, other) {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o664); err != nil {
			t.Fatalf("create %s: %v", f, err)
		}
	}

	if err := ReapShadowDisks(dir, handle); err != nil {
		t.Fatalf("ReapShadowDisks: %v", err)
	}

	// Owned files must be gone.
	for _, f := range own {
		if _, err := os.Stat(filepath.Join(dir, f)); !os.IsNotExist(err) {
			t.Errorf("%s: expected removed, still present (err=%v)", f, err)
		}
	}
	// Other handle's file must remain untouched.
	if _, err := os.Stat(filepath.Join(dir, other)); err != nil {
		t.Errorf("%s: expected still present, got %v", other, err)
	}
}

// TestReapShadowDisks_Idempotent verifies that calling ReapShadowDisks when
// no shadow disks for the handle exist is a no-op (not an error).
func TestReapShadowDisks_Idempotent(t *testing.T) {
	dir := t.TempDir()
	if err := ReapShadowDisks(dir, "proj/name"); err != nil {
		t.Fatalf("ReapShadowDisks with no matching files: %v", err)
	}
}

// TestReapShadowDisks_DoesNotTouchLegacyFiles verifies that legacy-format
// shadow disks (pre-B1, ending in ".shadow.ext4") are not removed by
// ReapShadowDisks — they do not match the glob <safeHandle>.shadow.*.ext4
// because their "dirName.shadow.ext4" structure has nothing after ".shadow.".
//
// Legacy files are unconditionally unowned and will be reclaimed by the
// reaper via IsShadowDisk + ShadowDiskSafeHandle classification (which
// returns ok=false for them, signalling "unowned").
func TestReapShadowDisks_DoesNotTouchLegacyFiles(t *testing.T) {
	dir := t.TempDir()

	// Pre-B1 legacy files — dirName.shadow.ext4 format.
	legacy := []string{
		"node_modules.shadow.ext4",
		"dist.shadow.ext4",
		"target.shadow.ext4",
	}
	for _, f := range legacy {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o664); err != nil {
			t.Fatalf("create %s: %v", f, err)
		}
	}

	// ReapShadowDisks for any handle should not touch the legacy files because
	// "node_modules.shadow.ext4" does not match "proj_name.shadow.*.ext4".
	if err := ReapShadowDisks(dir, "proj/name"); err != nil {
		t.Fatalf("ReapShadowDisks: %v", err)
	}
	for _, f := range legacy {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("legacy file %s should be untouched, got %v", f, err)
		}
	}
}

// ── Service.Remove integration: shadow disk reclamation ─────────────────────

// TestServiceRemove_ReapsShadowDisks is the integration proof for residual (a):
// Service.Remove must reclaim shadow disk images when a sandbox is removed.
//
// Setup:
//  1. Create a real sandbox record via svc.Create (store only, no VM boot).
//  2. Pre-create B1-format shadow disk files for the sandbox's handle in the
//     disk directory. This simulates what cmd_sandbox's workspace path does.
//  3. Measure allocated size before Remove.
//  4. Call svc.Remove.
//  5. Assert all shadow disk files are gone.
//
// This proves that Service.Remove → ReapShadowDisks is wired correctly and
// reclaims handle-keyed shadow disks as part of the single reclamation
// contract.
func TestServiceRemove_ReapsShadowDisks(t *testing.T) {
	diskDir := t.TempDir()

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc := New(st, fake.New(), lifecycle.New()).WithDiskDir(diskDir)

	ctx := context.Background()

	// Step 1: create a sandbox record (store only — fake driver, no VM boot).
	sb, err := svc.Create(ctx, "b5test", "reap-proof", CreateOptions{})
	if err != nil {
		t.Fatalf("svc.Create: %v", err)
	}

	// Step 2: pre-create B1-format shadow disk files, simulating the files that
	// buildShadowDiskSpecs + createShadowDisk would have written for this handle.
	handle := sb.Handle() // "b5test/reap-proof"
	safeHandle := strings.ReplaceAll(handle, "/", "_")
	shadowFiles := []string{
		safeHandle + ".shadow.node_modules.ext4",
		safeHandle + ".shadow._next.ext4",
		safeHandle + ".shadow.target.ext4",
		safeHandle + ".shadow.dist.ext4",
	}
	for _, f := range shadowFiles {
		// Use a 1 MiB placeholder (non-sparse) so allocated size is measurable.
		if err := os.WriteFile(filepath.Join(diskDir, f), make([]byte, 1<<20), 0o664); err != nil {
			t.Fatalf("create placeholder %s: %v", f, err)
		}
	}

	// Step 3: measure allocated size before Remove using the package-level helper.
	var beforeBytes int64
	for _, f := range shadowFiles {
		beforeBytes += allocatedBytes(filepath.Join(diskDir, f))
	}
	if beforeBytes == 0 {
		t.Fatal("beforeBytes is 0 — placeholder files were not created")
	}

	// Step 4: Remove the sandbox.
	if err := svc.Remove(ctx, handle); err != nil {
		t.Fatalf("svc.Remove: %v", err)
	}

	// Step 5: all shadow disk files must be gone.
	var afterBytes int64
	for _, f := range shadowFiles {
		p := filepath.Join(diskDir, f)
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Errorf("shadow disk %s still present after Remove (err=%v) — leak not fixed", f, statErr)
		}
		afterBytes += allocatedBytes(p)
	}

	if afterBytes != 0 {
		t.Errorf("after Remove: %d bytes remain for handle %q — shadow disks not fully reclaimed", afterBytes, handle)
	}
	t.Logf("create→rm reclamation: before=%d bytes (%.1f MiB), after=%d bytes (handle %q)",
		beforeBytes, float64(beforeBytes)/1024/1024, afterBytes, handle)
}

// ── Scratch disk reclamation (SD-AC7, RES-R-005/006/009/012) ─────────────────

// TestReapDiskCopy_RemovesScratchDisk proves that ReapDiskCopy removes the
// per-sandbox scratch disk (-scratch.ext4) and is idempotent when absent
// (RES-R-005, RES-R-006).
func TestReapDiskCopy_RemovesScratchDisk(t *testing.T) {
	dir := t.TempDir()
	id := domain.NewSandboxID()

	scratchPath := filepath.Join(dir, id.String()+"-scratch.ext4")
	if err := os.WriteFile(scratchPath, []byte("scratch"), 0o600); err != nil {
		t.Fatalf("create scratch disk: %v", err)
	}
	// Fixture self-check: the file must exist before calling ReapDiskCopy.
	if _, err := os.Stat(scratchPath); err != nil {
		t.Fatalf("scratch disk was not created (fixture premise failed): %v", err)
	}

	if err := ReapDiskCopy(dir, id); err != nil {
		t.Fatalf("ReapDiskCopy: %v", err)
	}
	if _, err := os.Stat(scratchPath); !os.IsNotExist(err) {
		t.Errorf("scratch disk still exists after ReapDiskCopy (err=%v) — RES-R-005/006 not satisfied", err)
	}

	// Idempotency: second call with no scratch disk must not error.
	if err := ReapDiskCopy(dir, id); err != nil {
		t.Fatalf("ReapDiskCopy (second call, absent scratch disk): %v", err)
	}
}

// TestScratchDiskEnumeratedByIndex proves ResourceIndex.List() returns a
// KindDiskScratch entry for a -scratch.ext4 file (RES-R-009).
func TestScratchDiskEnumeratedByIndex(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	if err := os.MkdirAll(disksDir, 0o700); err != nil {
		t.Fatalf("create disks dir: %v", err)
	}

	id := domain.NewSandboxID()
	scratchFile := id.String() + "-scratch.ext4"
	if err := os.WriteFile(filepath.Join(disksDir, scratchFile), []byte("x"), 0o600); err != nil {
		t.Fatalf("create scratch disk file: %v", err)
	}

	idx := NewResourceIndex(IndexConfig{StateRoot: stateRoot})
	resources, err := idx.List()
	if err != nil {
		t.Fatalf("ResourceIndex.List: %v", err)
	}

	for _, r := range resources {
		if r.Kind == KindDiskScratch && r.OwnerID == id {
			return // found — test passes
		}
	}
	t.Errorf("ResourceIndex.List did not return KindDiskScratch for %s — regression: scratch disk not enumerated (RES-R-009)", scratchFile)
}

// TestServiceRemove_ReapsScratchDisk is the integration proof for SD-AC7:
// Service.Remove must reclaim the per-sandbox scratch disk image.
//
// Setup mirrors TestServiceRemove_ReapsShadowDisks:
//  1. Create a real sandbox record via svc.Create (store only, no VM boot).
//  2. Pre-create the scratch disk at ScratchDiskHostPath — simulates what
//     CreateAndBoot writes for a workspace sandbox.
//  3. Assert the file exists before Remove (fixture self-check).
//  4. Call svc.Remove.
//  5. Assert the scratch disk is gone.
func TestServiceRemove_ReapsScratchDisk(t *testing.T) {
	diskDir := t.TempDir()

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}
	svc := New(st, fake.New(), lifecycle.New()).WithDiskDir(diskDir)

	ctx := context.Background()

	// Step 1: create sandbox record (store only — fake driver, no VM boot).
	sb, err := svc.Create(ctx, "scratchtest", "sd-ac7", CreateOptions{})
	if err != nil {
		t.Fatalf("svc.Create: %v", err)
	}

	// Step 2: pre-create the scratch disk file.
	scratchPath := ScratchDiskHostPath(diskDir, sb.ID.String())
	if err := os.WriteFile(scratchPath, []byte("scratch"), 0o600); err != nil {
		t.Fatalf("create scratch disk: %v", err)
	}
	// Fixture self-check.
	if _, err := os.Stat(scratchPath); err != nil {
		t.Fatalf("scratch disk was not created (fixture premise failed): %v", err)
	}

	// Step 3: Remove the sandbox.
	if err := svc.Remove(ctx, sb.Handle()); err != nil {
		t.Fatalf("svc.Remove: %v", err)
	}

	// Step 4: scratch disk must be gone — SD-AC7.
	if _, err := os.Stat(scratchPath); !os.IsNotExist(err) {
		t.Errorf("scratch disk still present after svc.Remove (err=%v) — SD-AC7 not satisfied", err)
	}
}
