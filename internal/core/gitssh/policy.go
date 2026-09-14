package gitssh

import (
	"strings"
)

// HostPolicy is one egress.policy entry from nexus3.yaml.
type HostPolicy struct {
	Host  string   // e.g. "github.com"
	Paths []string // e.g. ["/example-org/example-app/**"]
}

// AllowedRepo is one (sshHost, owner/repo) entry in the relay allowlist.
type AllowedRepo struct {
	SSHHost   string // e.g. "git@github.com"
	OwnerRepo string // e.g. "example-org/example-app" (no leading /, no .git suffix)
}

// DeriveAllowlist produces SSH relay allowlist entries from nexus3.yaml egress
// policy entries. For github.com entries (only — other hosts have no git SSH
// equivalent), paths of the form /owner/repo/** or /owner/repo.git/** emit one
// AllowedRepo{SSHHost:"git@github.com", OwnerRepo:"owner/repo"}.
// Exact paths (/owner/repo, /owner/repo.git) without a glob suffix are also
// accepted and emit the same entry.
// Duplicate (SSHHost, OwnerRepo) pairs are deduplicated.
// api.github.com paths use the /repos/owner/repo form; those are NOT translated
// (ssh connections go to github.com, not api.github.com).
func DeriveAllowlist(policies []HostPolicy) []AllowedRepo {
	seen := map[string]struct{}{}
	var result []AllowedRepo

	for _, p := range policies {
		if !strings.EqualFold(p.Host, "github.com") {
			continue
		}
		for _, rawPath := range p.Paths {
			// Strip leading /
			path := strings.TrimPrefix(rawPath, "/")
			// Strip trailing /** or .git/** suffixes for glob paths
			path = strings.TrimSuffix(path, "/**")
			path = strings.TrimSuffix(path, ".git/**")
			// Also handle /owner/repo.git without glob
			path = strings.TrimSuffix(path, ".git")

			segments := strings.SplitN(path, "/", 3)
			if len(segments) < 2 {
				continue
			}
			owner := segments[0]
			repo := segments[1]

			// Validate segments
			if owner == "" || owner == "." || owner == ".." {
				continue
			}
			if repo == "" || repo == "." || repo == ".." {
				continue
			}
			// Strip any remaining .git suffix
			repo = strings.TrimSuffix(repo, ".git")
			if repo == "" {
				continue
			}

			ownerRepo := owner + "/" + repo
			key := "git@github.com:" + ownerRepo
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, AllowedRepo{
				SSHHost:   "git@github.com",
				OwnerRepo: ownerRepo,
			})
		}
	}
	return result
}
