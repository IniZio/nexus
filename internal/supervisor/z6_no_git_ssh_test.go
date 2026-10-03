package supervisor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/service"
)

func TestZ6_GitIdentitySoftFailsWhenGitAbsent(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	orig := seedGitIdentityFn
	seedGitIdentityFn = service.SeedGitIdentity
	t.Cleanup(func() { seedGitIdentityFn = orig })

	origProfile := seedShellProfileFn
	seedShellProfileFn = func(_ context.Context, _ domain.SandboxID, _, _ int, _ service.GuestSeeder) error { return nil }
	t.Cleanup(func() { seedShellProfileFn = origProfile })

	origUID := seedHostUIDFn
	seedHostUIDFn = func(_ context.Context, _ domain.SandboxID, _, _ int, _ service.GuestSeeder) error { return nil }
	t.Cleanup(func() { seedHostUIDFn = origUID })

	origOnboard := seedAgentOnboardingFn
	seedAgentOnboardingFn = func(_ context.Context, _ domain.SandboxID, _ string, _ map[string]json.RawMessage, _ service.GuestExecer) error {
		return nil
	}
	t.Cleanup(func() { seedAgentOnboardingFn = origOnboard })

	origHelper := seedGitCredentialHelperFn
	seedGitCredentialHelperFn = func(_ context.Context, _ domain.SandboxID, _ service.GuestSeeder) error { return nil }
	t.Cleanup(func() { seedGitCredentialHelperFn = origHelper })

	in := guestSeedInputs{
		SourcePaths:            []string{"/repo"},
		GitSeeder:              noopSeeder,
		ProfileSeeder:          noopSeeder,
		CredentialHelperSeeder: noopSeeder,
	}

	err := probeAndSeedGuest(context.Background(), pingOKProber{}, in)
	if err != nil {
		t.Errorf("probeAndSeedGuest returned error when git absent from PATH; expected soft-fail: %v", err)
	}
}

func TestZ6_GitSSHRelayStartNoExec(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	socketDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	startGitSSHRelay(ctx, socketDir, domain.Sandbox{}, nil)()
}
