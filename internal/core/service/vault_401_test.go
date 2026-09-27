package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/vaulttest"
)

func setupVaultForRefresh(t *testing.T, principal, integration string, allowedProjects []string) (*vaulttest.Fake, vault.Key) {
	t.Helper()
	v := vaulttest.NewFake()
	key := vault.Key{Principal: principal, Integration: integration}
	rec := vault.Record{
		AccessToken:     "tok",
		Expiry:          time.Now().Add(time.Hour),
		AllowedProjects: allowedProjects,
	}
	if err := v.Put(context.Background(), key, rec); err != nil {
		t.Fatalf("Put: %v", err)
	}
	return v, key
}

func TestBuildVaultForceRefreshFns_ProjectDenied(t *testing.T) {
	v, _ := setupVaultForRefresh(t, "local:u", "github", []string{"other"})
	broker := cred.NewBroker()
	sandboxID := domain.SandboxID{}
	fns := buildVaultForceRefreshFns(v, sandboxID, "local:u", "p", broker)
	fn, ok := fns["github.com"]
	if !ok {
		t.Fatal("expected fn for github.com")
	}
	_, err := fn(context.Background())
	if !errors.Is(err, vault.ErrProjectNotAllowed) {
		t.Errorf("want ErrProjectNotAllowed, got %v", err)
	}
}

func TestBuildVaultForceRefreshFns_WildcardAllowed(t *testing.T) {
	v, _ := setupVaultForRefresh(t, "local:u", "github", []string{"*"})
	broker := cred.NewBroker()
	sandboxID := domain.SandboxID{}
	fns := buildVaultForceRefreshFns(v, sandboxID, "local:u", "p", broker)
	fn := fns["github.com"]
	tok, err := fn(context.Background())
	if err != nil {
		t.Fatalf("wildcard: unexpected error: %v", err)
	}
	if tok == "" {
		t.Error("expected non-empty token")
	}
}

func TestBuildVaultForceRefreshFns_ExplicitProjectAllowed(t *testing.T) {
	v, _ := setupVaultForRefresh(t, "local:u", "github", []string{"p"})
	broker := cred.NewBroker()
	sandboxID := domain.SandboxID{}
	fns := buildVaultForceRefreshFns(v, sandboxID, "local:u", "p", broker)
	fn := fns["github.com"]
	tok, err := fn(context.Background())
	if err != nil {
		t.Fatalf("explicit project: unexpected error: %v", err)
	}
	if tok == "" {
		t.Error("expected non-empty token")
	}
}

func TestBuildVaultForceRefreshFns_NilVaultReturnsNil(t *testing.T) {
	broker := cred.NewBroker()
	fns := buildVaultForceRefreshFns(nil, domain.SandboxID{}, "local:u", "p", broker)
	if fns != nil {
		t.Error("expected nil fns when vault is nil")
	}
}
