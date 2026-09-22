package service

import (
	"bytes"
	"context"
	"testing"

	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

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

	if err := broker.SetRealToken(sid, AnthropicAPIHost, realToken); err != nil {
		t.Fatalf("SetRealToken: %v", err)
	}

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

	if !bytes.Contains(payload, []byte("CLAUDE_CODE_OAUTH_TOKEN=")) {
		t.Errorf("payload must contain CLAUDE_CODE_OAUTH_TOKEN= (broker placeholder path)\npayload:\n%s", payload)
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
		key := "NEXUS_CRED_" + hostToEnvKey(host) + "_TOKEN="
		if !bytes.Contains(payload, []byte(key)) {
			t.Errorf("payload missing NEXUS_CRED_* key for host %q\npayload:\n%s", host, payload)
		}
	}
}

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

func TestSeedGuestAgent_AnthropicAuthToken_SeededAndResolvable(t *testing.T) {
	// NO t.Parallel: t.Setenv races with other tests reading ANTHROPIC_AUTH_TOKEN.
	ctx := context.Background()

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

	if err := broker.SetRealToken(sid, AnthropicAPIHost, realToken); err != nil {
		t.Fatalf("SetRealToken: %v", err)
	}

	payload := cap.payload

	wantVar := "ANTHROPIC_AUTH_TOKEN=" + placeholder
	if !bytes.Contains(payload, []byte(wantVar)) {
		t.Errorf("payload missing %q\npayload:\n%s", wantVar, payload)
	}

	// ANTHROPIC_AUTH_TOKEN and CLAUDE_CODE_OAUTH_TOKEN are mutually exclusive.
	if bytes.Contains(payload, []byte("CLAUDE_CODE_OAUTH_TOKEN=")) {
		t.Errorf("payload must NOT contain CLAUDE_CODE_OAUTH_TOKEN when kindAuthToken is active\npayload:\n%s", payload)
	}

	if bytes.Contains(payload, []byte(realToken)) {
		t.Errorf("payload must NOT contain the real token\npayload:\n%s", payload)
	}

	// Correct sandbox resolves placeholder to real token.
	got, ok := broker.ResolveScoped(placeholder, sid, AnthropicAPIHost)
	if !ok {
		t.Errorf("ResolveScoped(%q, sid, AnthropicAPIHost): ok=false, want true", placeholder)
	}
	if got != realToken {
		t.Errorf("ResolveScoped returned %q, want %q", got, realToken)
	}

	// Cross-sandbox theft prevented: different sandbox does not resolve.
	gotOther, okOther := broker.ResolveScoped(placeholder, otherSid, AnthropicAPIHost)
	if okOther {
		t.Errorf("ResolveScoped(%q, otherSid, AnthropicAPIHost): ok=true (cross-sandbox leak), want false; got token=%q", placeholder, gotOther)
	}
}

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

	if bytes.Contains(cap.payload, []byte(realToken)) {
		t.Errorf("cred.env payload delivered to guest must NOT contain real token\npayload:\n%s", cap.payload)
	}
	if !bytes.Contains(cap.payload, []byte("CLAUDE_CODE_OAUTH_TOKEN=")) {
		t.Errorf("cred.env payload must contain CLAUDE_CODE_OAUTH_TOKEN= (broker placeholder path)\npayload:\n%s", cap.payload)
	}
	// MUTATION-PIN: NODE_EXTRA_CA_CERTS present proves agent seeding path executed.
	if !bytes.Contains(cap.payload, []byte("NODE_EXTRA_CA_CERTS=")) {
		t.Errorf("cred.env payload missing NODE_EXTRA_CA_CERTS (agent seeding path must still write CA cert env)\npayload:\n%s", cap.payload)
	}
}
