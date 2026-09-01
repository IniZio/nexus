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
//
// The D2 lock is released immediately after the call because the caller does
// not have a store.Create commit to protect — these tests exercise the verdict
// table, not the D2 commit window.
func CheckRWAttach(ctx context.Context, vs *volumestore.VolumeStore, st store.Store, diskDir, name, sandboxID string) error {
	lk, err := checkRWAttach(ctx, vs, st, diskDir, name, sandboxID)
	if lk != nil {
		_ = lk.Unlock()
		_ = lk.Close()
	}
	return err
}

// IsVolumeLiveRecord is exported for testing only. It reports whether a
// sandbox is actively running and could hold a volume in active use.
func IsVolumeLiveRecord(sb domain.Sandbox) bool {
	return isVolumeLiveRecord(sb)
}

// SetHookBeforeStoreCreate installs fn as svc's testHookBeforeStoreCreate for
// the duration of one test. Call the returned cleanup to clear the hook.
// This hook fires inside CreateAndBoot while all volumeLeases are still held
// (the D2 window), letting tests run Prune and confirm the held lock prevents
// deletion. fn is called on the goroutine that is executing CreateAndBoot.
func SetHookBeforeStoreCreate(svc *Service, fn func() error) (cleanup func()) {
	// Scoped to svc, not to the package: only creates driven through this
	// Service fire the hook. See the field comment on Service for the
	// cross-test firing this scoping closes.
	svc.testHookBeforeStoreCreate.Store(&fn)
	return func() { svc.testHookBeforeStoreCreate.Store(nil) }
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

// DetachVolumeLocked is exported for testing only. It calls the internal
// detachVolumeLocked function so that tests can verify deadline semantics
// and Service.Remove continuation on detach timeout without spinning up a
// full service.
func DetachVolumeLocked(ctx context.Context, vs *volumestore.VolumeStore, name, sandboxID string) error {
	return detachVolumeLocked(ctx, vs, name, sandboxID)
}

// SocketPathForID is exported for testing only. It wraps the internal
// socketPathForID so tests can verify that every socket kind resolves to
// res.Path rather than a fabricated .sock path.
func SocketPathForID(res HostResource, socketDir string) string {
	return socketPathForID(res, socketDir)
}
