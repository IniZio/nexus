package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/connectors"
	"github.com/IniZio/nexus/internal/core/vault/vaulttest"
	"github.com/IniZio/nexus/internal/core/driver/fake"
)

// fakeVaultWithGitHub returns a Fake vault pre-seeded with a GitHub record.
func fakeVaultWithGitHub(t *testing.T, principal, project, token string) *vaulttest.Fake {
	t.Helper()
	v := vaulttest.NewFake()
	if err := v.Put(context.Background(), vault.Key{Principal: principal, Integration: "github"}, vault.Record{
		AccessToken:     token,
		Expiry:          time.Now().Add(8 * time.Hour),
		AllowedProjects: []string{project},
	}); err != nil {
		t.Fatalf("vault Put: %v", err)
	}
	return v
}

func TestResolveGitHubFromVaultForPrincipal(t *testing.T) {
	t.Parallel()
	const (
		principal = "local:testuser"
		project   = "myproject"
		token     = "ghs_vault_token"
	)
	v := fakeVaultWithGitHub(t, principal, project, token)

	bind, ok, err := GitHubSecretFromVault(context.Background(), v, principal, project)
	if err != nil {
		t.Fatalf("GitHubSecretFromVault: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if bind.Token != token {
		t.Errorf("Token = %q, want %q", bind.Token, token)
	}
	if bind.Env != BuiltinGitHubEnv {
		t.Errorf("Env = %q, want %q", bind.Env, BuiltinGitHubEnv)
	}
	for _, wantHost := range GitHubSecretHosts {
		found := false
		for _, h := range bind.Hosts {
			if h == wantHost {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Hosts missing %q; got %v", wantHost, bind.Hosts)
		}
	}
}

func TestVaultRecordOutsideAllowedProjectsVoided(t *testing.T) {
	t.Parallel()
	const principal = "local:testuser"
	v := vaulttest.NewFake()
	if err := v.Put(context.Background(), vault.Key{Principal: principal, Integration: "github"}, vault.Record{
		AccessToken:     "ghs_restricted",
		Expiry:          time.Now().Add(8 * time.Hour),
		AllowedProjects: []string{"allowed-project"},
	}); err != nil {
		t.Fatalf("vault Put: %v", err)
	}

	_, _, err := GitHubSecretFromVault(context.Background(), v, principal, "different-project")
	if !errors.Is(err, vault.ErrProjectNotAllowed) {
		t.Errorf("want ErrProjectNotAllowed, got %v", err)
	}
}

func TestHostGateStillVoidsNonGitHub(t *testing.T) {
	t.Parallel()
	binds, err := ResolveEnvelopeSecrets(context.Background(), []string{"GH_TOKEN@evil.com"})
	if err != nil {
		t.Fatalf("ResolveEnvelopeSecrets: %v", err)
	}
	if len(binds) != 0 {
		t.Errorf("GH_TOKEN@evil.com: want 0 binds (host-gate void), got %d: %+v", len(binds), binds)
	}
}

func TestUnlinkedPrincipalFailsClosed(t *testing.T) {
	t.Parallel()
	v := vaulttest.NewFake()
	_, _, err := GitHubSecretFromVault(context.Background(), v, "local:nobody", "anyproject")
	if !errors.Is(err, vault.ErrUnlinked) {
		t.Errorf("want ErrUnlinked for unlinked principal, got %v", err)
	}
}

func TestCreateHonorsPrincipalEnv(t *testing.T) {
	ctx := context.Background()
	t.Setenv("NEXUS_PRINCIPAL", "local:envuser")

	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	img := putFakeImage(t, ctx, cache)
	fd := fake.New()
	svc := newTestSvc(t, fd)

	sb, err := CreateAndBoot(ctx, svc, cache, fakeDriverFactory(fd), noopProbe, "proj", "env-principal", CreateAndBootOptions{
		Image:     ImageSpec{Digest: string(img.Digest)},
		CacheRoot: cacheRoot,
		DiskDir:   t.TempDir(),
		Broker:    cred.NewBroker(),
	})
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	if sb.Principal != "local:envuser" {
		t.Errorf("Principal = %q, want local:envuser", sb.Principal)
	}
}

func TestGitHubConnectorHostsMatchSecretHosts(t *testing.T) {
	t.Parallel()
	c := connectors.NewGitHub("")
	connHosts := c.AllowedHosts()

	if len(connHosts) != len(GitHubSecretHosts) {
		t.Fatalf("connector AllowedHosts len=%d, GitHubSecretHosts len=%d; must match", len(connHosts), len(GitHubSecretHosts))
	}
	connSet := make(map[string]struct{}, len(connHosts))
	for _, h := range connHosts {
		connSet[h] = struct{}{}
	}
	for _, h := range GitHubSecretHosts {
		if _, ok := connSet[h]; !ok {
			t.Errorf("GitHubSecretHosts contains %q which is absent from connector AllowedHosts %v", h, connHosts)
		}
	}
}
