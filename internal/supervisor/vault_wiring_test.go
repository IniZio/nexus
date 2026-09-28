package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/vaulttest"
)

func TestSupervisorResolvesGitHubViaVault(t *testing.T) {
	fakeV := vaulttest.NewFake()
	ctx := context.Background()
	key := vault.Key{Principal: "alice", Integration: "github"}
	if err := fakeV.Put(ctx, key, vault.Record{AccessToken: "gh-tok", AllowedProjects: []string{"*"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	broker := cred.NewBroker()
	var sid domain.SandboxID
	sid[0] = 1
	sb := domain.Sandbox{
		ID:        sid,
		Principal: "alice",
		Envelope:  domain.Envelope{SecretHosts: service.GitHubSecretHosts},
	}

	payload := resolveGitHubFromVault(ctx, fakeV, broker, sb)

	if len(payload) == 0 {
		t.Fatal("resolveGitHubFromVault returned empty payload; GH_TOKEN placeholder not seeded")
	}
	payloadStr := string(payload)
	if !strings.Contains(payloadStr, "GH_TOKEN=") {
		t.Errorf("payload missing GH_TOKEN line; got %q", payloadStr)
	}
	if !strings.Contains(payloadStr, "GITHUB_TOKEN=") {
		t.Errorf("payload missing GITHUB_TOKEN alias; got %q", payloadStr)
	}
	ph := strings.TrimPrefix(strings.SplitN(payloadStr, "\n", 2)[0], "GH_TOKEN=")
	if _, ok := broker.Resolve(ph); !ok {
		t.Errorf("broker.Resolve(%q) = false; placeholder not registered for github.com", ph)
	}
}

func TestResolveGitHubFromVault_SkipsWhenGitHubNotInSecretHosts(t *testing.T) {
	fakeV := vaulttest.NewFake()
	ctx := context.Background()
	key := vault.Key{Principal: "alice", Integration: "github"}
	if err := fakeV.Put(ctx, key, vault.Record{AccessToken: "gh-tok", AllowedProjects: []string{"*"}}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	broker := cred.NewBroker()
	var sid domain.SandboxID
	sid[0] = 3
	sb := domain.Sandbox{
		ID:        sid,
		Principal: "alice",
		Envelope:  domain.Envelope{SecretHosts: []string{"api.anthropic.com"}},
	}

	payload := resolveGitHubFromVault(ctx, fakeV, broker, sb)
	if payload != nil {
		t.Fatalf("expected nil payload when GitHub not in SecretHosts; got %q", string(payload))
	}
}

func TestSupervisorMCPBindsViaVault(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	claudeDir := filepath.Join(tmpHome, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	creds := map[string]any{
		"mcpOAuth": map[string]any{
			"linear-server": map[string]any{
				"serverName":   "linear-server",
				"serverUrl":    "https://mcp.linear.app/sse",
				"accessToken":  "lin-tok",
				"refreshToken": "lin-ref",
			},
		},
	}
	data, _ := json.Marshal(creds)
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	fakeV := vaulttest.NewFake()
	ctx := context.Background()
	if err := fakeV.Put(ctx, vault.Key{Principal: "alice", Integration: "linear"}, vault.Record{
		AccessToken: "lin-tok", AllowedProjects: []string{"*"},
	}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	broker := cred.NewBroker()
	var sid domain.SandboxID
	sid[0] = 2

	seeds := resolveVaultMCPBindsForBroker(ctx, fakeV, broker, sid, "alice", "")
	if len(seeds) == 0 {
		t.Error("resolveVaultMCPBindsForBroker returned no seeds; vault MCP wiring is broken")
	}
	if _, ok := seeds["linear-server"]; !ok {
		t.Errorf("seeds missing linear-server; got %v", seeds)
	}
}
