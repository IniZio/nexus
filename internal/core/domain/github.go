package domain

import "strings"

// IsGitHubHost reports whether h is any GitHub or GitHub-adjacent hostname.
// This is the authoritative definition used by both the service layer (to
// determine whether a --secret bind requires a per-repo path guard) and the
// MITM proxy (to decide whether D-PD-36 path filtering applies to an
// intercepted request).
//
// Conservative: covers github.com, every subdomain (*.github.com — api, ssh,
// codeload, …), and GitHub's content-delivery domain (githubusercontent.com and
// *.githubusercontent.com). A single trailing dot (valid FQDN form) is stripped
// before comparison so "api.github.com." and "api.github.com" are equivalent.
// The input is otherwise lowercased and whitespace-trimmed before comparison.
func IsGitHubHost(h string) bool {
	h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
	return h == "github.com" ||
		h == "githubusercontent.com" ||
		strings.HasSuffix(h, ".github.com") ||
		strings.HasSuffix(h, ".githubusercontent.com")
}
