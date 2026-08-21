package supervisor

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"os"
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

	ok, _ := seedAgentAndHumanSecrets(ctx, sb, fakeCert(), caSeeder, credCap.fn(), broker, nil, nil)
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

	ok, _ := seedAgentAndHumanSecrets(ctx, sb, fakeCert(), caSeeder, credCap.fn(), broker, nil, nil)
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

	ok, _ := seedAgentAndHumanSecrets(ctx, sb, fakeCert(), caSeeder, credCap.fn(), broker, nil, nil)
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
		_, _ service.GuestSeeder, _ *cred.Broker, _ []*cred.Refresher, _ PerimeterCAGetter,
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

	var id domain.SandboxID
	id[0] = 0xD1
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
		_, _ service.GuestSeeder, _ *cred.Broker, _ []*cred.Refresher, _ PerimeterCAGetter,
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
				_ int, _ time.Duration, _ PerimeterCAGetter, seedAgentCreds bool,
			) (bool, bool) {
				loopCalled = true
				gotSeedAgentCreds = seedAgentCreds
				return true, true
			}
			seedAgentAndHumanSecretsFn = func(_ context.Context, _ domain.Sandbox, _ *x509.Certificate,
				_, _ service.GuestSeeder, _ *cred.Broker, _ []*cred.Refresher, _ PerimeterCAGetter,
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
	s := map[string]interface{}{
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
		1, 0, nil, true,
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
		broker, []*cred.Refresher{r}, nil,
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
