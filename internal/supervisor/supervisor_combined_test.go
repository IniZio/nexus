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

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
)

// captureGuestSeeder is a service.GuestSeeder stub that accumulates payloads
// and counts calls.
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

// fakeCert returns a *x509.Certificate with a non-nil Raw so that SeedCA
// proceeds past its nil-cert guard and calls the caSeeder. SeedCA calls
// pem.EncodeToMemory(cert.Raw); the Raw bytes' validity doesn't matter
// because the caSeeder used in these tests is a no-op.
func fakeCert() *x509.Certificate {
	return &x509.Certificate{Raw: []byte("fake-cert-der-for-test")}
}

// combinedSandboxWithEnvSecret returns a domain.Sandbox configured with both
// an agent name and a non-GitHub secret spec resolved from a process env var.
// This avoids calling `gh auth token` in supervisor-layer tests.
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

// TestSeedAgentAndHumanSecrets_ContainsAgentVars is the mutation guard for the
// agent half inside seedAgentAndHumanSecrets:
//
//	Drop the SeedGuestAgentAndSecrets call → CLAUDE_CODE_OAUTH_TOKEN disappears → RED.
func TestSeedAgentAndHumanSecrets_ContainsAgentVars(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "") // ensure kindOAuth path
	t.Setenv("NEXUS3_TEST_SECRET_A1", "supervisor-secret-for-a1")

	ctx := context.Background()
	var id domain.SandboxID
	id[0] = 0xA1
	sb := combinedSandboxWithEnvSecret(id, "NEXUS3_TEST_SECRET_A1")

	broker := cred.NewBroker()
	credCap := &captureGuestSeeder{}
	// caSeeder: no-op; SeedCA writes PEM to it but it's discarded.
	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }

	ok, _ := seedAgentAndHumanSecrets(ctx, sb, fakeCert(), caSeeder, credCap.fn(), broker, nil, nil, cred.ClaudeCodeProfile)
	if !ok {
		t.Fatal("seedAgentAndHumanSecrets returned ok=false; combined seeding failed")
	}

	payload := credCap.combined()

	// Agent half must be present.
	if !bytes.Contains(payload, []byte("CLAUDE_CODE_OAUTH_TOKEN=")) {
		t.Errorf("combined supervisor payload missing CLAUDE_CODE_OAUTH_TOKEN (agent half absent)\npayload:\n%s", payload)
	}
}

// TestSeedAgentAndHumanSecrets_ContainsSecretVars is the mutation guard for the
// secret half inside seedAgentAndHumanSecrets:
//
//	Drop SecretSpecs from the SeedGuestAgentAndSecrets call →
//	NEXUS3_CRED_EXAMPLE_COM_TOKEN disappears → RED.
func TestSeedAgentAndHumanSecrets_ContainsSecretVars(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("NEXUS3_TEST_SECRET_A2", "supervisor-secret-for-a2")

	ctx := context.Background()
	var id domain.SandboxID
	id[0] = 0xA2
	sb := combinedSandboxWithEnvSecret(id, "NEXUS3_TEST_SECRET_A2")

	broker := cred.NewBroker()
	credCap := &captureGuestSeeder{}
	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }

	ok, _ := seedAgentAndHumanSecrets(ctx, sb, fakeCert(), caSeeder, credCap.fn(), broker, nil, nil, cred.ClaudeCodeProfile)
	if !ok {
		t.Fatal("seedAgentAndHumanSecrets returned ok=false")
	}

	payload := credCap.combined()

	// Secret half must be present: applySecrets emits the bind's Env name
	// (NEXUS3_TEST_SECRET_A2) as the var, not a NEXUS3_CRED_* key.
	if !bytes.Contains(payload, []byte("NEXUS3_TEST_SECRET_A2=")) {
		t.Errorf("combined supervisor payload missing NEXUS3_TEST_SECRET_A2= (secret half absent)\npayload:\n%s", payload)
	}
}

