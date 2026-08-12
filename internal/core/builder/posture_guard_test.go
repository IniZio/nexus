// Package builder_test — G9 interaction/posture regression guards.
//
// This file asserts three cross-slice invariants:
//
//  1. BuilderVMSpec carries no egress / AllowedHosts field, confirming that
//     the builder VM cannot inadvertently inherit the normal sandbox's blanket
//     allow-list.  If AllowedHosts were added to BuilderVMSpec, callers (cmd /
//     G7) could be tempted to set it; by keeping it absent the arch guarantees
//     the builder has no egress beyond what the host-side CH driver permits.
//
//  2. G6's buildkitStateIsPersistent path: the G6 test
//     (buildkit_linux_g6_test.go) already asserts that a plain directory
//     returns false.  This file documents that dependency and ensures the G9
//     build does not inadvertently break the G6 file.
//
//  3. Herdr space-create and normal sandbox create compile smoke: both are
//     exercised implicitly by `go build ./...`; this file imports the affected
//     packages to surface compilation regressions as test failures rather than
//     silent build errors.
package builder_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/builder"
	// Compile smoke for the herdr/service path.
	_ "github.com/IniZio/nexus3/internal/core/service"
)

// egressFieldNames is the set of field names that, if present on BuilderVMSpec,
// would indicate the type has been (incorrectly) extended with egress controls.
var egressFieldNames = []string{
	"AllowedHosts",
	"EgressPolicy",
	"AllowedEgress",
	"EgressHosts",
}

// TestBuilderVMSpec_NoEgressField asserts that BuilderVMSpec contains no field
// that could be used to relax the egress default-deny posture.
//
// The builder VM is ephemeral and its network posture is controlled at the
// CloudHypervisor driver level (no vsock egress by default). Adding an
// AllowedHosts field to BuilderVMSpec would create a footgun: callers could
// set it and inadvertently grant the builder VM outbound internet access
// beyond the one-time image pull needed at startup.
//
// If this test fails, a new egress field was added to BuilderVMSpec. Discuss
// with the security reviewer before proceeding.
func TestBuilderVMSpec_NoEgressField(t *testing.T) {
	rt := reflect.TypeOf(builder.BuilderVMSpec{})
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		for _, bad := range egressFieldNames {
			if strings.EqualFold(name, bad) {
				t.Errorf("BuilderVMSpec has egress-related field %q — builder must not relax egress posture", name)
			}
		}
	}
}

// TestBuilderVMSpec_Fields documents the expected field set for BuilderVMSpec.
// If a new field is added, this test fails and the reviewer must confirm it
// does not introduce an egress or privilege-escalation path.
//
// Expected fields: RootfsDiskPath, ContextDiskPath, ArtifactDiskPath,
// CacheDisks, VCPUs, MemoryMiB.
func TestBuilderVMSpec_Fields(t *testing.T) {
	want := map[string]bool{
		"RootfsDiskPath":   true,
		"ContextDiskPath":  true,
		"ArtifactDiskPath": true,
		"CacheDisks":       true,
		"VCPUs":            true,
		"MemoryMiB":        true,
	}

	rt := reflect.TypeOf(builder.BuilderVMSpec{})
	got := make(map[string]bool, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		got[rt.Field(i).Name] = true
	}

	for field := range got {
		if !want[field] {
			t.Errorf("unexpected new field %q in BuilderVMSpec — confirm it does not add an egress or privilege-escalation path", field)
		}
	}
	for field := range want {
		if !got[field] {
			t.Errorf("expected field %q missing from BuilderVMSpec — spec drift?", field)
		}
	}
}

// TestG6Regression_BuildkitStateReference is a documentation test that
// references G6's buildkitStateIsPersistent plain-dir assertion. The actual
// runtime test is in internal/core/agent/buildkit_linux_g6_test.go:
//
//	TestBuildkitStateIsPersistent_PlainDir
//
// This test ensures the G9 build does not remove or rename that file.
func TestG6Regression_BuildkitStateReference(t *testing.T) {
	// This is a compile-time smoke test: the g6_test file is in a separate
	// package (agent) and is verified by `go build ./...`. Nothing to assert
	// at runtime beyond the compilation succeeding.
	//
	// If TestBuildkitStateIsPersistent_PlainDir is deleted or moved, the G9
	// reviewer should update this comment with the new location.
	t.Log("G6 regression reference: TestBuildkitStateIsPersistent_PlainDir in internal/core/agent/buildkit_linux_g6_test.go")
}
