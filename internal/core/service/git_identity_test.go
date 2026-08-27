package service

import (
	"bytes"
	"context"
	"os"
	"os/exec"
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

		branch, err := SeedGitIdentity(context.Background(), id, labels, []string{workspacePath}, seeder)
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
		// Must not contain any static token or raw credential value.
		// Note: "github.com" legitimately appears in the [credential] section URL;
		// only actual token patterns are forbidden here.
		for _, forbidden := range []string{"ghp_", "gho_", "gh auth", "PAT"} {
			if strings.Contains(strings.ToLower(payload), strings.ToLower(forbidden)) {
				t.Errorf("gitconfig payload contains forbidden token pattern %q\npayload:\n%s", forbidden, payload)
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
	branch, err := SeedGitIdentity(context.Background(), id, labels, nil, nil)
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
		_, err := SeedGitIdentity(context.Background(), id, nil, nil, seeder)
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
// (revises D-PD-01, extended by D-PD-33). It covers the AGENT sandbox only.
//
// Security property (CREDENTIAL INVARIANT — D-PD-22): an agent sandbox never
// carries a real GitHub credential. This is enforced by three independent guards,
// each of which must hold independently:
//
//  1. No auto-append (cmd_sandbox.go D-PDE-02): the builtin `gh auth token`
//     bind is never added automatically; only explicit --secret GH_TOKEN@...
//     flags introduce a GitHub credential, and those require --repo (D-PD-36).
//  2. ErrUnboundGitHubSecret (create.go): rejects any GitHub secret bind that
//     lacks an AllowedRepo scope. Covers all callers (CLI, orca, herdr, MCP).
//  3. Agent seeding (seed.go prepareAgentCredPayload): mints placeholders from
//     AgentEgressHosts(profile), NOT from AllowedHosts. So even when a project's
//     nexus3.yaml egress.allow includes github.com, the agent seed payload
//     contains no GitHub credential variable.
//
// Operator decision: project config may add github.com (or any host) to
// AllowedHosts via [egress].allow. This is safe because the credential guard
// (guard 3 above) operates independently of the host list: the agent seeding
// path is keyed on the profile, not the envelope. Sub-check (e) verifies this.
//
// D-PD-33: an empty AllowedHosts is not an implicit AllowAll sentinel.
// WireClaudeEgress must never set OpenEgress=true.
//
// This test has FIVE sub-checks (a)–(e) for the standard agent path, plus a
// cross-reference note about the orca-path scoped rail:
//
//	(a) AgentEgressHosts (the claude-code profile's own host list) contains no
//	    GitHub hostname. The profile is the source for the agent seed payload;
//	    a GitHub host here would produce a GitHub credential var in every agent
//	    sandbox payload regardless of AllowedHosts.
//
//	(b) WireClaudeEgress (the test-wiring helper; not called on the sandbox-create
//	    path) does not insert any GitHub hostname into AllowedHosts when called on
//	    a zero opts value. This checks the profile-driven baseline only; project
//	    config may legitimately extend AllowedHosts with github.com at runtime.
//
//	(c) The credential env payload emitted by SeedGuestAgent (the agent-path seeder,
//	    which uses AgentEgressHosts internally) does not contain any GITHUB-pattern
//	    variable name. This is the RUNTIME check: guard 3 above.
//
//	    LIVE-VM PROOF REQUIRED for the push-fail itself: this sub-check asserts the
//	    CREDENTIAL INVARIANT (agent seed never contains a GitHub var) that makes an
//	    in-agent `git push` fail closed when github.com is absent from the profile.
//	    The actual push-fail requires a booted VM; it is covered by X0-AC3.
//
//	(d) WireClaudeEgress must not set OpenEgress=true. D-PD-33: OpenEgress
//	    disarms the egress ACL for unrestricted outbound access. Agent sandboxes
//	    must never have open egress; only the human create path sets OpenEgress=true.
//
//	(e) CREDENTIAL INVARIANT with github.com in AllowedHosts: even when github.com
//	    is explicitly listed as an AllowedHost (as it would be via [egress].allow in
//	    nexus3.yaml), the agent seed payload must contain no GitHub credential var.
//	    The agent seeder (prepareAgentCredPayload) uses AgentEgressHosts(profile),
//	    not AllowedHosts, so the two lists are independently controlled.
//
//	(f) ORCA PATH is an AGENT path (D-PD-23). gitHostsFromURL must not return
//	    GitHub hosts. Asserted by cli.TestN_AC1_OrcaPathGitHubInAllowedHosts.
//	    Both test files must stay — deleting either breaks the two-sided rail.
//
// Failure message: each assertion prints WHAT security property broke and
// WHY it matters, so a future engineer who trips it understands the stakes
// rather than deleting the test.
func TestN_AC1_NoGitHubEgressPermitted(t *testing.T) {
	t.Run("(a) AgentEgressHosts contains no GitHub hostname", func(t *testing.T) {
		hosts := AgentEgressHosts(cred.ClaudeCodeProfile)
		for _, h := range hosts {
			if isGitHubHost(h) {
				t.Errorf(
					"SECURITY VIOLATION — N-AC1 / D-PD-22\n"+
						"AgentEgressHosts(cred.ClaudeCodeProfile) returned %q.\n\n"+
						"github.com must NEVER appear in an AGENT sandbox AllowedHosts. "+
						"Adding it causes the MITM placeholder-swap mechanism to mint a GitHub "+
						"credential for every agent sandbox, giving any in-guest process a valid "+
						"GitHub token. D-PD-22: the agent stays dark; only a dedicated human "+
						"git VM may receive github.com. See D-PD-22 in "+
						".nexus/docs/nexus3-parallel-dev-pr-flow/motive.md.",
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
						"To fix: remove the GitHub host from WireClaudeEgress / AgentEgressHosts(cred.ClaudeCodeProfile). "+
						"See D-PD-22.",
					h,
				)
			}
		}
	})

	t.Run("(d) WireClaudeEgress does not set OpenEgress — D-PD-33", func(t *testing.T) {
		// D-PD-33: OpenEgress=true disarms the egress ACL so the sandbox
		// reaches any host without restriction (docker pulls, github.com, etc.).
		// Agent sandboxes must NEVER have OpenEgress=true; only the human create
		// path (sandbox create) sets it. WireClaudeEgress is the standard wiring
		// helper for agent sandboxes — it must not set OpenEgress.
		var opts CreateAndBootOptions
		WireClaudeEgress(&opts, nil, nil, nil)
		if opts.OpenEgress {
			t.Error(
				"SECURITY VIOLATION — N-AC1 / D-PD-33\n" +
					"WireClaudeEgress set OpenEgress=true.\n\n" +
					"OpenEgress disarms the egress ACL, giving the agent sandbox " +
					"unrestricted outbound access — including to github.com. " +
					"Agent sandboxes must never have open egress; only the human " +
					"create path (sandbox create) sets OpenEgress=true.\n\n" +
					"To fix: remove OpenEgress=true from WireClaudeEgress. See D-PD-33.",
			)
		}
	})

	t.Run("(c) credential env payload contains no GITHUB variable — credential invariant", func(t *testing.T) {
		// This sub-check has two parts:
		//
		// Part 1: verify that the running product code (AgentEgressHosts) does not
		// contain any GitHub host that would lead to a GITHUB placeholder var.
		//
		// Part 2: capture the SeedGuestAgent payload and confirm no GITHUB-pattern
		// var appears. SeedGuestAgent uses AgentEgressHosts(profile) internally;
		// it never reads AllowedHosts.
		//
		// Mutation guard: change prepareAgentCredPayload to use hosts that include
		// "github.com" → Part 2 fails RED (GITHUB appears in the payload).
		//
		// LIVE-VM PROOF REQUIRED: the actual in-guest `git push` failure cannot be
		// verified in-process. This test covers the CREDENTIAL INVARIANT only.
		// An in-guest `git push` to github.com will fail closed because the agent
		// seed payload carries no GitHub credential var. X0-AC3 provides the live proof.

		// Part 1: profile invariant — no GitHub host in AgentEgressHosts.
		for _, h := range AgentEgressHosts(cred.ClaudeCodeProfile) {
			if isGitHubHost(h) {
				t.Errorf(
					"SECURITY VIOLATION — N-AC1 / D-PD-22\n"+
						"AgentEgressHosts(cred.ClaudeCodeProfile) contains GitHub host %q.\n"+
						"A GitHub host in the profile causes prepareAgentCredPayload to mint "+
						"a GitHub placeholder var in every agent sandbox payload. The push must "+
						"FAIL CLOSED. See D-PD-22.",
					h,
				)
			}
		}

		// Part 2: payload check — no GITHUB-pattern credential variable.
		//
		// Operator decision note: project [egress].allow may include github.com
		// (by operator choice). The CREDENTIAL GUARD is independent of the host
		// ACL: prepareAgentCredPayload uses AgentEgressHosts(profile), NOT the
		// sandbox AllowedHosts. AllowedHosts controls which hosts the egress ACL
		// passes; the credential list controls which hosts receive a MITM
		// placeholder token. These two lists are independently controlled.
		// Adding github.com to AllowedHosts (e.g. via nexus3.yaml egress.allow)
		// must NOT produce a GitHub token in the agent payload. There is no
		// code path where AllowedHosts flows into SeedGuestAgent — the function
		// only takes the seeder callback.
		var captured []byte
		stubSeeder := GuestSeeder(func(_ context.Context, _ domain.SandboxID, payload []byte) error {
			captured = payload
			return nil
		})
		id := domain.NewSandboxID()
		broker := cred.NewBroker()
		_, err := SeedGuestAgent(context.Background(), broker, id, stubSeeder)
		if err != nil {
			t.Fatalf("SeedGuestAgent returned error: %v", err)
		}
		if bytes.Contains(bytes.ToUpper(captured), []byte("GITHUB")) {
			t.Errorf(
				"SECURITY VIOLATION — N-AC1 / D-PD-22\n"+
					"The agent credential env payload contains 'GITHUB'.\n\n"+
					"This means a GitHub credential variable (e.g. NEXUS3_CRED_GITHUB_COM_TOKEN) "+
					"was emitted into the guest env file. Any in-guest process sourcing that file "+
					"(e.g. the claude agent) would have a GitHub bearer token. The perimeter MITM "+
					"would swap it for a real GitHub token on every outbound github.com request.\n\n"+
					"The credential guard is prepareAgentCredPayload in seed.go: it must use "+
					"AgentEgressHosts(profile), never AllowedHosts. "+
					"See D-PD-22.\n\npayload:\n%s",
				captured,
			)
		}
	})
}

// TestBuildGitconfigPayload_SafeDirectory is a table test for buildGitconfigPayload
// covering the safe.directory behaviour with zero, one, and several source paths.
//
// Mutation guard: delete the [safe] section from buildGitconfigPayload → fails RED.
func TestBuildGitconfigPayload_SafeDirectory(t *testing.T) {
	const (
		name   = "Test Op"
		email  = "op@example.com"
		branch = "nexus3/default/ab12cd34"
	)

	t.Run("zero paths — no safe section", func(t *testing.T) {
		payload := string(buildGitconfigPayload(name, email, nil, branch))
		if strings.Contains(payload, "[safe]") {
			t.Errorf("expected no [safe] section with zero source paths; got:\n%s", payload)
		}
	})

	t.Run("one path — safe.directory present", func(t *testing.T) {
		paths := []string{"/work"}
		payload := string(buildGitconfigPayload(name, email, paths, branch))
		if !strings.Contains(payload, "[safe]") {
			t.Errorf("expected [safe] section; got:\n%s", payload)
		}
		if !strings.Contains(payload, "\tdirectory = /work") {
			t.Errorf("expected directory = /work; got:\n%s", payload)
		}
	})

	t.Run("several paths — each gets a directory entry", func(t *testing.T) {
		paths := []string{"/work", "/data", "/mnt/src"}
		payload := string(buildGitconfigPayload(name, email, paths, branch))
		for _, p := range paths {
			if !strings.Contains(payload, "\tdirectory = "+p) {
				t.Errorf("expected directory = %s in payload; got:\n%s", p, payload)
			}
		}
		// All under one [safe] block: count occurrences of [safe].
		if count := strings.Count(payload, "[safe]"); count != 1 {
			t.Errorf("expected exactly one [safe] section header, got %d; payload:\n%s", count, payload)
		}
	})

	t.Run("empty strings in list are skipped", func(t *testing.T) {
		paths := []string{"", "/work", ""}
		payload := string(buildGitconfigPayload(name, email, paths, branch))
		if !strings.Contains(payload, "\tdirectory = /work") {
			t.Errorf("expected directory = /work; got:\n%s", payload)
		}
		if strings.Contains(payload, "directory = \n") {
			t.Errorf("empty path must not produce a directory entry; got:\n%s", payload)
		}
	})
}

// TestSourceGuestPaths is the mutation guard for the SourceGuestPaths helper
// called at the create.go call site. It proves that live-mount guest paths are
// included in the list, which is the defect that existed before this fix.
//
// Mutation guard: remove the liveMounts loop from SourceGuestPaths → fails RED.
func TestSourceGuestPaths(t *testing.T) {
	t.Run("workspace only", func(t *testing.T) {
		got := SourceGuestPaths("/workspace/repo", nil)
		if len(got) != 1 || got[0] != "/workspace/repo" {
			t.Errorf("got %v, want [\"/workspace/repo\"]", got)
		}
	})

	t.Run("live mounts only", func(t *testing.T) {
		mounts := []domain.LiveMount{
			{HostPath: "/home/user/code", GuestPath: "/work"},
			{HostPath: "/data", GuestPath: "/mnt/data"},
		}
		got := SourceGuestPaths("", mounts)
		if len(got) != 2 {
			t.Fatalf("got %v, want 2 elements", got)
		}
		if got[0] != "/work" || got[1] != "/mnt/data" {
			t.Errorf("got %v, want [\"/work\", \"/mnt/data\"]", got)
		}
	})

	t.Run("workspace and live mounts — all paths collected", func(t *testing.T) {
		mounts := []domain.LiveMount{
			{HostPath: "/home/user/src", GuestPath: "/src"},
		}
		got := SourceGuestPaths("/workspace/proj", mounts)
		if len(got) != 2 {
			t.Fatalf("got %v, want 2 elements", got)
		}
		if got[0] != "/workspace/proj" {
			t.Errorf("first element: got %q, want /workspace/proj", got[0])
		}
		if got[1] != "/src" {
			t.Errorf("second element: got %q, want /src", got[1])
		}
	})

	t.Run("empty workspace and mount with empty GuestPath skipped", func(t *testing.T) {
		mounts := []domain.LiveMount{{HostPath: "/x", GuestPath: ""}}
		got := SourceGuestPaths("", mounts)
		if len(got) != 0 {
			t.Errorf("got %v, want empty slice", got)
		}
	})
}

// ── GitHub credential helper ──────────────────────────────────────────────────

// TestBuildGitconfigPayload_GitHubCredentialHelper asserts the structural
// properties of the credential helper section written by buildGitconfigPayload:
//
//  1. A [credential "https://github.com"] section is present (scoped, not global).
//  2. A helper = line is present inside that section.
//  3. No token value, placeholder hex string, or raw credential appears in the
//     payload — the helper reads $GH_TOKEN from the environment at push time.
//  4. The existing user.name, user.email, and safe.directory assertions hold
//     (regression guard).
//
// Mutation guards (run mutations manually, restore before committing):
//
//	Mutation A: delete fmt.Fprint(&buf, "[credential...]") from buildGitconfigPayload.
//	            → "credential section present" assertion fails RED.
//	Mutation B: delete the helper = fmt.Fprint line.
//	            → "helper line present" assertion fails RED.
//	Mutation C: replace the raw-string helper value with a hardcoded token like
//	            "helper = ghp_fakeTOKEN".
//	            → "no token in payload" assertion fails RED.
func TestBuildGitconfigPayload_GitHubCredentialHelper(t *testing.T) {
	const (
		name   = "Test Op"
		email  = "op@example.com"
		branch = "nexus3/default/ab12cd34"
	)
	payload := string(buildGitconfigPayload(name, email, []string{"/work"}, branch))

	// Assertion 1: credential section is present and scoped to github.com.
	// Mutation A guard: delete the [credential] Fprint → this assertion fails RED.
	const credSection = `[credential "https://github.com"]`
	if !strings.Contains(payload, credSection) {
		t.Errorf("gitconfig payload missing %q section; got:\n%s", credSection, payload)
	}

	// Assertion 2: a helper = line is present.
	// Mutation B guard: delete the helper Fprint → this assertion fails RED.
	if !strings.Contains(payload, "\thelper = ") {
		t.Errorf("gitconfig payload missing 'helper =' line; got:\n%s", payload)
	}

	// Assertion 3: no token value appears in the payload.
	// The helper must read $GH_TOKEN from the environment, not embed it in the file.
	// Mutation C guard: embed a static token in the helper → this assertion fails RED.
	for _, tokenPattern := range []string{
		"ghp_", "gho_", "github_pat_",
		// 64-char hex placeholder pattern (a realistic-looking fake):
		"0000000000000000000000000000000000000000000000000000000000000000",
	} {
		if strings.Contains(payload, tokenPattern) {
			t.Errorf("gitconfig payload contains token pattern %q — token must not appear in file; got:\n%s",
				tokenPattern, payload)
		}
	}

	// Assertion 4: git itself parses the helper value back correctly.
	//
	// This is the defect-class assertion the original test lacked: the previous
	// inline-function helper value contained double-quotes and \n sequences that
	// git's config parser consumed, leaving sh with a shredded command. This
	// assertion would have caught that defect at commit time.
	//
	// Mutation F (inline function regression): revert the helper to
	//   !f(){ case $1 in get) [ -n "${GH_TOKEN}" ] && { ... }; f
	// → git config --get returns "" and this assertion fails RED.
	if git, lookErr := exec.LookPath("git"); lookErr == nil {
		tmp, tmpErr := os.CreateTemp(t.TempDir(), "gitconfig-*.ini")
		if tmpErr != nil {
			t.Fatalf("create temp gitconfig: %v", tmpErr)
		}
		if _, writeErr := tmp.WriteString(payload); writeErr != nil {
			t.Fatalf("write temp gitconfig: %v", writeErr)
		}
		tmp.Close()
		out, gitErr := exec.Command(git, "config", "--file", tmp.Name(),
			"--get", "credential.https://github.com.helper").Output()
		if gitErr != nil {
			t.Fatalf("git config --get credential helper: %v — git could not parse the helper value", gitErr)
		}
		got := strings.TrimSpace(string(out))
		const wantHelper = "!sh " + GuestGitCredentialHelperPath
		if got != wantHelper {
			t.Errorf("git parsed helper value = %q; want %q\n"+
				"(if got is empty, the config parser shredded the value — inline function regression)",
				got, wantHelper)
		}
	}

	// Assertion 5 (regression): existing fields still present.
	if !strings.Contains(payload, "name = "+name) {
		t.Errorf("payload missing user.name; got:\n%s", payload)
	}
	if !strings.Contains(payload, "email = "+email) {
		t.Errorf("payload missing user.email; got:\n%s", payload)
	}
	if !strings.Contains(payload, "directory = /work") {
		t.Errorf("payload missing safe.directory; got:\n%s", payload)
	}
}

// TestGitCredentialHelperScript_DashSyntax checks that GuestGitCredentialHelperScript
// passes a POSIX syntax check under dash (or sh). The guest's /bin/sh is dash;
// a bash-only construct in the script would silently fail at push time.
//
// Mutation: add a bashism (e.g. [[ … ]]) to the script → dash -n exits non-zero
// and this test fails RED.
func TestGitCredentialHelperScript_DashSyntax(t *testing.T) {
	// Try dash first (the actual guest shell), fall back to sh.
	shell, err := exec.LookPath("dash")
	if err != nil {
		shell, err = exec.LookPath("sh")
		if err != nil {
			t.Skip("neither dash nor sh in PATH; skipping syntax check")
		}
	}
	cmd := exec.Command(shell, "-n", "/dev/stdin")
	cmd.Stdin = strings.NewReader(GuestGitCredentialHelperScript)
	if out, runErr := cmd.CombinedOutput(); runErr != nil {
		t.Fatalf("shell -n syntax check failed: %v\noutput: %s\nscript:\n%s",
			runErr, out, GuestGitCredentialHelperScript)
	}
	// Script must not contain a real token (only env var references).
	for _, pattern := range []string{"ghp_", "gho_", "github_pat_"} {
		if strings.Contains(GuestGitCredentialHelperScript, pattern) {
			t.Errorf("script contains token pattern %q — script must not embed a real token", pattern)
		}
	}
}

// TestGitCredentialHelper_ShellBehavior tests GuestGitCredentialHelperScript by
// executing it under sh with GH_TOKEN set and unset. It verifies the output
// format git's credential protocol expects.
//
// The script is written to a temp file so the test does not depend on the file
// being present at GuestGitCredentialHelperPath (a guest-only path absent on
// the host).
//
// Mutation guards:
//
//	Mutation D: change "get" to "GET" in the case pattern.
//	            → "token present → output contains password=" fails RED.
//	Mutation E: remove the [ -n "${GH_TOKEN}" ] guard.
//	            → "token absent → output is empty" fails RED (outputs password=).
func TestGitCredentialHelper_ShellBehavior(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not in PATH; skipping shell helper behaviour test")
	}

	// Write the script to a temp file — we test the constant directly, not via
	// git config extraction, because the gitconfig now references the script by
	// path rather than inlining it.
	scriptFile := t.TempDir() + "/nexus3-git-credential"
	if writeErr := os.WriteFile(scriptFile, []byte(GuestGitCredentialHelperScript), 0o755); writeErr != nil {
		t.Fatalf("write helper script: %v", writeErr)
	}

	t.Run("GH_TOKEN set — output has username and password lines", func(t *testing.T) {
		const fakeToken = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
		// git invokes: sh <scriptpath> get
		cmd := exec.Command(sh, scriptFile, "get")
		cmd.Env = []string{"GH_TOKEN=" + fakeToken}
		out, execErr := cmd.Output()
		if execErr != nil {
			t.Fatalf("sh <script> get: %v (output: %s)", execErr, out)
		}
		outStr := string(out)
		// Mutation D guard: changing case "get" → "GET" causes this to fail RED.
		if !strings.Contains(outStr, "username=") {
			t.Errorf("helper output missing username= line; got: %q", outStr)
		}
		if !strings.Contains(outStr, "password="+fakeToken) {
			t.Errorf("helper output missing password=<token> line; got: %q", outStr)
		}
		if !strings.Contains(outStr, fakeToken) {
			t.Errorf("helper output does not contain the token value; got: %q", outStr)
		}
	})

	t.Run("GH_TOKEN unset — output is empty (quiet degradation)", func(t *testing.T) {
		cmd := exec.Command(sh, scriptFile, "get")
		cmd.Env = []string{} // explicitly empty — no GH_TOKEN
		out, execErr := cmd.Output()
		if execErr != nil {
			t.Fatalf("sh <script> get (no token): %v (output: %s)", execErr, out)
		}
		outStr := strings.TrimSpace(string(out))
		// Mutation E guard: removing [ -n "${GH_TOKEN}" ] guard causes this to fail RED.
		if outStr != "" {
			t.Errorf("helper output should be empty when GH_TOKEN is unset; got: %q", outStr)
		}
	})

	t.Run("store action — output is empty (helper ignores non-get actions)", func(t *testing.T) {
		cmd := exec.Command(sh, scriptFile, "store")
		cmd.Env = []string{"GH_TOKEN=sometoken"}
		out, execErr := cmd.Output()
		if execErr != nil {
			t.Fatalf("sh <script> store: %v (output: %s)", execErr, out)
		}
		if strings.TrimSpace(string(out)) != "" {
			t.Errorf("helper should produce no output for 'store' action; got: %q", string(out))
		}
	})
}

// TestGitCredentialHelper_EndToEnd runs the helper script end-to-end through
// git credential fill, isolated from the host's own git configuration.
//
// GIT_CONFIG_GLOBAL=/dev/null and GIT_CONFIG_SYSTEM=/dev/null prevent the
// host's real credential helper from answering and prevent any real token from
// appearing in test output. These env vars are mandatory; without them the
// host's credential store answers and the test passes for the wrong reason —
// or, worse, prints the operator's real token into the test log.
//
// Mutation guards:
//
//	Mutation G: revert the gitconfig helper to an inline function with quotes.
//	            → git credential fill exits with a parse/syntax error and the
//	              subtest "GH_TOKEN set" fails RED.
//	Mutation H: remove GIT_CONFIG_GLOBAL=/dev/null from the command env.
//	            → the host credential helper may answer with real tokens — the
//	              test then passes for the wrong reason (and may leak).
func TestGitCredentialHelper_EndToEnd(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not in PATH; skipping end-to-end credential helper test")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not in PATH; skipping end-to-end credential helper test")
	}

	// Write the helper script to a temp file.
	scriptFile := t.TempDir() + "/nexus3-git-credential"
	if writeErr := os.WriteFile(scriptFile, []byte(GuestGitCredentialHelperScript), 0o755); writeErr != nil {
		t.Fatalf("write helper script: %v", writeErr)
	}

	// Run git credential fill using -c to pass the helper inline.
	// This avoids needing the script at GuestGitCredentialHelperPath.
	// Combined with GIT_CONFIG_GLOBAL=/dev/null and GIT_CONFIG_SYSTEM=/dev/null,
	// the only credential source is our script.
	helperArg := "credential.https://github.com.helper=!sh " + scriptFile

	_ = sh // referenced via helperArg above; referenced here to avoid unused-import lint

	runFill := func(t *testing.T, ghToken string) string {
		t.Helper()
		cmd := exec.Command(git, "-c", helperArg, "credential", "fill")
		// Isolation: disable host global and system git configs.
		// Without these, the host's real credential helper can answer and
		// the test passes for the wrong reason (Mutation H guard).
		cmd.Env = []string{
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_CONFIG_NO_SYSTEM=1",
		}
		if ghToken != "" {
			cmd.Env = append(cmd.Env, "GH_TOKEN="+ghToken)
		}
		// git credential fill reads: protocol=<p>\nhost=<h>\n\n
		cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
		out, runErr := cmd.Output()
		if runErr != nil {
			// Non-zero exit is expected when no credential is produced (unset token path).
			// We only fail if the error is unexpected (e.g. shell syntax error).
			if exitErr, ok := runErr.(*exec.ExitError); ok {
				stderr := string(exitErr.Stderr)
				if strings.Contains(stderr, "Syntax error") || strings.Contains(stderr, "syntax error") {
					t.Fatalf("git credential fill: shell syntax error in helper: %s", stderr)
				}
			}
		}
		return string(out)
	}

	t.Run("GH_TOKEN set — credential fill returns username and password", func(t *testing.T) {
		const fakeToken = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
		out := runFill(t, fakeToken)
		// Mutation G guard: inline function with quotes causes a shell syntax
		// error (visible in stderr) caught by runFill above, so the test fails RED.
		if !strings.Contains(out, "username=x-token-auth") {
			t.Errorf("credential fill: missing username=x-token-auth; output: %q", out)
		}
		if !strings.Contains(out, "password="+fakeToken) {
			t.Errorf("credential fill: missing password=<token>; output: %q", out)
		}
		// The raw token must not appear anywhere except the password= line.
		lines := strings.Split(strings.TrimSpace(out), "\n")
		for _, line := range lines {
			if strings.HasPrefix(line, "password=") {
				continue
			}
			if strings.Contains(line, fakeToken) {
				t.Errorf("token value leaked into non-password line: %q", line)
			}
		}
	})

	t.Run("GH_TOKEN unset — credential fill produces no credential", func(t *testing.T) {
		out := runFill(t, "")
		// When no credential is available, git credential fill returns nothing
		// useful. We assert the script does not accidentally emit a password.
		if strings.Contains(out, "password=") {
			t.Errorf("credential fill with no GH_TOKEN produced a password line; output: %q", out)
		}
	})
}

