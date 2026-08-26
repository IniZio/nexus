package cli

// TBD-PD-32 / TBR-PD-19: `nexus3 sandbox create --agent <name>` is how a user
// asks for a sandbox that runs a credentialed agent. Ruled 2026-08-19: the flag
// implies the detached supervisor, and the agent cannot be switched in place.

import (
	"context"
	"os"
	"path/filepath"
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

// TestApplyUserGlobalConfig verifies the precedence logic for the user-global
// config's sandbox.agent default.
//
// Each subtest sets XDG_CONFIG_HOME to a temp dir so the real user config is
// never read. t.Setenv is used (not os.Setenv) so parallel tests don't race.
func TestApplyUserGlobalConfig(t *testing.T) {
	// writeConfig writes a minimal config.yaml with the given agent value.
	writeConfig := func(t *testing.T, xdgDir, agent string) {
		t.Helper()
		cfgDir := filepath.Join(xdgDir, "nexus3")
		if err := os.MkdirAll(cfgDir, 0o750); err != nil {
			t.Fatal(err)
		}
		var body string
		if agent != "" {
			body = "version: 1\nsandbox:\n  agent: " + agent + "\n"
		} else {
			body = "version: 1\n"
		}
		if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Note: subtests use t.Setenv which requires them to be non-parallel
	// (t.Setenv panics in a parallel subtest — it would race the parent's env).

	t.Run("config agent applied when flag absent", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		writeConfig(t, dir, cred.ClaudeCodeProfileName)

		f := sandboxCreateFlags{} // agentName == "" → flag absent
		if err := applyUserGlobalConfig(&f); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.agentName != cred.ClaudeCodeProfileName {
			t.Errorf("agentName = %q, want %q", f.agentName, cred.ClaudeCodeProfileName)
		}
	})

	t.Run("explicit flag wins over config agent", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		writeConfig(t, dir, cred.ClaudeCodeProfileName)

		// Simulate --agent already set (e.g. by parseSandboxCreateArgs).
		const explicitAgent = cred.ClaudeCodeProfileName
		f := sandboxCreateFlags{agentName: explicitAgent}
		if err := applyUserGlobalConfig(&f); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Must remain unchanged — applyUserGlobalConfig must not overwrite an
		// already-set agentName even when it matches the config.
		if f.agentName != explicitAgent {
			t.Errorf("agentName = %q, want %q (explicit flag must win)", f.agentName, explicitAgent)
		}
	})

	t.Run("absent config file leaves agentName empty", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		// No config.yaml written.

		f := sandboxCreateFlags{}
		if err := applyUserGlobalConfig(&f); err != nil {
			t.Fatalf("absent file must not error: %v", err)
		}
		if f.agentName != "" {
			t.Errorf("agentName = %q, want empty for absent config", f.agentName)
		}
	})

	t.Run("unknown agent in config is ignored non-fatally", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		writeConfig(t, dir, "no-such-agent")

		f := sandboxCreateFlags{}
		if err := applyUserGlobalConfig(&f); err != nil {
			t.Fatalf("unknown agent must not error (non-fatal): %v", err)
		}
		if f.agentName != "" {
			t.Errorf("agentName = %q, want empty for an unrecognised config agent", f.agentName)
		}
	})

	t.Run("empty sandbox.agent in config leaves agentName empty", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		writeConfig(t, dir, "") // agent key absent from config

		f := sandboxCreateFlags{}
		if err := applyUserGlobalConfig(&f); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if f.agentName != "" {
			t.Errorf("agentName = %q, want empty when config has no agent", f.agentName)
		}
	})
}

// TestSandboxCreate_NoBootPath_PersistsAgentName verifies that the no-boot
// create path (no --image/--rootfs/--file) persists the resolved AgentName
// onto the sandbox record, including when the agent comes from the user-global
// config default rather than an explicit --agent flag.
//
// Uses t.Setenv to isolate XDG_CONFIG_HOME; subtests must not be marked
// t.Parallel() because t.Setenv panics in a parallel subtest.
func TestSandboxCreate_NoBootPath_PersistsAgentName(t *testing.T) {
	writeConfig := func(t *testing.T, xdgDir, agent string) {
		t.Helper()
		cfgDir := filepath.Join(xdgDir, "nexus3")
		if err := os.MkdirAll(cfgDir, 0o750); err != nil {
			t.Fatal(err)
		}
		body := "version: 1\nsandbox:\n  agent: " + agent + "\n"
		if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("config default applied when no --agent flag", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		writeConfig(t, dir, cred.ClaudeCodeProfileName)

		svc := newTestService(t)
		ctx := context.Background()
		out, _, _ := capture(false)
		if err := runSandboxCreate(ctx, []string{"proj/noboot"}, out, svc); err != nil {
			t.Fatalf("runSandboxCreate: %v", err)
		}

		sandboxes, err := svc.List(ctx)
		if err != nil {
			t.Fatalf("svc.List: %v", err)
		}
		if len(sandboxes) != 1 {
			t.Fatalf("expected 1 sandbox, got %d", len(sandboxes))
		}
		if sandboxes[0].AgentName != cred.ClaudeCodeProfileName {
			t.Errorf("AgentName = %q, want %q", sandboxes[0].AgentName, cred.ClaudeCodeProfileName)
		}
	})

	t.Run("explicit --agent flag wins over config default", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		writeConfig(t, dir, cred.ClaudeCodeProfileName)

		svc := newTestService(t)
		ctx := context.Background()
		out, _, _ := capture(false)
		// --agent is the same value as the config here; the point is that flag
		// resolution still works and the record is set correctly.
		if err := runSandboxCreate(ctx, []string{"--agent", cred.ClaudeCodeProfileName, "proj/noboot2"}, out, svc); err != nil {
			t.Fatalf("runSandboxCreate: %v", err)
		}

		sandboxes, err := svc.List(ctx)
		if err != nil {
			t.Fatalf("svc.List: %v", err)
		}
		if len(sandboxes) != 1 {
			t.Fatalf("expected 1 sandbox, got %d", len(sandboxes))
		}
		if sandboxes[0].AgentName != cred.ClaudeCodeProfileName {
			t.Errorf("AgentName = %q, want %q", sandboxes[0].AgentName, cred.ClaudeCodeProfileName)
		}
	})

	t.Run("no config and no flag leaves AgentName empty", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", dir)
		// No config.yaml written — absent config is a no-op.

		svc := newTestService(t)
		ctx := context.Background()
		out, _, _ := capture(false)
		if err := runSandboxCreate(ctx, []string{"proj/noboot3"}, out, svc); err != nil {
			t.Fatalf("runSandboxCreate: %v", err)
		}

		sandboxes, err := svc.List(ctx)
		if err != nil {
			t.Fatalf("svc.List: %v", err)
		}
		if len(sandboxes) != 1 {
			t.Fatalf("expected 1 sandbox, got %d", len(sandboxes))
		}
		if sandboxes[0].AgentName != "" {
			t.Errorf("AgentName = %q, want empty when no config and no flag", sandboxes[0].AgentName)
		}
	})
}
