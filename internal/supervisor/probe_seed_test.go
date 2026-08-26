package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/service"
)

// alwaysFailProber is a GuestProber whose Ping always returns a non-nil error.
type alwaysFailProber struct{ err error }

func (p *alwaysFailProber) Ping(_ context.Context) error { return p.err }

// alwaysOKProber is a GuestProber whose Ping always returns nil.
type alwaysOKProber struct{}

func (p *alwaysOKProber) Ping(_ context.Context) error { return nil }

// TestProbeAndSeedGuest_DeadProberReturnsError is the mutation guard for the
// ProbeGuestAgent call inside probeAndSeedGuest (D-M4, assertion a):
//
//	Delete ProbeGuestAgent(…) from probeAndSeedGuest → this test fails RED.
//
// When the guest agent is unreachable, probeAndSeedGuest must return a non-nil
// error. RunDetached checks this return and refuses to write supervisor.pid
// (the READY signal), so the spawning CLI gets a hard failure instead of a
// false success for a sandbox whose guest never came up.
func TestProbeAndSeedGuest_DeadProberReturnsError(t *testing.T) {
	seederCalled := false
	old := seedShellProfileFn
	seedShellProfileFn = func(_ context.Context, _ domain.SandboxID, _ service.GuestSeeder) error {
		seederCalled = true
		return nil
	}
	t.Cleanup(func() { seedShellProfileFn = old })

	// An already-cancelled context drains the 30s probe window instantly so the
	// test completes in microseconds rather than waiting out the full timeout.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := probeAndSeedGuest(ctx, &alwaysFailProber{err: errors.New("vsock refused")}, guestSeedInputs{})
	if err == nil {
		t.Fatal("probeAndSeedGuest with dead prober returned nil — ProbeGuestAgent call may be missing (D-M4 mutation guard)")
	}
	if seederCalled {
		t.Error("shell-profile seeder must not be called when probe fails")
	}
}

// TestProbeAndSeedGuest_LiveProberSeedIsInvoked is the mutation guard for the
// seedShellProfileFn call inside probeAndSeedGuest (D-M4, assertion b):
//
//	Delete seedShellProfileFn(…) from probeAndSeedGuest → this test fails RED.
//
// When the guest agent is reachable, the shell-profile seeder must be invoked
// so that an agent started interactively in a guest shell (the herdr pane path)
// receives its credential via the login-shell drop-in.
func TestProbeAndSeedGuest_LiveProberSeedIsInvoked(t *testing.T) {
	seedCalled := false
	old := seedShellProfileFn
	seedShellProfileFn = func(_ context.Context, _ domain.SandboxID, _ service.GuestSeeder) error {
		seedCalled = true
		return nil
	}
	t.Cleanup(func() { seedShellProfileFn = old })

	err := probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{})
	if err != nil {
		t.Fatalf("probeAndSeedGuest with live prober: unexpected error %v", err)
	}
	if !seedCalled {
		t.Fatal("seedShellProfileFn was not called — SeedGuestShellProfile wiring missing from probeAndSeedGuest (D-M4 mutation guard)")
	}
}

// TestProbeAndSeedGuest_AgentOnboardingIsInvoked is the mutation guard for the
// seedAgentOnboardingFn call inside probeAndSeedGuest (D-J10):
//
//	Delete seedAgentOnboardingFn(…) from probeAndSeedGuest → this test fails RED.
//
// When the guest agent is reachable, the onboarding seeder must be invoked so
// that an interactively started claude skips the first-run wizards and reaches
// its prompt directly.
func TestProbeAndSeedGuest_AgentOnboardingIsInvoked(t *testing.T) {
	onboardCalled := false
	old := seedAgentOnboardingFn
	seedAgentOnboardingFn = func(_ context.Context, _ domain.SandboxID, _ string, _ map[string]json.RawMessage, _ service.GuestExecer) error {
		onboardCalled = true
		return nil
	}
	t.Cleanup(func() { seedAgentOnboardingFn = old })

	err := probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{})
	if err != nil {
		t.Fatalf("probeAndSeedGuest with live prober: unexpected error %v", err)
	}
	if !onboardCalled {
		t.Fatal("seedAgentOnboardingFn was not called — SeedGuestAgentOnboarding wiring missing from probeAndSeedGuest (D-J10 mutation guard)")
	}
}

