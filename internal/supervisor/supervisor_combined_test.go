package supervisor

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
)

type captureGuestSeeder struct {
	payloads [][]byte
	calls    int
}

func (c *captureGuestSeeder) fn() service.GuestSeeder {
	return func(_ context.Context, _ domain.SandboxID, payload []byte) error {
		c.payloads = append(c.payloads, append([]byte(nil), payload...))
		c.calls++
		return nil
	}
}

func (c *captureGuestSeeder) combined() []byte {
	var out []byte
	for _, p := range c.payloads {
		out = append(out, p...)
	}
	return out
}

// fakeCert returns a cert with non-nil Raw so SeedCA proceeds past its nil-cert guard.
func fakeCert() *x509.Certificate {
	return &x509.Certificate{Raw: []byte("fake-cert-der-for-test")}
}

// combinedSandboxWithEnvSecret returns a sandbox with agent name and env-resolved secret.
func combinedSandboxWithEnvSecret(id domain.SandboxID, envKey string) domain.Sandbox {
	// Use "example.com" — not in the GitHub host list, so ResolveEnvelopeSecrets
	// reads the token from os.Getenv(envKey) rather than `gh auth token`.
	spec := envKey + "@example.com"
	return domain.Sandbox{
		ID:        id,
		AgentName: "claude",
		Envelope: domain.Envelope{
			SecretHosts: []string{"example.com"},
			SecretSpecs: []string{spec},
		},
	}
}

// Mutation guard: Drop SeedGuestAgentAndSecrets call → NODE_EXTRA_CA_CERTS disappears → RED.
func TestSeedAgentAndHumanSecrets_ContainsAgentVars(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "") // ensure kindOAuth path
	t.Setenv("NEXUS_TEST_SECRET_A1", "supervisor-secret-for-a1")

	ctx := context.Background()
	var id domain.SandboxID
	id[0] = 0xA1
	sb := combinedSandboxWithEnvSecret(id, "NEXUS_TEST_SECRET_A1")

	broker := cred.NewBroker()
	credCap := &captureGuestSeeder{}
	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }

	ok, _ := seedAgentAndHumanSecrets(ctx, sb, fakeCert(), caSeeder, credCap.fn(), broker, nil, nil, cred.MustProfileByName(cred.ClaudeCodeProfileName), nil, nil)
	if !ok {
		t.Fatal("seedAgentAndHumanSecrets returned ok=false; combined seeding failed")
	}

	payload := credCap.combined()

	if !bytes.Contains(payload, []byte("CLAUDE_CODE_OAUTH_TOKEN=")) {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN must be seeded for claude-code profile (broker placeholder path)\npayload:\n%s", payload)
	}
	if !bytes.Contains(payload, []byte("NODE_EXTRA_CA_CERTS=")) {
		t.Errorf("combined supervisor payload missing NODE_EXTRA_CA_CERTS (agent half absent — MUTATION GUARD)\npayload:\n%s", payload)
	}
}

// Mutation guard: Drop SecretSpecs → NEXUS_CRED_EXAMPLE_COM_TOKEN disappears → RED.
func TestSeedAgentAndHumanSecrets_ContainsSecretVars(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("NEXUS_TEST_SECRET_A2", "supervisor-secret-for-a2")

	ctx := context.Background()
	var id domain.SandboxID
	id[0] = 0xA2
	sb := combinedSandboxWithEnvSecret(id, "NEXUS_TEST_SECRET_A2")

	broker := cred.NewBroker()
	credCap := &captureGuestSeeder{}
	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }

	ok, _ := seedAgentAndHumanSecrets(ctx, sb, fakeCert(), caSeeder, credCap.fn(), broker, nil, nil, cred.MustProfileByName(cred.ClaudeCodeProfileName), nil, nil)
	if !ok {
		t.Fatal("seedAgentAndHumanSecrets returned ok=false")
	}

	payload := credCap.combined()

	if !bytes.Contains(payload, []byte("NEXUS_TEST_SECRET_A2=")) {
		t.Errorf("combined supervisor payload missing NEXUS_TEST_SECRET_A2= (secret half absent)\npayload:\n%s", payload)
	}
}

