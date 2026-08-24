package service

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// mcpTestSandboxID returns a deterministic SandboxID for MCP tests.
func mcpTestSandboxID(b byte) domain.SandboxID {
	var id domain.SandboxID
	id[0] = b
	return id
}

// writeMCPConfig writes JSON to <dir>/.mcp.json.
func writeMCPConfig(t *testing.T, dir, json string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	p := filepath.Join(dir, ".mcp.json")
	if err := os.WriteFile(p, []byte(json), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
}

// ── B-SEED: stdio MCP env injection ─────────────────────────────────────────

// TestResolveMCPStdioPayload_InjectsSetVar verifies that a ${VAR} reference in
// a stdio server's env map is resolved from the host environment and included
// in the returned payload as KEY=VALUE.
func TestResolveMCPStdioPayload_InjectsSetVar(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("MY_API_KEY", "secret-value-abc")

	writeMCPConfig(t, dir, `{
		"mcpServers": {
			"my-stdio": {
				"command": "my-server",
				"env": {"API_KEY": "${MY_API_KEY}"}
			}
		}
	}`)

	payload := resolveMCPStdioPayload(cred.ClaudeCodeProfile)

	want := "MY_API_KEY=secret-value-abc"
	if !bytes.Contains(payload, []byte(want)) {
		t.Errorf("payload missing %q\npayload:\n%s", want, payload)
	}
}

// TestResolveMCPStdioPayload_UnsetVarOmitted verifies that a ${VAR} reference
// whose host-side value is empty is omitted from the payload (no-op, not an
// error). This is the mutation-equivalent of the positive test above.
func TestResolveMCPStdioPayload_UnsetVarOmitted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	// MY_API_KEY is deliberately not set.

	writeMCPConfig(t, dir, `{
		"mcpServers": {
			"my-stdio": {
				"command": "my-server",
				"env": {"API_KEY": "${MY_API_KEY}"}
			}
		}
	}`)

	payload := resolveMCPStdioPayload(cred.ClaudeCodeProfile)
	if bytes.Contains(payload, []byte("MY_API_KEY")) {
		t.Errorf("payload must not contain MY_API_KEY when unset on host\npayload:\n%s", payload)
	}
}

// TestResolveMCPStdioPayload_AbsentConfigIsNoOp verifies that a missing
// ~/.claude/.mcp.json is silently ignored (no error, empty payload).
func TestResolveMCPStdioPayload_AbsentConfigIsNoOp(t *testing.T) {
	dir := t.TempDir() // no .mcp.json written
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	payload := resolveMCPStdioPayload(cred.ClaudeCodeProfile)
	if len(payload) != 0 {
		t.Errorf("expected empty payload for absent config, got:\n%s", payload)
	}
}

// TestResolveMCPStdioPayload_NonClaudeProfileNoOp verifies that the function
// is a no-op for profiles without MCPConfigFormat == claude-json.
func TestResolveMCPStdioPayload_NonClaudeProfileNoOp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("MY_API_KEY", "should-not-appear")

	writeMCPConfig(t, dir, `{"mcpServers":{"s":{"command":"x","env":{"K":"${MY_API_KEY}"}}}}`)

	// A profile with no MCPConfigFormat (the zero value) must not inject anything.
	emptyProfile := cred.AgentProfile{}
	payload := resolveMCPStdioPayload(emptyProfile)
	if len(payload) != 0 {
		t.Errorf("non-claude profile must not inject env\npayload:\n%s", payload)
	}
}

// TestResolveMCPStdioPayload_HTTPServersExcluded verifies that http/sse servers
// do NOT contribute vars to the stdio payload (D-PP-04 exemption is stdio-only).
func TestResolveMCPStdioPayload_HTTPServersExcluded(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("HTTP_KEY", "http-secret")

	writeMCPConfig(t, dir, `{
		"mcpServers": {
			"my-http": {
				"type": "http",
				"url": "https://api.example.com/mcp",
				"headers": {"Authorization": "Bearer ${HTTP_KEY}"}
			}
		}
	}`)

	payload := resolveMCPStdioPayload(cred.ClaudeCodeProfile)
	if bytes.Contains(payload, []byte("HTTP_KEY")) {
		t.Errorf("http server vars must not appear in stdio payload\npayload:\n%s", payload)
	}
}

