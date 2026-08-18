package volumestore_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/volumestore"
)

// ── mock SandboxLister ────────────────────────────────────────────────────────

type mockSandboxLister struct {
	sandboxes []domain.Sandbox
}

func (m *mockSandboxLister) List(_ context.Context) ([]domain.Sandbox, error) {
	return m.sandboxes, nil
}

// ── helpers ────────────────────────────────────────────────────────────────────

// writeMetaOnly writes a bare meta.json for a volume without materialising any
// backing resource. This simulates case (a): a create that crashed between
// writing the record and materialising the backing file.
func writeMetaOnly(t *testing.T, root, name string, attachments []volumestore.VolumeAttachment) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	rec := volumestore.VolumeRecord{
		Name:        name,
		Kind:        volumestore.KindDisk,
		SizeBytes:   1024,
		Attachments: attachments,
		CreatedAt:   time.Now().UTC(),
	}
	data, _ := json.MarshalIndent(rec, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), data, 0o644); err != nil {
		t.Fatalf("write meta.json: %v", err)
	}
}

// writeDiskOnly creates a disk.ext4 file inside the volume directory WITHOUT
// a meta.json. This simulates case (b): an orphaned backing file.
func writeDiskOnly(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "disk.ext4"), []byte("fake"), 0o644); err != nil {
		t.Fatalf("write disk.ext4: %v", err)
	}
}

// writeFullVolume writes both meta.json and a disk.ext4 stub, simulating a
// complete (non-orphaned, non-stub) volume.
func writeFullVolume(t *testing.T, root, name string, attachments []volumestore.VolumeAttachment) {
	t.Helper()
	writeMetaOnly(t, root, name, attachments)
	diskPath := filepath.Join(root, name, "disk.ext4")
	if err := os.WriteFile(diskPath, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write disk.ext4: %v", err)
	}
}

// ── 7.5(a) Stub record ────────────────────────────────────────────────────────

