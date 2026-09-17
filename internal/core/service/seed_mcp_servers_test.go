package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func TestSeedGuestAgentOnboarding_MCPServersIncluded(t *testing.T) {
	dir := t.TempDir()
	spy, rec := newSpyExecer(t, dir)

	servers := map[string]json.RawMessage{
		"linear-server": json.RawMessage(`{"type":"http","url":"https://mcp.linear.app/sse","headers":{"Authorization":"${NEXUS_MCP_LINEAR_SERVER_AUTHORIZATION}"}}`),
	}

	var id domain.SandboxID
	if err := SeedGuestAgentOnboarding(context.Background(), id, "/work", servers, spy); err != nil {
		t.Fatalf("SeedGuestAgentOnboarding: %v", err)
	}
	if !rec.Called {
		t.Fatal("execer was not called")
	}

	outPath := filepath.Join(dir, ".claude.json")
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("output file not written: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw: %s", err, data)
	}

	mcpRaw, ok := cfg["mcpServers"]
	if !ok {
		t.Fatal("mcpServers key absent from /root/.claude.json — host-side MCP merge not applied " +
			"(D-MCP-HOST RED: reproduce by removing cfg.MCPServers = servers from SeedGuestAgentOnboarding)")
	}
	mcp, ok := mcpRaw.(map[string]any)
	if !ok {
		t.Fatalf("mcpServers is not an object: %T %v", mcpRaw, mcpRaw)
	}
	if _, ok := mcp["linear-server"]; !ok {
		t.Errorf("mcpServers[\"linear-server\"] missing; got keys: %v", mcp)
	}

	// Authorization placeholder preserved: MITM refresher swaps it at request time.
	raw, _ := json.Marshal(mcp["linear-server"])
	if !strings.Contains(string(raw), "${NEXUS_MCP_LINEAR_SERVER_AUTHORIZATION}") {
		t.Errorf("Authorization placeholder not preserved verbatim in merged output: %s", raw)
	}
}

func TestSeedGuestAgentOnboarding_NilServersOmitsMCPKey(t *testing.T) {
	dir := t.TempDir()
	spy, _ := newSpyExecer(t, dir)

	var id domain.SandboxID
	if err := SeedGuestAgentOnboarding(context.Background(), id, "/work", nil, spy); err != nil {
		t.Fatalf("SeedGuestAgentOnboarding(nil servers): %v", err)
	}

	outPath := filepath.Join(dir, ".claude.json")
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("output file not written: %v", err)
	}

	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw: %s", err, data)
	}
	if _, ok := cfg["mcpServers"]; ok {
		t.Errorf("mcpServers key must be absent when servers is nil (omitempty); got: %v", cfg["mcpServers"])
	}
}