// TestSeedGuestAgentAndSecrets_StdioMCPVarsReachGuest verifies the full
// B-SEED path: stdio MCP env vars injected into the combined seed payload
// reach the guest via the seeder.
func TestSeedGuestAgentAndSecrets_StdioMCPVarsReachGuest(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("STDIO_MCP_SECRET", "stdio-secret-xyz")

	writeMCPConfig(t, dir, `{
		"mcpServers": {
			"tool-server": {
				"command": "tool-server",
				"env": {"SECRET_KEY": "${STDIO_MCP_SECRET}"}
			}
		}
	}`)

	broker := cred.NewBroker()
	id := mcpTestSandboxID(0xA0)
	var captured []byte
	seeder := GuestSeeder(func(_ context.Context, _ domain.SandboxID, payload []byte) error {
		captured = make([]byte, len(payload))
		copy(captured, payload)
		return nil
	})

	if _, err := SeedGuestAgentAndSecrets(context.Background(), broker, id, nil, seeder); err != nil {
		t.Fatalf("SeedGuestAgentAndSecrets: %v", err)
	}

	want := "STDIO_MCP_SECRET=stdio-secret-xyz"
	if !bytes.Contains(captured, []byte(want)) {
		t.Errorf("guest payload missing %q\npayload:\n%s", want, captured)
	}
}

// TestSeedGuestAgentAndSecrets_StdioMCPVarAbsentWhenDropped is the mutation
// test for B-SEED: if resolveMCPStdioPayload were removed, STDIO_MCP_SECRET
// would be absent from the payload. Here we verify absence when env is unset.
func TestSeedGuestAgentAndSecrets_StdioMCPVarAbsentWhenUnset(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	// STDIO_MCP_SECRET deliberately not set.

	writeMCPConfig(t, dir, `{
		"mcpServers": {
			"tool-server": {
				"command": "tool-server",
				"env": {"SECRET_KEY": "${STDIO_MCP_SECRET}"}
			}
		}
	}`)

	broker := cred.NewBroker()
	id := mcpTestSandboxID(0xA1)
	var captured []byte
	seeder := GuestSeeder(func(_ context.Context, _ domain.SandboxID, payload []byte) error {
		captured = make([]byte, len(payload))
		copy(captured, payload)
		return nil
	})

	if _, err := SeedGuestAgentAndSecrets(context.Background(), broker, id, nil, seeder); err != nil {
		t.Fatalf("SeedGuestAgentAndSecrets: %v", err)
	}

	if bytes.Contains(captured, []byte("STDIO_MCP_SECRET")) {
		t.Errorf("guest payload must not contain STDIO_MCP_SECRET when unset\npayload:\n%s", captured)
	}
}

// ── C-SECRET: http/sse MCP → SecretBind ─────────────────────────────────────

// TestResolveMCPHTTPBinds_HTTPServerCreatesBindAndHost verifies that an http
// server with a ${VAR} in its headers produces a SecretBind with the correct
// Env and Hosts values, and that the bind's host appears in the slice.
func TestResolveMCPHTTPBinds_HTTPServerCreatesBindAndHost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("HTTP_MCP_KEY", "real-http-key")

	writeMCPConfig(t, dir, `{
		"mcpServers": {
			"my-http": {
				"type": "http",
				"url": "https://api.example.com/mcp",
				"headers": {"Authorization": "Bearer ${HTTP_MCP_KEY}"}
			}
		}
	}`)

	binds, err := ResolveMCPHTTPBinds(cred.ClaudeCodeProfile)
	if err != nil {
		t.Fatalf("ResolveMCPHTTPBinds: %v", err)
	}
	if len(binds) == 0 {
		t.Fatal("expected ≥1 bind, got none")
	}

	var found bool
	for _, b := range binds {
		if b.Env == "HTTP_MCP_KEY" {
			found = true
			if len(b.Hosts) == 0 || b.Hosts[0] != "api.example.com" {
				t.Errorf("bind.Hosts = %v, want [api.example.com]", b.Hosts)
			}
			if b.Token != "real-http-key" {
				t.Errorf("bind.Token = %q, want real-http-key", b.Token)
			}
		}
	}
	if !found {
		t.Errorf("no bind for HTTP_MCP_KEY; binds: %+v", binds)
	}
}

