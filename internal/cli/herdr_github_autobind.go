package cli

// GitHub auto-bind: injects GH_TOKEN + scoped path policies when the principal
// ran /link github and the checkout origin is github.com.
// Rationale in doc/design/controller-github-auto-bind.md.

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/config"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/vault"
)

// herdrGitHubAutoBindSecret is the secret token injected on successful auto-bind.
const herdrGitHubAutoBindSecret = "GH_TOKEN@github.com,api.github.com,uploads.github.com"

// ownerRe and repoRe guard against wildcard/glob injection in GitHub owner and repo names.
var (
	ownerRe = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	repoRe  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// parseGitHubOrigin parses a git remote URL and returns the owner and repo
// when the host is exactly github.com (case-insensitive).
// Accepted forms: git@github.com:O/R(.git), ssh://git@github.com/O/R(.git),
// https://github.com/O/R(.git). Trailing slash and optional user@ in https are ok.
// ".git" suffix is stripped from repo. Returns ok=false for other hosts,
// missing owner/repo, extra path segments, empty input, "." / ".." segments,
// query strings, fragments, opaque URIs, raw (escaped) paths, or names that
// do not match the owner/repo character sets (rejects wildcards like * and **).
func parseGitHubOrigin(raw string) (owner, repo string, ok bool) {
	if raw == "" {
		return "", "", false
	}

	var ownerRepo string

	switch {
	case strings.HasPrefix(strings.ToLower(raw), "https://") || strings.HasPrefix(strings.ToLower(raw), "http://"):
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", false
		}
		if !strings.EqualFold(u.Hostname(), "github.com") {
			return "", "", false
		}
		// Reject query, fragment, opaque, or percent-encoded path segments.
		if u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
			return "", "", false
		}
		ownerRepo = strings.TrimPrefix(u.Path, "/")

	case strings.HasPrefix(strings.ToLower(raw), "ssh://"):
		u, err := url.Parse(raw)
		if err != nil {
			return "", "", false
		}
		if !strings.EqualFold(u.Hostname(), "github.com") {
			return "", "", false
		}
		if u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || u.RawPath != "" {
			return "", "", false
		}
		ownerRepo = strings.TrimPrefix(u.Path, "/")

	default:
		// SCP form: [user@]github.com:owner/repo[.git][/]
		colonIdx := strings.Index(raw, ":")
		if colonIdx < 0 {
			return "", "", false
		}
		hostPart := raw[:colonIdx]
		if atIdx := strings.LastIndex(hostPart, "@"); atIdx >= 0 {
			hostPart = hostPart[atIdx+1:]
		}
		if !strings.EqualFold(hostPart, "github.com") {
			return "", "", false
		}
		ownerRepo = raw[colonIdx+1:]
	}

	// Strip trailing slash and optional .git suffix.
	ownerRepo = strings.TrimRight(ownerRepo, "/")
	ownerRepo = strings.TrimSuffix(ownerRepo, ".git")
	ownerRepo = strings.TrimRight(ownerRepo, "/")

	parts := strings.Split(ownerRepo, "/")
	if len(parts) != 2 {
		return "", "", false
	}
	owner, repo = parts[0], parts[1]
	if owner == "" || repo == "" {
		return "", "", false
	}
	// Reject "." or ".." segments, then validate charset (also rejects wildcards).
	if owner == "." || owner == ".." || repo == "." || repo == ".." {
		return "", "", false
	}
	if !ownerRe.MatchString(owner) || !repoRe.MatchString(repo) {
		return "", "", false
	}
	return owner, repo, true
}

// herdrConfigDeclaresGitHub reports whether cfg already declares any GitHub
// egress: a secrets host or a policy host that IsGitHubHost returns true for.
func herdrConfigDeclaresGitHub(cfg config.Config) bool {
	for _, s := range cfg.Egress.Secrets {
		for _, h := range s.Hosts {
			if domain.IsGitHubHost(h) {
				return true
			}
		}
	}
	for _, p := range cfg.Egress.Policy {
		if domain.IsGitHubHost(p.Host) {
			return true
		}
	}
	return false
}

// herdrGitHubLinked reports whether principal has a linked GitHub credential
// in vault v scoped to project. Returns false when v is nil, principal is empty,
// or Source returns any error (ErrUnlinked, ErrProjectNotAllowed, or other).
func herdrGitHubLinked(v vault.Vault, principal, project string) bool {
	if v == nil || principal == "" {
		return false
	}
	_, err := v.Source(vault.Key{Principal: principal, Integration: "github"}, project)
	return err == nil
}