func TestSeedAgentAndHumanSecrets_OneWrite(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("NEXUS_TEST_SECRET_A3", "supervisor-secret-for-a3")

	ctx := context.Background()
	var id domain.SandboxID
	id[0] = 0xA3
	sb := combinedSandboxWithEnvSecret(id, "NEXUS_TEST_SECRET_A3")

	broker := cred.NewBroker()
	credCap := &captureGuestSeeder{}
	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }

	ok, _ := seedAgentAndHumanSecrets(ctx, sb, fakeCert(), caSeeder, credCap.fn(), broker, nil, nil, cred.MustProfileByName(cred.ClaudeCodeProfileName), nil, nil)
	if !ok {
		t.Fatal("seedAgentAndHumanSecrets returned ok=false")
	}

	if credCap.calls != 1 {
		t.Errorf("credSeeder called %d times, want exactly 1 (overwrite prevented)", credCap.calls)
	}
}

// ── dispatch tests ──
func sandboxWithProxy(agentName string, secretHosts []string) domain.Sandbox {
	return domain.Sandbox{
		AgentName: agentName,
		Envelope:  domain.Envelope{SecretHosts: secretHosts},
	}
}

func TestChooseSeedRoute_Dispatch(t *testing.T) {
	cases := []struct {
		name      string
		sb        domain.Sandbox
		wantRoute seedRoute
	}{
		{
			name:      "no_proxy_returns_none",
			sb:        domain.Sandbox{Envelope: domain.Envelope{OpenEgress: true}},
			wantRoute: routeNone,
		},
		{
			name:      "agent_only_returns_agent",
			sb:        sandboxWithProxy("claude", nil),
			wantRoute: routeAgent,
		},
		{
			name:      "secrets_only_returns_human",
			sb:        sandboxWithProxy("", []string{"github.com"}),
			wantRoute: routeHumanSecrets,
		},
		{
			// MUT-A guard: disabling the combined case makes this return routeHumanSecrets.
			name:      "agent_and_secrets_returns_combined",
			sb:        sandboxWithProxy("claude", []string{"github.com"}),
			wantRoute: routeCombined,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := chooseSeedRoute(tc.sb)
			if got != tc.wantRoute {
				t.Errorf("chooseSeedRoute = %v, want %v", got, tc.wantRoute)
			}
		})
	}
}

// MUT-B: swap routeCombined and routeHumanSecrets cases → agent+secrets → routeHumanSecrets → RED.
func TestChooseSeedRoute_Ordering(t *testing.T) {
	sb := sandboxWithProxy("claude", []string{"github.com"})
	got := chooseSeedRoute(sb)
	if got != routeCombined {
		t.Errorf("chooseSeedRoute for agent+secrets = %v, want routeCombined (%v)\n"+
			"If routeHumanSecrets was returned, the combined case is ordered after the human-secrets case;\n"+
			"that silently drops the Claude credential from agent+secrets sandboxes.",
			got, routeCombined)
	}
	if got == routeHumanSecrets {
		t.Errorf("agent+secrets sandbox routed to routeHumanSecrets: ordering defect — combined case must precede human-secrets case")
	}
}

