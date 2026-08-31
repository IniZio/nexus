package service_test

// TestReap_NeverTouchesVolumes is a guard test: it FAILS if ResourceIndex.List()
// is ever extended to scan the volumes directory.
//
// # Why the discriminating fixture matters
//
// A plain disk.ext4 inside the volumes dir matches NONE of the four enumeration
// patterns in ResourceIndex.List() (.raw, -workspace.ext4, .shadow.*, .create-intent.json).
// If the fixture contained only disk.ext4, the test would PASS even after
// widening the scan to include volumes/ — the file would simply be ignored by
// the pattern switch and never classified. The test would give false confidence.
//
// The <ULID>-workspace.ext4 decoy is the discriminating element: if the scanner
// ever reaches the volumes dir, that file WILL match the KindDiskWorkspace
// pattern, WILL be classified ReapStatusOrphan (no sandbox record exists for
// that ULID), and WILL be deleted by apply=true. The test then fails — correctly.
//
// Do not "simplify" the fixture by removing the <ULID>-workspace.ext4 file.
// Without it, the test does not prove the structural guarantee.
//
// @verifies D-PD-85 (structural reaper non-interference for volumes dir)
import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
)

func TestReap_NeverTouchesVolumes(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	sockDir := t.TempDir()

	// Create disks/ so the scanner has a valid (empty) target.
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	// Create volumes/test-vol/ with the discriminating fixture.
	volDir := filepath.Join(stateRoot, "volumes", "test-vol")
	mustMkdir(t, volDir)

	// meta.json: represents the volume record (kind=disk).
	metaPath := filepath.Join(volDir, "meta.json")
	mustWriteFile(t, metaPath, []byte(`{"name":"test-vol","kind":"disk"}`))

	// disk.ext4: the volume backing file. Matches no enumeration pattern on
	// its own, but must survive to confirm the directory is never touched.
	backingPath := filepath.Join(volDir, "disk.ext4")
	mustWriteFile(t, backingPath, []byte("fake-ext4"))

	// <ULID>-workspace.ext4: the discriminating decoy. This filename WOULD be
	// classified KindDiskWorkspace by ResourceIndex.List() if it ever scanned
	// this directory. No sandbox record exists for this ULID.
	decoyID := domain.NewSandboxID()
	decoyPath := filepath.Join(volDir, decoyID.String()+"-workspace.ext4")
	mustWriteFile(t, decoyPath, []byte("fake-workspace"))

	// Scanner is pointed at stateRoot only — List() will scan stateRoot/disks
	// and never descend into stateRoot/volumes.
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: sockDir,
	})

	// Empty store: no sandbox records exist.
	st := newEmptyStore(t)

	report, err := service.Reap(context.Background(), st, idx, true /* apply */, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	// Nothing in disks/ → nothing reaped.
	if len(report.Deleted) != 0 {
		t.Errorf("Reap deleted %d file(s); want 0: %v", len(report.Deleted), report.Deleted)
	}

	// Assert both volume files survived.
	for _, path := range []string{backingPath, decoyPath} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("volume file deleted by reaper (scan boundary breach): %s: %v", path, err)
		}
	}

	// meta.json is not an ext4 disk; verify it survived too.
	if _, err := os.Stat(metaPath); err != nil {
		t.Errorf("volume meta.json deleted by reaper: %v", err)
	}
}
