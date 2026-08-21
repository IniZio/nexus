package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

func TestParseSecretSpec(t *testing.T) {
	t.Parallel()
	got, err := ParseSecretSpec("GH_TOKEN@github.com,api.github.com")
	if err != nil {
		t.Fatalf("ParseSecretSpec: %v", err)
	}
	if got.Env != "GH_TOKEN" {
		t.Errorf("Env = %q", got.Env)
	}
	if len(got.Hosts) != 2 || got.Hosts[0] != "github.com" || got.Hosts[1] != "api.github.com" {
		t.Errorf("Hosts = %v", got.Hosts)
	}
	for _, bad := range []string{"", "GH_TOKEN", "@github.com", "GH TOKEN@github.com", "GH_TOKEN@"} {
		if _, err := ParseSecretSpec(bad); err == nil {
			t.Errorf("ParseSecretSpec(%q): want error", bad)
		}
	}
}

func TestMergeSecrets_ExplicitWinsOverBuiltin(t *testing.T) {
	t.Parallel()
	explicit := []SecretBind{{Env: "GH_TOKEN", Hosts: []string{"github.com"}, Token: "explicit"}}
	builtin := SecretBind{Env: "GH_TOKEN", Hosts: GitHubSecretHosts, Token: "builtin"}
	got := MergeSecrets(explicit, builtin)
	if len(got) != 1 || got[0].Token != "explicit" {
		t.Errorf("MergeSecrets = %+v, want explicit token", got)
	}
}

func TestApplySecrets_PlaceholderNotRealToken(t *testing.T) {
	t.Parallel()
	broker := cred.NewBroker()
	id := seedTestID(0x51)
	const real = "ghs_real_token_never_in_guest"
	extra, hosts, err := applySecrets(broker, id, []SecretBind{{
		Env:   BuiltinGitHubEnv,
		Hosts: GitHubSecretHosts,
		Token: real,
	}})
	if err != nil {
		t.Fatalf("applySecrets: %v", err)
	}
	if bytes.Contains(extra, []byte(real)) {
		t.Fatalf("guest extra leaked real token:\n%s", extra)
	}
	if !bytes.Contains(extra, []byte("GH_TOKEN=")) || !bytes.Contains(extra, []byte("GITHUB_TOKEN=")) {
		t.Fatalf("missing GH_TOKEN/GITHUB_TOKEN:\n%s", extra)
	}
	// D-PD-33: GitHubSecretHosts now includes uploads.github.com (3 hosts).
	if len(hosts) != len(GitHubSecretHosts) {
		t.Errorf("hosts = %v; want len=%d matching GitHubSecretHosts", hosts, len(GitHubSecretHosts))
	}
	// Extract placeholder and confirm broker swap for both GitHub hosts.
	line := strings.SplitN(string(extra), "\n", 2)[0]
	_, ph, _ := strings.Cut(line, "=")
	for _, h := range GitHubSecretHosts {
		// RegisterPlaceholder keyed by host; ResolveScoped keys by placeholder.
		got, ok := broker.ResolveScoped(ph, id)
		if !ok || got != real {
			t.Errorf("ResolveScoped for bind covering %s: ok=%v tok=%q", h, ok, got)
		}
	}
}

func TestCreateAndBoot_HumanSecrets_NotOnAllowedHosts(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)
	broker := cred.NewBroker()
	const real = "ghs_human_path_token"
	cap := &captureSeeder{}
	svc := newTestSvc(t, fake.New())
	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fake.New()), noopProbe, "proj", "human", CreateAndBootOptions{
		Image:     ImageSpec{Digest: string(img.Digest)},
		CacheRoot: cacheRoot,
		DiskDir:   t.TempDir(),
		Broker:    broker,
		Seeder:    cap.fn(),
		Secrets: []SecretBind{{
			Env:   BuiltinGitHubEnv,
			Hosts: append([]string(nil), GitHubSecretHosts...),
			Token: real,
		}},
		AllowedRepo: "test/repo", // D-PD-36: required for any GitHub secret (service-layer guard)
	})
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	for _, h := range sb.Envelope.AllowedHosts {
		if isGitHubHost(h) {
			t.Errorf("human AllowedHosts must stay empty of GitHub; got %q", h)
		}
	}
	gotSecret := strings.Join(sb.Envelope.SecretHosts, ",")
	if !strings.Contains(gotSecret, "github.com") || !strings.Contains(gotSecret, "api.github.com") {
		t.Errorf("SecretHosts = %v", sb.Envelope.SecretHosts)
	}
	if len(sb.Envelope.SecretSpecs) != 1 || !strings.Contains(sb.Envelope.SecretSpecs[0], "GH_TOKEN@") {
		t.Errorf("SecretSpecs = %v", sb.Envelope.SecretSpecs)
	}
	if bytes.Contains(cap.payload, []byte(real)) {
		t.Errorf("cred.env leaked real token:\n%s", cap.payload)
	}
	if !bytes.Contains(cap.payload, []byte("GH_TOKEN=")) {
		t.Errorf("cred.env missing GH_TOKEN:\n%s", cap.payload)
	}
}