// ── route→seeder binding tests ──
func TestRunSeedRoute_CombinedCallsCombinedSeeder(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("NEXUS_TEST_SECRET_RS1", "rs1-secret")

	var combinedCalled, humanCalled bool

	origCombined := seedAgentAndHumanSecretsFn
	origHuman := seedHumanSecretsFn
	t.Cleanup(func() {
		seedAgentAndHumanSecretsFn = origCombined
		seedHumanSecretsFn = origHuman
	})
	seedAgentAndHumanSecretsFn = func(_ context.Context, _ domain.Sandbox, _ *x509.Certificate,
		_, _ service.GuestSeeder, _ *cred.Broker, _ []*cred.Refresher, _ PerimeterCAGetter, _ cred.AgentProfile, _ []cred.AgentProfile, _ service.GuestSeeder,
	) (bool, bool) {
		combinedCalled = true
		return true, true
	}
	seedHumanSecretsFn = func(_ context.Context, _ domain.Sandbox, _ *x509.Certificate,
		_, _ service.GuestSeeder, _ *cred.Broker, _ PerimeterCAGetter,
	) (bool, bool) {
		humanCalled = true
		return true, true
	}

	in := seedRouteInputs{
		SB:          sandboxWithProxy("claude", []string{"github.com"}),
		Cert:        fakeCert(),
		CASeeder:    func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil },
		AgentSeeder: func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil },
		Broker:      cred.NewBroker(),
	}

	ok, _ := runSeedRoute(context.Background(), routeCombined, in)
	if !ok {
		t.Fatal("runSeedRoute returned ok=false for routeCombined")
	}
	if !combinedCalled {
		t.Error("seedAgentAndHumanSecretsFn was NOT called for routeCombined (wrong seeder dispatched)")
	}
	if humanCalled {
		t.Error("seedHumanSecretsFn was called for routeCombined (routing defect: combined fell through to human-only)")
	}
}

func TestRunSeedRoute_HumanSecretsCallsHumanSeeder(t *testing.T) {
	var humanCalled, combinedCalled bool

	origCombined := seedAgentAndHumanSecretsFn
	origHuman := seedHumanSecretsFn
	t.Cleanup(func() {
		seedAgentAndHumanSecretsFn = origCombined
		seedHumanSecretsFn = origHuman
	})
	seedAgentAndHumanSecretsFn = func(_ context.Context, _ domain.Sandbox, _ *x509.Certificate,
		_, _ service.GuestSeeder, _ *cred.Broker, _ []*cred.Refresher, _ PerimeterCAGetter, _ cred.AgentProfile, _ []cred.AgentProfile, _ service.GuestSeeder,
	) (bool, bool) {
		combinedCalled = true
		return true, true
	}
	seedHumanSecretsFn = func(_ context.Context, _ domain.Sandbox, _ *x509.Certificate,
		_, _ service.GuestSeeder, _ *cred.Broker, _ PerimeterCAGetter,
	) (bool, bool) {
		humanCalled = true
		return true, true
	}

	in := seedRouteInputs{
		SB:          sandboxWithProxy("", []string{"github.com"}),
		Cert:        fakeCert(),
		CASeeder:    func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil },
		AgentSeeder: func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil },
		Broker:      cred.NewBroker(),
	}

	ok, _ := runSeedRoute(context.Background(), routeHumanSecrets, in)
	if !ok {
		t.Fatal("runSeedRoute returned ok=false for routeHumanSecrets")
	}
	if !humanCalled {
		t.Error("seedHumanSecretsFn was NOT called for routeHumanSecrets")
	}
	if combinedCalled {
		t.Error("seedAgentAndHumanSecretsFn was called for routeHumanSecrets (routing defect)")
	}
}

