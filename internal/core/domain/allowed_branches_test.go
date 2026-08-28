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

// ---- ResolvedProtectedBranches tests (T1-AC2) ----

// TestResolvedProtectedBranches_Unconfigured verifies that an Envelope with no
// ProtectedBranches set returns nil (no protection configured — not "protect all").
func TestResolvedProtectedBranches_Unconfigured(t *testing.T) {
	e := domain.Envelope{}
	got := e.ResolvedProtectedBranches()
	if got != nil {
		t.Errorf("ResolvedProtectedBranches() on empty Envelope = %v; want nil", got)
	}
}

// TestResolvedProtectedBranches_NilField is the same as Unconfigured but makes
// the nil vs empty-slice distinction explicit.
func TestResolvedProtectedBranches_NilField(t *testing.T) {
	e := domain.Envelope{ProtectedBranches: nil}
	got := e.ResolvedProtectedBranches()
	if got != nil {
		t.Errorf("ResolvedProtectedBranches() on nil ProtectedBranches = %v; want nil", got)
	}
}

// TestResolvedProtectedBranches_Configured verifies that an explicit
// ProtectedBranches value is returned as-is (copied).
func TestResolvedProtectedBranches_Configured(t *testing.T) {
	e := domain.Envelope{ProtectedBranches: []string{"refs/heads/main", "refs/heads/release/**"}}
	got := e.ResolvedProtectedBranches()
	want := []string{"refs/heads/main", "refs/heads/release/**"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResolvedProtectedBranches() = %v; want %v", got, want)
	}
}

// TestResolvedProtectedBranches_ReturnsCopy verifies no aliasing.
func TestResolvedProtectedBranches_ReturnsCopy(t *testing.T) {
	e := domain.Envelope{ProtectedBranches: []string{"refs/heads/main"}}
	got := e.ResolvedProtectedBranches()
	got[0] = "mutated"
	if e.ProtectedBranches[0] == "mutated" {
		t.Error("ResolvedProtectedBranches() returned an alias of ProtectedBranches; want a copy")
	}
}

// TestResolvedAllowedBranches_DefaultUnchanged verifies that adding
// ProtectedBranches does NOT alter the ResolvedAllowedBranches default.
// Regression guard: the default must remain ["refs/heads/nexus3/**"].
func TestResolvedAllowedBranches_DefaultUnchanged(t *testing.T) {
	e := domain.Envelope{ProtectedBranches: []string{"refs/heads/main"}}
	got := e.ResolvedAllowedBranches()
	want := []string{"refs/heads/nexus3/**"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ResolvedAllowedBranches() with ProtectedBranches set = %v; want %v (default unchanged)", got, want)
	}
}
