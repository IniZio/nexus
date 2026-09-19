package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
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

// TestProbeAndSeedGuest_OverlaySkippedWithLiveRWMount is the mutation guard (D-1).
func TestProbeAndSeedGuest_OverlaySkippedWithLiveRWMount(t *testing.T) {
	stubClaudePrivateState(t)
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
	stubClaudePrivateState(t)
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
	stubClaudePrivateState(t)
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
	stubClaudePrivateState(t)
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

// stubClaudePrivateState replaces seedClaudePrivateStateFn with a no-op for
// tests that exercise other seeds with a nil execer.
func stubClaudePrivateState(t *testing.T) {
	t.Helper()
	old := seedClaudePrivateStateFn
	seedClaudePrivateStateFn = func(_ context.Context, _ domain.SandboxID, _ service.GuestExecer) error { return nil }
	t.Cleanup(func() { seedClaudePrivateStateFn = old })
}

func TestProbeAndSeedGuest_ClaudePrivateStateSeededWithLiveRWMount(t *testing.T) {
	// The live rw ~/.claude mount shares the host's session state with the
	// guest; the private binds are what keep /root/.claude/projects and
	// friends per-sandbox. They must run exactly when the live mount is present.
	called := 0
	old := seedClaudePrivateStateFn
	seedClaudePrivateStateFn = func(_ context.Context, _ domain.SandboxID, _ service.GuestExecer) error {
		called++
		return nil
	}
	t.Cleanup(func() { seedClaudePrivateStateFn = old })

	if err := probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{HasClaudeRWMount: true}); err != nil {
		t.Fatalf("probeAndSeedGuest: %v", err)
	}
	if called != 1 {
		t.Fatalf("seedClaudePrivateStateFn called %d times with HasClaudeRWMount=true; want 1", called)
	}
	called = 0
	if err := probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{}); err != nil {
		t.Fatalf("probeAndSeedGuest: %v", err)
	}
	if called != 0 {
		t.Fatalf("seedClaudePrivateStateFn called without a live rw mount; the overlay path owns isolation there")
	}
}

func TestProbeAndSeedGuest_ClaudePrivateStateFailClosed(t *testing.T) {
	// A sandbox that cannot mask its session dirs would write transcripts into
	// the host's ~/.claude/projects (the 2026-06-22 leak). Boot must abort.
	old := seedClaudePrivateStateFn
	seedClaudePrivateStateFn = func(_ context.Context, _ domain.SandboxID, _ service.GuestExecer) error {
		return errors.New("mount --bind: EPERM")
	}
	t.Cleanup(func() { seedClaudePrivateStateFn = old })

	err := probeAndSeedGuest(context.Background(), &alwaysOKProber{}, guestSeedInputs{HasClaudeRWMount: true})
	if err == nil {
		t.Fatal("probeAndSeedGuest returned nil when the private-state seed failed; boot would continue with host sessions exposed")
	}
	if !strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("error should name the fail-closed policy; got %v", err)
	}
}

func TestSeedClaudePrivateState_ScriptContent(t *testing.T) {
	var script string
	execer := service.GuestExecer(func(_ context.Context, _ domain.SandboxID, argv []string, _ io.Reader) (int32, error) {
		if len(argv) >= 3 {
			script = argv[2]
		}
		return 0, nil
	})
	if err := seedClaudePrivateState(context.Background(), domain.NewSandboxID(), execer); err != nil {
		t.Fatalf("seedClaudePrivateState: %v", err)
	}
	for _, want := range []string{
		"mount --bind \"$backing/$d\" \"$root/$d\"",
		"mount --bind \"$backing/$f\" \"$root/$f\"",
		claudePrivateBackingRoot,
		"root=/root/.claude",
		"/var/lib/nexus/agentcfg-private",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script missing %q\n%s", want, script)
		}
	}
	for _, d := range []string{"projects", "todos", "shell-snapshots"} {
		if !slices.Contains(claudePrivateDirs, d) {
			t.Errorf("claudePrivateDirs must include %q — it holds per-session state", d)
		}
	}
	if !slices.Contains(claudePrivateFiles, "history.jsonl") {
		t.Error("claudePrivateFiles must include history.jsonl")
	}
	for _, shared := range []string{".credentials.json", "settings.json", "plugins", "skills", "commands", "CLAUDE.md"} {
		if slices.Contains(claudePrivateDirs, shared) || slices.Contains(claudePrivateFiles, shared) {
			t.Errorf("%q must stay on the shared live mount (credential write-through / operator config)", shared)
		}
	}
}

func TestSeedClaudePrivateState_NonZeroExitIsError(t *testing.T) {
	execer := service.GuestExecer(func(_ context.Context, _ domain.SandboxID, _ []string, _ io.Reader) (int32, error) {
		return 1, nil
	})
	if err := seedClaudePrivateState(context.Background(), domain.NewSandboxID(), execer); err == nil {
		t.Fatal("exit 1 from the bind script must surface as an error")
	}
}
