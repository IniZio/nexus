package vaulttest

import (
	"context"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/vault"
)

// RunStoreContract verifies that store satisfies the vault.Store contract.
func RunStoreContract(t *testing.T, store vault.Store) {
	t.Helper()
	ctx := context.Background()
	key := vault.Key{Principal: "local:testuser", Integration: "github"}
	rec := vault.Record{
		AccessToken:     "tok",
		RefreshToken:    "ref",
		Expiry:          time.Now().Add(time.Hour),
		Scopes:          []string{"repo"},
		AllowedProjects: []string{"*"},
	}

	t.Run("get_missing_returns_ErrUnlinked", func(t *testing.T) {
		_, err := store.Get(ctx, key)
		if err == nil {
			t.Fatal("expected error for missing key")
		}
	})

	t.Run("put_then_get", func(t *testing.T) {
		if err := store.Put(ctx, key, rec); err != nil {
			t.Fatalf("Put: %v", err)
		}
		got, err := store.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.AccessToken != rec.AccessToken {
			t.Errorf("AccessToken mismatch: got %q want %q", got.AccessToken, rec.AccessToken)
		}
	})

	t.Run("list_returns_key", func(t *testing.T) {
		keys, err := store.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		found := false
		for _, k := range keys {
			if k == key {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("key %v not found in List", key)
		}
	})

	t.Run("delete_then_get_missing", func(t *testing.T) {
		if err := store.Delete(ctx, key); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		_, err := store.Get(ctx, key)
		if err == nil {
			t.Fatal("expected error after delete")
		}
	})
}

// RunVaultContract verifies that v satisfies the vault.Vault contract.
func RunVaultContract(t *testing.T, v vault.Vault) {
	t.Helper()
	ctx := context.Background()
	key := vault.Key{Principal: "local:contractuser", Integration: "github"}
	rec := vault.Record{
		AccessToken:     "vault-tok",
		Expiry:          time.Now().Add(time.Hour),
		AllowedProjects: []string{"myproject"},
	}

	t.Run("source_unlinked_returns_ErrUnlinked", func(t *testing.T) {
		_, err := v.Source(key, "myproject")
		if err == nil {
			t.Fatal("expected ErrUnlinked")
		}
	})

	t.Run("put_source_allowed_project", func(t *testing.T) {
		if err := v.Put(ctx, key, rec); err != nil {
			t.Fatalf("Put: %v", err)
		}
		src, err := v.Source(key, "myproject")
		if err != nil {
			t.Fatalf("Source: %v", err)
		}
		tok, _, err := src.Token(ctx)
		if err != nil {
			t.Fatalf("Token: %v", err)
		}
		if tok != rec.AccessToken {
			t.Errorf("token mismatch: got %q want %q", tok, rec.AccessToken)
		}
	})

	t.Run("source_denied_project_returns_ErrProjectNotAllowed", func(t *testing.T) {
		_, err := v.Source(key, "otherproject")
		if err == nil {
			t.Fatal("expected ErrProjectNotAllowed")
		}
	})

	t.Run("wildcard_allows_any_project", func(t *testing.T) {
		key2 := vault.Key{Principal: "local:contractuser", Integration: "linear"}
		rec2 := vault.Record{
			AccessToken:     "linear-tok",
			Expiry:          time.Now().Add(time.Hour),
			AllowedProjects: []string{"*"},
		}
		if err := v.Put(ctx, key2, rec2); err != nil {
			t.Fatalf("Put: %v", err)
		}
		_, err := v.Source(key2, "anyproject")
		if err != nil {
			t.Errorf("wildcard should allow any project: %v", err)
		}
	})
}

// RunConnectorContract verifies that c satisfies the vault.Connector contract.
func RunConnectorContract(t *testing.T, c vault.Connector) {
	t.Helper()
	ctx := context.Background()

	t.Run("id_nonempty", func(t *testing.T) {
		if c.ID() == "" {
			t.Error("ID must not be empty")
		}
	})

	t.Run("link_flow_valid", func(t *testing.T) {
		f := c.LinkFlow()
		if f != vault.LinkFlowDevice && f != vault.LinkFlowPKCE {
			t.Errorf("unexpected LinkFlow value: %q", f)
		}
	})

	switch c.LinkFlow() {
	case vault.LinkFlowDevice:
		t.Run("device_start", func(t *testing.T) {
			da, err := c.StartDevice(ctx)
			if err != nil {
				t.Fatalf("StartDevice: %v", err)
			}
			if da.DeviceCode == "" {
				t.Error("DeviceCode must not be empty")
			}
			if da.UserCode == "" {
				t.Error("UserCode must not be empty")
			}
		})
	case vault.LinkFlowPKCE:
		t.Run("pkce_auth_url", func(t *testing.T) {
			u, verifier, err := c.AuthURL("state")
			if err != nil {
				t.Fatalf("AuthURL: %v", err)
			}
			if u == "" {
				t.Error("AuthURL must not be empty")
			}
			if verifier == "" {
				t.Error("code verifier must not be empty")
			}
		})
	}
}

// TestSourceImplementsCredentialSource is a compile-time check exported as a
// runnable test so the AC test invocation picks it up.
func TestSourceImplementsCredentialSource(t *testing.T) {
	var _ cred.CredentialSource = (*fakeSource)(nil)
}
