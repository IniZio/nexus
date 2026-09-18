package service

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/IniZio/nexus/internal/core/domain"
)

// ── git configuration constants ──
// GuestGitconfigPath is where per-sandbox git identity is written.
const GuestGitconfigPath = "/etc/gitconfig"

// GuestGitCredentialHelperPath is the guest path for the credential-helper script.
const GuestGitCredentialHelperPath = "/usr/local/bin/nexus-git-credential"

// GuestGitCredentialHelperScript is the credential-helper script (POSIX sh, GH_TOKEN never to config/URLs).
const GuestGitCredentialHelperScript = `#!/bin/sh
# nexus git credential helper.
case "$1" in
get)
	[ -n "${GH_TOKEN}" ] || exit 0
	printf 'username=x-token-auth\n'
	printf 'password=%s\n' "${GH_TOKEN}"
	;;
esac
exit 0
`

const GitCloneHeadroomBytes int64 = 89 * 1024 * 1024 // D-PD-19: ~89 MiB shallow clone → ~178 MiB ext4 projection; guard for ≥17 GiB hosts

func sandboxShortID(id domain.SandboxID) string {
	s := id.String()
	raw := strings.TrimPrefix(s, "sb-")
	if len(raw) > 8 {
		raw = raw[len(raw)-8:]
	}
	return strings.ToLower(raw)
}

var hostGitConfigGet = gitConfigGetImpl // for test injection

func gitConfigGetImpl(key string) (string, error) {
	out, err := exec.Command("git", "config", "--global", "--get", key).Output()
	if err != nil {
		// Exit code 1: key not set (not a hard error).
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("run git config --global --get %s: %w", key, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// HostGitIdentity reads operator's git identity from host config (clawk pattern, D-PD-02, N-AC1 unchanged).
func HostGitIdentity() (name, email string, err error) {
	name, err = hostGitConfigGet("user.name")
	if err != nil {
		return "", "", fmt.Errorf("git_identity: read host git user.name: %w", err)
	}
	if name == "" {
		return "", "", fmt.Errorf(
			"git_identity: host git user.name is not configured; " +
				"fix with: git config --global user.name 'Your Name'",
		)
	}
	email, err = hostGitConfigGet("user.email")
	if err != nil {
		return "", "", fmt.Errorf("git_identity: read host git user.email: %w", err)
	}
	if email == "" {
		return "", "", fmt.Errorf(
			"git_identity: host git user.email is not configured; " +
				"fix with: git config --global user.email 'you@example.com'",
		)
	}
	return name, email, nil
}

func sanitizeBranchSlug(slug string) string {
	var buf strings.Builder
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '/':
			buf.WriteRune(r)
		default:
			buf.WriteByte('-')
		}
	}
	return strings.Trim(buf.String(), "-")
}

// SandboxBranchName returns git branch nexus/<motive>/<id> (D-PD-03); deterministic.
func SandboxBranchName(labels map[string]string, id domain.SandboxID) string {
	slug := labels["motive"]
	if slug == "" {
		slug = "default"
	}
	slug = sanitizeBranchSlug(slug)
	if slug == "" {
		slug = "default"
	}
	return fmt.Sprintf("nexus/%s/%s", slug, sandboxShortID(id))
}

// SourceGuestPaths collects in-guest paths for git safe.directory entries.
func SourceGuestPaths(workspacePath string, liveMounts []domain.LiveMount) []string {
	var paths []string
	if workspacePath != "" {
		paths = append(paths, workspacePath)
	}
	for _, m := range liveMounts {
		if m.GuestPath != "" {
			paths = append(paths, m.GuestPath)
		}
	}
	return paths
}

func buildGitconfigPayload(name, email string, sourcePaths []string, branch string) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "[user]\n")
	fmt.Fprintf(&buf, "\tname = %s\n", name)
	fmt.Fprintf(&buf, "\temail = %s\n", email)
	var dirs []string
	for _, p := range sourcePaths {
		if p != "" {
			dirs = append(dirs, p)
		}
	}
	if len(dirs) > 0 {
		fmt.Fprintf(&buf, "[safe]\n")
		for _, d := range dirs {
			fmt.Fprintf(&buf, "\tdirectory = %s\n", d)
		}
	}
	fmt.Fprintf(&buf, "[init]\n")
	fmt.Fprintf(&buf, "\tdefaultBranch = %s\n", branch)
	fmt.Fprintf(&buf, "[core]\n")
	fmt.Fprintf(&buf, "\tsafecrlf = false\n")
	buf.WriteString("\tsshCommand = /sbin/nexus-agent git-ssh\n")
	buf.WriteString("[credential \"https://github.com\"]\n")
	buf.WriteString("\thelper = !sh " + GuestGitCredentialHelperPath + "\n")

	return buf.Bytes()
}

// SeedGitIdentity writes gitconfig (identity/safe dirs/branch D-PD-03); no token/key; N-AC1 (TestN_AC1_NoGitHubEgressPermitted).
func SeedGitIdentity(
	ctx context.Context,
	id domain.SandboxID,
	labels map[string]string,
	sourcePaths []string,
	seeder GuestSeeder,
) (branch string, err error) {
	branch = SandboxBranchName(labels, id)
	if seeder == nil {
		return branch, nil
	}
	name, email, err := HostGitIdentity()
	if err != nil {
		return branch, err
	}
	payload := buildGitconfigPayload(name, email, sourcePaths, branch)
	if err := seeder(ctx, id, payload); err != nil {
		return branch, fmt.Errorf("git_identity: write %s: %w", GuestGitconfigPath, err)
	}
	return branch, nil
}

// SeedGitCredentialHelper writes credential-helper script to the guest.
func SeedGitCredentialHelper(ctx context.Context, id domain.SandboxID, seeder GuestSeeder) error {
	if seeder == nil {
		return nil
	}
	if err := seeder(ctx, id, []byte(GuestGitCredentialHelperScript)); err != nil {
		return fmt.Errorf("git_identity: write %s: %w", GuestGitCredentialHelperPath, err)
	}
	return nil
}

// HostHeadSHA returns HEAD SHA (40-hex) for BaseRef (D-PD-19); ("", nil) if repoPath empty.
func HostHeadSHA(repoPath string) (string, error) {
	if repoPath == "" {
		return "", nil
	}
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("git_identity: rev-parse HEAD in %s: %w", repoPath, err)
	}
	sha := strings.TrimSpace(string(out))
	if len(sha) != 40 {
		return "", fmt.Errorf("git_identity: unexpected HEAD SHA length %d (want 40) in %s: %q", len(sha), repoPath, sha)
	}
	return sha, nil
}

func isGitHubHost(h string) bool {
	return domain.IsGitHubHost(h)
}