// TestRunSeedRoute_AgentCallsSeedLoop closes the last uncovered route binding.
// An independent review mutated routeAgent to dispatch at the combined seeder and
// found NOTHING caught it — the other three arms were guarded. Its failure mode
// mirrors the defect this set exists to fix: instead of an agent losing its
// credential, a guest that runs NO agent is handed agent credential env vars.
//
// routeAgent is reached by two kinds of sandbox: one with an agent, and
// (via !OpenEgress) a closed-egress sandbox with no agent and no secrets. Both
// take this arm; only the first may receive agent credentials. Asserting only
// "seedLoopFn was called" would pass while that distinction was inverted.
//
// MUT: dispatch routeAgent at seedAgentAndHumanSecretsFn → RED.
//
// Hardcode final argument to true → no-agent subtest RED.
func TestRunSeedRoute_AgentCallsSeedLoop(t *testing.T) {
	cases := []struct {
		name              string
		sb                domain.Sandbox
		wantSeedAgentCred bool
	}{
		{
			name:              "agent sandbox receives agent credentials",
			sb:                sandboxWithProxy("claude-code", nil),
			wantSeedAgentCred: true,
		},
		{
			// Closed egress, no agent, no secrets: still has a proxy (the
			// !OpenEgress clause), still routes here, but must NOT be given
			// credential env vars for an agent it does not run.
			name:              "closed-egress sandbox with no agent gets the CA only",
			sb:                domain.Sandbox{},
			wantSeedAgentCred: false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var loopCalled, combinedCalled, humanCalled bool
			var gotSeedAgentCreds bool

			origLoop, origCombined, origHuman := seedLoopFn, seedAgentAndHumanSecretsFn, seedHumanSecretsFn
			t.Cleanup(func() {
				seedLoopFn, seedAgentAndHumanSecretsFn, seedHumanSecretsFn = origLoop, origCombined, origHuman
			})

			seedLoopFn = func(_ context.Context, _ domain.SandboxID, _ **x509.Certificate,
				_, _ service.GuestSeeder, _ *cred.Broker, _ []*cred.Refresher,
				_ int, _ time.Duration, _ PerimeterCAGetter, seedAgentCreds bool, _ cred.AgentProfile, _ []cred.AgentProfile, _ service.GuestSeeder,
			) (bool, bool) {
				loopCalled = true
				gotSeedAgentCreds = seedAgentCreds
				return true, true
			}
			seedAgentAndHumanSecretsFn = func(_ context.Context, _ domain.Sandbox, _ *x509.Certificate,
				_, _ service.GuestSeeder, _ *cred.Broker, _ []*cred.Refresher, _ PerimeterCAGetter, _ cred.AgentProfile, _ []cred.AgentProfile, _ service.GuestSeeder,
			) (bool, bool) {
				combinedCalled = true
				return true, true
			}
			seedHumanSecretsFn = func(_ context.Context, _ domain.Sandbox, _ *x509.Certificate,
				_, _ service.GuestSeeder, _ *cred.Broker, _ PerimeterCAGetter,
			) (bool, bool) {
				humanCalled = true
				return true, true
			}

			in := seedRouteInputs{
				SB:          c.sb,
				Cert:        fakeCert(),
				CASeeder:    func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil },
				AgentSeeder: func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil },
				Broker:      cred.NewBroker(),
			}

			if ok, _ := runSeedRoute(context.Background(), routeAgent, in); !ok {
				t.Fatal("runSeedRoute returned ok=false for routeAgent")
			}
			if !loopCalled {
				t.Error("seedLoopFn was NOT called for routeAgent")
			}
			if combinedCalled || humanCalled {
				t.Errorf("routeAgent reached the wrong seeder (combined=%v human=%v)", combinedCalled, humanCalled)
			}
			if gotSeedAgentCreds != c.wantSeedAgentCred {
				t.Errorf("seedAgentCreds = %v, want %v — a guest that runs no agent must not be seeded agent credentials",
					gotSeedAgentCreds, c.wantSeedAgentCred)
			}
		})
	}
}

// writeFreshSupervisorStore writes a fresh cred store so NewRefresher returns cached token without HTTP.
func writeFreshSupervisorStore(t *testing.T, accessToken string) string {
	t.Helper()
	s := map[string]any{
		"access_token":   accessToken,
		"refresh_token":  "rt-dummy-supervisor-test",
		"expires_at":     time.Now().Add(time.Hour).Format(time.RFC3339),
		"token_type":     "Bearer",
		"client_id":      "test-client",
		"client_secret":  "",
		"token_endpoint": "http://localhost:0/no-http-calls",
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("writeFreshSupervisorStore: marshal: %v", err)
	}
	p := filepath.Join(t.TempDir(), "store.json")
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatalf("writeFreshSupervisorStore: write: %v", err)
	}
	return p
}

