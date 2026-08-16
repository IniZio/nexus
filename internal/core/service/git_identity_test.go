package service

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// ── Host identity resolution ──────────────────────────────────────────────────

// withFakeHostGitConfig replaces hostGitConfigGet with a stub that returns
// the provided name and email, runs f, then restores the original. This
// allows deterministic testing of HostGitIdentity without depending on the
// test host's global git config.
func withFakeHostGitConfig(name, email string, f func()) {
	orig := hostGitConfigGet
	defer func() { hostGitConfigGet = orig }()
	hostGitConfigGet = func(key string) (string, error) {
		switch key {
		case "user.name":
			return name, nil
		case "user.email":
			return email, nil
		default:
			return "", nil
		}
	}
	f()
}

func TestHostGitIdentity_ReturnsNameAndEmail(t *testing.T) {
	withFakeHostGitConfig("Alice Operator", "alice@example.com", func() {
		name, email, err := HostGitIdentity()
		if err != nil {
			t.Fatalf("HostGitIdentity: %v", err)
		}
		if name != "Alice Operator" {
			t.Errorf("name: want %q, got %q", "Alice Operator", name)
		}
		if email != "alice@example.com" {
			t.Errorf("email: want %q, got %q", "alice@example.com", email)
		}
	})
}

func TestHostGitIdentity_MissingName(t *testing.T) {
	withFakeHostGitConfig("", "alice@example.com", func() {
		_, _, err := HostGitIdentity()
		if err == nil {
			t.Fatal("expected error when user.name is not configured")
		}
		if !strings.Contains(err.Error(), "user.name") {
			t.Errorf("error should mention 'user.name'; got: %v", err)
		}
		if !strings.Contains(err.Error(), "git config --global") {
			t.Errorf("error should contain the fix command; got: %v", err)
		}
	})
}

func TestHostGitIdentity_MissingEmail(t *testing.T) {
	withFakeHostGitConfig("Alice Operator", "", func() {
		_, _, err := HostGitIdentity()
		if err == nil {
			t.Fatal("expected error when user.email is not configured")
		}
		if !strings.Contains(err.Error(), "user.email") {
			t.Errorf("error should mention 'user.email'; got: %v", err)
		}
		if !strings.Contains(err.Error(), "git config --global") {
			t.Errorf("error should contain the fix command; got: %v", err)
		}
	})
}

func TestHostGitIdentity_MissingBoth(t *testing.T) {
	withFakeHostGitConfig("", "", func() {
		_, _, err := HostGitIdentity()
		if err == nil {
			t.Fatal("expected error when both user.name and user.email are not configured")
		}
		// Error must mention user.name (checked first).
		if !strings.Contains(err.Error(), "user.name") {
			t.Errorf("error should mention 'user.name'; got: %v", err)
		}
	})
}

// ── SandboxBranchName — format, determinism, defaults ────────────────────────

func TestSandboxBranchName_Format(t *testing.T) {
	id := domain.NewSandboxID()
	labels := map[string]string{"motive": "my-feature-1"}
	branch := SandboxBranchName(labels, id)

	if !strings.HasPrefix(branch, "nexus3/") {
		t.Errorf("branch does not start with 'nexus3/': %q", branch)
	}
	parts := strings.SplitN(branch, "/", 3)
	if len(parts) != 3 {
		t.Fatalf("branch does not have 3 slash-separated segments: %q", branch)
	}
	if parts[1] != "my-feature-1" {
		t.Errorf("branch motive segment: want %q, got %q", "my-feature-1", parts[1])
	}
}

func TestSandboxBranchName_Deterministic(t *testing.T) {
	id := domain.NewSandboxID()
	labels := map[string]string{"motive": "test-motive"}
	a := SandboxBranchName(labels, id)
	b := SandboxBranchName(labels, id)
	if a != b {
		t.Errorf("SandboxBranchName is not deterministic: %q vs %q", a, b)
	}
}

