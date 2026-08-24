package service

import (
	"strings"
	"testing"
)

func TestMCPParseVarRefs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		input    string
		wantName []string
		wantDef  []string
	}{
		{"${API_KEY}", []string{"API_KEY"}, []string{""}},
		{"Bearer ${TOKEN}", []string{"TOKEN"}, []string{""}},
		{"${BASE:-https://d.example.com}", []string{"BASE"}, []string{"https://d.example.com"}},
		{"no refs here", nil, nil},
		{"${A} and ${B:-def}", []string{"A", "B"}, []string{"", "def"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			refs := ParseMCPVarRefs(tc.input)
			if len(refs) != len(tc.wantName) {
				t.Fatalf("got %d refs, want %d", len(refs), len(tc.wantName))
			}
			for i, ref := range refs {
				if ref.Name != tc.wantName[i] {
					t.Errorf("refs[%d].Name = %q, want %q", i, ref.Name, tc.wantName[i])
				}
				if ref.Default != tc.wantDef[i] {
					t.Errorf("refs[%d].Default = %q, want %q", i, ref.Default, tc.wantDef[i])
				}
			}
		})
	}
}

func TestMCPParseConfig_Stdio(t *testing.T) {
	t.Parallel()
	raw := `{
		"mcpServers": {
			"my-tool": {
				"command": "/usr/local/bin/mytool",
				"args": ["--verbose"],
				"env": {
					"API_KEY": "${API_KEY}",
					"BASE": "${BASE:-https://default.example.com}"
				}
			}
		}
	}`
	servers, err := ParseMCPConfig(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	s := servers[0]
	if s.Name != "my-tool" {
		t.Errorf("Name = %q", s.Name)
	}
	if s.Transport != MCPTransportStdio {
		t.Errorf("Transport = %q, want stdio", s.Transport)
	}
	if s.Host != "" {
		t.Errorf("Host = %q, want empty for stdio", s.Host)
	}
	if s.URL != "" {
		t.Errorf("URL = %q, want empty for stdio", s.URL)
	}
	if len(s.CredVarRefs) != 2 {
		t.Errorf("CredVarRefs = %v, want 2 entries", s.CredVarRefs)
	}
	// sorted: API_KEY before BASE
	if s.CredVarRefs[0] != "API_KEY" || s.CredVarRefs[1] != "BASE" {
		t.Errorf("CredVarRefs = %v, want [API_KEY BASE]", s.CredVarRefs)
	}
}

func TestMCPParseConfig_HTTP(t *testing.T) {
	t.Parallel()
	raw := `{
		"mcpServers": {
			"remote": {
				"type": "http",
				"url": "https://api.example.com/mcp",
				"headers": {"Authorization": "Bearer ${TOKEN}"}
			}
		}
	}`
	servers, err := ParseMCPConfig(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	s := servers[0]
	if s.Transport != MCPTransportHTTP {
		t.Errorf("Transport = %q, want http", s.Transport)
	}
	if s.Host != "api.example.com" {
		t.Errorf("Host = %q, want api.example.com", s.Host)
	}
	if s.URL != "https://api.example.com/mcp" {
		t.Errorf("URL = %q", s.URL)
	}
	if len(s.CredVarRefs) != 1 || s.CredVarRefs[0] != "TOKEN" {
		t.Errorf("CredVarRefs = %v, want [TOKEN]", s.CredVarRefs)
	}
}

func TestMCPParseConfig_SSE(t *testing.T) {
	t.Parallel()
	raw := `{
		"mcpServers": {
			"events": {
				"type": "sse",
				"url": "https://x.example.com/sse",
				"headers": {"X-Key": "${K}"}
			}
		}
	}`
	servers, err := ParseMCPConfig(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(servers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(servers))
	}
	s := servers[0]
	if s.Transport != MCPTransportSSE {
		t.Errorf("Transport = %q, want sse", s.Transport)
	}
	if s.Host != "x.example.com" {
		t.Errorf("Host = %q, want x.example.com", s.Host)
	}
	if len(s.CredVarRefs) != 1 || s.CredVarRefs[0] != "K" {
		t.Errorf("CredVarRefs = %v, want [K]", s.CredVarRefs)
	}
}

func TestMCPParseConfig_LiteralCred(t *testing.T) {
	t.Parallel()
	// A literal bearer token — no ${} refs — must produce zero CredVarRefs.
	raw := `{
		"mcpServers": {
			"literal": {
				"type": "http",
				"url": "https://api.example.com/mcp",
				"headers": {"Authorization": "Bearer abc123"}
			}
		}
	}`
	servers, err := ParseMCPConfig(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(servers[0].CredVarRefs) != 0 {
		t.Errorf("CredVarRefs = %v, want empty for literal cred", servers[0].CredVarRefs)
	}
}

func TestMCPParseConfig_NoCreds(t *testing.T) {
	t.Parallel()
	raw := `{
		"mcpServers": {
			"local": {
				"command": "/bin/local-tool"
			}
		}
	}`
	servers, err := ParseMCPConfig(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(servers[0].CredVarRefs) != 0 {
		t.Errorf("CredVarRefs = %v, want empty", servers[0].CredVarRefs)
	}
}

func TestMCPParseConfig_MalformedJSON(t *testing.T) {
	t.Parallel()
	_, err := ParseMCPConfig(strings.NewReader(`{not valid json`))
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestMCPParseConfig_EmptyMCPServers(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		`{}`,
		`{"mcpServers": {}}`,
	} {
		raw := raw
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			servers, err := ParseMCPConfig(strings.NewReader(raw))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(servers) != 0 {
				t.Errorf("expected 0 servers, got %d", len(servers))
			}
		})
	}
}

func TestMCPParseConfig_DefaultSyntax(t *testing.T) {
	t.Parallel()
	raw := `{
		"mcpServers": {
			"with-default": {
				"command": "/bin/tool",
				"env": {"BASE_URL": "${BASE_URL:-https://default.example.com}"}
			}
		}
	}`
	servers, err := ParseMCPConfig(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	refs := ParseMCPVarRefs("${BASE_URL:-https://default.example.com}")
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].Name != "BASE_URL" {
		t.Errorf("Name = %q, want BASE_URL", refs[0].Name)
	}
	if refs[0].Default != "https://default.example.com" {
		t.Errorf("Default = %q, want https://default.example.com", refs[0].Default)
	}
	if len(servers[0].CredVarRefs) != 1 || servers[0].CredVarRefs[0] != "BASE_URL" {
		t.Errorf("CredVarRefs = %v", servers[0].CredVarRefs)
	}
}
