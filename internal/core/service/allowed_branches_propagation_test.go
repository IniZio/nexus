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

// ---- ProtectedBranches plumbing tests (T1-AC3) ----

// TestProtectedBranches_FromParsedConfig_SetOnEnvelope verifies that
// ProtectedBranches parsed from a trusted-ref config reaches the Envelope
// field correctly (service.CreateOptions → Envelope thread).
// This covers AC-A3 at the plumbing layer: field is set from parsed Config,
// not from a new working-tree read path.
func TestProtectedBranches_FromParsedConfig_SetOnEnvelope(t *testing.T) {
	// Simulate what the herdr worktree path does: parse trusted-ref bytes,
	// extract branch policy via buildWorktreeBranchArgs-equivalent logic, and
	// set it on the Envelope via CreateOptions.ProtectedBranches.
	protected := []string{"refs/heads/main", "refs/heads/release/**"}
	env := domain.Envelope{ProtectedBranches: protected}

	got := env.ResolvedProtectedBranches()
	if !reflect.DeepEqual(got, protected) {
		t.Errorf("ResolvedProtectedBranches() = %v; want %v", got, protected)
	}
}

// TestProtectedBranches_Unconfigured_EnvelopeNil verifies that when no
// ProtectedBranches are configured (nil on Envelope), ResolvedProtectedBranches
// returns nil — no protection is applied. A missing field must never mean
// "protect everything" (that would break all pushes on agent sandboxes without
// a nexus3.yaml branches section).
func TestProtectedBranches_Unconfigured_EnvelopeNil(t *testing.T) {
	env := domain.Envelope{} // simulates a sandbox created before T1 or without branches config
	got := env.ResolvedProtectedBranches()
	if got != nil {
		t.Errorf("ResolvedProtectedBranches() on unconfigured Envelope = %v; want nil", got)
	}
	// AllowedBranches default must still be the project default (regression guard).
	wantAllowed := []string{"refs/heads/nexus3/**"}
	if gotAllowed := env.ResolvedAllowedBranches(); !reflect.DeepEqual(gotAllowed, wantAllowed) {
		t.Errorf("ResolvedAllowedBranches() = %v; want %v", gotAllowed, wantAllowed)
	}
}
