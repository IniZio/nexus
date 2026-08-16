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
	"testing"

	"github.com/IniZio/nexus3/internal/core/service"
)

// isN_AC1GitHubHost matches any GitHub or GitHub-CDN hostname. Kept in
// lockstep with service.isGitHubHost / isGitHubEgressHost.
func isN_AC1GitHubHost(h string) bool {
	switch h {
	case "github.com", "api.github.com", "ssh.github.com",
		"codeload.github.com", "objects.githubusercontent.com":
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
		for _, h := range service.AgentEgressHosts() {
			if isN_AC1GitHubHost(h) {
				t.Errorf(
					"SAFETY INVARIANT BROKEN — N-AC1 (D-PD-23)\n"+
						"AgentEgressHosts() now includes %q.\n"+
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