// TestProbeAndSeedGuest_BypassConsentIsSeeded is the mutation guard for the
// seedBypassConsentFn call inside probeAndSeedGuest (D-J12). The call is
// conditional on AgentCfgLowerGuestPath being empty (sharing OFF). When sharing
// is ON, the bypass key is already in the staged lower settings.json and an
// upper-layer write would shadow the entire file, dropping enabledPlugins.
//
// Mutation guards:
//
//	Delete the seedBypassConsentFn call → sharing-OFF sub-test fails RED.
//	Keep calling it unconditionally → sharing-ON sub-test fails RED (upper shadows lower).
func TestProbeAndSeedGuest_BypassConsentIsSeeded(t *testing.T) {
	t.Run("sharing_off_calls_bypass_seed", func(t *testing.T) {
		called := false
		old := seedBypassConsentFn
		seedBypassConsentFn = func(_ context.Context, _ domain.SandboxID, _ service.GuestExecer) error {
			called = true
			return nil
		}
		t.Cleanup(func() { seedBypassConsentFn = old })

		// AgentCfgLowerGuestPath == "" → sharing OFF → seedBypassConsentFn must be called.
		err := probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{})
		if err != nil {
			t.Fatalf("probeAndSeedGuest: %v", err)
		}
		if !called {
			t.Fatal("seedBypassConsentFn was not called when sharing is OFF — bypass consent will be missing (D-J12 mutation guard)")
		}
	})

	t.Run("sharing_on_skips_bypass_seed", func(t *testing.T) {
		called := false
		old := seedBypassConsentFn
		seedBypassConsentFn = func(_ context.Context, _ domain.SandboxID, _ service.GuestExecer) error {
			called = true
			return nil
		}
		t.Cleanup(func() { seedBypassConsentFn = old })

		// Stub seedOverlayClaudeConfigFn so the test doesn't need a live VM.
		oldOvl := seedOverlayClaudeConfigFn
		seedOverlayClaudeConfigFn = func(_ context.Context, _ domain.SandboxID, _ string, _ service.GuestExecer) error {
			return nil
		}
		t.Cleanup(func() { seedOverlayClaudeConfigFn = oldOvl })

		// AgentCfgLowerGuestPath != "" → sharing ON → bypass key is in the lower
		// layer; seedBypassConsentFn must NOT be called (upper write would shadow
		// the entire lower settings.json, dropping enabledPlugins).
		err := probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{
			AgentCfgLowerGuestPath: "/run/nexus3/agentcfg-lower",
		})
		if err != nil {
			t.Fatalf("probeAndSeedGuest: %v", err)
		}
		if called {
			t.Fatal("seedBypassConsentFn was called when sharing is ON — upper write would shadow lower settings.json, dropping enabledPlugins (overlayfs file-granular regression)")
		}
	})
}

// TestProbeAndSeedGuest_GitIdentitySeededForAnySourcePaths is the mutation
// guard for the seedGitIdentityFn call inside probeAndSeedGuest.
//
// It pins the fix for a defect that a payload-level test could not see. The
// gitconfig seed used to live on the human-secrets branch, gated on
// `len(sb.Envelope.SecretHosts) > 0` — whether the sandbox holds a push
// credential. The gitconfig answers two questions that have nothing to do with
// pushing: may git read this directory at all (safe.directory), and whose name
// goes on a commit (identity). The result was that every `--agent` sandbox
// failed `git log` in its own mounted source with "detected dubious ownership",
// while unit tests over the payload builder stayed green throughout, because
// the payload was always correct — it was simply never written.
//
// So this test asserts the CALL, with no secrets configured at all.
func TestProbeAndSeedGuest_GitIdentitySeededForAnySourcePaths(t *testing.T) {
	var gotPaths []string
	called := false
	old := seedGitIdentityFn
	seedGitIdentityFn = func(_ context.Context, _ domain.SandboxID, _ map[string]string, sourcePaths []string, _ service.GuestSeeder) (string, error) {
		called = true
		gotPaths = sourcePaths
		return "branch", nil
	}
	t.Cleanup(func() { seedGitIdentityFn = old })

	err := probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{
		SourcePaths: []string{"/work"},
	})
	if err != nil {
		t.Fatalf("probeAndSeedGuest with live prober: unexpected error %v", err)
	}
	if !called {
		t.Fatal("seedGitIdentityFn was not called for a sandbox with source paths and no secrets — " +
			"the guest would report dubious ownership on its own mounted source")
	}
	if len(gotPaths) != 1 || gotPaths[0] != "/work" {
		t.Errorf("source paths not forwarded to the seeder: got %v, want [/work]", gotPaths)
	}
}

// A sandbox with nothing mounted has no directory to exempt and no repository
// to attribute, so it must not write a gitconfig at all.
func TestProbeAndSeedGuest_NoGitIdentityWithoutSourcePaths(t *testing.T) {
	called := false
	old := seedGitIdentityFn
	seedGitIdentityFn = func(_ context.Context, _ domain.SandboxID, _ map[string]string, _ []string, _ service.GuestSeeder) (string, error) {
		called = true
		return "", nil
	}
	t.Cleanup(func() { seedGitIdentityFn = old })

	if err := probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{}); err != nil {
		t.Fatalf("probeAndSeedGuest: unexpected error %v", err)
	}
	if called {
		t.Error("seedGitIdentityFn was called for a sandbox with no source paths")
	}
}