func TestSeedLoop_ForcePushWritesRealToken(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "") // ensure kindOAuth path

	const realToken = "tok-real-seedloop-fp"
	var id domain.SandboxID
	id[0] = 0xF1

	broker := cred.NewBroker()
	storePath := writeFreshSupervisorStore(t, realToken)
	r, err := cred.NewRefresher(storePath, service.AnthropicAPIHost, broker)
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}

	if _, err := broker.RegisterPlaceholder(id, service.AnthropicAPIHost, ""); err != nil {
		t.Fatalf("RegisterPlaceholder (initial): %v", err)
	}

	r.Register(id)

	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatalf("Token (ticker): %v", err)
	}

	if ph, ok := broker.Placeholder(id, service.AnthropicAPIHost); !ok {
		t.Fatal("broker has no placeholder after ticker push (precondition)")
	} else if got, _ := broker.Resolve(ph); got != realToken {
		t.Fatalf("after ticker push: placeholder resolves to %q, want %q (precondition)", got, realToken)
	}

	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }
	agentSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }
	cert := fakeCert()
	ok, _ := SeedLoop(
		context.Background(), id, &cert,
		caSeeder, agentSeeder,
		broker, []*cred.Refresher{r},
		1, 0, nil, true, cred.MustProfileByName(cred.ClaudeCodeProfileName), nil, nil,
	)
	if !ok {
		t.Fatal("SeedLoop returned ok=false; seed failed")
	}

	ph, hasPh := broker.Placeholder(id, service.AnthropicAPIHost)
	if !hasPh {
		t.Fatal("broker has no placeholder for anthropic scope after SeedLoop")
	}
	got, ok2 := broker.Resolve(ph)
	if !ok2 {
		t.Fatalf("broker.Resolve(%q) = false after SeedLoop", ph)
	}
	if got != realToken {
		t.Errorf("broker.Resolve(placeholder) = %q, want %q\n"+
			"(ForcePush in SeedLoop did not write real token to re-minted scope;\n"+
			" revert supervisor.go:SeedLoop ForcePush → Token to reproduce)", got, realToken)
	}
}

func TestSeedAgentAndHumanSecrets_ForcePushWritesRealToken(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("NEXUS_TEST_SAHS_FP", "secret-val-for-fp-test")

	const realToken = "tok-real-sahs-fp"
	var id domain.SandboxID
	id[0] = 0xF2

	broker := cred.NewBroker()
	storePath := writeFreshSupervisorStore(t, realToken)
	r, err := cred.NewRefresher(storePath, service.AnthropicAPIHost, broker)
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}

	if _, err := broker.RegisterPlaceholder(id, service.AnthropicAPIHost, ""); err != nil {
		t.Fatalf("RegisterPlaceholder (initial): %v", err)
	}

	r.Register(id)

	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatalf("Token (ticker): %v", err)
	}

	if ph, ok := broker.Placeholder(id, service.AnthropicAPIHost); !ok {
		t.Fatal("broker has no placeholder after ticker push (precondition)")
	} else if got, _ := broker.Resolve(ph); got != realToken {
		t.Fatalf("after ticker push: placeholder resolves to %q, want %q (precondition)", got, realToken)
	}

	sb := combinedSandboxWithEnvSecret(id, "NEXUS_TEST_SAHS_FP")
	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }
	credCap := &captureGuestSeeder{}
	ok, _ := seedAgentAndHumanSecrets(
		context.Background(), sb, fakeCert(),
		caSeeder, credCap.fn(),
		broker, []*cred.Refresher{r}, nil, cred.MustProfileByName(cred.ClaudeCodeProfileName), nil, nil,
	)
	if !ok {
		t.Fatal("seedAgentAndHumanSecrets returned ok=false; combined seeding failed")
	}

	ph, hasPh := broker.Placeholder(id, service.AnthropicAPIHost)
	if !hasPh {
		t.Fatal("broker has no placeholder for anthropic scope after seedAgentAndHumanSecrets")
	}
	got, ok2 := broker.Resolve(ph)
	if !ok2 {
		t.Fatalf("broker.Resolve(%q) = false after seedAgentAndHumanSecrets", ph)
	}
	if got != realToken {
		t.Errorf("broker.Resolve(placeholder) = %q, want %q\n"+
			"(ForcePush in seedAgentAndHumanSecrets did not write real token to re-minted scope;\n"+
			" revert supervisor.go:seedAgentAndHumanSecrets ForcePush → Token to reproduce)", got, realToken)
	}
}

