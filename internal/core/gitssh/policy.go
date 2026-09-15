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

// DeriveAllowlist maps nexus3.yaml egress entries to SSH relay allowlist entries (github.com only).
func DeriveAllowlist(policies []HostPolicy) []AllowedRepo {
	seen := map[string]struct{}{}
	var result []AllowedRepo

	for _, p := range policies {
		if !strings.EqualFold(p.Host, "github.com") {
			continue
		}
		for _, rawPath := range p.Paths {
			path := strings.TrimPrefix(rawPath, "/")
			path = strings.TrimSuffix(path, "/**")
			path = strings.TrimSuffix(path, ".git/**")
			path = strings.TrimSuffix(path, ".git")

			segments := strings.SplitN(path, "/", 3)
			if len(segments) < 2 {
				continue
			}
			owner := segments[0]
			repo := segments[1]

			if owner == "" || owner == "." || owner == ".." {
				continue
			}
			if repo == "" || repo == "." || repo == ".." {
				continue
			}
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
