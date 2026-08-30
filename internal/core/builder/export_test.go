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

// CaptureBootSpecFromContainerfile exposes captureBootSpecFromContainerfile for
// white-box tests that verify the Containerfile-parse → boot.json path.
// The first argument is the raw content of a .nexus/Containerfile.
var CaptureBootSpecFromContainerfile = captureBootSpecFromContainerfile

// CaptureBootSpec exposes captureBootSpec for white-box tests that verify the
// OCI-config-merge → boot.json path (D-DC-31).
// ociCfg is the effective merged OCI image config (nil = Containerfile fallback).
var CaptureBootSpec = captureBootSpec

// ParseOCIConfigFromTar exposes parseOCIConfigFromTar for white-box tests that
// verify the OCI layout tar → image config extraction path.
var ParseOCIConfigFromTar = parseOCIConfigFromTar

// ExportAndUnpack exposes exportAndUnpack for white-box tests that verify the
// tar-exporter unpack path's fail-closed behaviour and errgroup join semantics.
var ExportAndUnpack = exportAndUnpack

// NewSizeVerifiedFS exposes newSizeVerifiedFS for integration tests that need
// to inject a custom inner FS and verify that the truncation guard fires.
var NewSizeVerifiedFS = newSizeVerifiedFS

// NewSizeVerifiedSet exposes newSizeVerifiedSet for integration tests that need
// to group multiple mounts under a shared cancel-cause and verify that a
// violation in any member cancels the Solve context.
var NewSizeVerifiedSet = newSizeVerifiedSet

// BuildLocalMounts exposes buildLocalMounts for tests that assert every entry
// in the LocalMounts map is set-wrapped (CI-visible regression guard).
var BuildLocalMounts = buildLocalMounts
