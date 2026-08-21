package cli

// TestN_AC1_OrcaPathGitHubInAllowedHosts is the orca-path sub-rail of the
// N-AC1 standing security test (D-PD-23). See service.TestN_AC1_NoGitHubEgressPermitted
// for the service-layer half. Both tests must remain in the codebase — deleting
// either breaks the two-sided rail.
//
// D-PD-23 / D-PD-25: the orca path is an AGENT sandbox. gitHostsFromURL
// MUST NOT return GitHub hosts. github.com is bound only as a SecretHost on
// a human sandbox (builtin gh / --secret), never via AllowedHosts on an agent.
import (
	"strings"
	"testing"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
)

// isN_AC1GitHubHost matches any GitHub or GitHub-CDN hostname.
//
// This is deliberately an INDEPENDENT oracle: it does not call
// domain.IsGitHubHost, so that a bug in the production predicate cannot hide a
// violation of rail N-AC1. Independence only helps if the oracle is at least as
// strict as production, so it normalises first — an oracle that is WEAKER than
// what it audits will report a clean rail while the rail is broken.
//
// It was exactly that, until 2026-08-20: the bare switch below missed the
// trailing-dot FQDN spelling ("github.com."), which the orca path really did
// place into an agent sandbox's AllowedHosts. This test passed throughout.
func isN_AC1GitHubHost(h string) bool {
	h = strings.ToLower(strings.TrimSpace(h))
	for strings.HasSuffix(h, ".") { // trailing-dot FQDN: "github.com." == "github.com"
		h = h[:len(h)-1]
	}
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		h = h[:i] // strip :port
	}
	switch h {
	case "github.com", "githubusercontent.com", "api.github.com",
		"ssh.github.com", "codeload.github.com",
		"objects.githubusercontent.com":
		return true
	}
	const ghSuffix = ".github.com"
	const gcSuffix = ".githubusercontent.com"
	return (len(h) > len(ghSuffix) && h[len(h)-len(ghSuffix):] == ghSuffix) ||
		(len(h) > len(gcSuffix) && h[len(h)-len(gcSuffix):] == gcSuffix)
}

func TestN_AC1_OrcaPathGitHubInAllowedHosts(t *testing.T) {
	t.Run("(1) gitHostsFromURL returns no GitHub host", func(t *testing.T) {
		for _, u := range []string{
			"https://github.com/org/repo.git",
			"https://codeload.github.com/org/repo",
			"https://api.github.com/repos/org/repo",
		} {
			for _, h := range gitHostsFromURL(u) {
				if isN_AC1GitHubHost(h) {
					t.Errorf("gitHostsFromURL(%q) returned GitHub host %q; D-PD-23: orca stays dark", u, h)
				}
			}
		}
	})

	t.Run("(2) safety invariant: AgentEgressHosts excludes github.com", func(t *testing.T) {
		for _, h := range service.AgentEgressHosts(cred.ClaudeCodeProfile) {
			if isN_AC1GitHubHost(h) {
				t.Errorf(
					"SAFETY INVARIANT BROKEN — N-AC1 (D-PD-23)\n"+
						"AgentEgressHosts(cred.ClaudeCodeProfile) now includes %q.\n"+
						"Companion: service.TestN_AC1_NoGitHubEgressPermitted",
					h,
				)
			}
		}
	})

	t.Run("(2b) safety invariant: WireClaudeEgress excludes github.com", func(t *testing.T) {
		var opts service.CreateAndBootOptions
		service.WireClaudeEgress(&opts, nil, nil, nil)
		for _, h := range opts.AllowedHosts {
			if isN_AC1GitHubHost(h) {
				t.Errorf(
					"SAFETY INVARIANT BROKEN — N-AC1 (D-PD-23)\n"+
						"WireClaudeEgress set AllowedHosts to include %q.\n"+
						"Companion: service.TestN_AC1_NoGitHubEgressPermitted",
					h,
				)
			}
		}
	})
}

// TestN_AC1_OracleIsAtLeastAsStrictAsProduction turns the oracle's stated
// invariant into a mechanism.
//
// isN_AC1GitHubHost is deliberately independent of domain.IsGitHubHost, so a
// bug in production cannot hide a violation of rail N-AC1. That independence is
// only worth anything while the oracle is at least as strict as production —
// otherwise it reports a clean rail while the rail is broken, which is exactly
// what happened with the trailing-dot FQDN spelling before 2026-08-20.
//
// Hand-maintaining that property failed twice: the first hardening pass still
// missed bare "githubusercontent.com". So assert it instead of claiming it —
// every spelling production calls GitHub, the oracle must also call GitHub.
func TestN_AC1_OracleIsAtLeastAsStrictAsProduction(t *testing.T) {
	bases := []string{
		"github.com", "githubusercontent.com", "api.github.com",
		"ssh.github.com", "codeload.github.com",
		"objects.githubusercontent.com", "raw.githubusercontent.com",
		"gist.github.com", "example.com", "notgithub.com",
		"github.com.evil.test", "", "localhost",
	}
	// Spelling variants an attacker or a sloppy URL could produce.
	variants := []func(string) string{
		func(s string) string { return s },
		func(s string) string { return s + "." },
		func(s string) string { return s + ".." },
		strings.ToUpper,
		func(s string) string { return "  " + s + "  " },
		func(s string) string { return s + ":443" },
	}
	for _, b := range bases {
		for _, v := range variants {
			h := v(b)
			if domain.IsGitHubHost(h) && !isN_AC1GitHubHost(h) {
				t.Errorf("oracle WEAKER than production for %q: production=true, oracle=false", h)
			}
		}
	}
}
