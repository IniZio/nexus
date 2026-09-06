package supervisor

// TestSeedLoop_CredFileSeederCalledForFileBased is the mutation guard for the
// SeedGuestCredFile call inside SeedLoop.
//
// # What it proves
//
// SeedLoop must call service.SeedGuestCredFile so the agent's credential JSON
// file (e.g. cursor/auth.json) is written to GuestCredDirPath before the
// supervisor signals READY. The env payload redirects the agent's lookup via
// XDG_CONFIG_HOME=GuestCredDirPath (wired in buildAgentSeedPayload, S8), but
// the file itself is only written when SeedGuestCredFile is called with a
// populated file seeder — the gap this slice closes.
//
// # Mutation proof
//
// Remove or comment out the `service.SeedGuestCredFile(...)` call in SeedLoop
// and re-run the test:
//
//	FAIL: supervisor.TestSeedLoop_CredFileSeederCalledForFileBased
//	    supervisor_credfile_seed_test.go:NN: credFileSeeder not called for
//	    file-based profile; SeedGuestCredFile call is absent or bypassed
//
// go vet confirms the mutant compiles (no type error is introduced by removing
// one function call). The test is the only guard.
//
// # Claude Code path unchanged
//
// When profile.CredentialFile == "" (Claude Code), SeedGuestCredFile is a
// no-op that returns nil without calling seeder. The credFileSeeder spy would
// therefore not fire, but the test only checks it for a file-based profile.
// Existing tests (TestSeedLoop_ForcePushWritesRealToken etc.) use
// cred.ClaudeCodeProfile with credFileSeeder=nil and must still pass.
import (
	"context"
	"crypto/x509"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
)

func TestSeedLoop_CredFileSeederCalledForFileBased(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "") // ensure kindOAuth path

	var id domain.SandboxID
	id[0] = 0xCF // arbitrary, distinct from other tests

	broker := cred.NewBroker()
	caSeeder := service.GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
		return nil
	})
	agentSeeder := service.GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
		return nil
	})

	// credFileSeeder spy: records whether it was called.
	credFileCalled := false
	credFileSeeder := service.GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
		credFileCalled = true
		return nil
	})

	cert := &x509.Certificate{}
	// CursorAgentProfile has CredentialFile != "", so SeedGuestCredFile must
	// call credFileSeeder. One attempt with immediate success (no retry needed).
	ok, _ := SeedLoop(
		context.Background(), id, &cert,
		caSeeder, agentSeeder,
		broker, nil,
		1, 0, nil, true, cred.CursorAgentProfile, credFileSeeder,
	)
	if !ok {
		t.Fatal("SeedLoop returned ok=false; all seeders succeeded — unexpected failure")
	}

	// MUTATION GUARD: removing the SeedGuestCredFile call in SeedLoop makes
	// credFileCalled stay false here, failing this assertion.
	if !credFileCalled {
		t.Error("credFileSeeder not called for file-based profile; " +
			"SeedGuestCredFile call is absent or bypassed in SeedLoop")
	}
}

// TestSeedLoop_CredFileSeederNotCalledForClaudeCode proves that Claude Code's
// path is byte-identical to before: credFileSeeder is never called when the
// profile has no CredentialFile.
func TestSeedLoop_CredFileSeederNotCalledForClaudeCode(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")

	var id domain.SandboxID
	id[0] = 0xCC

	broker := cred.NewBroker()
	caSeeder := service.GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil })
	agentSeeder := service.GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil })

	credFileCalled := false
	credFileSeeder := service.GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
		credFileCalled = true
		return nil
	})

	cert := &x509.Certificate{}
	ok, _ := SeedLoop(
		context.Background(), id, &cert,
		caSeeder, agentSeeder,
		broker, nil,
		1, 0, nil, true, cred.ClaudeCodeProfile, credFileSeeder,
	)
	if !ok {
		t.Fatal("SeedLoop with ClaudeCodeProfile returned ok=false")
	}

	// ClaudeCodeProfile.CredentialFile == "" so SeedGuestCredFile is a no-op;
	// the spy must not fire.
	if credFileCalled {
		t.Error("credFileSeeder was called for Claude Code profile; " +
			"SeedGuestCredFile must be a no-op when CredentialFile is empty")
	}
}