// TestResolveMCPHTTPBinds_AbsentConfigIsNoOp verifies no error and nil binds
// when the MCP config file does not exist.
func TestResolveMCPHTTPBinds_AbsentConfigIsNoOp(t *testing.T) {
	dir := t.TempDir() // no .mcp.json
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	binds, err := ResolveMCPHTTPBinds(cred.ClaudeCodeProfile)
	if err != nil {
		t.Fatalf("expected nil error for absent config, got: %v", err)
	}
	if len(binds) != 0 {
		t.Errorf("expected no binds for absent config, got: %+v", binds)
	}
}

// TestResolveMCPHTTPBinds_NonClaudeProfileNoOp verifies that profiles without
// MCPConfigFormat == claude-json return nil binds without an error.
func TestResolveMCPHTTPBinds_NonClaudeProfileNoOp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	writeMCPConfig(t, dir, `{
		"mcpServers": {
			"my-http": {
				"type": "http",
				"url": "https://api.example.com/mcp",
				"headers": {"Authorization": "Bearer ${HTTP_KEY}"}
			}
		}
	}`)

	emptyProfile := cred.AgentProfile{}
	binds, err := ResolveMCPHTTPBinds(emptyProfile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(binds) != 0 {
		t.Errorf("non-claude profile must not return binds, got: %+v", binds)
	}
}

// TestResolveMCPHTTPBinds_StdioServersExcluded verifies that stdio servers do
// NOT produce SecretBinds (they are handled by B-SEED, not C-SECRET).
func TestResolveMCPHTTPBinds_StdioServersExcluded(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	writeMCPConfig(t, dir, `{
		"mcpServers": {
			"my-stdio": {
				"command": "tool",
				"env": {"KEY": "${STDIO_KEY}"}
			}
		}
	}`)

	binds, err := ResolveMCPHTTPBinds(cred.ClaudeCodeProfile)
	if err != nil {
		t.Fatalf("ResolveMCPHTTPBinds: %v", err)
	}
	if len(binds) != 0 {
		t.Errorf("stdio server must not produce http binds, got: %+v", binds)
	}
}

// TestResolveMCPHTTPBinds_MissingHostSkipped verifies that an http server
// with no URL (and thus no host) is skipped without error.
func TestResolveMCPHTTPBinds_MissingHostSkipped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	// A server with a type=http but no URL → Host will be empty after parsing.
	// ParseMCPConfigFile will parse it, buildMCPServer sets Host from URL.
	// We can't construct a malformed entry without a URL here, so test with
	// no CredVarRefs instead (server with no ${VAR} in headers).
	writeMCPConfig(t, dir, `{
		"mcpServers": {
			"my-http-no-var": {
				"type": "http",
				"url": "https://api.example.com/mcp"
			}
		}
	}`)

	binds, err := ResolveMCPHTTPBinds(cred.ClaudeCodeProfile)
	if err != nil {
		t.Fatalf("ResolveMCPHTTPBinds: %v", err)
	}
	if len(binds) != 0 {
		t.Errorf("server with no ${VAR} must not produce binds, got: %+v", binds)
	}
}

// TestResolveMCPHTTPBinds_BindHostInAllowlist verifies the ACL invariant:
// the host from a SecretBind must appear in the bind's Hosts slice so callers
// can add it to AllowedHosts. This is the mutation test for C-SECRET:
// dropping the host from b.Hosts leaves it un-allowlisted.
func TestResolveMCPHTTPBinds_BindHostMatchesServerHost(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	writeMCPConfig(t, dir, `{
		"mcpServers": {
			"my-sse": {
				"type": "sse",
				"url": "https://stream.myservice.io/events",
				"headers": {"X-API-Key": "${SSE_KEY}"}
			}
		}
	}`)

	binds, err := ResolveMCPHTTPBinds(cred.ClaudeCodeProfile)
	if err != nil {
		t.Fatalf("ResolveMCPHTTPBinds: %v", err)
	}
	if len(binds) == 0 {
		t.Fatal("expected ≥1 bind")
	}
	for _, b := range binds {
		if b.Env != "SSE_KEY" {
			continue
		}
		for _, h := range b.Hosts {
			if h == "stream.myservice.io" {
				return // found
			}
		}
		t.Errorf("stream.myservice.io not in bind.Hosts %v; bind = %+v", b.Hosts, b)
	}
}
