package service

import (
	"bytes"
	"context"
	"testing"

	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// TestSeedGuestAgent_ClaudeVarsPresentRealTokenAbsent is the primary invariant
// test for the agent egress seeding path. It verifies:
//
//  1. CLAUDE_CODE_OAUTH_TOKEN is present in the payload and equals the
//     placeholder minted for api.anthropic.com.
//  2. NODE_EXTRA_CA_CERTS is present and equals GuestCACertPath.
//  3. CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 is present.
//  4. The real token (registered via SetRealToken AFTER seeding) is NOT present
//     in the payload — zero-cred-in-guest invariant.
func TestSeedGuestAgent_ClaudeVarsPresentRealTokenAbsent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	broker := cred.NewBroker()
	sid := seedTestID(20)
	const realToken = "real-anthropic-secret-xyzzy"

	cap := &captureSeeder{}
	recs, err := SeedGuestAgent(ctx, broker, sid, cap.fn())
	if err != nil {
		t.Fatalf("SeedGuestAgent: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("SeedGuestAgent returned no records")
	}

	// Wire in the real token host-side (after seeding, as production does).
	if err := broker.SetRealToken(sid, AnthropicAPIHost, realToken); err != nil {
		t.Fatalf("SetRealToken: %v", err)
	}

	// Find the api.anthropic.com placeholder from the returned records.
	var anthropicPlaceholder string
	for _, rec := range recs {
		if rec.Host == AnthropicAPIHost {
			anthropicPlaceholder = rec.Placeholder
			break
		}
	}
	if anthropicPlaceholder == "" {
		t.Fatal("no PlaceholderRecord found for api.anthropic.com")
	}

	payload := cap.payload

	// Invariant 1: CLAUDE_CODE_OAUTH_TOKEN = placeholder for api.anthropic.com.
	want1 := "CLAUDE_CODE_OAUTH_TOKEN=" + anthropicPlaceholder
	if !bytes.Contains(payload, []byte(want1)) {
		t.Errorf("payload missing %q\npayload:\n%s", want1, payload)
	}

	// Invariant 2: NODE_EXTRA_CA_CERTS points at the MITM CA cert path.
	want2 := "NODE_EXTRA_CA_CERTS=" + GuestCACertPath
	if !bytes.Contains(payload, []byte(want2)) {
		t.Errorf("payload missing %q\npayload:\n%s", want2, payload)
	}

	// Invariant 3: non-essential traffic disabled.
	want3 := "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1"
	if !bytes.Contains(payload, []byte(want3)) {
		t.Errorf("payload missing %q\npayload:\n%s", want3, payload)
	}

	// Invariant 4: real token structurally absent from the guest payload.
	if bytes.Contains(payload, []byte(realToken)) {
		t.Errorf("payload must NOT contain the real token\npayload:\n%s", payload)
	}
}

// TestSeedGuestAgent_BothAnthropicHostsSeeded verifies that SeedGuestAgent
// registers placeholders for both AgentEgressHosts (api.anthropic.com and
// platform.claude.com) and includes their NEXUS3_CRED_* lines in the payload.
func TestSeedGuestAgent_BothAnthropicHostsSeeded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	broker := cred.NewBroker()
	sid := seedTestID(21)

	cap := &captureSeeder{}
	recs, err := SeedGuestAgent(ctx, broker, sid, cap.fn())
	if err != nil {
		t.Fatalf("SeedGuestAgent: %v", err)
	}

	hosts := AgentEgressHosts(cred.ClaudeCodeProfile)
	if len(recs) != len(hosts) {
		t.Fatalf("expected %d records (one per AgentEgressHost), got %d", len(hosts), len(recs))
	}

	payload := cap.payload
	for _, host := range hosts {
		key := "NEXUS3_CRED_" + hostToEnvKey(host) + "_TOKEN="
		if !bytes.Contains(payload, []byte(key)) {
			t.Errorf("payload missing NEXUS3_CRED_* key for host %q\npayload:\n%s", host, payload)
		}
	}
}

// TestWireClaudeEgress_AllowedHostsSet verifies that WireClaudeEgress populates
// AllowedHosts with both Anthropic egress hosts, sets UseAgentSeed, and wires
// broker and seeder into the options.
func TestWireClaudeEgress_AllowedHostsSet(t *testing.T) {
	t.Parallel()
	broker := cred.NewBroker()
	cap := &captureSeeder{}

	var opts CreateAndBootOptions
	WireClaudeEgress(&opts, broker, cap.fn(), nil)

	if !opts.UseAgentSeed {
		t.Error("WireClaudeEgress: UseAgentSeed not set")
	}
	if opts.Broker != broker {
		t.Error("WireClaudeEgress: Broker not wired")
	}
	if opts.Seeder == nil {
		t.Error("WireClaudeEgress: Seeder not wired")
	}

	want := map[string]bool{
		AnthropicAPIHost:   false,
		ClaudePlatformHost: false,
	}
	for _, h := range opts.AllowedHosts {
		if _, ok := want[h]; ok {
			want[h] = true
		}
	}
	for host, found := range want {
		if !found {
			t.Errorf("AllowedHosts missing %q; got: %v", host, opts.AllowedHosts)
		}
	}
}

// TestSeedGuestAgent_NilBrokerNoOp verifies that a nil broker causes
// SeedGuestAgent to skip seeding entirely.
func TestSeedGuestAgent_NilBrokerNoOp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	sid := seedTestID(22)

	cap := &captureSeeder{}
	recs, err := SeedGuestAgent(ctx, nil, sid, cap.fn())
	if err != nil {
		t.Fatalf("SeedGuestAgent with nil broker: %v", err)
	}
	if recs != nil {
		t.Errorf("expected nil records with nil broker, got %v", recs)
	}
	if cap.calls != 0 {
		t.Errorf("seeder called %d times with nil broker, want 0", cap.calls)
	}
}