func TestRegisterMCPOAuthPlaceholders(t *testing.T) {
	broker := cred.NewBroker()
	sid := domain.SandboxID{99}

	configs := []service.MCPOAuthRefreshConfig{
		{ServerName: "linear-server", Host: "mcp.linear.app", AccessToken: "tok-linear-1"},
		{ServerName: "glitchtip", Host: "app.glitchtip.com", AccessToken: "tok-glitch-1"},
		// These two must be skipped (empty access_token / empty host).
		{ServerName: "empty-token", Host: "example.com", AccessToken: ""},
		{ServerName: "empty-host", Host: "", AccessToken: "some-tok"},
	}

	registerMCPOAuthPlaceholders(broker, sid, configs)

	for _, tc := range []struct {
		host       string
		initialTok string
		rotatedTok string
	}{
		{"mcp.linear.app", "tok-linear-1", "tok-linear-2"},
		{"app.glitchtip.com", "tok-glitch-1", "tok-glitch-2"},
	} {
		ph, ok := broker.Placeholder(sid, tc.host)
		if !ok || ph == "" {
			t.Errorf("broker.Placeholder(sid, %q) = (%q, %v); want non-empty placeholder — "+
				"registerMCPOAuthPlaceholders did not call RegisterPlaceholder for this host", tc.host, ph, ok)
			continue
		}

		// Verify the initial real token resolves correctly via the placeholder.
		got, resolveOK := broker.Resolve(ph)
		if !resolveOK || got != tc.initialTok {
			t.Errorf("broker.Resolve(%q) = (%q, %v), want (%q, true) for host %q",
				ph, got, resolveOK, tc.initialTok, tc.host)
		}

		// Simulate ForcePush: SetRealToken must succeed because the scope is
		// registered. This is the direct proof that the fix unblocks ForcePush.
		if err := broker.SetRealToken(sid, tc.host, tc.rotatedTok); err != nil {
			t.Errorf("broker.SetRealToken after registerMCPOAuthPlaceholders: %v — "+
				"scope not registered; ForcePush would have returned 'no placeholder registered'", err)
			continue
		}

		// After rotation, Resolve must return the new token (MITM swap path).
		got, resolveOK = broker.Resolve(ph)
		if !resolveOK || got != tc.rotatedTok {
			t.Errorf("broker.Resolve after SetRealToken: got (%q, %v), want (%q, true) for host %q",
				got, resolveOK, tc.rotatedTok, tc.host)
		}
	}

	for _, host := range []string{"example.com"} {
		if _, ok := broker.Placeholder(sid, host); ok {
			t.Errorf("broker should NOT have placeholder for %q (empty access_token was provided)", host)
		}
	}
}

