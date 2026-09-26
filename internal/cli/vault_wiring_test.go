package cli

import (
	"testing"

	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/vaulttest"
)

func TestSandboxCreateWiresHostVault(t *testing.T) {
	fakeV := vaulttest.NewFake()
	called := false
	orig := openHostVaultFn
	openHostVaultFn = func() (vault.Vault, error) {
		called = true
		return fakeV, nil
	}
	defer func() { openHostVaultFn = orig }()

	svc, err := newSandboxService()
	if err != nil {
		t.Fatalf("newSandboxService: %v", err)
	}
	if svc == nil {
		t.Fatal("newSandboxService returned nil service")
	}
	if !called {
		t.Error("openHostVaultFn was not called; vault is not wired into newSandboxService")
	}
}