// TestProbeAndSeedGuest_GitCredentialHelperSeeded is the mutation guard for the
// seedGitCredentialHelperFn call inside probeAndSeedGuest.
//
// The gitconfig written by seedGitIdentityFn references the helper script at
// GuestGitCredentialHelperPath. If the script is never seeded, every git push
// inside the guest fails with "credential helper not found" — a failure that
// is invisible to the gitconfig payload tests (which assert on bytes written,
// not on whether the referenced script file exists).
//
// This test asserts the CALL, following the same pattern as the gitconfig
// mutation guard above:
//
//	Delete seedGitCredentialHelperFn(…) from probeAndSeedGuest → this test
//	fails RED.
func TestProbeAndSeedGuest_GitCredentialHelperSeeded(t *testing.T) {
	called := false
	old := seedGitCredentialHelperFn
	seedGitCredentialHelperFn = func(_ context.Context, _ domain.SandboxID, _ service.GuestSeeder) error {
		called = true
		return nil
	}
	t.Cleanup(func() { seedGitCredentialHelperFn = old })

	err := probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{
		SourcePaths: []string{"/work"},
	})
	if err != nil {
		t.Fatalf("probeAndSeedGuest with live prober: unexpected error %v", err)
	}
	if !called {
		t.Fatal("seedGitCredentialHelperFn was not called for a sandbox with source paths — " +
			"the git credential helper script will be absent from the guest and pushes will fail")
	}
}

// TestProbeAndSeedGuest_NoGitCredentialHelperWithoutSourcePaths asserts that
// the helper script is not seeded for sandboxes with no source paths (same
// gate as the gitconfig).
func TestProbeAndSeedGuest_NoGitCredentialHelperWithoutSourcePaths(t *testing.T) {
	called := false
	old := seedGitCredentialHelperFn
	seedGitCredentialHelperFn = func(_ context.Context, _ domain.SandboxID, _ service.GuestSeeder) error {
		called = true
		return nil
	}
	t.Cleanup(func() { seedGitCredentialHelperFn = old })

	if err := probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{}); err != nil {
		t.Fatalf("probeAndSeedGuest: unexpected error %v", err)
	}
	if called {
		t.Error("seedGitCredentialHelperFn was called for a sandbox with no source paths")
	}
}

// TestProbeAndSeedGuest_UserMountsSeeded is the mutation guard for the
// seedUserMountsFn call inside probeAndSeedGuest.
//
//	Delete the seedUserMountsFn block from probeAndSeedGuest → this test fails RED.
//
// When UserMounts is non-nil, probeAndSeedGuest must invoke seedUserMountsFn
// so the guest receives the home symlink, PATH drop-in, and overlay mounts.
func TestProbeAndSeedGuest_UserMountsSeeded(t *testing.T) {
	called := false
	old := seedUserMountsFn
	seedUserMountsFn = func(_ context.Context, _ domain.SandboxID, _ service.UserMountManifest, _ service.GuestExecer) error {
		called = true
		return nil
	}
	t.Cleanup(func() { seedUserMountsFn = old })

	manifest := service.UserMountManifest{
		HostHome: "$HOME",
		Mounts: []service.ResolvedUserMount{
			{
				HostPath:         "$HOME/.claude/plugins",
				GuestPath:        "/root/.claude/plugins",
				Overlay:          true,
				StagingGuestPath: "/run/nexus3/usermount/plugins",
			},
		},
	}
	err := probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{
		UserMounts: &manifest,
	})
	if err != nil {
		t.Fatalf("probeAndSeedGuest with live prober: unexpected error %v", err)
	}
	if !called {
		t.Fatal("seedUserMountsFn was not called — SeedGuestUserMounts wiring missing from probeAndSeedGuest")
	}
}

// TestProbeAndSeedGuest_NoUserMountsWhenAbsent asserts that seedUserMountsFn
// is not called when UserMounts is nil (sharing disabled or manifest absent).
func TestProbeAndSeedGuest_NoUserMountsWhenAbsent(t *testing.T) {
	called := false
	old := seedUserMountsFn
	seedUserMountsFn = func(_ context.Context, _ domain.SandboxID, _ service.UserMountManifest, _ service.GuestExecer) error {
		called = true
		return nil
	}
	t.Cleanup(func() { seedUserMountsFn = old })

	if err := probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{}); err != nil {
		t.Fatalf("probeAndSeedGuest: unexpected error %v", err)
	}
	if called {
		t.Error("seedUserMountsFn was called for a sandbox with no user mounts manifest")
	}
}