func TestMCPOAuthSeedPayload(t *testing.T) {
	broker := cred.NewBroker()
	sid := domain.SandboxID{42}
	const realToken = "real-linear-access-token"

	configs := []service.MCPOAuthRefreshConfig{
		{ServerName: "linear-server", Host: "mcp.linear.app", AccessToken: realToken},
		// Skip entries must not appear in returned map.
		{ServerName: "bad-empty-token", Host: "example.com", AccessToken: ""},
	}

	seeds := registerMCPOAuthPlaceholders(broker, sid, configs)

	ph, ok := seeds["linear-server"]
	if !ok || ph == "" {
		t.Fatalf("registerMCPOAuthPlaceholders returned no placeholder for linear-server: seeds=%v", seeds)
	}
	if _, bad := seeds["bad-empty-token"]; bad {
		t.Error("registerMCPOAuthPlaceholders must not include skipped servers in the returned map")
	}

	payload := buildMCPOAuthCredPayload(seeds)
	wantLine := "NEXUS_MCP_LINEAR_SERVER_AUTHORIZATION='Bearer " + ph + "'\n"
	if !bytes.Contains(payload, []byte(wantLine)) {
		t.Errorf("buildMCPOAuthCredPayload payload missing expected line %q:\n%s", wantLine, payload)
	}
	if bytes.Contains(payload, []byte(realToken)) {
		t.Errorf("buildMCPOAuthCredPayload must not contain the real token (D-PP-04); got:\n%s", payload)
	}

	resolved, resolveOK := broker.ResolveScoped(ph, sid, "mcp.linear.app")
	if !resolveOK || resolved != realToken {
		t.Errorf("broker.ResolveScoped(%q, sid, \"mcp.linear.app\") = (%q, %v); want (%q, true)", ph, resolved, resolveOK, realToken)
	}
}

func TestMCPOAuthSeedPayloadShellSourceable(t *testing.T) {
	const ph = "deadbeefcafef00d1234567890abcdef"
	payload := buildMCPOAuthCredPayload(map[string]string{"linear-server": ph})

	dir := t.TempDir()
	credEnv := filepath.Join(dir, "cred.env")
	if err := os.WriteFile(credEnv, payload, 0o600); err != nil {
		t.Fatalf("write cred.env: %v", err)
	}

	script := "set -a; . " + credEnv + "; set +a; printf %s \"$NEXUS_MCP_LINEAR_SERVER_AUTHORIZATION\""
	out, err := exec.Command("/bin/sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("sourcing cred.env failed (unquoted value breaks the shell): %v", err)
	}

	want := "Bearer " + ph
	if string(out) != want {
		t.Errorf("sourced NEXUS_MCP_LINEAR_SERVER_AUTHORIZATION = %q; want %q\n"+
			"the cred.env value must be shell-quoted so POSIX `. file` preserves the space", string(out), want)
	}
}

func TestRunSeedRoute_AgentUsesSandboxProfile(t *testing.T) {
	var gotProfile cred.AgentProfile

	origLoop := seedLoopFn
	t.Cleanup(func() { seedLoopFn = origLoop })
	seedLoopFn = func(_ context.Context, _ domain.SandboxID, _ **x509.Certificate,
		_, _ service.GuestSeeder, _ *cred.Broker, _ []*cred.Refresher,
		_ int, _ time.Duration, _ PerimeterCAGetter, _ bool, profile cred.AgentProfile, _ []cred.AgentProfile, _ service.GuestSeeder,
	) (bool, bool) {
		gotProfile = profile
		return true, true
	}

	in := seedRouteInputs{
		SB:          sandboxWithProxy(cred.CursorAgentProfileName, nil),
		Cert:        fakeCert(),
		CASeeder:    func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil },
		AgentSeeder: func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil },
		Broker:      cred.NewBroker(),
	}

	if ok, _ := runSeedRoute(context.Background(), routeAgent, in); !ok {
		t.Fatal("runSeedRoute returned ok=false for routeAgent")
	}
	if gotProfile.Name != cred.CursorAgentProfileName {
		t.Errorf("SeedLoop received profile %q, want %q — a cursor sandbox must not be reseeded with claude's profile",
			gotProfile.Name, cred.CursorAgentProfileName)
	}
	if gotProfile.APIKeyEnvVar != "CURSOR_API_KEY" {
		t.Errorf("SeedLoop received profile with APIKeyEnvVar %q, want CURSOR_API_KEY", gotProfile.APIKeyEnvVar)
	}
}