// TestSeedAgentAndHumanSecrets_OneWrite asserts the credSeeder is called
// exactly once. Two writes would mean the second silently overwrites the first.
func TestSeedAgentAndHumanSecrets_OneWrite(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("NEXUS3_TEST_SECRET_A3", "supervisor-secret-for-a3")

	ctx := context.Background()
	var id domain.SandboxID
	id[0] = 0xA3
	sb := combinedSandboxWithEnvSecret(id, "NEXUS3_TEST_SECRET_A3")

	broker := cred.NewBroker()
	credCap := &captureGuestSeeder{}
	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }

	ok, _ := seedAgentAndHumanSecrets(ctx, sb, fakeCert(), caSeeder, credCap.fn(), broker, nil, nil, cred.ClaudeCodeProfile)
	if !ok {
		t.Fatal("seedAgentAndHumanSecrets returned ok=false")
	}

	if credCap.calls != 1 {
		t.Errorf("credSeeder called %d times, want exactly 1 (overwrite prevented)", credCap.calls)
	}
}

// --- dispatch tests ---
// These tests call chooseSeedRoute (the production decision function) and
// assert the route. They are the mutation guards for MUT-A and MUT-B.
//
// Residual gap (stated plainly): these tests cover the decision function AND
// the binding from a route to the seeder it invokes. What remains uncovered is
// the single line where RunDetached calls runSeedRoute(chooseSeedRoute(sb), …).
// A mutation deleting that call would still pass, because RunDetached does real
// I/O (VM boot, perimeter start) and cannot be driven from a unit test here.

// sandboxWithProxy returns a domain.Sandbox that SandboxHasMITMProxy reports
// true for.
//
// SandboxHasMITMProxy is !OpenEgress || len(SecretHosts) > 0 || AgentName != "".
// The !OpenEgress clause is first and broadest, and omitting it from this
// description would matter: it is what makes a closed-egress sandbox with no
// agent and no secrets reach routeAgent. That case is exactly the one whose
// consequence is worth guarding — routing it wrongly would hand agent
// credential env vars to a guest that runs no agent.
func sandboxWithProxy(agentName string, secretHosts []string) domain.Sandbox {
	return domain.Sandbox{
		AgentName: agentName,
		Envelope:  domain.Envelope{SecretHosts: secretHosts},
	}
}

