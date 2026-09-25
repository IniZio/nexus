package service

import (
	"reflect"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/perimeter/mitm"
)

// TestBuildMCPPolicies verifies that buildMCPPolicies converts domain.EgressMCPPolicies
// to mitm.MCPPolicy field-for-field and handles nil/empty input gracefully.
//
// Note: the SecretHosts append (service.go ~1004-1008) is inside startSupervisor,
// which requires a full VM driver harness. No lightweight test seam currently exists
// for that code path; the mitm.Config construction is not separately factored.
// Asserting buildMCPPolicies output is the best non-invasive coverage available.
func TestBuildMCPPolicies(t *testing.T) {
	t.Run("nil input returns nil", func(t *testing.T) {
		got := buildMCPPolicies(nil)
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("empty input returns nil", func(t *testing.T) {
		got := buildMCPPolicies(domain.EgressMCPPolicies{})
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})

	t.Run("path allow and args copied field-for-field", func(t *testing.T) {
		in := domain.EgressMCPPolicies{
			"mcp.example.com": {
				Path:  "/sse",
				Allow: []string{"read_file", "list_dir"},
				Args: map[string]map[string]string{
					"read_file": {"path": "/workspace/**"},
				},
			},
		}
		want := map[string]mitm.MCPPolicy{
			"mcp.example.com": {
				Path:  "/sse",
				Allow: []string{"read_file", "list_dir"},
				Args: map[string]map[string]string{
					"read_file": {"path": "/workspace/**"},
				},
			},
		}
		got := buildMCPPolicies(in)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("mismatch:\n  want %#v\n   got %#v", want, got)
		}
	})

	t.Run("multiple hosts all present in output", func(t *testing.T) {
		in := domain.EgressMCPPolicies{
			"alpha.example.com": {Allow: []string{"tool_a"}},
			"beta.example.com":  {Allow: []string{"tool_b"}},
		}
		got := buildMCPPolicies(in)
		if len(got) != 2 {
			t.Fatalf("want 2 hosts, got %d: %v", len(got), got)
		}
		for host, pol := range in {
			gp, ok := got[host]
			if !ok {
				t.Errorf("host %q missing from output", host)
				continue
			}
			want := mitm.MCPPolicy{Path: pol.Path, Allow: pol.Allow, Args: pol.Args}
			if !reflect.DeepEqual(gp, want) {
				t.Errorf("host %q: want %#v, got %#v", host, want, gp)
			}
		}
	})

	t.Run("nil args field stays nil", func(t *testing.T) {
		in := domain.EgressMCPPolicies{
			"tools.example.com": {Allow: []string{"ping"}, Args: nil},
		}
		got := buildMCPPolicies(in)
		if got["tools.example.com"].Args != nil {
			t.Errorf("expected nil Args, got %v", got["tools.example.com"].Args)
		}
	})
}
