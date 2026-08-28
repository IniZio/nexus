package domain_test

import (
	"reflect"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// TestResolvedAllowedBranches_Default verifies that an Envelope with no
// AllowedBranches set returns the project default ["refs/heads/nexus3/**"].
// The "/**" suffix enables namespace-prefix matching at any depth, which is
// required by the D-PD-03 branch convention nexus3/<motive-slug>/<sandbox-id>.
// This covers assertion (a) of the S0 spec.
func TestResolvedAllowedBranches_Default(t *testing.T) {
	e := domain.Envelope{}
	got := e.ResolvedAllowedBranches()
	want := []string{"refs/heads/nexus3/**"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResolvedAllowedBranches() on empty Envelope = %v; want %v", got, want)
	}
}

// TestResolvedAllowedBranches_NilField is the same as Default but makes the
// nil vs empty-slice distinction explicit.
func TestResolvedAllowedBranches_NilField(t *testing.T) {
	e := domain.Envelope{AllowedBranches: nil}
	got := e.ResolvedAllowedBranches()
	want := []string{"refs/heads/nexus3/**"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResolvedAllowedBranches() on nil AllowedBranches = %v; want %v", got, want)
	}
}

// TestResolvedAllowedBranches_DefaultIsDoubleStarNamespace verifies that the
// default pattern uses "/**" (namespace-prefix depth-unlimited semantics) and
// NOT the old single-level "*".  Full behavioral assertions — that the pattern
// matches refs/heads/nexus3/<slug>/<id> (2 levels), refs/heads/nexus3/x
// (1 level), and denies refs/heads/main and refs/heads/rogue — are in the
// mitm package (TestRefMatchesGlob_*), where refMatchesGlob is defined.
func TestResolvedAllowedBranches_DefaultIsDoubleStarNamespace(t *testing.T) {
	e := domain.Envelope{}
	got := e.ResolvedAllowedBranches()
	if len(got) == 0 {
		t.Fatal("ResolvedAllowedBranches() returned empty slice")
	}
	const want = "refs/heads/nexus3/**"
	if got[0] != want {
		t.Errorf("default pattern = %q; want %q (D-PD-03: any depth under nexus3/)", got[0], want)
	}
}

// TestResolvedAllowedBranches_Custom verifies that an explicit AllowedBranches
// value is returned as-is (no default override).
func TestResolvedAllowedBranches_Custom(t *testing.T) {
	e := domain.Envelope{AllowedBranches: []string{"refs/heads/feature/*", "refs/heads/fix/*"}}
	got := e.ResolvedAllowedBranches()
	want := []string{"refs/heads/feature/*", "refs/heads/fix/*"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResolvedAllowedBranches() = %v; want %v", got, want)
	}
}

// TestResolvedAllowedBranches_ReturnsCopy verifies that mutating the returned
// slice does not affect the Envelope (no aliasing).
func TestResolvedAllowedBranches_ReturnsCopy(t *testing.T) {
	e := domain.Envelope{AllowedBranches: []string{"refs/heads/nexus3/*"}}
	got := e.ResolvedAllowedBranches()
	got[0] = "mutated"
	if e.AllowedBranches[0] == "mutated" {
		t.Error("ResolvedAllowedBranches() returned an alias of AllowedBranches; want a copy")
	}
}

// TestResolvedAllowedBranches_DefaultUnchanged verifies that an empty Envelope
// (no AllowedBranches set) returns the hardcoded default ["refs/heads/nexus3/**"].
// Regression guard: the default must remain stable.
func TestResolvedAllowedBranches_DefaultUnchanged(t *testing.T) {
	e := domain.Envelope{}
	got := e.ResolvedAllowedBranches()
	want := []string{"refs/heads/nexus3/**"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResolvedAllowedBranches() on empty Envelope = %v; want %v (default unchanged)", got, want)
	}
}
