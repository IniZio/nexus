package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/service"
)

type alwaysFailProber struct{ err error }

func (p *alwaysFailProber) Ping(_ context.Context) error { return p.err }

type alwaysOKProber struct{}

func (p *alwaysOKProber) Ping(_ context.Context) error { return nil }

func TestProbeAndSeedGuest_DeadProberReturnsError(t *testing.T) {
	seederCalled := false
	old := seedShellProfileFn
	seedShellProfileFn = func(_ context.Context, _ domain.SandboxID, _ int, _ int, _ service.GuestSeeder) error {
		seederCalled = true
		return nil
	}
	t.Cleanup(func() { seedShellProfileFn = old })

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

func TestProbeAndSeedGuest_LiveProberSeedIsInvoked(t *testing.T) {
	seedCalled := false
	old := seedShellProfileFn
	seedShellProfileFn = func(_ context.Context, _ domain.SandboxID, _ int, _ int, _ service.GuestSeeder) error {
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

// TestProbeAndSeedGuest_AgentOnboardingIsInvoked is the mutation guard (D-J10).
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

// TestProbeAndSeedGuest_GitCredentialHelperSeeded is the mutation guard.
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

// TestProbeAndSeedGuest_UserMountsSeeded is the mutation guard.
func TestProbeAndSeedGuest_UserMountsSeeded(t *testing.T) {
	called := false
	old := seedUserMountsFn
	seedUserMountsFn = func(_ context.Context, _ domain.SandboxID, _ service.UserMountManifest, _ service.GuestExecer) error {
		called = true
		return nil
	}
	t.Cleanup(func() { seedUserMountsFn = old })

	manifest := service.UserMountManifest{
		HostHome: "/home/alice",
		Mounts: []service.ResolvedUserMount{
			{
				HostPath:         "/home/alice/.claude/plugins",
				GuestPath:        "/root/.claude/plugins",
				Overlay:          true,
				StagingGuestPath: "/run/nexus/usermount/plugins",
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