// herdrGitHubAutoBind is pure. It appends herdrGitHubAutoBindSecret and adds
// scoped path policies for the sandbox's origin repo when all of:
//   - herdrConfigDeclaresGitHub(cfg) is false
//   - linked is true
//   - parseGitHubOrigin(originURL) succeeds
//
// Path policies added per host (no /graphql, no root /**):
//
//	github.com:         /O/R/**, /O/R.git/**
//	api.github.com:     /repos/O/R/**, /user
//	uploads.github.com: /repos/O/R/**
//
// Does not mutate the caller's secrets backing array (copy before append).
// Returns inputs unchanged with added=false otherwise.
func herdrGitHubAutoBind(cfg config.Config, originURL string, linked bool, secrets []string, pp domain.EgressPathPolicies) ([]string, domain.EgressPathPolicies, bool) {
	if herdrConfigDeclaresGitHub(cfg) {
		return secrets, pp, false
	}
	if !linked {
		return secrets, pp, false
	}
	owner, repo, ok := parseGitHubOrigin(originURL)
	if !ok {
		return secrets, pp, false
	}

	out := make([]string, len(secrets), len(secrets)+1)
	copy(out, secrets)
	out = append(out, herdrGitHubAutoBindSecret)

	pp = egressAddHostPolicy(pp, "github.com", domain.EgressHostPolicy{
		Paths: []string{
			fmt.Sprintf("/%s/%s/**", owner, repo),
			fmt.Sprintf("/%s/%s.git/**", owner, repo),
		},
	})
	pp = egressAddHostPolicy(pp, "api.github.com", domain.EgressHostPolicy{
		Paths: []string{
			fmt.Sprintf("/repos/%s/%s/**", owner, repo),
			"/user",
		},
	})
	pp = egressAddHostPolicy(pp, "uploads.github.com", domain.EgressHostPolicy{
		Paths: []string{
			fmt.Sprintf("/repos/%s/%s/**", owner, repo),
		},
	})
	return out, pp, true
}

// herdrOriginURLFn runs `git -C dir config --get remote.origin.url` with a 5s
// timeout. Returns "" on any error. Overridable for tests.
var herdrOriginURLFn = func(ctx context.Context, dir string) string {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// herdrGitHubLinkedFn opens the host vault and checks GitHub linkage for principal and project.
// Returns (false, err) on open error; (herdrGitHubLinked(v, principal, project), nil) otherwise.
// Overridable for tests.
var herdrGitHubLinkedFn = func(principal, project string) (bool, error) {
	v, err := openHostVaultFn()
	if err != nil {
		return false, err
	}
	return herdrGitHubLinked(v, principal, project), nil
}

// herdrApplyGitHubAutoBind is the call-site wrapper invoked during worktree
// sandbox creation. See doc/design/controller-github-auto-bind.md for rationale.
//
// Order:
//  1. If herdrConfigDeclaresGitHub(cfg) → return inputs (no vault open, no git).
//  2. origin := herdrOriginURLFn(ctx, checkoutPath); if parseGitHubOrigin fails → return inputs.
//  3. principal := os.Getenv(vault.PrincipalEnv); if empty → return inputs (no vault open).
//  4. Call herdrGitHubLinkedFn(principal, project); on err: print "vault unavailable" to w and return inputs.
//  5. Call herdrGitHubAutoBind; if added, print "scoped to O/R" to w.
func herdrApplyGitHubAutoBind(ctx context.Context, w io.Writer, checkoutPath, project string, cfg config.Config, secrets []string, pp domain.EgressPathPolicies) ([]string, domain.EgressPathPolicies) {
	if herdrConfigDeclaresGitHub(cfg) {
		return secrets, pp
	}
	origin := herdrOriginURLFn(ctx, checkoutPath)
	if _, _, ok := parseGitHubOrigin(origin); !ok {
		return secrets, pp
	}
	principal := os.Getenv(vault.PrincipalEnv)
	if principal == "" {
		return secrets, pp
	}
	linked, err := herdrGitHubLinkedFn(principal, project)
	if err != nil {
		fmt.Fprintf(w, "worktree-sandbox: github auto-bind skipped: vault unavailable: %v\n", err)
		return secrets, pp
	}
	out, pp2, added := herdrGitHubAutoBind(cfg, origin, linked, secrets, pp)
	if added {
		owner, repo, _ := parseGitHubOrigin(origin)
		fmt.Fprintf(w, "worktree-sandbox: github auto-bind: GH_TOKEN scoped to %s/%s\n", owner, repo)
	}
	return out, pp2
}