func TestSandboxBranchName_DefaultSlug(t *testing.T) {
	id := domain.NewSandboxID()
	branch := SandboxBranchName(nil, id)
	if !strings.HasPrefix(branch, "nexus3/default/") {
		t.Errorf("branch without motive label should use 'default' slug: %q", branch)
	}
}

func TestSandboxBranchName_DifferentIDsDistinct(t *testing.T) {
	labels := map[string]string{"motive": "same-motive"}
	a := SandboxBranchName(labels, domain.NewSandboxID())
	b := SandboxBranchName(labels, domain.NewSandboxID())
	if a == b {
		t.Errorf("SandboxBranchName returned the same branch for two different IDs: %q", a)
	}
}

// ── SeedGitIdentity payload ───────────────────────────────────────────────────

func TestSeedGitIdentity_Payload(t *testing.T) {
	const (
		wantName  = "Test Operator"
		wantEmail = "operator@example.com"
	)

	withFakeHostGitConfig(wantName, wantEmail, func() {
		id := domain.NewSandboxID()
		labels := map[string]string{"motive": "slice-g1"}
		const workspacePath = "/workspace/myrepo"

		var captured []byte
		seeder := GuestSeeder(func(_ context.Context, _ domain.SandboxID, payload []byte) error {
			captured = payload
			return nil
		})

		branch, err := SeedGitIdentity(context.Background(), id, labels, workspacePath, seeder)
		if err != nil {
			t.Fatalf("SeedGitIdentity: %v", err)
		}

		payload := string(captured)

		// Must contain the operator's real identity.
		if !strings.Contains(payload, wantName) {
			t.Errorf("gitconfig payload missing user.name %q; got:\n%s", wantName, payload)
		}
		if !strings.Contains(payload, wantEmail) {
			t.Errorf("gitconfig payload missing user.email %q; got:\n%s", wantEmail, payload)
		}
		// Must NOT contain any bot-pattern identity.
		for _, forbidden := range []string{"nexus3-bot", "noreply.nexus3"} {
			if strings.Contains(payload, forbidden) {
				t.Errorf("gitconfig payload contains bot-pattern string %q (D-PD-02 reversed); payload:\n%s", forbidden, payload)
			}
		}
		// Must contain safe.directory.
		if !strings.Contains(payload, workspacePath) {
			t.Errorf("gitconfig payload missing safe.directory %q; got:\n%s", workspacePath, payload)
		}
		// Must contain the branch name.
		if !strings.Contains(payload, branch) {
			t.Errorf("gitconfig payload missing branch %q; got:\n%s", branch, payload)
		}
		// Must not contain any operator credential or GitHub indicator.
		for _, forbidden := range []string{"github.com", "ghp_", "gho_", "gh auth", "PAT"} {
			if strings.Contains(strings.ToLower(payload), strings.ToLower(forbidden)) {
				t.Errorf("gitconfig payload contains forbidden string %q\npayload:\n%s", forbidden, payload)
			}
		}
	})
}

func TestSeedGitIdentity_NilSeederIsNoop(t *testing.T) {
	// Nil seeder must never resolve host identity (no git config lookup).
	// Override to return empty to prove the nil-seeder path is truly a no-op.
	orig := hostGitConfigGet
	defer func() { hostGitConfigGet = orig }()
	hostGitConfigGet = func(key string) (string, error) {
		t.Errorf("hostGitConfigGet called even though seeder is nil (should be a no-op)")
		return "", nil
	}

	id := domain.NewSandboxID()
	labels := map[string]string{"motive": "test"}
	branch, err := SeedGitIdentity(context.Background(), id, labels, "", nil)
	if err != nil {
		t.Errorf("SeedGitIdentity with nil seeder should be a no-op, got error: %v", err)
	}
	if branch == "" {
		t.Error("SeedGitIdentity with nil seeder should still return a branch name")
	}
}

