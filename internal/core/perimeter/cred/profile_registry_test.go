package cred

// TBD-PD-32: the agent registry is what lets a sandbox name its agent and get
// the right egress allowlist and credential seed back. These tests pin the
// three properties call sites depend on: name resolution is exact, unknown
// names are reported rather than defaulted, and the egress list a caller
// receives cannot be used to corrupt the shared profile value.

import (
	"slices"
	"testing"
)

func TestProfileByName_ResolvesRegisteredAgent(t *testing.T) {
	p, ok := ProfileByName(ClaudeCodeProfileName)
	if !ok {
		t.Fatalf("ProfileByName(%q): not registered", ClaudeCodeProfileName)
	}
	if p.Name != ClaudeCodeProfileName {
		t.Errorf("resolved profile Name = %q, want %q", p.Name, ClaudeCodeProfileName)
	}
	if p.PlaceholderEnvVar != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("resolved profile PlaceholderEnvVar = %q, want CLAUDE_CODE_OAUTH_TOKEN", p.PlaceholderEnvVar)
	}
}

// An unknown name must be reported as unknown. Returning the default instead
// would answer `--agent codex` with Claude Code's credential seed and egress
// allowlist — a silent wrong answer rather than an error the user can see.
func TestProfileByName_UnknownAgentIsNotDefaulted(t *testing.T) {
	p, ok := ProfileByName("no-such-agent")
	if ok {
		t.Fatalf("ProfileByName(%q) = %+v, ok=true; want ok=false", "no-such-agent", p)
	}
	if p.Name != "" || p.PlaceholderEnvVar != "" {
		t.Errorf("unknown agent returned a populated profile %+v; want the zero value", p)
	}
}

func TestProfileNames_ListsEveryRegisteredAgentSorted(t *testing.T) {
	names := ProfileNames()
	if len(names) != len(profiles) {
		t.Fatalf("ProfileNames() returned %d names, want %d (one per registered profile)", len(names), len(profiles))
	}
	if !slices.IsSorted(names) {
		t.Errorf("ProfileNames() = %v, want sorted (it is shown to users in help and error text)", names)
	}
	for _, n := range names {
		if _, ok := profiles[n]; !ok {
			t.Errorf("ProfileNames() returned %q which is not in the registry", n)
		}
	}
}

// Egress must hand back a copy. Callers assign the result straight to
// Envelope.AllowedHosts and to broker host lists; if it aliased the package
// value, one sandbox appending a host would widen the allowlist for every
// sandbox created afterwards in the same process.
func TestEgress_ReturnsIsolatedCopy(t *testing.T) {
	first := ClaudeCodeProfile.Egress()
	if len(first) == 0 {
		t.Fatal("Egress() returned no hosts for ClaudeCodeProfile")
	}
	first[0] = "evil.example.com"
	first = append(first, "also-evil.example.com")

	second := ClaudeCodeProfile.Egress()
	if slices.Contains(second, "evil.example.com") || slices.Contains(second, "also-evil.example.com") {
		t.Errorf("mutating one Egress() result leaked into the next: %v", second)
	}
}

// Every registered profile must be able to reach the host its token
// authenticates to. A profile whose CredentialedHost is outside its own egress
// allowlist would seed a placeholder for a host the perimeter blocks — the
// agent would fail at runtime with a network error, not a credential error.
func TestRegisteredProfiles_CredentialedHostIsReachable(t *testing.T) {
	for name, p := range profiles {
		if p.Name != name {
			t.Errorf("profile registered under %q has Name %q; the key and the Name must agree", name, p.Name)
		}
		if p.CredentialedHost == "" {
			t.Errorf("profile %q has no CredentialedHost", name)
			continue
		}
		if !slices.Contains(p.EgressHosts, p.CredentialedHost) {
			t.Errorf("profile %q: CredentialedHost %q is not in EgressHosts %v", name, p.CredentialedHost, p.EgressHosts)
		}
	}
}
