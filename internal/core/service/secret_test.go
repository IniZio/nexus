package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/vaulttest"
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

// TestApplySecrets_EmittedGHTokenResolvesAllGitHubHosts is the regression test
// for the multi-host bind bug: the placeholder emitted as GH_TOKEN must resolve
// via ResolveScoped for EVERY host in GitHubSecretHosts, not only github.com.
//
// Bug: previously RegisterPlaceholder was called once per host, minting a distinct
// placeholder per host. Only the first host's placeholder was emitted as GH_TOKEN.
// ResolveScoped(GH_TOKEN, id, "api.github.com") returned ("", false) because
// GH_TOKEN was registered under "github.com" only → HTTP 401 on gh CLI / GraphQL.
func TestApplySecrets_EmittedGHTokenResolvesAllGitHubHosts(t *testing.T) {
	t.Parallel()
	broker := cred.NewBroker()
	id := seedTestID(0x53)
	const real = "ghs_shared_regression_token"
	extra, _, err := applySecrets(broker, id, []SecretBind{{
		Env:   BuiltinGitHubEnv,
		Hosts: GitHubSecretHosts,
		Token: real,
	}})
	if err != nil {
		t.Fatalf("applySecrets: %v", err)
	}

	// Extract the emitted GH_TOKEN placeholder from the env output.
	var emittedPH string
	for _, line := range bytes.Split(extra, []byte("\n")) {
		if bytes.HasPrefix(line, []byte("GH_TOKEN=")) {
			emittedPH = string(bytes.TrimPrefix(line, []byte("GH_TOKEN=")))
			break
		}
	}
	if emittedPH == "" {
		t.Fatal("GH_TOKEN not found in applySecrets output")
	}

	// The emitted placeholder MUST resolve to the real token for EVERY host in
	// the bind — this is the exact call the MITM proxy makes on each request.
	for _, h := range GitHubSecretHosts {
		got, ok := broker.ResolveScoped(emittedPH, id, h)
		if !ok || got != real {
			t.Errorf("ResolveScoped(emittedGHToken, id, %q): ok=%v tok=%q; want (%q, true) — MITM would return 401", h, ok, got, real)
		}
	}

	// Host-boundary: emitted placeholder must NOT resolve for an unrelated host.
	if tok, ok := broker.ResolveScoped(emittedPH, id, "unrelated.example.com"); ok {
		t.Errorf("ResolveScoped(unrelated host): got (%q, true); want (\"\", false) — host-boundary breach", tok)
	}

	// Sandbox-boundary: emitted placeholder must NOT resolve for a different sandbox.
	var otherID domain.SandboxID
	otherID[0] = 0xDE
	if tok, ok := broker.ResolveScoped(emittedPH, otherID, GitHubSecretHosts[0]); ok {
		t.Errorf("ResolveScoped(other sandbox): got (%q, true); want (\"\", false) — sandbox-boundary breach", tok)
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
	// Each GitHub host has its own placeholder registered; resolve via that
	// host's placeholder to confirm the broker swap path.
	for _, h := range GitHubSecretHosts {
		ph, hasPH := broker.Placeholder(id, h)
		if !hasPH {
			t.Errorf("broker.Placeholder(id, %q): no placeholder registered", h)
			continue
		}
		got, ok := broker.ResolveScoped(ph, id, h)
		if !ok || got != real {
			t.Errorf("ResolveScoped for host %s: ok=%v tok=%q, want (%q, true)", h, ok, got, real)
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

// TestGitHubFromVault_UnlinkedPrincipalNoBuiltin verifies that an unlinked
// principal returns an error rather than falling back to any local credential.
func TestGitHubFromVault_UnlinkedPrincipalNoBuiltin(t *testing.T) {
	t.Parallel()
	v := vaulttest.NewFake()
	_, ok, err := GitHubSecretFromVault(context.Background(), v, "local:nobody", "proj")
	if ok || !errors.Is(err, vault.ErrUnlinked) {
		t.Fatalf("ok=%v err=%v, want ok=false and ErrUnlinked", ok, err)
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

// TestResolveEnvelopeSecrets_GHTokenSkippedWithoutVault verifies D13: with no
// vault in ResolveEnvelopeSecrets, GH_TOKEN@github.com produces 0 binds
// (fail closed — no gh auth token fallback).
func TestResolveEnvelopeSecrets_GHTokenSkippedWithoutVault(t *testing.T) {
	t.Parallel()
	binds, err := ResolveEnvelopeSecrets(context.Background(), []string{"GH_TOKEN@github.com,api.github.com"})
	if err != nil {
		t.Fatalf("ResolveEnvelopeSecrets: %v", err)
	}
	if len(binds) != 0 {
		t.Fatalf("D13: want 0 binds (no gh fallback), got %d: %+v", len(binds), binds)
	}
}

// TestResolveEnvelopeSecrets_HostGate_GHTokenNonGitHubVoided is the key
// negative control for T9-AC2. GH_TOKEN bound to a non-GitHub host must NOT
// source the operator's gh token — the exfil path must be closed.
//
// Mutation evidence: removing the allGitHubHosts gate in ResolveEnvelopeSecrets
// causes this test to return a non-empty bind with the faked gh token instead of
// an empty slice, breaking the assertion. The test FAILS if the gate is removed.
func TestResolveEnvelopeSecrets_HostGate_GHTokenNonGitHubVoided(t *testing.T) {
	t.Parallel()
	binds, err := ResolveEnvelopeSecrets(context.Background(), []string{"GH_TOKEN@evil.com"})
	if err != nil {
		t.Fatalf("ResolveEnvelopeSecrets: unexpected error: %v", err)
	}
	if len(binds) != 0 {
		t.Fatalf("GH_TOKEN@evil.com: want 0 binds (exfil gate), got %d: %+v", len(binds), binds)
	}
}

// TestResolveEnvelopeSecrets_HostGate_GHTokenAllGitHubFailsClosedD13 verifies
// that GH_TOKEN bound exclusively to GitHub hosts is NOT auto-sourced (D13).
func TestResolveEnvelopeSecrets_HostGate_GHTokenAllGitHubFailsClosedD13(t *testing.T) {
	t.Parallel()
	binds, err := ResolveEnvelopeSecrets(context.Background(), []string{"GH_TOKEN@github.com,api.github.com"})
	if err != nil {
		t.Fatalf("ResolveEnvelopeSecrets: %v", err)
	}
	if len(binds) != 0 {
		t.Fatalf("D13: want 0 binds (no gh fallback), got %d: %+v", len(binds), binds)
	}
}

func TestResolveEnvelopeSecrets_HostGate_NonGitHubEnvFromProcessEnv(t *testing.T) {
	t.Setenv("GITLAB_TOKEN", "glpat_test_value")
	binds, err := ResolveEnvelopeSecrets(context.Background(), []string{"GITLAB_TOKEN@gitlab.com"})
	if err != nil {
		t.Fatalf("ResolveEnvelopeSecrets: %v", err)
	}
	if len(binds) != 1 || binds[0].Token != "glpat_test_value" {
		t.Fatalf("GITLAB_TOKEN@gitlab.com: want 1 bind from process env, got %+v", binds)
	}
}

func TestResolveEnvelopeSecrets_HostGate_MixedGHHostVoided(t *testing.T) {
	t.Parallel()
	binds, err := ResolveEnvelopeSecrets(context.Background(), []string{"GH_TOKEN@github.com,evil.com"})
	if err != nil {
		t.Fatalf("ResolveEnvelopeSecrets: unexpected error: %v", err)
	}
	if len(binds) != 0 {
		t.Fatalf("GH_TOKEN@github.com,evil.com: want 0 binds (mixed-host gate), got %d: %+v", len(binds), binds)
	}
}

// TestSeedGuestSecrets_NoTokenLeakD13 verifies D13: with no vault,
// GH_TOKEN@github.com specs produce no payload (fail closed, no token leak).
func TestSeedGuestSecrets_NoTokenLeakD13(t *testing.T) {
	t.Parallel()
	const fake = "ghs_should_never_appear"
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
	if bytes.Contains(payload, []byte(fake)) {
		t.Fatalf("leaked token: %s", payload)
	}
	if bytes.Contains(payload, []byte("GH_TOKEN=")) {
		t.Fatalf("D13: GH_TOKEN must not be seeded without vault: %s", payload)
	}
}
