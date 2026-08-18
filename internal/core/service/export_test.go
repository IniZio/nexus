package service

import (
	"context"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/volumestore"
)

// ApplyRWVerdictTable is exported for testing only. It implements the §4.1
// concurrency guard verdict table for kind=disk rw attach.
func ApplyRWVerdictTable(ctx context.Context, vs *volumestore.VolumeStore, st store.Store, diskDir, name, sandboxID string) error {
	return applyRWVerdictTable(ctx, vs, st, diskDir, name, sandboxID)
}

// CheckRWAttach is exported for testing only. It is the full lock-then-verdict-table
// call path used by CreateAndBoot, including the per-volume advisory flock.
// Cross-process tests use this to verify that the flock gates concurrent
// processes (killing M3: if flock is replaced with sync.Mutex, two processes
// can both succeed in the N=8 storm test).
func CheckRWAttach(ctx context.Context, vs *volumestore.VolumeStore, st store.Store, diskDir, name, sandboxID string) error {
	return checkRWAttach(ctx, vs, st, diskDir, name, sandboxID)
}

// IsVolumeLiveRecord is exported for testing only. It reports whether a
// sandbox is actively running and could hold a volume in active use.
func IsVolumeLiveRecord(sb domain.Sandbox) bool {
	return isVolumeLiveRecord(sb)
}

// HoldCreateIntentForTest writes a create-intent file for id in diskDir and holds
// the exclusive flock lease. It returns a release function that removes the file
// and drops the flock. This is exported for tests that need to simulate an
// in-flight create without running a full CreateAndBoot (GAP 1 rows 3/4 tests,
// GAP 2 cross-process tests).
func HoldCreateIntentForTest(diskDir string, id domain.SandboxID) (release func(), err error) {
	lease, err := writeCreateIntent(diskDir, id, "", "")
	if err != nil {
		return nil, err
	}
	return func() { lease.release() }, nil
}
