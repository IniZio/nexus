package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
)

// TestBuildGuestMCPServers_MountMappedCommandRewritten drives the sandbox
// create call site from real config files: mount-mapped stdio commands are
// rewritten to the guest path, host-only absolute commands are dropped.
func TestBuildGuestMCPServers_MountMappedCommandRewritten(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))

	binDir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgDir := filepath.Join(home, ".config", "nexus")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"),
		[]byte("version: 1\nsandbox:\n  mounts:\n    - \"~/.local/bin:/root/.local/bin\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	claudeJSON, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"tool":     map[string]any{"command": filepath.Join(binDir, "tool"), "args": []string{"serve"}},
		"hostonly": map[string]any{"command": "/opt/host-only/bin/server"},
		"npx-one":  map[string]any{"command": "npx", "args": []string{"-y", "x"}},
	}})
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), claudeJSON, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := buildGuestMCPServers(sandboxCreateFlags{}, cred.ClaudeCodeProfile, "")
	if err != nil {
		t.Fatalf("buildGuestMCPServers: %v", err)
	}
	var tool struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal(got.Servers["tool"], &tool); err != nil {
		t.Fatalf("tool entry missing or malformed: %s", got.Servers["tool"])
	}
	if tool.Command != "/root/.local/bin/tool" {
		t.Errorf("command = %q, want /root/.local/bin/tool", tool.Command)
	}
	if len(tool.Args) != 1 || tool.Args[0] != "serve" {
		t.Errorf("args = %v, want [serve]", tool.Args)
	}
	if _, ok := got.Servers["hostonly"]; ok {
		t.Error("host-only absolute command must be dropped")
	}
	if _, ok := got.Servers["npx-one"]; !ok {
		t.Error("PATH-relative command must be kept")
	}

	off, err := buildGuestMCPServers(sandboxCreateFlags{noUserMounts: true}, cred.ClaudeCodeProfile, "")
	if err != nil {
		t.Fatalf("buildGuestMCPServers --no-user-mounts: %v", err)
	}
	if _, ok := off.Servers["tool"]; ok {
		t.Error("without user mounts the mount-mapped command has no guest path and must be dropped")
	}
}
