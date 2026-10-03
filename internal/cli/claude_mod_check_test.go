package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestClaudeModVersionMatchesManifest(t *testing.T) {
	raw, err := os.ReadFile("../../plugins/nexus-subagent/.claude-plugin/plugin.json")
	if err != nil {
		t.Fatal(err)
	}
	var m struct{ Version string }
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if m.Version != claudeModVersion {
		t.Fatalf("claudeModVersion=%s but plugin.json=%s", claudeModVersion, m.Version)
	}
}

func TestClaudeModCheck(t *testing.T) {
	cases := []struct {
		name   string
		st     claudeModState
		ok     bool
		detail string
	}{
		{"claude absent", claudeModState{}, false, "not found"},
		{"mod missing", claudeModState{ClaudeVersion: "2.1.288 (Claude Code)"}, false, "not installed"},
		{"claude too old", claudeModState{ClaudeVersion: "2.1.100", ModInstalled: true, ModVersion: claudeModVersion}, false, "older than minimum"},
		{"mod stale", claudeModState{ClaudeVersion: "2.2.0", ModInstalled: true, ModVersion: "0.1.0"}, false, "differs"},
		{"mod version unknown", claudeModState{ClaudeVersion: "2.2.0", ModInstalled: true}, false, "version unknown"},
		{"good", claudeModState{ClaudeVersion: "2.1.288", ModInstalled: true, ModVersion: claudeModVersion}, true, "installed"},
	}
	for _, c := range cases {
		cr := checkClaudeMod(func() claudeModState { return c.st })
		if !cr.Optional || cr.OK != c.ok || !strings.Contains(cr.Detail, c.detail) {
			t.Errorf("%s: got %+v", c.name, cr)
		}
	}
}