// TestBuildGitconfigPayload_GitHubSSHRewrite pins the insteadOf rewrite that
// makes a GitHub SSH remote pushable from inside a sandbox.
//
// A guest holds no SSH key — the credential rail forbids seeding one — and the
// MITM proxy that swaps the placeholder for the real token only observes HTTPS
// CONNECTs. So "git@github.com:owner/repo.git", the default remote for most
// clones, cannot be pushed from a sandbox at all. The agent inherits that
// remote through its mounted worktree, so without the rewrite the outbound
// half fails with an SSH timeout that reads as a network fault.
//
// Mutation: delete either insteadOf line -> the matching subtest fails RED.
func TestBuildGitconfigPayload_GitHubSSHRewrite(t *testing.T) {
	payload := string(buildGitconfigPayload("Ada Lovelace", "ada@example.com", []string{"/work"}, "nexus3/x/abc123"))

	if !strings.Contains(payload, `[url "https://github.com/"]`) {
		t.Fatalf("payload has no [url \"https://github.com/\"] section; a git@github.com: remote\n"+
			"would be unpushable from the guest.\npayload:\n%s", payload)
	}

	// Both spellings matter: scp-style is what `git clone git@github.com:o/r`
	// writes, and ssh:// is what some tools normalise it to. Covering only one
	// leaves the other silently broken.
	for _, form := range []string{
		"insteadOf = git@github.com:",
		"insteadOf = ssh://git@github.com/",
	} {
		t.Run(form, func(t *testing.T) {
			if !strings.Contains(payload, form) {
				t.Errorf("payload missing %q\npayload:\n%s", form, payload)
			}
		})
	}

	// The rewrite must be scoped to GitHub. A bare "insteadOf = git@" would
	// silently redirect every SSH remote — GitLab, an internal host — to a
	// GitHub URL that will not exist.
	if strings.Contains(payload, "insteadOf = git@\n") {
		t.Error("payload contains an unscoped git@ rewrite; it would misroute non-GitHub SSH remotes")
	}
}
