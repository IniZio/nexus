package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// TestSeedGuestAgentOnboarding_MCPServersIncluded is the mutation guard for
// the node-free host-side MCP merge (D-MCP-HOST).
//
// It verifies that mcpServers passed to SeedGuestAgentOnboarding appear in the
// written /root/.claude.json without any in-guest toolchain (no node, no jq,
// no python). The spy executes the actual shell script on the host /bin/sh.
//
// RED/GREEN contract:
//
//	RED  — remove cfg.MCPServers = servers from SeedGuestAgentOnboarding, or
//	        remove the MCPServers field from claudeOnboardingConfig → mcpServers
//	        key absent from the output file → test fails at
//	        "mcpServers key absent".
//	GREEN — host-side merge in place → mcpServers present, placeholder
//	        Authorization value preserved verbatim.
//
// This closes the gap that the previous stub-execer suite missed: the stubs
// returned success while the real node script exited 127 in the guest.
func TestSeedGuestAgentOnboarding_MCPServersIncluded(t *testing.T) {
	dir := t.TempDir()
	spy, rec := newSpyExecer(t, dir)

	servers := map[string]json.RawMessage{
		"linear-server": json.RawMessage(`{"type":"http","url":"https://mcp.linear.app/sse","headers":{"Authorization":"${NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION}"}}`),
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

	// Authorization placeholder must be preserved verbatim: the MITM refresher
	// swaps it at request time; inlining a real token here would be a security
	// defect.
	raw, _ := json.Marshal(mcp["linear-server"])
	if !strings.Contains(string(raw), "${NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION}") {
		t.Errorf("Authorization placeholder not preserved verbatim in merged output: %s", raw)
	}
}

// TestSeedGuestAgentOnboarding_NilServersOmitsMCPKey verifies that when no
// MCP servers are provided the mcpServers key is absent (omitempty behaviour).
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