func TestSeedGitIdentity_MissingHostConfig_FailsCreate(t *testing.T) {
	// When host git identity is not configured, SeedGitIdentity must return
	// an actionable error rather than silently falling back to a bot identity.
	withFakeHostGitConfig("", "", func() {
		id := domain.NewSandboxID()
		var called bool
		seeder := GuestSeeder(func(_ context.Context, _ domain.SandboxID, _ []byte) error {
			called = true
			return nil
		})
		_, err := SeedGitIdentity(context.Background(), id, nil, "", seeder)
		if err == nil {
			t.Fatal("SeedGitIdentity should fail when host git identity is not configured")
		}
		if called {
			t.Error("seeder must not be called when identity resolution fails")
		}
		if !strings.Contains(err.Error(), "git config --global") {
			t.Errorf("error should contain fix hint; got: %v", err)
		}
	})
}

// ── N-AC1: The standing security regression test ──────────────────────────────
//
// TestN_AC1_NoGitHubEgressPermitted is the durable rail for D-PD-22
// (revises D-PD-01). It covers the AGENT sandbox only.
//
// Security property: "the agent guest commits locally; it never holds a
// GitHub credential and never reaches github.com." A separate human git VM
// is the only role that may receive github.com in AllowedHosts (D-PD-22).
// This test does not cover that role — it is not built yet.
//
// No GitHub token, SSH key, gh-auth state, or forwarded ssh-agent socket
// ever enters an agent sandbox. github.com NEVER appears in AllowedHosts
// for the standard agent path (AgentEgressHosts / WireClaudeEgress).
//
// This test has THREE sub-checks (a)–(c) for the standard agent path, plus a
// cross-reference note about the orca-path scoped exception (d):
//
//	(a) AgentEgressHosts — the source of AllowedHosts for every agent sandbox —
//	    contains no GitHub hostname. A violation here means every agent sandbox
//	    would receive a GitHub credential via the MITM placeholder-swap mechanism.
//
//	(b) WireClaudeEgress (the standard wiring helper for agent sandboxes) does
//	    not insert any GitHub hostname into AllowedHosts. A violation here means
//	    the standard create path would seed GitHub tokens into every agent sandbox.
//
//	(c) The credential env payload emitted by SeedGuest (called with
//	    AgentEgressHosts()) does not contain any GITHUB-pattern variable name.
//	    This is the runtime check: even if (a) or (b) were somehow bypassed,
//	    the payload itself must not carry a GitHub credential var.
//
//	    LIVE-VM PROOF REQUIRED for the push-fail itself: sub-check (c) here
//	    asserts the CONFIGURATION INVARIANT (AllowedHosts never contains github.com)
//	    that makes an in-agent `git push` fail closed. The actual push-fail
//	    requires a booted VM; it is covered by X0-AC3 in the E2E flow.
//
//	(d) ORCA PATH is an AGENT path (D-PD-23). gitHostsFromURL must not return
//	    GitHub hosts. Asserted by cli.TestN_AC1_OrcaPathGitHubInAllowedHosts.
//	    Both test files must stay — deleting either breaks the two-sided rail.
//
// Failure message: each assertion prints WHAT security property broke and
// WHY it matters, so a future engineer who trips it understands the stakes
// rather than deleting the test.
func TestN_AC1_NoGitHubEgressPermitted(t *testing.T) {
	t.Run("(a) AgentEgressHosts contains no GitHub hostname", func(t *testing.T) {
		hosts := AgentEgressHosts()
		for _, h := range hosts {
			if isGitHubHost(h) {
				t.Errorf(
					"SECURITY VIOLATION — N-AC1 / D-PD-22\n"+
						"AgentEgressHosts() returned %q.\n\n"+
						"github.com must NEVER appear in an AGENT sandbox AllowedHosts. "+
						"Adding it causes the MITM placeholder-swap mechanism to mint a GitHub "+
						"credential for every agent sandbox, giving any in-guest process a valid "+
						"GitHub token. D-PD-22: the agent stays dark; only a dedicated human "+
						"git VM may receive github.com. See D-PD-22 in "+
						".groundwork/motives/nexus3-parallel-dev-pr-flow/motive.md.",
					h,
				)
			}
		}
	})

	t.Run("(b) WireClaudeEgress does not wire github.com into AllowedHosts", func(t *testing.T) {
		var opts CreateAndBootOptions
		// WireClaudeEgress accepts nil broker/seeder/src; it only sets slice fields.
		WireClaudeEgress(&opts, nil, nil, nil)
		for _, h := range opts.AllowedHosts {
			if isGitHubHost(h) {
				t.Errorf(
					"SECURITY VIOLATION — N-AC1 / D-PD-22\n"+
						"WireClaudeEgress set AllowedHosts to include %q.\n\n"+
						"This would expose a GitHub credential to every in-guest process that "+
						"sources the credential env file at boot. WireClaudeEgress must set "+
						"AllowedHosts to Anthropic API hosts ONLY (api.anthropic.com, "+
						"platform.claude.com). Adding github.com here widens the egress perimeter "+
						"for every agent sandbox created through the standard wiring path.\n\n"+
						"To fix: remove the GitHub host from WireClaudeEgress / AgentEgressHosts(). "+
						"See D-PD-22.",
					h,
				)
			}
		}
	})

	t.Run("(c) credential env payload contains no GITHUB variable — config invariant", func(t *testing.T) {
		// This sub-check has two parts:
		//
		// Part 1: verify that the running product code (AgentEgressHosts) does not
		// contain any GitHub host that would lead to a GITHUB placeholder var.
		//
		// Part 2: capture the SeedGuest payload and confirm no GITHUB-pattern var
		// appears, even if the host list were somehow expanded by a future change.
		//
		// LIVE-VM PROOF REQUIRED: the actual in-guest `git push` failure cannot be
		// verified in-process. This test covers the configuration invariant only.
		// An in-guest `git push` to github.com will fail closed because the MITM
		// perimeter rejects connections to hosts absent from AllowedHosts. X0-AC3
		// provides the live-VM proof.

		// Part 1: config invariant — no GitHub host in egress set.
		for _, h := range AgentEgressHosts() {
			if isGitHubHost(h) {
				t.Errorf(
					"SECURITY VIOLATION — N-AC1 / D-PD-22\n"+
						"AgentEgressHosts() contains GitHub host %q.\n"+
						"An in-agent `git push` to github.com would succeed because the MITM "+
						"perimeter allows egress to this host. The push must FAIL CLOSED. "+
						"See D-PD-22.",
					h,
				)
			}
		}

		// Part 2: payload check — no GITHUB-pattern credential variable.
		var captured []byte
		stubSeeder := GuestSeeder(func(_ context.Context, _ domain.SandboxID, payload []byte) error {
			captured = payload
			return nil
		})
		id := domain.NewSandboxID()
		broker := cred.NewBroker()
		_, err := SeedGuest(context.Background(), broker, id, AgentEgressHosts(), stubSeeder)
		if err != nil {
			t.Fatalf("SeedGuest returned error: %v", err)
		}
		if bytes.Contains(bytes.ToUpper(captured), []byte("GITHUB")) {
			t.Errorf(
				"SECURITY VIOLATION — N-AC1 / D-PD-22\n"+
					"The credential env payload contains 'GITHUB'.\n\n"+
					"This means a GitHub credential variable (e.g. NEXUS3_CRED_GITHUB_COM_TOKEN) "+
					"was emitted into the guest env file. Any in-guest process sourcing that file "+
					"(e.g. the claude agent) would have a GitHub bearer token. The perimeter MITM "+
					"would swap it for a real GitHub token on every outbound github.com request.\n\n"+
					"To fix: remove github.com from AgentEgressHosts() and AllowedHosts. "+
					"See D-PD-22.\n\npayload:\n%s",
				captured,
			)
		}
	})
}

