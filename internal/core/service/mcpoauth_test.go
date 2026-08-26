package service

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeTempCreds(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal creds fixture: %v", err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write creds fixture: %v", err)
	}
	return p
}

func TestBuildMCPOAuthBinds_MissingFile(t *testing.T) {
	binds, refresh, err := BuildMCPOAuthBinds("/nonexistent/path/.credentials.json")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if binds != nil || refresh != nil {
		t.Fatalf("expected nil binds and refresh for missing file")
	}
}

func TestBuildMCPOAuthBinds_EmptyMCPOAuth(t *testing.T) {
	p := writeTempCreds(t, map[string]any{
		"mcpOAuth": map[string]any{},
	})
	binds, refresh, err := BuildMCPOAuthBinds(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binds != nil || refresh != nil {
		t.Fatalf("expected nil binds and refresh for empty mcpOAuth")
	}
}

func TestBuildMCPOAuthBinds_AbsentMCPOAuthKey(t *testing.T) {
	p := writeTempCreds(t, map[string]any{})
	binds, refresh, err := BuildMCPOAuthBinds(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if binds != nil || refresh != nil {
		t.Fatalf("expected nil binds and refresh when key absent")
	}
}

func TestBuildMCPOAuthBinds_TwoServers(t *testing.T) {
	// Override discovery: linear succeeds, glitchtip fails (tests fallback).
	orig := discoverTokenEndpoint
	defer func() { discoverTokenEndpoint = orig }()

	discoverTokenEndpoint = func(authServerURL string) (string, error) {
		switch authServerURL {
		case "https://mcp.linear.app":
			return "https://mcp.linear.app/oauth/token", nil
		case "https://glitchtip.example.com":
			return "", fmt.Errorf("discovery failed")
		default:
			return "", fmt.Errorf("unexpected authServerURL: %s", authServerURL)
		}
	}

	fixture := map[string]any{
		"mcpOAuth": map[string]any{
			"linear-server|fp1": map[string]any{
				"serverName":   "linear-server",
				"serverUrl":    "https://mcp.linear.app/mcp",
				"accessToken":  "REDACTED_LINEAR_ACCESS",
				"refreshToken": "REDACTED_LINEAR_REFRESH",
				"expiresAt":    int64(1787736666078),
				"scope":        "read write",
				"clientId":     "https://claude.ai/oauth/claude-code-client-metadata",
				"discoveryState": map[string]any{
					"authorizationServerUrl": "https://mcp.linear.app",
					"oauthMetadataFound":     true,
				},
			},
			"glitchtip|fp2": map[string]any{
				"serverName":   "glitchtip",
				"serverUrl":    "https://glitchtip.example.com/mcp",
				"accessToken":  "REDACTED_GLITCH_ACCESS",
				"refreshToken": "REDACTED_GLITCH_REFRESH",
				"expiresAt":    int64(1787736666000),
				"scope":        "read",
				"clientId":     "glitchtip-client-id",
				"discoveryState": map[string]any{
					"authorizationServerUrl": "https://glitchtip.example.com",
					"oauthMetadataFound":     false,
				},
			},
		},
	}

	p := writeTempCreds(t, fixture)
	binds, refresh, err := BuildMCPOAuthBinds(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(binds) != 2 {
		t.Fatalf("expected 2 binds, got %d", len(binds))
	}
	if len(refresh) != 2 {
		t.Fatalf("expected 2 refresh configs, got %d", len(refresh))
	}

	// Results come sorted by key: "glitchtip|fp2" < "linear-server|fp1"
	// glitchtip first.
	gBind := binds[0]
	lBind := binds[1]
	gRef := refresh[0]
	lRef := refresh[1]

	// Verify glitchtip bind.
	if gBind.ServerName != "glitchtip" {
		t.Errorf("glitchtip bind.ServerName = %q, want %q", gBind.ServerName, "glitchtip")
	}
	if gBind.Header != "Authorization" {
		t.Errorf("glitchtip bind.Header = %q, want Authorization", gBind.Header)
	}
	wantGlitchEnv := syntheticMCPVar("glitchtip", "Authorization") // NEXUS3_MCP_GLITCHTIP_AUTHORIZATION
	if gBind.Bind.Env != wantGlitchEnv {
		t.Errorf("glitchtip Bind.Env = %q, want %q", gBind.Bind.Env, wantGlitchEnv)
	}
	if gBind.Bind.Token != "Bearer REDACTED_GLITCH_ACCESS" {
		t.Errorf("glitchtip Bind.Token = %q, want Bearer REDACTED_GLITCH_ACCESS", gBind.Bind.Token)
	}
	if len(gBind.Bind.Hosts) != 1 || gBind.Bind.Hosts[0] != "glitchtip.example.com" {
		t.Errorf("glitchtip Bind.Hosts = %v, want [glitchtip.example.com]", gBind.Bind.Hosts)
	}

	// Verify linear bind.
	if lBind.ServerName != "linear-server" {
		t.Errorf("linear bind.ServerName = %q, want linear-server", lBind.ServerName)
	}
	wantLinearEnv := syntheticMCPVar("linear-server", "Authorization") // NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION
	if lBind.Bind.Env != wantLinearEnv {
		t.Errorf("linear Bind.Env = %q, want %q", lBind.Bind.Env, wantLinearEnv)
	}
	if lBind.Bind.Token != "Bearer REDACTED_LINEAR_ACCESS" {
		t.Errorf("linear Bind.Token = %q", lBind.Bind.Token)
	}
	if len(lBind.Bind.Hosts) != 1 || lBind.Bind.Hosts[0] != "mcp.linear.app" {
		t.Errorf("linear Bind.Hosts = %v, want [mcp.linear.app]", lBind.Bind.Hosts)
	}

	// Verify linear refresh: discovered endpoint.
	if lRef.TokenEndpoint != "https://mcp.linear.app/oauth/token" {
		t.Errorf("linear TokenEndpoint = %q, want discovered value", lRef.TokenEndpoint)
	}
	if lRef.RefreshToken != "REDACTED_LINEAR_REFRESH" {
		t.Errorf("linear RefreshToken = %q", lRef.RefreshToken)
	}
	if lRef.ClientID != "https://claude.ai/oauth/claude-code-client-metadata" {
		t.Errorf("linear ClientID = %q", lRef.ClientID)
	}

	// Verify glitchtip refresh: fallback endpoint.
	wantGlitchEndpoint := "https://glitchtip.example.com/token"
	if gRef.TokenEndpoint != wantGlitchEndpoint {
		t.Errorf("glitchtip TokenEndpoint = %q, want fallback %q", gRef.TokenEndpoint, wantGlitchEndpoint)
	}
	if gRef.RefreshToken != "REDACTED_GLITCH_REFRESH" {
		t.Errorf("glitchtip RefreshToken = %q", gRef.RefreshToken)
	}
}
