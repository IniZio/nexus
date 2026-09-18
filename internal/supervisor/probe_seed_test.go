package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
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
	seedShellProfileFn = func(_ context.Context, _ domain.SandboxID, _ service.GuestSeeder) error {
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

// TestProbeAndSeedGuest_OverlaySkippedWithLiveRWMount is the mutation guard (D-1).
func TestProbeAndSeedGuest_OverlaySkippedWithLiveRWMount(t *testing.T) {
	overlayCalled := false
	oldOvl := seedOverlayClaudeConfigFn
	seedOverlayClaudeConfigFn = func(_ context.Context, _ domain.SandboxID, _ string, _ service.GuestExecer) error {
		overlayCalled = true
		return nil
	}
	t.Cleanup(func() { seedOverlayClaudeConfigFn = oldOvl })

	err := probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{
		AgentCfgLowerGuestPath: "/run/nexus/agentcfg-lower",
		HasClaudeRWMount:       true,
	})
	if err != nil {
		t.Fatalf("probeAndSeedGuest: %v", err)
	}
	if overlayCalled {
		t.Fatal("seedOverlayClaudeConfigFn was called when HasClaudeRWMount=true — " +
			"overlay must be skipped when a live rw /root/.claude mount is present (D-1 mutation guard)")
	}
}

func TestProbeAndSeedGuest_ClaudeHomeSymlinkSeeded(t *testing.T) {
	called := false
	var gotHome string
	old := seedClaudeHomeSymlinkFn
	seedClaudeHomeSymlinkFn = func(_ context.Context, _ domain.SandboxID, hostHome string, _ service.GuestExecer) error {
		called = true
		gotHome = hostHome
		return nil
	}
	t.Cleanup(func() { seedClaudeHomeSymlinkFn = old })

	err := probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{
		HasClaudeRWMount: true,
		ClaudeHostHome:   "/home/testuser",
	})
	if err != nil {
		t.Fatalf("probeAndSeedGuest: %v", err)
	}
	if !called {
		t.Fatal("seedClaudeHomeSymlinkFn was not called — companion symlink wiring missing")
	}
	if gotHome != "/home/testuser" {
		t.Fatalf("seedClaudeHomeSymlinkFn got hostHome=%q, want /home/testuser", gotHome)
	}
}

func TestProbeAndSeedGuest_ClaudeHomeSymlinkSkippedRootHome(t *testing.T) {
	called := false
	old := seedClaudeHomeSymlinkFn
	seedClaudeHomeSymlinkFn = func(_ context.Context, _ domain.SandboxID, _ string, _ service.GuestExecer) error {
		called = true
		return nil
	}
	t.Cleanup(func() { seedClaudeHomeSymlinkFn = old })

	_ = probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{
		HasClaudeRWMount: true,
		ClaudeHostHome:   "/root",
	})
	if called {
		t.Fatal("seedClaudeHomeSymlinkFn must not be called when ClaudeHostHome==/root")
	}
}

func TestProbeAndSeedGuest_ClaudeHomeSymlinkSkippedEmptyHome(t *testing.T) {
	called := false
	old := seedClaudeHomeSymlinkFn
	seedClaudeHomeSymlinkFn = func(_ context.Context, _ domain.SandboxID, _ string, _ service.GuestExecer) error {
		called = true
		return nil
	}
	t.Cleanup(func() { seedClaudeHomeSymlinkFn = old })

	_ = probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{
		HasClaudeRWMount: true,
		ClaudeHostHome:   "",
	})
	if called {
		t.Fatal("seedClaudeHomeSymlinkFn must not be called when ClaudeHostHome is empty")
	}
}

func TestSeedClaudeHomeSymlink_ScriptContent(t *testing.T) {
	var capturedScript string
	execer := service.GuestExecer(func(_ context.Context, _ domain.SandboxID, argv []string, _ io.Reader) (int32, error) {
		if len(argv) >= 3 {
			capturedScript = argv[2]
		}
		return 0, nil
	})
	if err := seedClaudeHomeSymlink(context.Background(), domain.SandboxID{}, "/home/newman", execer); err != nil {
		t.Fatalf("seedClaudeHomeSymlink: %v", err)
	}
	for _, want := range []string{"mkdir -p /home/newman", "ln -sfn /root/.claude /home/newman/.claude"} {
		if !strings.Contains(capturedScript, want) {
			t.Errorf("script missing %q\nscript:\n%s", want, capturedScript)
		}
	}
}
