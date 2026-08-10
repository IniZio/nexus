package builder

// NewWithClient exposes the unexported newWithClient constructor for use in
// the external test package (package builder_test). This is the standard Go
// pattern for granting test-only access to an unexported constructor without
// polluting the public API.
var NewWithClient = newWithClient

// RunMke2fs exposes the unexported runMke2fs function for integration tests
// that need to produce a raw ext4 image directly from a source directory.
var RunMke2fs = runMke2fs

// CopyDirIntoContext exposes the unexported copyDirIntoContext function for
// unit tests that verify workspace-escape prevention.
var CopyDirIntoContext = copyDirIntoContext