// TestChooseSeedRoute_Dispatch is the mutation guard for MUT-A:
// mutating `case agentSandbox && humanSecrets:` to `case false && agentSandbox && humanSecrets:`
// must make this test RED (the combined sandbox falls through to routeHumanSecrets instead).
func TestChooseSeedRoute_Dispatch(t *testing.T) {
	cases := []struct {
		name      string
		sb        domain.Sandbox
		wantRoute seedRoute
	}{
		{
			// OpenEgress=true, no AgentName, no SecretHosts → SandboxHasMITMProxy=false → routeNone.
			// (OpenEgress defaults false, meaning curated allowlist → proxy is required; must be
			// explicitly true to get open egress and skip the proxy.)
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

// TestChooseSeedRoute_Ordering is the mutation guard for MUT-B:
// swapping the routeCombined and routeHumanSecrets cases in chooseSeedRoute
// makes an agent+secrets sandbox return routeHumanSecrets, and this test
// turns RED because it asserts routeCombined.
func TestChooseSeedRoute_Ordering(t *testing.T) {
	sb := sandboxWithProxy("claude", []string{"github.com"})
	got := chooseSeedRoute(sb)
	if got != routeCombined {
		t.Errorf("chooseSeedRoute for agent+secrets = %v, want routeCombined (%v)\n"+
			"If routeHumanSecrets was returned, the combined case is ordered after the human-secrets case;\n"+
			"that silently drops the Claude credential from agent+secrets sandboxes.",
			got, routeCombined)
	}
	// Also assert it is NOT routeHumanSecrets to give an unambiguous ordering signal.
	if got == routeHumanSecrets {
		t.Errorf("agent+secrets sandbox routed to routeHumanSecrets: ordering defect — combined case must precede human-secrets case")
	}
}

// --- route→seeder binding tests ---
// These tests call runSeedRoute (the production dispatch function) with spy
// function vars and assert WHICH seeder was invoked. They close the gap between
// "chooseSeedRoute returns the right route" and "runSeedRoute calls the right
// seeder for that route".
//
// Residual gap (stated plainly): nothing asserts that RunDetached calls
// runSeedRoute(chooseSeedRoute(sb), ...) at all. That is a single wiring line,
// and testing it would require driving RunDetached through its full I/O
// (VM boot, perimeter). The established TestProbeAndSeedGuest_* pattern covers
// that class of gap when the blast radius is acceptable.

// TestRunSeedRoute_CombinedCallsCombinedSeeder is the mutation guard for the
// route→seeder binding. Make routeCombined call seedHumanSecretsFn instead of
// seedAgentAndHumanSecretsFn → this test turns RED (combinedCalled=false).
func TestRunSeedRoute_CombinedCallsCombinedSeeder(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("NEXUS3_TEST_SECRET_RS1", "rs1-secret")

	var combinedCalled, humanCalled bool

	// Spy replacements — restore after test.
	origCombined := seedAgentAndHumanSecretsFn
	origHuman := seedHumanSecretsFn
	t.Cleanup(func() {
		seedAgentAndHumanSecretsFn = origCombined
		seedHumanSecretsFn = origHuman
	})
	seedAgentAndHumanSecretsFn = func(_ context.Context, _ domain.Sandbox, _ *x509.Certificate,
		_, _ service.GuestSeeder, _ *cred.Broker, _ []*cred.Refresher, _ PerimeterCAGetter, _ cred.AgentProfile,
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

// TestRunSeedRoute_HumanSecretsCallsHumanSeeder guards routeHumanSecrets binding.
func TestRunSeedRoute_HumanSecretsCallsHumanSeeder(t *testing.T) {
	var humanCalled, combinedCalled bool

	origCombined := seedAgentAndHumanSecretsFn
	origHuman := seedHumanSecretsFn
	t.Cleanup(func() {
		seedAgentAndHumanSecretsFn = origCombined
		seedHumanSecretsFn = origHuman
	})
	seedAgentAndHumanSecretsFn = func(_ context.Context, _ domain.Sandbox, _ *x509.Certificate,
		_, _ service.GuestSeeder, _ *cred.Broker, _ []*cred.Refresher, _ PerimeterCAGetter, _ cred.AgentProfile,
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
//
// An independent review mutated routeAgent to dispatch at the combined seeder
// and found NOTHING caught it — the other three arms were guarded, this one
// was not. Its failure mode is the mirror image of the defect this whole change
// set exists to fix: instead of an agent losing its credential, a guest that
// runs NO agent is handed agent credential env vars.
//
// The seedAgentCreds argument is asserted too, not just the call. routeAgent is
// reached by two different kinds of sandbox — one with an agent, and (via the
// !OpenEgress clause of SandboxHasMITMProxy) a closed-egress sandbox with no
// agent and no secrets. Both take this arm; only the first may receive agent
// credentials. A test that asserted only "seedLoopFn was called" would pass
// while that distinction was inverted.
//
// Mutation: dispatch routeAgent at seedAgentAndHumanSecretsFn -> the call
// assertion goes RED. Hardcode the final argument to true -> the no-agent
// subtest goes RED.
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
				_ int, _ time.Duration, _ PerimeterCAGetter, seedAgentCreds bool, _ cred.AgentProfile,
			) (bool, bool) {
				loopCalled = true
				gotSeedAgentCreds = seedAgentCreds
				return true, true
			}
			seedAgentAndHumanSecretsFn = func(_ context.Context, _ domain.Sandbox, _ *x509.Certificate,
				_, _ service.GuestSeeder, _ *cred.Broker, _ []*cred.Refresher, _ PerimeterCAGetter, _ cred.AgentProfile,
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

// ── Finding 1: supervisor-level ForcePush wiring tests ───────────────────────

// writeFreshSupervisorStore writes a DedicatedCredStore JSON with a fresh
// access_token (expires 1 hour from now) so NewRefresher's lockedToken fast
// path returns the cached token without any HTTP call.
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

// TestSeedLoop_ForcePushWritesRealToken is the mutation guard for
// supervisor.go:SeedLoop's ForcePush call.
//
// Sequence (from reviewer's construction hint):
//  1. RegisterPlaceholder — initial seed, scope minted with realToken="".
//  2. r.Register — wire refresher.
//  3. r.Token — ticker push: rotation detected (lastToken "" → realToken), broker set.
//  4. SeedLoop — internally calls SeedGuestAgent → RegisterPlaceholder re-mints scope,
//     wiping realToken back to "". Then ForcePush writes realToken unconditionally.
//  5. Assert broker.Resolve(placeholder) == realToken.
//
// Mutation proof: revert supervisor.go:SeedLoop's r.ForcePush(ctx, id) to
// r.Token(ctx) discarding the result. Token() detects no rotation (lastToken
// unchanged), vend() skips the push, broker scope stays at realToken="", and:
//
//	broker.Resolve(placeholder) == "" ≠ realToken → RED
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

	// Step 1: initial RegisterPlaceholder (simulates first seed attempt before
	// guest was reachable; scope exists but realToken is empty).
	if _, err := broker.RegisterPlaceholder(id, service.AnthropicAPIHost, ""); err != nil {
		t.Fatalf("RegisterPlaceholder (initial): %v", err)
	}

	// Step 2: wire refresher.
	r.Register(id)

	// Step 3: ticker fires — rotation detected (lastToken "" → realToken) → push.
	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatalf("Token (ticker): %v", err)
	}

	// Verify ticker pushed correctly (precondition for the mutation to bite).
	if ph, ok := broker.Placeholder(id, service.AnthropicAPIHost); !ok {
		t.Fatal("broker has no placeholder after ticker push (precondition)")
	} else if got, _ := broker.Resolve(ph); got != realToken {
		t.Fatalf("after ticker push: placeholder resolves to %q, want %q (precondition)", got, realToken)
	}

	// Step 4: SeedLoop — re-mints scope via SeedGuestAgent → RegisterPlaceholder
	// wipes realToken. Then ForcePush must write it back.
	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }
	agentSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }
	cert := fakeCert()
	ok, _ := SeedLoop(
		context.Background(), id, &cert,
		caSeeder, agentSeeder,
		broker, []*cred.Refresher{r},
		1, 0, nil, true, cred.ClaudeCodeProfile,
	)
	if !ok {
		t.Fatal("SeedLoop returned ok=false; seed failed")
	}

	// Step 5: the ForcePush inside SeedLoop must have written the real token to
	// the newly-minted scope. Use broker.Placeholder + broker.Resolve so the
	// assertion is on the OBSERVABLE OUTCOME, not on call counts.
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

// TestSeedAgentAndHumanSecrets_ForcePushWritesRealToken is the mutation guard
// for supervisor.go:seedAgentAndHumanSecrets's ForcePush call.
//
// Same construction as TestSeedLoop_ForcePushWritesRealToken but exercises the
// combined agent+secrets path (seedAgentAndHumanSecrets) instead of the
// agent-only path (SeedLoop). The two tests protect INDEPENDENT call sites:
// reverting seedAgentAndHumanSecrets's ForcePush leaves this test RED while
// the SeedLoop test stays GREEN, and vice versa.
//
// Mutation proof: revert supervisor.go:seedAgentAndHumanSecrets's r.ForcePush
// to r.Token discarding the result. vend() skips the push (no rotation), broker
// scope stays at realToken="", and:
//
//	broker.Resolve(placeholder) == "" ≠ realToken → RED
func TestSeedAgentAndHumanSecrets_ForcePushWritesRealToken(t *testing.T) {
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	t.Setenv("NEXUS3_TEST_SAHS_FP", "secret-val-for-fp-test")

	const realToken = "tok-real-sahs-fp"
	var id domain.SandboxID
	id[0] = 0xF2

	broker := cred.NewBroker()
	storePath := writeFreshSupervisorStore(t, realToken)
	r, err := cred.NewRefresher(storePath, service.AnthropicAPIHost, broker)
	if err != nil {
		t.Fatalf("NewRefresher: %v", err)
	}

	// Step 1: initial RegisterPlaceholder (same as SeedLoop test above).
	if _, err := broker.RegisterPlaceholder(id, service.AnthropicAPIHost, ""); err != nil {
		t.Fatalf("RegisterPlaceholder (initial): %v", err)
	}

	// Step 2: wire refresher.
	r.Register(id)

	// Step 3: ticker fires — rotation detected → push.
	if _, _, err := r.Token(context.Background()); err != nil {
		t.Fatalf("Token (ticker): %v", err)
	}

	// Verify ticker pushed (precondition).
	if ph, ok := broker.Placeholder(id, service.AnthropicAPIHost); !ok {
		t.Fatal("broker has no placeholder after ticker push (precondition)")
	} else if got, _ := broker.Resolve(ph); got != realToken {
		t.Fatalf("after ticker push: placeholder resolves to %q, want %q (precondition)", got, realToken)
	}

	// Step 4: seedAgentAndHumanSecrets — re-mints scope via SeedGuestAgentAndSecrets
	// → RegisterPlaceholder wipes realToken. Then ForcePush must write it back.
	sb := combinedSandboxWithEnvSecret(id, "NEXUS3_TEST_SAHS_FP")
	caSeeder := func(_ context.Context, _ domain.SandboxID, _ []byte) error { return nil }
	credCap := &captureGuestSeeder{}
	ok, _ := seedAgentAndHumanSecrets(
		context.Background(), sb, fakeCert(),
		caSeeder, credCap.fn(),
		broker, []*cred.Refresher{r}, nil, cred.ClaudeCodeProfile,
	)
	if !ok {
		t.Fatal("seedAgentAndHumanSecrets returned ok=false; combined seeding failed")
	}

	// Step 5: ForcePush inside seedAgentAndHumanSecrets must have written the
	// real token to the newly-minted scope.
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

// TestRegisterMCPOAuthPlaceholders verifies that registerMCPOAuthPlaceholders
// calls broker.RegisterPlaceholder for each valid MCPOAuthRefreshConfig so that
// a subsequent ForcePush (broker.SetRealToken) succeeds for every registered
// host. This test FAILS if registerMCPOAuthPlaceholders does not call
// broker.RegisterPlaceholder — broker.Placeholder will return ("", false) and
// broker.SetRealToken will return "no placeholder registered".
//
// Mutation proof: remove the broker.RegisterPlaceholder call from
// registerMCPOAuthPlaceholders and this test fails on the broker.Placeholder
// assertion.
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

	// Both valid configs must have a placeholder in the broker.
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

	// Skipped configs must NOT appear in the broker.
	for _, host := range []string{"example.com"} {
		if _, ok := broker.Placeholder(sid, host); ok {
			t.Errorf("broker should NOT have placeholder for %q (empty access_token was provided)", host)
		}
	}
}

// TestMCPOAuthSeedPayload verifies the full MCP OAuth guest-env seed path:
//
//	(a) registerMCPOAuthPlaceholders returns a serverName→placeholder hex map,
//	(b) buildMCPOAuthCredPayload emits NEXUS3_MCP_<SERVER>_AUTHORIZATION=Bearer <placeholder>
//	    lines — NOT the real token (D-PP-04 zero-cred-in-guest),
//	(c) the bare placeholder (without "Bearer ") resolves to the real token via
//	    the broker — swapAuthorization strips "Bearer " from the incoming header,
//	    resolves the bare hex, and re-emits "Bearer <realToken>" to egress.
//
// This test FAILS if:
//   - registerMCPOAuthPlaceholders does not return the minted placeholder,
//   - buildMCPOAuthCredPayload omits the "Bearer " prefix (MITM won't match),
//   - buildMCPOAuthCredPayload includes the real token (cred-in-guest violation).
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

	// (a) map must contain an entry for the valid server only.
	ph, ok := seeds["linear-server"]
	if !ok || ph == "" {
		t.Fatalf("registerMCPOAuthPlaceholders returned no placeholder for linear-server: seeds=%v", seeds)
	}
	if _, bad := seeds["bad-empty-token"]; bad {
		t.Error("registerMCPOAuthPlaceholders must not include skipped servers in the returned map")
	}

	// (b) payload must contain the env-var line with quoted "Bearer <ph>" — not
	// the real token. The value is single-quoted so POSIX `. file` sourcing
	// preserves the space (see TestMCPOAuthSeedPayloadShellSourceable).
	payload := buildMCPOAuthCredPayload(seeds)
	wantLine := "NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION='Bearer " + ph + "'\n"
	if !bytes.Contains(payload, []byte(wantLine)) {
		t.Errorf("buildMCPOAuthCredPayload payload missing expected line %q:\n%s", wantLine, payload)
	}
	if bytes.Contains(payload, []byte(realToken)) {
		t.Errorf("buildMCPOAuthCredPayload must not contain the real token (D-PP-04); got:\n%s", payload)
	}

	// (c) broker resolves the bare placeholder hex to the real token.
	// swapAuthorization will strip "Bearer " from the Authorization header, call
	// ResolveScoped with the bare hex, and prepend "Bearer " to the real token.
	resolved, resolveOK := broker.ResolveScoped(ph, sid, "mcp.linear.app")
	if !resolveOK || resolved != realToken {
		t.Errorf("broker.ResolveScoped(%q, sid, \"mcp.linear.app\") = (%q, %v); want (%q, true)", ph, resolved, resolveOK, realToken)
	}
}

// TestMCPOAuthSeedPayloadShellSourceable is the regression bite for the herdr
// worktree "Linear not authenticated" bug. The MCP OAuth cred.env value is
// "Bearer <placeholder>" — the only cred.env value with a space. Both guest
// consumers (launchCredSourcedArgv and guestShellProfileScript) load it with
// POSIX `. file` sourcing. An unquoted `KEY=Bearer <hex>` line is parsed by the
// shell as the assignment `KEY=Bearer` followed by the command `<hex>`, so the
// variable is never exported: the agent sends an empty Authorization header and
// Linear returns 401.
//
// This test sources the real buildMCPOAuthCredPayload output through /bin/sh
// exactly as the guest does and asserts the exported value is the full
// "Bearer <placeholder>". It FAILS on the unquoted `%s=Bearer %s` payload
// (yields "") and PASSES only when the value is shell-quoted.
func TestMCPOAuthSeedPayloadShellSourceable(t *testing.T) {
	const ph = "deadbeefcafef00d1234567890abcdef"
	payload := buildMCPOAuthCredPayload(map[string]string{"linear-server": ph})

	dir := t.TempDir()
	credEnv := filepath.Join(dir, "cred.env")
	if err := os.WriteFile(credEnv, payload, 0o600); err != nil {
		t.Fatalf("write cred.env: %v", err)
	}

	// Mirror the guest sourcing convention: `set -a; . file; set +a` then print
	// the variable, identical to launchCredSourcedArgv / guestShellProfileScript.
	script := "set -a; . " + credEnv + "; set +a; printf %s \"$NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION\""
	out, err := exec.Command("/bin/sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("sourcing cred.env failed (unquoted value breaks the shell): %v", err)
	}

	want := "Bearer " + ph
	if string(out) != want {
		t.Errorf("sourced NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION = %q; want %q\n"+
			"the cred.env value must be shell-quoted so POSIX `. file` preserves the space", string(out), want)
	}
}

// TestRunSeedRoute_AgentUsesSandboxProfile is the mutation guard for the
// hardcoded-claude regression this cursor slice fixed: routeAgent must
// resolve the profile from the sandbox's OWN AgentName (via
// cred.ProfileByName), not always cred.ClaudeCodeProfile. Before this fix,
// service.SeedGuestAgent (which SeedLoop called directly) always emitted
// Claude's env vars regardless of which agent the sandbox actually ran, so a
// --agent cursor sandbox would never receive CURSOR_API_KEY.
//
// Mutation: hardcode resolveSeedProfile to always return cred.ClaudeCodeProfile
// -> the cursor case below goes RED (gotProfile.Name == "claude-code" instead
// of "cursor").
func TestRunSeedRoute_AgentUsesSandboxProfile(t *testing.T) {
	var gotProfile cred.AgentProfile

	origLoop := seedLoopFn
	t.Cleanup(func() { seedLoopFn = origLoop })
	seedLoopFn = func(_ context.Context, _ domain.SandboxID, _ **x509.Certificate,
		_, _ service.GuestSeeder, _ *cred.Broker, _ []*cred.Refresher,
		_ int, _ time.Duration, _ PerimeterCAGetter, _ bool, profile cred.AgentProfile,
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
