package volumestore

import (
	"context"

	"github.com/IniZio/nexus3/internal/core/store"
)

// SetTestHookAfterMetaWrite sets the hook that fires after meta.json is
// written and before the backing resource is materialised.  Used by tests to
// simulate a crash between the two steps and verify the D-PD-89 ordering.
//
// This file is compiled only when running tests (export_test.go convention).
func SetTestHookAfterMetaWrite(s *VolumeStore, fn func() error) {
	s.testHookAfterMetaWrite = fn
}

// SetTestHookAfterRmRead sets the hook that fires inside Rm after reading the
// volume record (and verifying it has no attachments) but before deleting any
// files.  Used by the D3 TOCTOU test to inject a concurrent Attach that should
// be serialised by the per-volume flock.
func SetTestHookAfterRmRead(s *VolumeStore, fn func() error) {
	s.testHookAfterRmRead = fn
}

// HoldVolumeLockForTest acquires the per-volume exclusive flock for name and
// returns a release function.  Used by the D2 TOCTOU test to simulate the
// window in service/create.go between checkRWAttach (attachment written to
// meta.json) and svc.store.Create (sandbox record committed), during which the
// volume lock must be held so Prune cannot classify the volume as "detached".
func HoldVolumeLockForTest(s *VolumeStore, name string) (release func(), err error) {
	lk, err := store.OpenLock(s.LockPath(name))
	if err != nil {
		return nil, err
	}
	if err := lk.Exclusive(context.Background()); err != nil {
		_ = lk.Close()
		return nil, err
	}
	return func() {
		_ = lk.Unlock()
		_ = lk.Close()
	}, nil
}
