package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/vaulttest"
)

func TestImportHostMCPOAuthIntoVault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	p := writeTempCreds(t, map[string]any{
		"mcpOAuth": map[string]any{
			"linear-server|fp1": map[string]any{
				"serverName":  "linear-server",
				"serverUrl":   "https://mcp.linear.app/mcp",
				"accessToken": "lin_access",
				"expiresAt":   time.Now().Add(8 * time.Hour).UnixMilli(),
			},
			"glitchtip|fp2": map[string]any{
				"serverName":  "glitchtip",
				"serverUrl":   "https://glitchtip.example.com/mcp",
				"accessToken": "gt_access",
				"expiresAt":   time.Now().Add(8 * time.Hour).UnixMilli(),
			},
		},
	})

	v := vaulttest.NewFake()
	const principal = "local:testuser"
	if err := ImportMCPOAuthIntoVault(ctx, v, principal, p); err != nil {
		t.Fatalf("ImportMCPOAuthIntoVault: %v", err)
	}

	// linear-server → integration "linear" (mcp.linear.app)
	rec, err := v.Get(ctx, vault.Key{Principal: principal, Integration: "linear"})
	if err != nil {
		t.Fatalf("Get linear: %v", err)
	}
	if rec.AccessToken != "lin_access" {
		t.Errorf("linear AccessToken = %q, want lin_access", rec.AccessToken)
	}

	// glitchtip → integration "glitchtip" (not a linear host)
	rec2, err := v.Get(ctx, vault.Key{Principal: principal, Integration: "glitchtip"})
	if err != nil {
		t.Fatalf("Get glitchtip: %v", err)
	}
	if rec2.AccessToken != "gt_access" {
		t.Errorf("glitchtip AccessToken = %q, want gt_access", rec2.AccessToken)
	}
}

func TestImportDoesNotOverwriteExisting(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	p := writeTempCreds(t, map[string]any{
		"mcpOAuth": map[string]any{
			"linear-server|fp1": map[string]any{
				"serverName":  "linear-server",
				"serverUrl":   "https://mcp.linear.app/mcp",
				"accessToken": "new_token_from_file",
				"expiresAt":   time.Now().Add(8 * time.Hour).UnixMilli(),
			},
		},
	})

	v := vaulttest.NewFake()
	const principal = "local:testuser"
	key := vault.Key{Principal: principal, Integration: "linear"}

	if err := v.Put(ctx, key, vault.Record{
		AccessToken:     "existing_token",
		AllowedProjects: []string{"*"},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := ImportMCPOAuthIntoVault(ctx, v, principal, p); err != nil {
		t.Fatalf("ImportMCPOAuthIntoVault: %v", err)
	}

	rec, err := v.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.AccessToken != "existing_token" {
		t.Errorf("AccessToken = %q, want existing_token (must not overwrite)", rec.AccessToken)
	}
}

func TestMCPBindResolvesFromVault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	p := writeTempCreds(t, map[string]any{
		"mcpOAuth": map[string]any{
			"linear-server|fp1": map[string]any{
				"serverName":  "linear-server",
				"serverUrl":   "https://mcp.linear.app/mcp",
				"accessToken": "raw_token_ignored",
				"expiresAt":   time.Now().Add(8 * time.Hour).UnixMilli(),
			},
		},
	})

	v := vaulttest.NewFake()
	const principal = "local:testuser"
	if err := v.Put(ctx, vault.Key{Principal: principal, Integration: "linear"}, vault.Record{
		AccessToken:     "vault_token",
		Expiry:          time.Now().Add(8 * time.Hour),
		AllowedProjects: []string{"*"},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	binds, err := BuildMCPOAuthBindsFromVault(ctx, v, principal, "any-project", p)
	if err != nil {
		t.Fatalf("BuildMCPOAuthBindsFromVault: %v", err)
	}
	if len(binds) != 1 {
		t.Fatalf("want 1 bind, got %d", len(binds))
	}
	if binds[0].Bind.Token != "Bearer vault_token" {
		t.Errorf("Token = %q, want Bearer vault_token", binds[0].Bind.Token)
	}
	if binds[0].ServerName != "linear-server" {
		t.Errorf("ServerName = %q, want linear-server", binds[0].ServerName)
	}
}

func TestMCPUnlinkedFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	p := writeTempCreds(t, map[string]any{
		"mcpOAuth": map[string]any{
			"linear-server|fp1": map[string]any{
				"serverName":  "linear-server",
				"serverUrl":   "https://mcp.linear.app/mcp",
				"accessToken": "raw_token",
				"expiresAt":   time.Now().Add(8 * time.Hour).UnixMilli(),
			},
		},
	})

	v := vaulttest.NewFake()
	_, err := BuildMCPOAuthBindsFromVault(ctx, v, "local:nobody", "any-project", p)
	if !errors.Is(err, vault.ErrUnlinked) {
		t.Errorf("want ErrUnlinked for unlinked principal, got %v", err)
	}
}