// TestPruneStubRecord verifies case (a): a meta.json without a backing file is
// deleted when --apply is set.
//
// Mutation guard: if prune checked for disk.ext4 before meta.json, it would not
// find the stub and the meta.json would survive, failing the assertion.
func TestPruneStubRecord(t *testing.T) {
	root := t.TempDir()
	vs := volumestore.New(root)

	writeMetaOnly(t, root, "vol-stub", nil)

	metaPath := filepath.Join(root, "vol-stub", "meta.json")
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("setup: meta.json should exist before prune: %v", err)
	}

	res, err := vs.Prune(context.Background(), &mockSandboxLister{}, volumestore.PruneOptions{Apply: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if len(res.StubsDeleted) != 1 || res.StubsDeleted[0] != "vol-stub" {
		t.Errorf("StubsDeleted = %v; want [vol-stub]", res.StubsDeleted)
	}
	if _, err := os.Stat(metaPath); !os.IsNotExist(err) {
		t.Errorf("meta.json should be deleted after prune; stat err = %v", err)
	}
}

// TestPruneStubRecordDryRun verifies that without --apply the stub meta.json
// is reported but not deleted.
func TestPruneStubRecordDryRun(t *testing.T) {
	root := t.TempDir()
	vs := volumestore.New(root)

	writeMetaOnly(t, root, "vol-stub-dry", nil)
	metaPath := filepath.Join(root, "vol-stub-dry", "meta.json")

	res, err := vs.Prune(context.Background(), &mockSandboxLister{}, volumestore.PruneOptions{Apply: false})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if len(res.StubsDeleted) != 1 {
		t.Errorf("StubsDeleted = %v; want 1 entry", res.StubsDeleted)
	}
	// meta.json must survive in dry-run.
	if _, err := os.Stat(metaPath); err != nil {
		t.Errorf("meta.json must survive dry-run: %v", err)
	}
}

// ── 7.5(b) Orphaned backing file ─────────────────────────────────────────────

// TestPruneOrphanedBackingFile verifies case (b): a disk.ext4 without a
// meta.json is deleted when --apply is set.
func TestPruneOrphanedBackingFile(t *testing.T) {
	root := t.TempDir()
	vs := volumestore.New(root)

	writeDiskOnly(t, root, "vol-orphan")
	diskPath := filepath.Join(root, "vol-orphan", "disk.ext4")

	res, err := vs.Prune(context.Background(), &mockSandboxLister{}, volumestore.PruneOptions{Apply: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if len(res.OrphanedFilesDeleted) == 0 {
		t.Errorf("OrphanedFilesDeleted is empty; expected %s", diskPath)
	}
	if _, err := os.Stat(diskPath); !os.IsNotExist(err) {
		t.Errorf("disk.ext4 should be deleted after prune; stat err = %v", err)
	}
}

// ── 7.5(c) Detached volume — candidate only without --include-detached ────────

// TestPruneDetachedReportedNotDeleted verifies that a detached volume (no live
// sandboxes in either source) is reported as a candidate but NOT deleted when
// --include-detached is absent.
//
// Acceptance criterion 3: a detached volume is NOT deleted without
// --include-detached; the backing file must survive a plain prune.
func TestPruneDetachedReportedNotDeleted(t *testing.T) {
	root := t.TempDir()
	vs := volumestore.New(root)

	// Volume with stale attachment (sandbox "dead-sb" does not exist).
	writeFullVolume(t, root, "vol-detached", []volumestore.VolumeAttachment{
		{SandboxID: "dead-sandbox-id", AttachedAt: time.Now()},
	})
	diskPath := filepath.Join(root, "vol-detached", "disk.ext4")

	res, err := vs.Prune(context.Background(), &mockSandboxLister{}, volumestore.PruneOptions{Apply: true})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if len(res.DetachedCandidates) == 0 {
		t.Errorf("DetachedCandidates is empty; expected vol-detached")
	}
	if len(res.DetachedDeleted) != 0 {
		t.Errorf("DetachedDeleted = %v; want empty (--include-detached not set)", res.DetachedDeleted)
	}
	// Backing file must survive.
	if _, err := os.Stat(diskPath); err != nil {
		t.Errorf("disk.ext4 must survive plain prune: %v", err)
	}
}

// TestPruneDetachedDeletedWithFlag verifies that a detached volume IS deleted
// when --include-detached --apply is both set.
func TestPruneDetachedDeletedWithFlag(t *testing.T) {
	root := t.TempDir()
	vs := volumestore.New(root)

	writeFullVolume(t, root, "vol-gone", []volumestore.VolumeAttachment{
		{SandboxID: "dead-sandbox-id", AttachedAt: time.Now()},
	})
	volDir := filepath.Join(root, "vol-gone")

	res, err := vs.Prune(context.Background(), &mockSandboxLister{}, volumestore.PruneOptions{
		Apply:           true,
		IncludeDetached: true,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if len(res.DetachedDeleted) != 1 || res.DetachedDeleted[0] != "vol-gone" {
		t.Errorf("DetachedDeleted = %v; want [vol-gone]", res.DetachedDeleted)
	}
	if _, err := os.Stat(volDir); !os.IsNotExist(err) {
		t.Errorf("volume dir should be deleted; stat err = %v", err)
	}
}

// ── Dual-source disagreement: records win ─────────────────────────────────────

// TestPruneDualSourceRecordsWin proves the dual-source authority rule when the
// two sources DISAGREE: meta.json has an empty Attachments list (the detach
// step completed and cleared it), but the sandbox record still has MountedVolumes
// including the volume (the sandbox record write did not complete — crash window).
//
// Acceptance criterion 2: records win — the volume must NOT be deleted.
// A test where both sources agree does not prove this rule.
func TestPruneDualSourceRecordsWin(t *testing.T) {
	root := t.TempDir()
	vs := volumestore.New(root)

	// meta.json: Attachments is EMPTY — the meta source says "detached".
	writeFullVolume(t, root, "vol-live", nil)

	// Sandbox record: MountedVolumes references vol-live — the records source
	// says "attached". Records win.
	liveID := domain.NewSandboxID()
	lister := &mockSandboxLister{
		sandboxes: []domain.Sandbox{
			{
				ID: liveID,
				MountedVolumes: []domain.VolumeAttachment{
					{Name: "vol-live", GuestPath: "/mnt/data", Kind: "disk"},
				},
			},
		},
	}

	diskPath := filepath.Join(root, "vol-live", "disk.ext4")

	// Run prune with --include-detached --apply: if records did NOT win, the
	// volume would be deleted. Records winning means it survives.
	res, err := vs.Prune(context.Background(), lister, volumestore.PruneOptions{
		Apply:           true,
		IncludeDetached: true,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if len(res.DetachedDeleted) != 0 {
		t.Errorf("DetachedDeleted = %v; want empty — records must block deletion", res.DetachedDeleted)
	}
	if len(res.DetachedCandidates) != 0 {
		t.Errorf("DetachedCandidates = %v; want empty — vol-live is live per records", res.DetachedCandidates)
	}
	// Backing file must survive.
	if _, err := os.Stat(diskPath); err != nil {
		t.Errorf("disk.ext4 must survive when records say live: %v", err)
	}
}

// TestPruneDualSourceMetaAloneNotSufficient complements the records-win test:
// when meta.json lists a live sandbox but that sandbox's MountedVolumes does
// NOT include the volume, the meta source still makes the volume live (both
// sources contribute independently; this is the "union" half of the rule).
func TestPruneDualSourceMetaLiveBlocks(t *testing.T) {
	root := t.TempDir()
	vs := volumestore.New(root)

	liveID := domain.NewSandboxID()

	// meta.json lists liveID as attached.
	writeFullVolume(t, root, "vol-meta-live", []volumestore.VolumeAttachment{
		{SandboxID: liveID.String(), AttachedAt: time.Now()},
	})

	// Sandbox record exists but MountedVolumes is empty (the sources disagree
	// in the opposite direction from the records-win test).
	lister := &mockSandboxLister{
		sandboxes: []domain.Sandbox{
			{ID: liveID, MountedVolumes: nil},
		},
	}

	diskPath := filepath.Join(root, "vol-meta-live", "disk.ext4")

	res, err := vs.Prune(context.Background(), lister, volumestore.PruneOptions{
		Apply:           true,
		IncludeDetached: true,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if len(res.DetachedDeleted) != 0 {
		t.Errorf("DetachedDeleted = %v; want empty — meta.json lists a live sandbox", res.DetachedDeleted)
	}
	if _, err := os.Stat(diskPath); err != nil {
		t.Errorf("disk.ext4 must survive when meta.json lists a live sandbox: %v", err)
	}
}

// ── Live volume never touched ─────────────────────────────────────────────────

// TestPruneLiveVolumeUntouched verifies that a volume attached to a live sandbox
// (both sources agree) is never reported or deleted.
func TestPruneLiveVolumeUntouched(t *testing.T) {
	root := t.TempDir()
	vs := volumestore.New(root)

	liveID := domain.NewSandboxID()

	writeFullVolume(t, root, "vol-in-use", []volumestore.VolumeAttachment{
		{SandboxID: liveID.String(), AttachedAt: time.Now()},
	})

	lister := &mockSandboxLister{
		sandboxes: []domain.Sandbox{
			{
				ID: liveID,
				MountedVolumes: []domain.VolumeAttachment{
					{Name: "vol-in-use", GuestPath: "/mnt", Kind: "disk"},
				},
			},
		},
	}

	res, err := vs.Prune(context.Background(), lister, volumestore.PruneOptions{
		Apply:           true,
		IncludeDetached: true,
	})
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}

	if len(res.StubsDeleted)+len(res.OrphanedFilesDeleted)+len(res.DetachedCandidates)+len(res.DetachedDeleted) != 0 {
		t.Errorf("live volume must not appear in any prune result: %+v", res)
	}
}
