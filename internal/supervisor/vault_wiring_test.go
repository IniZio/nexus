package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
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
	sb := domain.Sandbox{ID: sid, Principal: "alice"}

	resolveGitHubFromVault(ctx, fakeV, broker, sb)

	src, srcErr := fakeV.Source(key, "")
	if srcErr != nil {
		t.Fatalf("Source: %v", srcErr)
	}
	tok, _, tokErr := src.Token(ctx)
	if tokErr != nil || tok != "gh-tok" {
		t.Errorf("vault github token = %q (err %v), want gh-tok", tok, tokErr)
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
