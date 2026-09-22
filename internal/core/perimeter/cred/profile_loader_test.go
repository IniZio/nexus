package cred

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

func resetProfilesForTest(t *testing.T) {
	t.Helper()
	profilesOnce = sync.Once{}
	profiles = map[string]AgentProfile{}
	t.Cleanup(func() {
		profilesOnce = sync.Once{}
		profiles = map[string]AgentProfile{}
	})
}

func TestLoadProfile_SyntheticProfile(t *testing.T) {
	synthetic := AgentProfile{
		Name:              "test-synthetic-agent",
		CredentialedHost:  "api.test-synthetic.example.com",
		EgressHosts:       []string{"api.test-synthetic.example.com"},
		PlaceholderEnvVar: "TEST_SYNTHETIC_TOKEN",
		APIKeyEnvVar:      "TEST_SYNTHETIC_API_KEY",
		MCPConfigFormat:   MCPConfigFormatClaudeJSON,
	}
	data, err := json.Marshal(synthetic)
	if err != nil {
		t.Fatalf("marshal synthetic profile: %v", err)
	}

	fsys := fstest.MapFS{
		"profiles/test-synthetic-agent.json": &fstest.MapFile{Data: data},
	}
	loaded, err := loadProfilesFromFS(fsys, "profiles")
	if err != nil {
		t.Fatalf("loadProfilesFromFS: %v", err)
	}

	p, ok := loaded["test-synthetic-agent"]
	if !ok {
		t.Fatalf("synthetic profile not found in loaded map")
	}
	if p.Name != "test-synthetic-agent" {
		t.Errorf("Name = %q, want test-synthetic-agent", p.Name)
	}
	if p.CredentialedHost != "api.test-synthetic.example.com" {
		t.Errorf("CredentialedHost = %q, want api.test-synthetic.example.com", p.CredentialedHost)
	}
	if p.PlaceholderEnvVar != "TEST_SYNTHETIC_TOKEN" {
		t.Errorf("PlaceholderEnvVar = %q, want TEST_SYNTHETIC_TOKEN", p.PlaceholderEnvVar)
	}

	unregister, err := RegisterProfileForTest(p)
	if err != nil {
		t.Fatalf("RegisterProfileForTest: %v", err)
	}
	t.Cleanup(unregister)

	resolved, ok := ProfileByName("test-synthetic-agent")
	if !ok {
		t.Fatalf("ProfileByName(%q) = false; want true", "test-synthetic-agent")
	}
	if resolved.CredentialedHost != p.CredentialedHost {
		t.Errorf("resolved CredentialedHost = %q, want %q", resolved.CredentialedHost, p.CredentialedHost)
	}
}

func TestDecodeProfile_Rejection(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		wantSub string
	}{
		{
			name:    "unknown field",
			doc:     `{"Name":"x","UnknownField":"bad","CredentialedHost":"h.example.com","EgressHosts":["h.example.com"]}`,
			wantSub: "UnknownField",
		},
		{
			name:    "missing Name",
			doc:     `{"CredentialedHost":"h.example.com","EgressHosts":["h.example.com"]}`,
			wantSub: "Name is required",
		},
		{
			name:    "malformed JSON",
			doc:     `{"Name": "x", "EgressHosts": [1, 2, 3}`,
			wantSub: "test-doc.json",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeProfile([]byte(tc.doc), "test-doc.json")
			if err == nil {
				t.Fatalf("expected error for %q, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestEmbeddedProfiles_NoCredentialMaterial(t *testing.T) {
	suspiciousPatterns := []string{
		"sk-ant-",
		"Bearer ",
		"eyJ",
	}

	entries, err := builtinProfileFS.ReadDir("profiles")
	if err != nil {
		t.Fatalf("read embedded profiles: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := builtinProfileFS.ReadFile("profiles/" + e.Name())
		if err != nil {
			t.Fatalf("read %q: %v", e.Name(), err)
		}
		for _, pat := range suspiciousPatterns {
			if bytes.Contains(data, []byte(pat)) {
				t.Errorf("embedded profile %q contains suspicious pattern %q", e.Name(), pat)
			}
		}
	}
}

func TestLoadProfilesFromFS_AllSixBuiltins(t *testing.T) {
	loaded, err := loadProfilesFromFS(builtinProfileFS, "profiles")
	if err != nil {
		t.Fatalf("loadProfilesFromFS built-ins: %v", err)
	}
	wantNames := []string{"claude-code", "codex", "cursor", "kiro", "oh-my-pi", "opencode"}
	for _, name := range wantNames {
		p, ok := loaded[name]
		if !ok {
			t.Errorf("built-in profile %q not loaded", name)
			continue
		}
		if p.Name != name {
			t.Errorf("profile[%q].Name = %q, want %q", name, p.Name, name)
		}
		if p.CredentialedHost == "" {
			t.Errorf("profile[%q].CredentialedHost is empty", name)
		}
	}
}

func TestLoadProfilesFromDir_WarnOnMalformed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nexus", "profiles")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	valid := AgentProfile{
		Name:             "test-warn-valid",
		CredentialedHost: "api.warn-valid.example.com",
		EgressHosts:      []string{"api.warn-valid.example.com"},
	}
	data, _ := json.Marshal(valid)
	if err := os.WriteFile(filepath.Join(dir, "valid.json"), data, 0o644); err != nil {
		t.Fatalf("write valid: %v", err)
	}

	bad := []byte(`{"Name":"claude-code","UnknownField":"oops","CredentialedHost":"h","EgressHosts":["h"]}`)
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), bad, 0o644); err != nil {
		t.Fatalf("write bad: %v", err)
	}

	var buf bytes.Buffer
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(oldDefault) })

	xdg := filepath.Dir(filepath.Dir(dir))
	t.Setenv("XDG_CONFIG_HOME", xdg)
	resetProfilesForTest(t)

	p, ok := ProfileByName("test-warn-valid")
	if !ok {
		t.Fatalf("valid override profile not resolvable via ProfileByName")
	}
	if p.CredentialedHost != "api.warn-valid.example.com" {
		t.Errorf("CredentialedHost = %q, want api.warn-valid.example.com", p.CredentialedHost)
	}

	builtin, ok := ProfileByName(ClaudeCodeProfileName)
	if !ok {
		t.Fatalf("built-in claude-code not resolvable after malformed override")
	}
	if builtin.CredentialedHost != "api.anthropic.com" {
		t.Errorf("built-in claude-code overwritten by malformed doc; CredentialedHost = %q", builtin.CredentialedHost)
	}

	if !strings.Contains(buf.String(), "bad.json") {
		t.Errorf("expected slog.Warn mentioning bad.json; got: %s", buf.String())
	}
}
