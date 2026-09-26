package vaulttest_test

import (
	"testing"

	"github.com/IniZio/nexus/internal/core/vault/vaulttest"
)

func TestFakesPassContracts(t *testing.T) {
	t.Run("MemStore", func(t *testing.T) {
		vaulttest.RunStoreContract(t, vaulttest.NewMemStore())
	})
	t.Run("Fake", func(t *testing.T) {
		vaulttest.RunVaultContract(t, vaulttest.NewFake())
	})
	t.Run("FakeConnector", func(t *testing.T) {
		vaulttest.RunConnectorContract(t, vaulttest.NewFakeConnector("test-connector"))
	})
}

func TestRegistryRejectsDuplicateID(t *testing.T) {
	reg := vaulttest.NewTestRegistry()
	c1 := vaulttest.NewFakeConnector("github")
	c2 := vaulttest.NewFakeConnector("github")

	if err := reg.Register(c1); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := reg.Register(c2); err == nil {
		t.Fatal("expected error on duplicate registration")
	}
}

func TestSourceImplementsCredentialSource(t *testing.T) {
	vaulttest.TestSourceImplementsCredentialSource(t)
}
