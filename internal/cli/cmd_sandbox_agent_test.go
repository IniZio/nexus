package cli

// TBD-PD-32 / TBR-PD-19: `nexus3 sandbox create --agent <name>` is how a user
// asks for a sandbox that runs a credentialed agent. Ruled 2026-08-19: the flag
// implies the detached supervisor, and the agent cannot be switched in place.

import (
	"slices"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

func TestParseSandboxCreate_Agent(t *testing.T) {
	t.Parallel()

	t.Run("known agent is accepted", func(t *testing.T) {
		t.Parallel()
		f, err := parseSandboxCreateArgs([]string{"--agent", cred.ClaudeCodeProfileName, "proj/name"})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if f.agentName != cred.ClaudeCodeProfileName {
			t.Errorf("agentName = %q, want %q", f.agentName, cred.ClaudeCodeProfileName)
		}
	})

	// An unknown name must be refused, not defaulted. Defaulting would answer
	// --agent codex with Claude Code's credentials and egress allowlist.
	t.Run("unknown agent is refused and lists the known ones", func(t *testing.T) {
		t.Parallel()
		_, err := parseSandboxCreateArgs([]string{"--agent", "codex", "proj/name"})
		if err == nil {
			t.Fatal("expected an error for an unknown agent name, got nil")
		}
		if !strings.Contains(err.Error(), cred.ClaudeCodeProfileName) {
			t.Errorf("error should list the known agents so the user can correct the typo; got %q", err)
		}
	})

	t.Run("missing value is refused", func(t *testing.T) {
		t.Parallel()
		if _, err := parseSandboxCreateArgs([]string{"--agent"}); err == nil {
			t.Fatal("expected an error for --agent with no value, got nil")
		}
	})

	// D-PD-33 (updated — dev-egress posture): --agent + --egress open is now
	// PERMITTED. The MITM proxy intercepts SecretHosts (the agent's
	// CredentialedHost) regardless of AllowAll, so the placeholder→real swap
	// fires correctly even with broad-allow egress.
	t.Run("explicit open egress is allowed", func(t *testing.T) {
		t.Parallel()
		_, err := parseSandboxCreateArgs([]string{"--agent", cred.ClaudeCodeProfileName, "--egress", "open", "proj/name"})
		if err != nil {
			t.Fatalf("expected --agent with --egress open to be accepted (dev-egress posture), got: %v", err)
		}
	})

	t.Run("default egress is not refused", func(t *testing.T) {
		t.Parallel()
		f, err := parseSandboxCreateArgs([]string{"--agent", cred.ClaudeCodeProfileName, "proj/name"})
		if err != nil {
			t.Fatalf("--agent without --egress must be accepted: %v", err)
		}
		if f.egressExplicit {
			t.Error("egressExplicit must stay false when --egress was not passed")
		}
	})

	// Without --agent nothing changes: no agent recorded, and the default
	// stays open egress.
	t.Run("absent flag records no agent", func(t *testing.T) {
		t.Parallel()
		f, err := parseSandboxCreateArgs([]string{"proj/name"})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if f.agentName != "" {
			t.Errorf("agentName = %q, want empty", f.agentName)
		}
		if f.egressClosed {
			t.Error("egress must default to open for a plain sandbox")
		}
	})
}

func TestResolveAgentPosture(t *testing.T) {
	t.Parallel()

	t.Run("agent freezes its own hosts and closes egress", func(t *testing.T) {
		t.Parallel()
		profile, hosts, openEgress := resolveAgentPosture(sandboxCreateFlags{
			agentName:  cred.ClaudeCodeProfileName,
			allowHosts: []string{"registry.npmjs.org"},
		})

		if profile.Name != cred.ClaudeCodeProfileName {
			t.Errorf("profile.Name = %q, want %q", profile.Name, cred.ClaudeCodeProfileName)
		}
		// D-PD-33: the DEFAULT (no --egress flag, egressExplicit=false) still
		// closes egress for an agent sandbox. Open egress requires an explicit
		// --egress open flag (dev-egress posture).
		if openEgress {
			t.Error("an agent sandbox without --egress open must default to closed egress")
		}
		for _, want := range profile.Egress() {
			if !slices.Contains(hosts, want) {
				t.Errorf("allowlist %v is missing the agent's own host %q", hosts, want)
			}
		}
		// --allow-host must survive: an agent still needs its task's registries.
		if !slices.Contains(hosts, "registry.npmjs.org") {
			t.Errorf("allowlist %v dropped the user's --allow-host entry", hosts)
		}
	})

	t.Run("no agent leaves the flags untouched", func(t *testing.T) {
		t.Parallel()
		profile, hosts, openEgress := resolveAgentPosture(sandboxCreateFlags{
			allowHosts: []string{"example.com"},
		})
		if profile.Name != "" {
			t.Errorf("profile.Name = %q, want empty for a sandbox with no agent", profile.Name)
		}
		if !openEgress {
			t.Error("a plain sandbox defaults to open egress")
		}
		if !slices.Equal(hosts, []string{"example.com"}) {
			t.Errorf("allowlist = %v, want the --allow-host list verbatim", hosts)
		}
	})

	t.Run("no agent honours --egress closed", func(t *testing.T) {
		t.Parallel()
		if _, _, openEgress := resolveAgentPosture(sandboxCreateFlags{egressClosed: true}); openEgress {
			t.Error("--egress closed must close egress")
		}
	})
}