// TestSeedGuestAgent_AnthropicAuthToken_SeededAndResolvable verifies the
// S-CRED auth-token path end-to-end at the unit level:
//
//  1. When ANTHROPIC_AUTH_TOKEN is set in the host env, [SeedGuestAgent]
//     emits ANTHROPIC_AUTH_TOKEN=<placeholder> in the guest payload (not the
//     real token).
//  2. CLAUDE_CODE_OAUTH_TOKEN is absent — the two kinds are mutually exclusive.
//  3. [cred.Broker.ResolveScoped] resolves the placeholder to the real token
//     for the correct sandbox scope.
//  4. ResolveScoped returns ("", false) for a different sandbox — cross-sandbox
//     theft is prevented.
func TestSeedGuestAgent_AnthropicAuthToken_SeededAndResolvable(t *testing.T) {
	// t.Parallel omitted: t.Setenv mutates global env state; parallel execution
	// would race with other tests that read ANTHROPIC_AUTH_TOKEN.
	ctx := context.Background()

	// Use a real-token string that is not 64 hex chars so the absence check
	// cannot false-negative against a hex placeholder.
	const realToken = "sk-ant-api-test-xyzzy-secret"

	broker := cred.NewBroker()
	sid := seedTestID(30)
	otherSid := seedTestID(31)

	// Set host env var so resolveAgentCredKind() returns kindAuthToken.
	t.Setenv("ANTHROPIC_AUTH_TOKEN", realToken)

	cap := &captureSeeder{}
	recs, err := SeedGuestAgent(ctx, broker, sid, cap.fn())
	if err != nil {
		t.Fatalf("SeedGuestAgent: %v", err)
	}
	if len(recs) == 0 {
		t.Fatal("SeedGuestAgent returned no records")
	}

	// Find the api.anthropic.com placeholder.
	var placeholder string
	for _, rec := range recs {
		if rec.Host == AnthropicAPIHost {
			placeholder = rec.Placeholder
			break
		}
	}
	if placeholder == "" {
		t.Fatal("no PlaceholderRecord found for AnthropicAPIHost")
	}

	// Wire the real token host-side after seeding (mirrors production order).
	if err := broker.SetRealToken(sid, AnthropicAPIHost, realToken); err != nil {
		t.Fatalf("SetRealToken: %v", err)
	}

	payload := cap.payload

	// Invariant 1: ANTHROPIC_AUTH_TOKEN equals the placeholder (not the real token).
	wantVar := "ANTHROPIC_AUTH_TOKEN=" + placeholder
	if !bytes.Contains(payload, []byte(wantVar)) {
		t.Errorf("payload missing %q\npayload:\n%s", wantVar, payload)
	}

	// Invariant 2: CLAUDE_CODE_OAUTH_TOKEN is absent — kinds are mutually exclusive.
	if bytes.Contains(payload, []byte("CLAUDE_CODE_OAUTH_TOKEN=")) {
		t.Errorf("payload must NOT contain CLAUDE_CODE_OAUTH_TOKEN when kindAuthToken is active\npayload:\n%s", payload)
	}

	// Invariant 3: real token structurally absent from the guest payload.
	if bytes.Contains(payload, []byte(realToken)) {
		t.Errorf("payload must NOT contain the real token\npayload:\n%s", payload)
	}

	// Invariant 4a: correct sandbox resolves placeholder to real token.
	got, ok := broker.ResolveScoped(placeholder, sid)
	if !ok {
		t.Errorf("ResolveScoped(%q, sid): ok=false, want true", placeholder)
	}
	if got != realToken {
		t.Errorf("ResolveScoped returned %q, want %q", got, realToken)
	}

	// Invariant 4b: different sandbox does NOT resolve — cross-sandbox theft prevented.
	gotOther, okOther := broker.ResolveScoped(placeholder, otherSid)
	if okOther {
		t.Errorf("ResolveScoped(%q, otherSid): ok=true (cross-sandbox leak), want false; got token=%q", placeholder, gotOther)
	}
}

// TestCreateAndBoot_AgentSeed_RealTokenAbsentFromPayload verifies the
// zero-cred-in-guest invariant at the CreateAndBoot level: after wiring a
// non-empty AgentEgressToken, the cred.env payload delivered to the guest does
// not contain the real token string.
func TestCreateAndBoot_AgentSeed_RealTokenAbsentFromPayload(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)

	broker := cred.NewBroker()
	const realToken = "real-anthropic-bearer-secret"
	cap := &captureSeeder{}

	svc := newTestSvc(t, fake.New())
	var opts CreateAndBootOptions
	opts.Image = ImageSpec{Digest: string(img.Digest)}
	opts.CacheRoot = cacheRoot
	opts.DiskDir = t.TempDir()
	WireClaudeEgress(&opts, broker, cap.fn(),
		cred.NewStaticCredentialSource(&cred.DedicatedCredStore{AccessToken: realToken}))

	if _, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fake.New()), noopProbe, "proj", "agentsandbox", opts); err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}

	// The seeder payload must not contain the real token.
	if bytes.Contains(cap.payload, []byte(realToken)) {
		t.Errorf("cred.env payload delivered to guest must NOT contain real token\npayload:\n%s", cap.payload)
	}
	// The payload must contain CLAUDE_CODE_OAUTH_TOKEN (with placeholder, not real token).
	if !bytes.Contains(cap.payload, []byte("CLAUDE_CODE_OAUTH_TOKEN=")) {
		t.Errorf("cred.env payload missing CLAUDE_CODE_OAUTH_TOKEN\npayload:\n%s", cap.payload)
	}
}
