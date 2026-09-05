package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// TestRunSeedRoute_CursorRealTokenPushed is the mutation guard for
// runSeedRoute's StaticCredSrc push block.
//
// Sequence:
//  1. Write a synthetic cursor auth.json with a known accessToken.
//  2. Build a StaticCredentialSource via NewCredentialSourceForProfile.
//  3. Pre-register a placeholder for api2.cursor.sh so seeding can mint a scope.
//  4. Call runSeedRoute with routeAgent and StaticCredSrc set.
//  5. Assert broker.Resolve(placeholder) == the known accessToken.
//
// Mutation proof: remove the StaticCredSrc push block from runSeedRoute.
// RegisterPlaceholder mints the scope with realToken=""; without the push,
// broker.Resolve(placeholder) returns "" ≠ realToken → RED.
func TestRunSeedRoute_CursorRealTokenPushed(t *testing.T) {
	const realToken = "tok-real-cursor-test"

	// ── 1. Write synthetic cursor auth.json ──────────────────────────────────
	tmpDir := t.TempDir()
	credDir := filepath.Join(tmpDir, "cursor")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatalf("mkdir cursor dir: %v", err)
	}
	authJSON, err := json.Marshal(map[string]string{
		"accessToken":  realToken,
		"refreshToken": "refresh-unused",
	})
	if err != nil {
		t.Fatalf("marshal auth.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(credDir, "auth.json"), authJSON, 0o600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}

	// Point XDG_CONFIG_HOME at tmpDir so CursorCredPath resolves to
	// tmpDir/cursor/auth.json (profile.CredentialFile == "cursor/auth.json").
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// ── 2. Build StaticCredentialSource ──────────────────────────────────────
	staticSrc, err := cred.NewCredentialSourceForProfile(cred.CursorAgentProfile)
	if err != nil {
		t.Fatalf("NewCredentialSourceForProfile: %v", err)
	}
	if staticSrc == nil {
		t.Fatal("NewCredentialSourceForProfile returned nil source for CursorAgentProfile (expected non-nil)")
	}

	// ── 3. Build broker and sandbox ──────────────────────────────────────────
	broker := cred.NewBroker()
	var id domain.SandboxID
	id[0] = 0xC2

	sb := domain.Sandbox{
		ID:        id,
		AgentName: cred.CursorAgentProfile.Name,
	}

	// Pre-register placeholder so RegisterPlaceholder inside SeedLoop can
	// re-mint the scope. The real token starts empty — the push must fill it.
	if _, err := broker.RegisterPlaceholder(id, cred.CursorAgentProfile.CredentialedHost, ""); err != nil {
		t.Fatalf("RegisterPlaceholder (pre-seed): %v", err)
	}

	// ── 4. Call runSeedRoute ──────────────────────────────────────────────────
	cert := fakeCert()
	noopSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }

	ok, _ := runSeedRoute(context.Background(), routeAgent, seedRouteInputs{
		SB:            sb,
		Cert:          cert,
		CASeeder:      noopSeeder,
		AgentSeeder:   noopSeeder,
		Broker:        broker,
		Refreshers:    nil, // cursor has no OAuth refreshers
		StaticCredSrc: staticSrc,
		Svc:           nil,
	})
	if !ok {
		t.Fatal("runSeedRoute returned ok=false; seed failed")
	}

	// ── 5. Assert real token was pushed ──────────────────────────────────────
	ph, hasPh := broker.Placeholder(id, cred.CursorAgentProfile.CredentialedHost)
	if !hasPh {
		t.Fatalf("broker has no placeholder for %s after runSeedRoute", cred.CursorAgentProfile.CredentialedHost)
	}
	got, ok2 := broker.Resolve(ph)
	if !ok2 {
		t.Fatalf("broker.Resolve(%q) = false after runSeedRoute", ph)
	}
	if got != realToken {
		t.Errorf("broker.Resolve(placeholder) = %q, want %q\n"+
			"(StaticCredSrc push in runSeedRoute did not write real token;\n"+
			" remove the push block from runSeedRoute to reproduce)", got, realToken)
	}
}