// TestCreateAndBoot_AgentSeed_GitHubSecret_NoRepo_Refused verifies that
// UseAgentSeed + GitHub secret + no AllowedRepo is refused by the unconditional
// pre-boot D-PD-36 guard (ErrUnboundGitHubSecret). D-SHL-05 lifted the blanket
// agent-GitHub ban; the AllowedRepo requirement is still enforced for every
// caller.
func TestCreateAndBoot_AgentSeed_GitHubSecret_NoRepo_Refused(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)
	broker := cred.NewBroker()
	cap := &captureSeeder{}
	svc := newTestSvc(t, fake.New())
	var opts CreateAndBootOptions
	opts.Image = ImageSpec{Digest: string(img.Digest)}
	opts.CacheRoot = cacheRoot
	opts.DiskDir = t.TempDir()
	opts.Secrets = []SecretBind{{
		Env:   BuiltinGitHubEnv,
		Hosts: append([]string(nil), GitHubSecretHosts...),
		Token: "should-never-bind",
	}}
	WireClaudeEgress(&opts, broker, cap.fn(), nil)
	// AllowedRepo deliberately omitted.
	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fake.New()), noopProbe, "proj", "agent", opts)
	if !errors.Is(err, ErrUnboundGitHubSecret) {
		t.Fatalf("CreateAndBoot err = %v, want ErrUnboundGitHubSecret", err)
	}
}

func TestBuiltinGitHubSecret_MissingGh(t *testing.T) {
	orig := lookupGitHubToken
	t.Cleanup(func() { lookupGitHubToken = orig })
	lookupGitHubToken = func(context.Context) (string, error) { return "", nil }
	_, ok, err := BuiltinGitHubSecret(context.Background())
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, want no builtin", ok, err)
	}
}

func TestBuiltinGitHubSecret_Present(t *testing.T) {
	orig := lookupGitHubToken
	t.Cleanup(func() { lookupGitHubToken = orig })
	lookupGitHubToken = func(context.Context) (string, error) { return "ghs_from_gh", nil }
	b, ok, err := BuiltinGitHubSecret(context.Background())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if b.Env != BuiltinGitHubEnv || b.Token != "ghs_from_gh" {
		t.Errorf("bind = %+v", b)
	}
}

// TestCreateAndBoot_MixedHostSecretRefused verifies that a secret bind mixing
// GitHub hosts with a non-GitHub host is rejected at create time with
// ErrMixedGitHubSecret. This drives the real validation path in create.go so
// that non-CLI callers (MCP, orca, herdr) cannot bypass it.
//
// Mutation evidence: removing the SecretMixesGitHubHosts check in create.go
// causes this test to receive nil instead of ErrMixedGitHubSecret.
func TestCreateAndBoot_MixedHostSecretRefused(t *testing.T) {
	ctx := context.Background()
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)
	svc := newTestSvc(t, fake.New())
	_, err = CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fake.New()), noopProbe, "proj", "human", CreateAndBootOptions{
		Image:     ImageSpec{Digest: string(img.Digest)},
		CacheRoot: cacheRoot,
		DiskDir:   t.TempDir(),
		Broker:    cred.NewBroker(),
		Secrets: []SecretBind{{
			Env:   "GH_TOKEN",
			Hosts: []string{"github.com", "internal.example.com"},
			Token: "ghs_mixed_host_token",
		}},
		AllowedRepo: "test/repo",
	})
	if !errors.Is(err, ErrMixedGitHubSecret) {
		t.Fatalf("CreateAndBoot err = %v, want ErrMixedGitHubSecret", err)
	}
}

func TestResolveEnvelopeSecrets_GitHubFromGh(t *testing.T) {
	orig := lookupGitHubToken
	t.Cleanup(func() { lookupGitHubToken = orig })
	lookupGitHubToken = func(context.Context) (string, error) { return "ghs_resolved", nil }
	binds, err := ResolveEnvelopeSecrets(context.Background(), []string{"GH_TOKEN@github.com,api.github.com"})
	if err != nil {
		t.Fatalf("ResolveEnvelopeSecrets: %v", err)
	}
	if len(binds) != 1 || binds[0].Token != "ghs_resolved" {
		t.Fatalf("binds = %+v", binds)
	}
}

func TestSeedGuestSecrets_NoTokenLeak(t *testing.T) {
	orig := lookupGitHubToken
	t.Cleanup(func() { lookupGitHubToken = orig })
	const real = "ghs_supervisor_rebind"
	lookupGitHubToken = func(context.Context) (string, error) { return real, nil }
	broker := cred.NewBroker()
	id := seedTestID(0x26)
	var payload []byte
	seeder := func(_ context.Context, _ domain.SandboxID, p []byte) error {
		payload = append(payload, p...)
		return nil
	}
	if err := SeedGuestSecrets(context.Background(), broker, id, []string{"GH_TOKEN@github.com,api.github.com"}, seeder); err != nil {
		t.Fatalf("SeedGuestSecrets: %v", err)
	}
	if bytes.Contains(payload, []byte(real)) {
		t.Fatalf("leaked token: %s", payload)
	}
	if !bytes.Contains(payload, []byte("GH_TOKEN=")) {
		t.Fatalf("missing GH_TOKEN: %s", payload)
	}
}

