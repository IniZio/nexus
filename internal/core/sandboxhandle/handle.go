// Package sandboxhandle provides shared handle and slug derivation used by
// both the CLI plugin and the controller backend.
package sandboxhandle

import "strings"

// Slug converts a nexus sandbox handle to a VolumeStore-legal slug.
// Uppercase letters are lowercased; any char not in [a-z0-9._] collapses to
// one '-'; leading/trailing separators are trimmed. Falls back to "wt".
func Slug(handle string) string {
	var b strings.Builder
	prev := byte('-')
	for i := 0; i < len(handle); i++ {
		c := handle[i]
		switch {
		case c >= 'A' && c <= 'Z':
			lc := c + ('a' - 'A')
			b.WriteByte(lc)
			prev = lc
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_':
			b.WriteByte(c)
			prev = c
		default:
			if prev != '-' {
				b.WriteByte('-')
				prev = '-'
			}
		}
	}
	slug := strings.Trim(b.String(), "-._")
	if slug == "" {
		slug = "wt"
	}
	return slug
}

// WorktreeHandle derives a deterministic sandbox handle from a repository
// name and a worktree identity (typically the worktree directory basename).
// Format: "<repoName>/<identity>" with sanitisation applied independently:
// chars not in [A-Za-z0-9._-] become '-'; consecutive '-' collapse;
// leading/trailing '-' are trimmed; empty parts fall back to "repo"/"worktree".
func WorktreeHandle(repoName, identity string) string {
	sanitize := func(s, fallback string) string {
		var b strings.Builder
		prev := '-'
		for _, r := range s {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
				b.WriteRune(r)
				prev = r
			} else {
				if prev != '-' {
					b.WriteByte('-')
				}
				prev = '-'
			}
		}
		slug := strings.Trim(b.String(), "-")
		if slug == "" {
			return fallback
		}
		return slug
	}
	return sanitize(repoName, "repo") + "/" + sanitize(identity, "worktree")
}
