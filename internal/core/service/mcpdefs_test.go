package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// writeMCPJSON writes a {"mcpServers": servers} JSON file at path.
func writeMCPJSON(t *testing.T, path string, servers map[string]any) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"mcpServers": servers})
	if err != nil {
		t.Fatalf("writeMCPJSON marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writeMCPJSON write %s: %v", path, err)
	}
}

// TestBuildSharedMCPServers_HTTPLiteralRedacted is the CORE SECURITY TEST.
//
// An http server with a literal "Authorization: Bearer sk-REALSECRET123" header
// must never leak the secret into the guest-visible Servers map. The redaction
// branch in sanitizeHTTPEntry (mcpdefs.go ~line 165–173, the
// isCredentialHeader→redact case) is the sole guard. If that branch is deleted:
//   - "sk-REALSECRET123" survives verbatim in Servers JSON → strings.Contains
//     assertion fires.
//   - No HTTPBind is minted → len(got.HTTPBinds)==0 → length assertion fires.
//
// Both assertions are independent mutation detectors for the same branch.
func TestBuildSharedMCPServers_HTTPLiteralRedacted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	writeMCPJSON(t, filepath.Join(dir, ".claude.json"), map[string]any{
		"linear": map[string]any{
			"type": "http",
			"url":  "https://api.linear.app/mcp",
			"headers": map[string]any{
				"Authorization": "Bearer sk-REALSECRET123",
			},
		},
	})

	got, err := BuildSharedMCPServers(cred.ClaudeCodeProfile, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, ok := got.Servers["linear"]
	if !ok {
		t.Fatal("want server 'linear' in Servers map")
	}
	rawStr := string(raw)

	// Mutation guard: deletion of the isCredentialHeader→redact branch makes
	// the next two assertions fail by leaking the secret.
	if strings.Contains(rawStr, "sk-REALSECRET123") {
		t.Error("SECURITY: raw secret 'sk-REALSECRET123' leaked into Servers JSON — redaction branch inactive")
	}
	if strings.Contains(rawStr, "REALSECRET") {
		t.Error("SECURITY: partial secret 'REALSECRET' leaked into Servers JSON")
	}

	// Header value must be replaced with the synthetic var ref.
	const synVar = "NEXUS3_MCP_LINEAR_AUTHORIZATION"
	if !strings.Contains(rawStr, "${"+synVar+"}") {
		t.Errorf("want synthetic var ref ${%s} in Servers JSON, got: %s", synVar, rawStr)
	}

	// Exactly one HTTPBind carrying the original literal as Token.
	if len(got.HTTPBinds) != 1 {
		t.Fatalf("want 1 HTTPBind, got %d: %+v", len(got.HTTPBinds), got.HTTPBinds)
	}
	b := got.HTTPBinds[0]
	if b.Token != "Bearer sk-REALSECRET123" {
		t.Errorf("bind.Token = %q, want %q", b.Token, "Bearer sk-REALSECRET123")
	}
	if b.Env != synVar {
		t.Errorf("bind.Env = %q, want %q", b.Env, synVar)
	}
	if len(b.Hosts) != 1 || b.Hosts[0] != "api.linear.app" {
		t.Errorf("bind.Hosts = %v, want [api.linear.app]", b.Hosts)
	}
}

// TestBuildSharedMCPServers_HTTPVarRefPreserved verifies that an http header
// using a ${VAR} reference is kept verbatim in the Servers JSON and a
// SecretBind is minted with Env==the var name and Token==the resolved env value.
func TestBuildSharedMCPServers_HTTPVarRefPreserved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("LINEAR_KEY", "sk-lin-realvalue")

	writeMCPJSON(t, filepath.Join(dir, ".claude.json"), map[string]any{
		"linear": map[string]any{
			"type": "http",
			"url":  "https://api.linear.app/mcp",
			"headers": map[string]any{
				"Authorization": "Bearer ${LINEAR_KEY}",
			},
		},
	})

	got, err := BuildSharedMCPServers(cred.ClaudeCodeProfile, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, ok := got.Servers["linear"]
	if !ok {
		t.Fatal("want server 'linear' in Servers map")
	}
	if !strings.Contains(string(raw), "${LINEAR_KEY}") {
		t.Errorf("want ${LINEAR_KEY} preserved verbatim in Servers JSON, got: %s", string(raw))
	}

	if len(got.HTTPBinds) != 1 {
		t.Fatalf("want 1 HTTPBind, got %d: %+v", len(got.HTTPBinds), got.HTTPBinds)
	}
	b := got.HTTPBinds[0]
	if b.Env != "LINEAR_KEY" {
		t.Errorf("bind.Env = %q, want LINEAR_KEY", b.Env)
	}
	if b.Token != "sk-lin-realvalue" {
		t.Errorf("bind.Token = %q, want sk-lin-realvalue", b.Token)
	}
}

