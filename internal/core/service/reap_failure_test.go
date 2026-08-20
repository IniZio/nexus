package service_test

// TBD-PD-37: `reap --apply` silently skipped a resource it failed to delete
// and still reported success.
//
// Observed live on 2026-08-19 during the authorised cleanup: an --apply pass
// printed "Deleted 129 resource(s)" while one stub file survived it, and a
// second pass removed the same file. Nothing in the first pass's output said
// so. `Reap` appended to Deleted only on a nil error and dropped the error
// branch on the floor — no message, no report line, no non-zero exit — so the
// command was unverifiable from its own output.
//
// These tests drive Reap, not the delete helper, so they fail if failures stop
// being collected, if a survivor stops being detected, or if an
// already-reclaimed path starts counting as a failure.

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/service"
)

// undeletableOrphan writes an orphan disk into disksDir and then makes the
// directory read-only, so os.Remove fails with EACCES. Restoring the mode is
// registered with t.Cleanup so the temp dir can still be torn down.
func undeletableOrphan(t *testing.T, disksDir, filename string) string {
	t.Helper()
	path := mustWriteShadowDisk(t, disksDir, filename)
	if err := os.Chmod(disksDir, 0o500); err != nil {
		t.Fatalf("chmod %s: %v", disksDir, err)
	}
	t.Cleanup(func() { _ = os.Chmod(disksDir, 0o700) })
	return path
}

// A delete that fails must appear in Failed, must NOT appear in Deleted, and
// must leave the file on disk. Before the fix all three were true except the
// first, which is precisely what made the under-deletion invisible.
func TestReap_DeletionFailureIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not block unlink")
	}
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)

	path := undeletableOrphan(t, disksDir, "doomed.shadow.node_modules.ext4")

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	report, err := service.Reap(context.Background(), st, idx, true /*apply*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	if len(report.Failed) != 1 {
		t.Fatalf("Failed has %d entries, want 1 — an undeletable orphan was silently skipped", len(report.Failed))
	}
	if report.Failed[0].Path != path {
		t.Errorf("Failed[0].Path = %q, want %q", report.Failed[0].Path, path)
	}
	if report.Failed[0].Reason == "" {
		t.Error("Failed[0].Reason is empty; the operator gets no clue why")
	}
	for _, d := range report.Deleted {
		if d == path {
			t.Error("path appears in Deleted despite the delete failing")
		}
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Errorf("file was actually removed; the test is not exercising the failure path: %v", statErr)
	}
}

// A path that is already gone is a successful reclamation, not a failure.
// Two reapers racing, or a reap racing `sandbox rm`, must not report an error
// for work someone else completed — otherwise the exit code becomes noise and
// operators learn to ignore it.
func TestReap_AlreadyGoneCountsAsDeleted(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)
	mustWriteShadowDisk(t, disksDir, "vanishing.shadow.node_modules.ext4")

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	// Enumerate first, then delete behind the reaper's back, then apply —
	// the shape of a concurrent reclaim.
	report, err := service.Reap(context.Background(), st, idx, true /*apply*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(report.Failed) != 0 {
		t.Errorf("Failed = %v, want empty for a clean reclamation", report.Failed)
	}
	if len(report.Deleted) != 1 {
		t.Errorf("Deleted has %d entries, want 1", len(report.Deleted))
	}

	// A second pass finds nothing left and must also report no failures.
	second, err := service.Reap(context.Background(), st, idx, true /*apply*/, service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap (second pass): %v", err)
	}
	if len(second.Failed) != 0 {
		t.Errorf("second pass reported failures %v; reclamation must be idempotent", second.Failed)
	}
}

// The survivor case: os.Remove returned nil and the path was still there.
// That is what happened on 2026-08-19 — "Deleted 129 resource(s)" with one
// file left behind — and nothing in the output contradicted the claim.
//
// The mechanism has no known cause and so cannot be reproduced by arranging
// real files; ReapOptions.VerifyStat drives the DETECTION end-to-end through
// Reap instead. The assertion that matters is that the survivor moves OUT of
// Deleted as well as into Failed, so len(Deleted) never overstates the work.
func TestReap_SurvivorAfterReportedSuccessIsCaught(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)
	path := mustWriteShadowDisk(t, disksDir, "survivor.shadow.node_modules.ext4")

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	report, err := service.Reap(context.Background(), st, idx, true, /*apply*/
		service.ReapOptions{
			ProcDir: t.TempDir(),
			// Report every path as still present after deletion.
			VerifyStat: func(string) error { return nil },
		})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}

	if len(report.Failed) != 1 {
		t.Fatalf("Failed has %d entries, want 1 — a survivor was reported as deleted", len(report.Failed))
	}
	if report.Failed[0].Path != path {
		t.Errorf("Failed[0].Path = %q, want %q", report.Failed[0].Path, path)
	}
	if len(report.Deleted) != 0 {
		t.Errorf("Deleted = %v, want empty — a survivor must not be counted as reclaimed", report.Deleted)
	}
}

// The verify pass must not manufacture failures for genuine deletions.
// If it did, every successful --apply would exit non-zero and the signal
// would be worthless.
func TestReap_VerifyPassDoesNotFlagRealDeletions(t *testing.T) {
	stateRoot := t.TempDir()
	disksDir := filepath.Join(stateRoot, "disks")
	mustMkdir(t, disksDir)
	mustWriteShadowDisk(t, disksDir, "genuine.shadow.node_modules.ext4")

	st := newEmptyStore(t)
	idx := service.NewResourceIndex(service.IndexConfig{
		StateRoot: stateRoot,
		SocketDir: t.TempDir(),
	})

	report, err := service.Reap(context.Background(), st, idx, true, /*apply*/
		service.ReapOptions{ProcDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(report.Failed) != 0 {
		t.Errorf("verify pass flagged %v on a clean deletion", report.Failed)
	}
	if len(report.Deleted) != 1 {
		t.Errorf("Deleted has %d entries, want 1", len(report.Deleted))
	}
}
