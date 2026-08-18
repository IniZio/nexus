package volumestore

// SetTestHookAfterMetaWrite sets the hook that fires after meta.json is
// written and before the backing resource is materialised.  Used by tests to
// simulate a crash between the two steps and verify the D-PD-89 ordering.
//
// This file is compiled only when running tests (export_test.go convention).
func SetTestHookAfterMetaWrite(s *VolumeStore, fn func() error) {
	s.testHookAfterMetaWrite = fn
}