// TestBuildSharedMCPServers_StdioLiteralKeptAndEnvResolved verifies that:
//   - stdio entries are kept verbatim in Servers (both ${VAR} refs and literals).
//   - StdioEnv contains KEY=VALUE lines only for ${VAR} refs whose names resolve
//     in the host environment; plain literal env values are NOT emitted.
func TestBuildSharedMCPServers_StdioLiteralKeptAndEnvResolved(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	t.Setenv("MY_TOK", "supersecrettokenvalue")

	writeMCPJSON(t, filepath.Join(dir, ".claude.json"), map[string]any{
		"mytool": map[string]any{
			"command": "/usr/bin/mytool",
			"env": map[string]any{
				"API_TOKEN": "${MY_TOK}",
				"PLAIN":     "literalvalue",
			},
		},
	})

	got, err := BuildSharedMCPServers(cred.ClaudeCodeProfile, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, ok := got.Servers["mytool"]
	if !ok {
		t.Fatal("want server 'mytool' in Servers map")
	}
	rawStr := string(raw)

	// Both env entries must survive verbatim in the Servers JSON.
	if !strings.Contains(rawStr, "${MY_TOK}") {
		t.Errorf("want ${MY_TOK} preserved verbatim in Servers JSON, got: %s", rawStr)
	}
	if !strings.Contains(rawStr, "literalvalue") {
		t.Errorf("want literal value preserved in Servers JSON, got: %s", rawStr)
	}

	stdioEnv := string(got.StdioEnv)

	// ${MY_TOK} ref must be resolved from the host environment.
	if !strings.Contains(stdioEnv, "MY_TOK=supersecrettokenvalue\n") {
		t.Errorf("StdioEnv missing MY_TOK resolved value; got: %q", stdioEnv)
	}
	// PLAIN is a literal value, not a ${VAR} ref — must NOT appear in StdioEnv.
	if strings.Contains(stdioEnv, "PLAIN=") {
		t.Errorf("StdioEnv must not contain PLAIN (literal, not a var ref); got: %q", stdioEnv)
	}
}

// TestBuildSharedMCPServers_UnionSource verifies that:
//   - Servers from .mcp.json and .claude.json are unioned.
//   - .claude.json wins on name collision.
func TestBuildSharedMCPServers_UnionSource(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	// alpha only in .mcp.json (lower priority).
	writeMCPJSON(t, filepath.Join(dir, ".mcp.json"), map[string]any{
		"alpha": map[string]any{
			"command": "/mcp-bin",
			"env":     map[string]any{"VER": "from-mcp"},
		},
	})
	// beta only in .claude.json; alpha is also present to test collision.
	writeMCPJSON(t, filepath.Join(dir, ".claude.json"), map[string]any{
		"beta": map[string]any{
			"command": "/beta-bin",
		},
		"alpha": map[string]any{
			"command": "/mcp-bin",
			"env":     map[string]any{"VER": "from-claude-json"},
		},
	})

	got, err := BuildSharedMCPServers(cred.ClaudeCodeProfile, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := got.Servers["alpha"]; !ok {
		t.Error("want 'alpha' in Servers (present in both sources)")
	}
	if _, ok := got.Servers["beta"]; !ok {
		t.Error("want 'beta' in Servers (from .claude.json only)")
	}

	// .claude.json must win the collision on "alpha".
	alphaRaw := string(got.Servers["alpha"])
	if !strings.Contains(alphaRaw, "from-claude-json") {
		t.Errorf("want .claude.json version of 'alpha'; got: %s", alphaRaw)
	}
	if strings.Contains(alphaRaw, "from-mcp") {
		t.Errorf(".mcp.json version of 'alpha' must be overridden; got: %s", alphaRaw)
	}
}

// TestBuildSharedMCPServers_NonClaudeFormatNoop verifies the profile gate:
// a profile with MCPConfigFormat != claude-json returns zero value, nil error,
// regardless of what config files exist on disk.
func TestBuildSharedMCPServers_NonClaudeFormatNoop(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	// Write a file to confirm the gate fires before any file I/O.
	writeMCPJSON(t, filepath.Join(dir, ".claude.json"), map[string]any{
		"myserver": map[string]any{"command": "/bin/server"},
	})

	noopProfile := cred.ClaudeCodeProfile
	noopProfile.MCPConfigFormat = cred.MCPConfigFormatNone

	got, err := BuildSharedMCPServers(noopProfile, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Servers != nil || got.HTTPBinds != nil || got.StdioEnv != nil {
		t.Errorf("want zero-value SharedMCPServers for non-claude-json format, got: %+v", got)
	}
}

// TestBuildSharedMCPServers_NonCredentialHeaderUntouched verifies that a
// non-credential literal header (Content-Type: application/json) is kept
// verbatim and no SecretBind is minted for it.
func TestBuildSharedMCPServers_NonCredentialHeaderUntouched(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	writeMCPJSON(t, filepath.Join(dir, ".claude.json"), map[string]any{
		"myserver": map[string]any{
			"type": "http",
			"url":  "https://api.example.com/mcp",
			"headers": map[string]any{
				"Content-Type": "application/json",
			},
		},
	})

	got, err := BuildSharedMCPServers(cred.ClaudeCodeProfile, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, ok := got.Servers["myserver"]
	if !ok {
		t.Fatal("want server 'myserver' in Servers map")
	}
	// Non-credential header value must remain verbatim.
	if !strings.Contains(string(raw), "application/json") {
		t.Errorf("want Content-Type value preserved verbatim, got: %s", string(raw))
	}
	// No bind must be minted for a non-credential header.
	if len(got.HTTPBinds) != 0 {
		t.Errorf("want no HTTPBinds for non-credential header, got %d: %+v", len(got.HTTPBinds), got.HTTPBinds)
	}
}

// injectOAuthFn overrides buildMCPOAuthBindsFn for the duration of a test.
// It restores the original on cleanup.
func injectOAuthFn(t *testing.T, fn func(string) ([]MCPOAuthBind, []MCPOAuthRefreshConfig, error)) {
	t.Helper()
	orig := buildMCPOAuthBindsFn
	buildMCPOAuthBindsFn = fn
	t.Cleanup(func() { buildMCPOAuthBindsFn = orig })
}

// linearOAuthBind returns a synthetic MCPOAuthBind for a Linear-like OAuth MCP.
func linearOAuthBind() MCPOAuthBind {
	return MCPOAuthBind{
		ServerName: "linear-server",
		ServerURL:  "https://mcp.linear.app/mcp",
		Bind: SecretBind{
			Env:   "NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION",
			Hosts: []string{"mcp.linear.app"},
			Token: "Bearer real-linear-access-token",
		},
		Header: "Authorization",
	}
}

// TestBuildSharedMCPServers_OAuthInjectsPlaceholderAndBind is the CORE SECURITY
// TEST for OAuth MCP integration.
//
// A Linear-like http MCP with NO headers in the host config + an injected OAuth
// bind must produce:
//   - Servers["linear-server"].headers.Authorization == "${NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION}"
//   - HTTPBinds contains the matching SecretBind (host mcp.linear.app, Bearer token)
//   - The real token string NEVER appears in the marshaled Servers map.
func TestBuildSharedMCPServers_OAuthInjectsPlaceholderAndBind(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	// Linear-like http MCP with NO headers (the common OAuth case).
	writeMCPJSON(t, filepath.Join(dir, ".claude.json"), map[string]any{
		"linear-server": map[string]any{
			"type": "http",
			"url":  "https://mcp.linear.app/mcp",
		},
	})

	injectOAuthFn(t, func(_ string) ([]MCPOAuthBind, []MCPOAuthRefreshConfig, error) {
		return []MCPOAuthBind{linearOAuthBind()}, nil, nil
	})

	got, err := BuildSharedMCPServers(cred.ClaudeCodeProfile, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// — Guest Servers entry —
	raw, ok := got.Servers["linear-server"]
	if !ok {
		t.Fatal("want server 'linear-server' in Servers map")
	}
	rawStr := string(raw)

	const synVar = "NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION"
	const realToken = "real-linear-access-token"

	// SECURITY: real token must NEVER appear in guest-visible Servers JSON.
	if strings.Contains(rawStr, realToken) {
		t.Errorf("SECURITY: real OAuth token leaked into Servers JSON — placeholder injection inactive; raw: %s", rawStr)
	}
	// Placeholder must be present.
	if !strings.Contains(rawStr, "${"+synVar+"}") {
		t.Errorf("want synthetic var ref ${%s} in Servers JSON, got: %s", synVar, rawStr)
	}

	// — HTTPBinds —
	var found bool
	for _, b := range got.HTTPBinds {
		if b.Env == synVar {
			found = true
			if b.Token != "Bearer "+realToken {
				t.Errorf("bind.Token = %q, want %q", b.Token, "Bearer "+realToken)
			}
			if len(b.Hosts) != 1 || b.Hosts[0] != "mcp.linear.app" {
				t.Errorf("bind.Hosts = %v, want [mcp.linear.app]", b.Hosts)
			}
		}
	}
	if !found {
		t.Errorf("HTTPBinds missing bind for %s; binds: %+v", synVar, got.HTTPBinds)
	}

	// Token must not appear in the full marshaled Servers map.
	fullMap, _ := json.Marshal(got.Servers)
	if strings.Contains(string(fullMap), realToken) {
		t.Errorf("SECURITY: real OAuth token found in full Servers marshal: %s", fullMap)
	}
}

// TestBuildSharedMCPServers_OAuthSynthesizesAbsentServer verifies that a server
// present in mcpOAuth but absent from the host config gets a synthesized guest
// entry with the placeholder header.
func TestBuildSharedMCPServers_OAuthSynthesizesAbsentServer(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	// No MCP servers in config at all.
	writeMCPJSON(t, filepath.Join(dir, ".claude.json"), map[string]any{})

	injectOAuthFn(t, func(_ string) ([]MCPOAuthBind, []MCPOAuthRefreshConfig, error) {
		return []MCPOAuthBind{linearOAuthBind()}, nil, nil
	})

	got, err := BuildSharedMCPServers(cred.ClaudeCodeProfile, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, ok := got.Servers["linear-server"]
	if !ok {
		t.Fatal("want synthesized 'linear-server' in Servers map")
	}
	rawStr := string(raw)

	const synVar = "NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION"
	if !strings.Contains(rawStr, "${"+synVar+"}") {
		t.Errorf("want ${%s} in synthesized entry, got: %s", synVar, rawStr)
	}
	if strings.Contains(rawStr, "real-linear-access-token") {
		t.Errorf("SECURITY: real token in synthesized Servers entry: %s", rawStr)
	}
	// Must be http type.
	if !strings.Contains(rawStr, `"http"`) {
		t.Errorf("synthesized entry should have type=http, got: %s", rawStr)
	}
}

// TestBuildSharedMCPServers_OAuthSynthesizedPreservesURLPath is the regression
// test for the Linear "not authenticated" bug: an OAuth MCP server absent from
// the top-level mcpServers map (Linear lives under a project scope in the host
// ~/.claude.json) is synthesized. The synthesized guest entry MUST preserve the
// full endpoint path from the mcpOAuth serverUrl (https://mcp.linear.app/mcp),
// not collapse to the bare host (https://mcp.linear.app) — the bare host returns
// HTTP 404 from Linear and surfaces in-agent as "not authenticated".
func TestBuildSharedMCPServers_OAuthSynthesizedPreservesURLPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	// linear-server is NOT in the top-level mcpServers map.
	writeMCPJSON(t, filepath.Join(dir, ".claude.json"), map[string]any{})

	injectOAuthFn(t, func(_ string) ([]MCPOAuthBind, []MCPOAuthRefreshConfig, error) {
		return []MCPOAuthBind{linearOAuthBind()}, nil, nil
	})

	got, err := BuildSharedMCPServers(cred.ClaudeCodeProfile, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, ok := got.Servers["linear-server"]
	if !ok {
		t.Fatal("want synthesized 'linear-server' in Servers map")
	}

	var e rawMCPEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("unmarshal synthesized entry: %v", err)
	}
	if e.URL != "https://mcp.linear.app/mcp" {
		t.Errorf("synthesized URL = %q, want %q (path dropped → Linear 404 → 'not authenticated')",
			e.URL, "https://mcp.linear.app/mcp")
	}
}

// TestBuildSharedMCPServers_OAuthProfileGateOff verifies that when the profile
// format is not ClaudeJSON, no OAuth binds are added even when the injectable
// would return them.
func TestBuildSharedMCPServers_OAuthProfileGateOff(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	writeMCPJSON(t, filepath.Join(dir, ".claude.json"), map[string]any{
		"linear-server": map[string]any{
			"type": "http",
			"url":  "https://mcp.linear.app/mcp",
		},
	})

	injectOAuthFn(t, func(_ string) ([]MCPOAuthBind, []MCPOAuthRefreshConfig, error) {
		return []MCPOAuthBind{linearOAuthBind()}, nil, nil
	})

	noopProfile := cred.ClaudeCodeProfile
	noopProfile.MCPConfigFormat = cred.MCPConfigFormatNone

	got, err := BuildSharedMCPServers(noopProfile, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Profile gate fires before OAuth — nothing should be in the result.
	if got.Servers != nil || got.HTTPBinds != nil || got.StdioEnv != nil {
		t.Errorf("want zero-value result for non-claude-json format, got: %+v", got)
	}
}

// TestBuildSharedMCPServers_OAuthDeduplicatesBind verifies that an OAuth bind
// is not duplicated when the same server already has a literal header that
// produced a bind via the normal sanitizeHTTPEntry path.
func TestBuildSharedMCPServers_OAuthDeduplicatesBind(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	const synVar = "NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION"

	// Server already has the Authorization header as a literal — sanitizeHTTPEntry
	// will produce a bind for synVar. The OAuth bind has the same Env name.
	writeMCPJSON(t, filepath.Join(dir, ".claude.json"), map[string]any{
		"linear-server": map[string]any{
			"type": "http",
			"url":  "https://mcp.linear.app/mcp",
			"headers": map[string]any{
				"Authorization": "Bearer literal-token",
			},
		},
	})

	injectOAuthFn(t, func(_ string) ([]MCPOAuthBind, []MCPOAuthRefreshConfig, error) {
		return []MCPOAuthBind{linearOAuthBind()}, nil, nil
	})

	got, err := BuildSharedMCPServers(cred.ClaudeCodeProfile, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Count how many binds have the synVar Env name.
	count := 0
	for _, b := range got.HTTPBinds {
		if b.Env == synVar {
			count++
		}
	}
	if count != 1 {
		t.Errorf("want exactly 1 bind for %s, got %d; binds: %+v", synVar, count, got.HTTPBinds)
	}
}

// writeClaudeDotJSON writes a full ~/.claude.json at path with both top-level
// mcpServers and a projects entry for the given projectKey.
func writeClaudeDotJSON(t *testing.T, path string, topLevel map[string]any, projectKey string, projectServers map[string]any) {
	t.Helper()
	projects := map[string]any{}
	if projectKey != "" {
		projects[projectKey] = map[string]any{"mcpServers": projectServers}
	}
	data, err := json.Marshal(map[string]any{
		"mcpServers": topLevel,
		"projects":   projects,
	})
	if err != nil {
		t.Fatalf("writeClaudeDotJSON marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("writeClaudeDotJSON write %s: %v", path, err)
	}
}

// TestBuildSharedMCPServers_ProjectScopedLiteralHTTP verifies that a
// project-scoped http MCP server in ~/.claude.json projects.<key>.mcpServers:
//   - Appears in result.Servers with its full URL preserved verbatim.
//   - Has its literal Authorization header redacted to a synthetic ${VAR}.
//   - Produces a matching HTTPBind (MITM swap path).
//   - When sourceDir does NOT match any project key, the project-only server is
//     absent and no panic occurs (pins the no-op path).
//
// MUTATION GUARD: the project-only server name ("proj-mcp") does not exist in
// the top-level mcpServers map, so any assertion on it can ONLY pass if
// readProjectScopedMCPFile is wired in.
func TestBuildSharedMCPServers_ProjectScopedLiteralHTTP(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	sourceDir := t.TempDir() // stands in for the sandbox's source directory

	const topServerName = "top-mcp"
	const projServerName = "proj-mcp"
	const projURL = "https://example.test/mcp"
	const projToken = "Bearer LITERAL-PROJECT-TOKEN"
	const projSynVar = "NEXUS3_MCP_PROJ_MCP_AUTHORIZATION"

	writeClaudeDotJSON(t, filepath.Join(dir, ".claude.json"),
		// top-level mcpServers
		map[string]any{
			topServerName: map[string]any{
				"type": "http",
				"url":  "https://top.example.test/mcp",
				"headers": map[string]any{
					"Authorization": "Bearer TOP-SECRET",
				},
			},
		},
		// project key + project-scoped servers
		sourceDir,
		map[string]any{
			projServerName: map[string]any{
				"type": "http",
				"url":  projURL,
				"headers": map[string]any{
					"Authorization": projToken,
				},
			},
		},
	)

	t.Run("matching sourceDir includes project-scoped server", func(t *testing.T) {
		got, err := BuildSharedMCPServers(cred.ClaudeCodeProfile, sourceDir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Top-level server must still be present.
		if _, ok := got.Servers[topServerName]; !ok {
			t.Errorf("want %q in Servers (top-level)", topServerName)
		}

		// Project-scoped server must appear.
		raw, ok := got.Servers[projServerName]
		if !ok {
			t.Fatalf("want %q in Servers (project-scoped)", projServerName)
		}
		rawStr := string(raw)

		// Full URL must be preserved verbatim — not collapsed to bare host.
		if !strings.Contains(rawStr, projURL) {
			t.Errorf("want full URL %q in Servers entry, got: %s", projURL, rawStr)
		}

		// SECURITY: literal token must be redacted.
		if strings.Contains(rawStr, "LITERAL-PROJECT-TOKEN") {
			t.Errorf("SECURITY: raw project token leaked into Servers JSON; got: %s", rawStr)
		}

		// Synthetic var ref must be injected.
		if !strings.Contains(rawStr, "${"+projSynVar+"}") {
			t.Errorf("want synthetic var ref ${%s}, got: %s", projSynVar, rawStr)
		}

		// HTTPBind for project-scoped server must exist.
		var found bool
		for _, b := range got.HTTPBinds {
			if b.Env == projSynVar {
				found = true
				if b.Token != projToken {
					t.Errorf("bind.Token = %q, want %q", b.Token, projToken)
				}
				if len(b.Hosts) != 1 || b.Hosts[0] != "example.test" {
					t.Errorf("bind.Hosts = %v, want [example.test]", b.Hosts)
				}
			}
		}
		if !found {
			t.Errorf("HTTPBinds missing bind for %s; binds: %+v", projSynVar, got.HTTPBinds)
		}
	})

	t.Run("non-matching sourceDir omits project-scoped server", func(t *testing.T) {
		got, err := BuildSharedMCPServers(cred.ClaudeCodeProfile, "/no/such/project")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Top-level server must still appear.
		if _, ok := got.Servers[topServerName]; !ok {
			t.Errorf("want %q in Servers (top-level)", topServerName)
		}

		// Project-only server must NOT appear.
		if _, ok := got.Servers[projServerName]; ok {
			t.Errorf("project-scoped server %q must be absent when sourceDir does not match any project key", projServerName)
		}
	})
}

// TestBuildSharedMCPServers_ProjectScopedOAuthInjected verifies that a
// project-scoped http server referenced by an OAuth bind gets the Authorization
// header placeholder injected (the existing branch) rather than synthesized,
// preserving its real full URL.
func TestBuildSharedMCPServers_ProjectScopedOAuthInjected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	sourceDir := t.TempDir()

	const projServerName = "linear-server"
	const projURL = "https://mcp.linear.app/mcp"

	// linear-server lives ONLY in the project scope (the common real-world case).
	writeClaudeDotJSON(t, filepath.Join(dir, ".claude.json"),
		map[string]any{}, // no top-level mcpServers
		sourceDir,
		map[string]any{
			projServerName: map[string]any{
				"type": "http",
				"url":  projURL,
			},
		},
	)

	injectOAuthFn(t, func(_ string) ([]MCPOAuthBind, []MCPOAuthRefreshConfig, error) {
		return []MCPOAuthBind{linearOAuthBind()}, nil, nil
	})

	got, err := BuildSharedMCPServers(cred.ClaudeCodeProfile, sourceDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	raw, ok := got.Servers[projServerName]
	if !ok {
		t.Fatalf("want %q in Servers after project-scope read + OAuth injection", projServerName)
	}

	var e rawMCPEntry
	if err := json.Unmarshal(raw, &e); err != nil {
		t.Fatalf("unmarshal entry: %v", err)
	}

	// URL must be the full project-scoped URL, not bare host or synthesized.
	if e.URL != projURL {
		t.Errorf("URL = %q, want %q (project URL must be preserved, not synthesized from OAuth host)", e.URL, projURL)
	}

	const synVar = "NEXUS3_MCP_LINEAR_SERVER_AUTHORIZATION"
	// Placeholder must be injected (existing branch, not synthesis).
	if e.Headers["Authorization"] != "${"+synVar+"}" {
		t.Errorf("Authorization header = %q, want ${%s}", e.Headers["Authorization"], synVar)
	}
}
