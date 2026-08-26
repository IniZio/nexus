package service_test

import (
	"reflect"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
)

// TestAllowedBranches_DefaultPropagates verifies that when Envelope.AllowedBranches
// is empty, ResolvedAllowedBranches produces the default, and that value
// populates mitm.Config.AllowedBranches as the production code in
// startSupervisor does. This covers assertion (b) of the S0 spec.
func TestAllowedBranches_DefaultPropagates(t *testing.T) {
	// Envelope with no AllowedBranches set — simulates an old record or one
	// created without --branches.
	env := domain.Envelope{}

	// Reproduce the construction in service.go startSupervisor.
	cfg := mitm.Config{
		AllowedBranches: env.ResolvedAllowedBranches(),
	}

	// Default is the namespace pattern (D-PD-03 nexus3/<slug>/<id> at any depth);
	// see R2-branch-glob-depth — path.Match's single-segment "*" could not match
	// the two-segment convention, so the default uses "**".
	want := []string{"refs/heads/nexus3/**"}
	if !reflect.DeepEqual(cfg.AllowedBranches, want) {
		t.Errorf("mitm.Config.AllowedBranches from empty Envelope = %v; want %v",
			cfg.AllowedBranches, want)
	}
}

// TestAllowedBranches_ExplicitPropagates verifies that an explicit value on
// Envelope.AllowedBranches survives the full plumbing chain into mitm.Config.
func TestAllowedBranches_ExplicitPropagates(t *testing.T) {
	explicit := []string{"refs/heads/topic/*", "refs/heads/release/*"}
	env := domain.Envelope{AllowedBranches: explicit}

	cfg := mitm.Config{
		AllowedBranches: env.ResolvedAllowedBranches(),
	}

	if !reflect.DeepEqual(cfg.AllowedBranches, explicit) {
		t.Errorf("mitm.Config.AllowedBranches = %v; want %v", cfg.AllowedBranches, explicit)
	}
}
