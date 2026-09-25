package cli

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/IniZio/nexus/internal/core/config"
	"github.com/IniZio/nexus/internal/core/domain"
)

// TestBuildWorktreeEgressArgs_MCPPolicies checks that egress.mcp config entries
// flow through buildWorktreeEgressArgs into the returned mcpPolicies map.
func TestBuildWorktreeEgressArgs_MCPPolicies(t *testing.T) {
	cfg := config.Config{}
	cfg.Egress.MCP = config.EgressMCPs{
		{
			Host:  "mcp.example.com",
			Path:  "/sse",
			Allow: []string{"read_file", "list_dir"},
			Args:  map[string]map[string]string{"read_file": {"path": "/workspace/**"}},
		},
	}
	_, _, _, mcpPolicies, err := buildWorktreeEgressArgs(cfg)
	if err != nil {
		t.Fatalf("buildWorktreeEgressArgs: %v", err)
	}
	want := domain.EgressMCPPolicies{
		"mcp.example.com": {
			Path:  "/sse",
			Allow: []string{"read_file", "list_dir"},
			Args:  map[string]map[string]string{"read_file": {"path": "/workspace/**"}},
		},
	}
	if !reflect.DeepEqual(mcpPolicies, want) {
		t.Errorf("mcpPolicies mismatch:\n  want %#v\n   got %#v", want, mcpPolicies)
	}
}

// TestHerdrWorktreeSandboxCreateArgs_MCPPolicies round-trips domain.EgressMCPPolicies
// through herdrWorktreeSandboxCreateArgs → --egress-mcp-json → parseSandboxCreateArgs.
func TestHerdrWorktreeSandboxCreateArgs_MCPPolicies(t *testing.T) {
	mp := domain.EgressMCPPolicies{
		"mcp.example.com": {
			Path:  "/sse",
			Allow: []string{"read_file", "list_dir"},
			Args:  map[string]map[string]string{"read_file": {"path": "/workspace/**"}},
		},
	}

	t.Run("non-empty mcpPolicies emits --egress-mcp-json", func(t *testing.T) {
		args := herdrWorktreeSandboxCreateArgs("owner/branch", "src:dst", "--image", "myimage", nil, nil, "", nil, mp, false)
		var jsonVal string
		for i, a := range args {
			if a == "--egress-mcp-json" && i+1 < len(args) {
				jsonVal = args[i+1]
				break
			}
		}
		if jsonVal == "" {
			t.Fatalf("--egress-mcp-json missing or has no value; args: %v", args)
		}
		var decoded domain.EgressMCPPolicies
		if err := json.Unmarshal([]byte(jsonVal), &decoded); err != nil {
			t.Fatalf("JSON decode of --egress-mcp-json: %v", err)
		}
		if !reflect.DeepEqual(mp, decoded) {
			t.Errorf("round-trip mismatch:\n  want %#v\n   got %#v", mp, decoded)
		}
	})

	t.Run("nil mcpPolicies omits --egress-mcp-json", func(t *testing.T) {
		args := herdrWorktreeSandboxCreateArgs("owner/branch", "src:dst", "--image", "myimage", nil, nil, "", nil, nil, false)
		for _, a := range args {
			if a == "--egress-mcp-json" {
				t.Errorf("unexpected --egress-mcp-json in args with nil mcpPolicies: %v", args)
			}
		}
	})

	t.Run("parseSandboxCreateArgs restores mcpPolicies", func(t *testing.T) {
		args := herdrWorktreeSandboxCreateArgs("owner/branch", "src:dst", "--image", "myimage", nil, nil, "", nil, mp, false)
		f, err := parseSandboxCreateArgs(args)
		if err != nil {
			t.Fatalf("parseSandboxCreateArgs: %v", err)
		}
		if !reflect.DeepEqual(f.mcpPolicies, mp) {
			t.Errorf("--egress-mcp-json did not survive parse:\n  want %#v\n   got %#v", mp, f.mcpPolicies)
		}
	})
}
