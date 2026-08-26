package service

import (
	"context"
	"fmt"
	"strings"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// SSHToHTTPS converts an SSH-form GitHub remote URL to its HTTPS equivalent.
// It handles two SSH URL forms:
//
//   - git@github.com:owner/repo.git  →  https://github.com/owner/repo.git
//   - ssh://git@github.com/owner/repo.git  →  https://github.com/owner/repo.git
//
// Returns (httpsURL, true) when a rewrite was performed; (rawURL, false)
// when the URL is already HTTPS, empty, or in an unrecognized form (including
// non-GitHub forges — callers must not rewrite those, as the MITM allowlist
// is github.com-specific). The caller should leave the remote unchanged when
// ok is false.
func SSHToHTTPS(rawURL string) (string, bool) {
	// scp-style: git@github.com:owner/repo.git
	if strings.HasPrefix(rawURL, "git@github.com:") {
		return "https://github.com/" + strings.TrimPrefix(rawURL, "git@github.com:"), true
	}
	// RFC 3986 ssh URL: ssh://git@github.com/owner/repo.git
	if strings.HasPrefix(rawURL, "ssh://git@github.com/") {
		return "https://github.com/" + strings.TrimPrefix(rawURL, "ssh://git@github.com/"), true
	}
	return rawURL, false
}

// SeedGitRemoteHTTPS rewrites the "origin" remote of the git repository at
// guestRepoPath from SSH form to HTTPS form via "git remote set-url".
// The rewrite is idempotent: an already-HTTPS remote is left unchanged.
// When the repository has no "origin" remote, the call silently succeeds.
//
// This must only be called on human git-VM sandboxes (AgentName == ""), not
// agent sandboxes. The HTTPS remote is required so that "git push" travels
// through the MITM proxy, which intercepts HTTPS traffic only.
//
// Returns nil when execer or guestRepoPath is empty/nil (no-op).
func SeedGitRemoteHTTPS(ctx context.Context, id domain.SandboxID, guestRepoPath string, execer GuestExecer) error {
	if guestRepoPath == "" || execer == nil {
		return nil
	}
	// POSIX sh one-liner: read the current origin URL; rewrite SSH→HTTPS in
	// place when it matches a known SSH prefix. Uses only builtins + git;
	// no sed/awk dependency. "$1" receives guestRepoPath (see argv below).
	//
	// "|| exit 0" after the get-url subshell makes a missing origin a silent
	// no-op: git remote get-url exits non-zero when the remote does not exist.
	const script = `u=$(git -C "$1" remote get-url origin 2>/dev/null) || exit 0
case "$u" in
  git@github.com:*)       git -C "$1" remote set-url origin "https://github.com/${u#git@github.com:}" ;;
  ssh://git@github.com/*) git -C "$1" remote set-url origin "https://github.com/${u#ssh://git@github.com/}" ;;
esac`
	// argv layout: /bin/sh -c SCRIPT SCRIPT_NAME REPO_PATH
	// "--" is the conventional positional $0 (script name); guestRepoPath
	// arrives as $1 inside the script.
	code, err := execer(ctx, id, []string{"/bin/sh", "-c", script, "--", guestRepoPath}, nil)
	if err != nil {
		return fmt.Errorf("seed git remote https: exec: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("seed git remote https: exit code %d", code)
	}
	return nil
}
