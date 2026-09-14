package service

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// GuestGitconfigPath is the well-known path inside the guest where the
// per-sandbox git identity configuration is written by SeedGitIdentity.
// Git reads /root/.gitconfig automatically when running as root (the
// standard in-guest agent user).
const GuestGitconfigPath = "/root/.gitconfig"

// GuestGitCredentialHelperPath is the well-known guest path for the
// credential-helper script seeded by SeedGitCredentialHelper.
// The gitconfig references it as: helper = !sh /usr/local/bin/nexus3-git-credential
// Using "!sh <path>" avoids any git config-parser quoting issues that arise
// with inline shell functions (double-quotes and \n sequences are
// interpreted by the config parser before sh ever sees them).
const GuestGitCredentialHelperPath = "/usr/local/bin/nexus3-git-credential"

// GuestGitCredentialHelperScript is the content of the credential-helper
// script placed at GuestGitCredentialHelperPath inside the guest.
//
// The script reads $GH_TOKEN from the guest environment at push time and
// emits the username/password pair that git's credential protocol expects.
// It is a no-op (exits 0, no output) when GH_TOKEN is unset.
//
// Shell compatibility: /bin/sh in the guest is dash (Debian bookworm).
// The script uses only POSIX constructs — case, printf, [ -n ] — and
// deliberately avoids bash, python3, jq, and curl, none of which are
// guaranteed to be present in the guest base image.
//
// Security: GH_TOKEN is the 64-hex MITM placeholder swapped in by the
// perimeter proxy. It is never written into a config file or remote URL.
const GuestGitCredentialHelperScript = `#!/bin/sh
# nexus3 git credential helper.
case "$1" in
get)
	[ -n "${GH_TOKEN}" ] || exit 0
	printf 'username=x-token-auth\n'
	printf 'password=%s\n' "${GH_TOKEN}"
	;;
esac
exit 0
`

// GitCloneHeadroomBytes is the additional workspace disk space consumed by
// a depth-1 shallow git clone of the host repository injected at seed time
// (decision D-PD-19). Measured from example-app: ~89 MiB.
//
// # TBD-OI-4 corrected headroom arithmetic
//
// The workspace ext4 image formula (from builder/worktreedisk.go) is:
//
//	projected = worktreeBytes × 2 + 64 MiB   (imageSizeHeadroomFactor=2, imageMinSizeBytes=64 MiB)
//
// The auto-mode guard: projected must be < 80 % of host free space.
//
// G1 injects an ~89 MiB shallow .git clone into the workspace disk content.
// This changes the arithmetic:
//
//	projected_new = (worktreeBytes + 89 MiB) × 2 + 64 MiB
//	             = projected_old + 89 MiB × 2
//	             = projected_old + 178 MiB
//
// So the git clone injection adds approximately 178 MiB to the projected
// ext4 image size.
//
// For the example-app pilot (worktree 6.36 GiB, host 50 GiB free):
//
//	projected_old = 6.36 × 2 + 0.064 = 12.784 GiB
//	projected_new = 12.784 + 0.178   = 12.962 GiB
//	safe          = 50 × 0.80        = 40 GiB
//	12.962 < 40 → PASSES
//
// Finding: the existing headroom formula accommodates the 89 MiB injection
// on hosts with ≥ 17 GiB free (the tightest feasible case). The injection is
// automatically covered when the .git content is present in the workspace
// source directory before WorktreeToDisk is called — the existing preflight
// guard sees it and accounts for it. No separate guard is required UNLESS
// G1 injects .git AFTER WorktreeToDisk measures the directory, in which case
// a pre-injection headroom check using this constant is required.
const GitCloneHeadroomBytes int64 = 89 * 1024 * 1024

// sandboxShortID returns 8 characters from the Crockford base32 portion of
// the sandbox ID (after the "sb-" prefix), lowercased.
//
// UUIDv7 encodes a 48-bit millisecond timestamp in the HIGH-order bytes,
// with random bits in the LOW-order bytes. The 26-char base32 string is
// MSB-first, so the FIRST chars are pure timestamp and IDENTICAL for any two
// IDs minted within the same millisecond. To keep the short ID unique we take
// the LAST 8 chars, which cover the most-random portion of the UUID.
//
// The same SandboxID always produces the same result (deterministic).
func sandboxShortID(id domain.SandboxID) string {
	s := id.String() // "sb-XXXXXXXXXXXXXXXXXXXXXXXXXXXX" (sb- + 26 chars)
	// Strip "sb-" prefix; take the last 8 chars (random portion of UUIDv7).
	raw := strings.TrimPrefix(s, "sb-")
	if len(raw) > 8 {
		raw = raw[len(raw)-8:]
	}
	return strings.ToLower(raw)
}

// hostGitConfigGet is the function used to read a single git config key from
// the host's global config. It is a package-level var so tests can inject a
// stub that returns controlled values without needing a real git binary or a
// specific global git config on the test host.
var hostGitConfigGet = gitConfigGetImpl

// gitConfigGetImpl shells out to `git config --global --get <key>` and
// returns the trimmed value. An exit code of 1 means the key is not set
// (returns "", nil). Any other error (git not found, permission error, etc.)
// is returned as-is.
func gitConfigGetImpl(key string) (string, error) {
	out, err := exec.Command("git", "config", "--global", "--get", key).Output()
	if err != nil {
		// Exit code 1: key not configured — not a hard error.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return "", nil
		}
		return "", fmt.Errorf("run git config --global --get %s: %w", key, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// HostGitIdentity reads the operator's git user.name and user.email from the
// host's global git config (via `git config --global --get`). Both fields
// must be configured; if either is absent an actionable error is returned
// telling the operator exactly which command to run.
//
// This follows the clawk pattern: bake the operator's real identity into the
// guest ~/.gitconfig at sandbox-create time so commits are attributed to
// them, never to a synthetic bot account (operator decision 2026-08-15,
// reversing D-PD-02).
//
// Identity is orthogonal to credentials: only name and email are read here.
// No token, SSH key, gh-auth state, or host credential file is forwarded
// into the guest. The N-AC1 security rail is unchanged.
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

// sanitizeBranchSlug replaces characters that are invalid in git branch names
// with hyphens and trims leading/trailing hyphens. Git prohibits, among
// others: spaces, ~, ^, :, ?, *, [, \, .., @{, consecutive dots, and
// control characters. This replacement is conservative — it passes through
// alphanumerics, hyphens, underscores, and forward slashes.
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

// SandboxBranchName returns the git branch name for a sandbox under a motive.
// Format: nexus3/<motive-slug>/<short-id> (D-PD-03).
//
// The motive slug is derived from labels["motive"]; "default" is used when
// the label is absent. The branch name is deterministic: the same labels and
// SandboxID always produce the same string. The short ID is taken from the
// random (low-order) bytes of the UUIDv7, so two sandboxes minted within the
// same millisecond still get distinct branch names. The host-side push guard
// (P1-AC3) enforces the nexus3/ prefix pattern.
func SandboxBranchName(labels map[string]string, id domain.SandboxID) string {
	slug := labels["motive"]
	if slug == "" {
		slug = "default"
	}
	slug = sanitizeBranchSlug(slug)
	if slug == "" {
		slug = "default"
	}
	return fmt.Sprintf("nexus3/%s/%s", slug, sandboxShortID(id))
}

// SourceGuestPaths collects the in-guest paths of all directories that need a
// git safe.directory entry: the capture-style workspace (if any) plus all live
// virtiofs mounts (--mount). Empty strings are silently skipped.
//
// Both CreateAndBoot (create.go) and the supervisor's RunDetached use this to
// build the sourcePaths list passed to SeedGitIdentity. Extracting it here
// makes the collection logic testable without a running VM.
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

// buildGitconfigPayload serialises the per-sandbox git configuration as a
// gitconfig INI file. The payload is safe to write directly to
// GuestGitconfigPath (/root/.gitconfig) inside the guest.
//
// Fields set:
//   - [user] name / email: the operator's host git identity (resolved by
//     HostGitIdentity at sandbox-create time; operator decision 2026-08-15)
//   - [safe] directory:    one entry per element of sourcePaths (prevents
//     "dubious ownership" errors for workspace and --mount directories)
//   - [init] defaultBranch: the per-sandbox branch name (D-PD-03)
//   - [core] safecrlf=false: suppress line-ending conversion warnings in the guest
//   - [credential "https://github.com"] helper: reads $GH_TOKEN from the guest
//     environment at push time; produces no credentials when the variable is
//     absent; the token is never written into the file (see inline comment below)
//
// git's config format allows multiple directory = entries under a single [safe]
// section; all entries are written there. Empty paths in sourcePaths are skipped.
func buildGitconfigPayload(name, email string, sourcePaths []string, branch string) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "[user]\n")
	fmt.Fprintf(&buf, "\tname = %s\n", name)
	fmt.Fprintf(&buf, "\temail = %s\n", email)
	// Collect non-empty paths; emit all under one [safe] block.
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
	// SSH command: route all git SSH sessions through the nexus3-agent shim.
	// The shim dials the host relay over vsock and the host invokes the real
	// ssh using SSH_AUTH_SOCK — no private key is ever seeded into the guest.
	buf.WriteString("\tsshCommand = /sbin/nexus3-agent git-ssh\n")
	// Credential helper for github.com pushes.
	//
	// This section is unconditional: every sandbox gets the helper regardless of
	// whether a GitHub secret is currently bound, because:
	//   (a) The helper is environment-driven — it reads $GH_TOKEN at push time
	//       and produces no credentials when the variable is absent, so it is a
	//       no-op on sandboxes without a token.
	//   (b) Adding it only when a GitHub secret is bound would require threading
	//       that decision through SeedGitIdentity's signature and into supervisor.go
	//       (owned by a different slice in this wave). Keeping the signature
	//       unchanged is the minimal, safe choice.
	//   (c) No token is written into the gitconfig file: the file is safe to log,
	//       inspect, and commit without ever revealing a credential.
	//
	// Helper protocol: git calls the helper with argument "get" (also "store",
	// "erase"). We respond only to "get". Output format is key=value, one per line.
	//
	// Shell compatibility: git executes "!" helpers via sh -c, which on Debian
	// bookworm is dash. The helper uses only POSIX shell constructs (case, printf,
	// [ -n ]) — no bashisms, no python3, no jq, no curl.
	//
	// Degradation when $GH_TOKEN is unset: the script's [ -n "${GH_TOKEN}" ] || exit 0
	// check exits 0 immediately, producing no output. Git sees no credential and
	// falls through to its normal error path (e.g. a 401 from the remote).
	// No prompt, no confusing error message from the helper itself.
	//
	// Security: $GH_TOKEN is the 64-hex placeholder swapped by the MITM proxy.
	// It is read from the guest environment at push time; it never appears in the
	// gitconfig file itself, in remote URLs, or in git's reflog.
	buf.WriteString("[credential \"https://github.com\"]\n")
	// Reference the script by path. The "!sh <path>" form contains no characters
	// that git's config parser treats specially (no double-quotes, no \n
	// sequences), so the value round-trips through the parser unchanged.
	// The script is seeded separately via SeedGitCredentialHelper.
	buf.WriteString("\thelper = !sh " + GuestGitCredentialHelperPath + "\n")

	return buf.Bytes()
}

// SeedGitIdentity pushes a per-sandbox git configuration file to the guest
// via seeder. The file is written to GuestGitconfigPath (/root/.gitconfig)
// and configures the operator's host git identity (user.name, user.email
// read from the host's global git config at create time), a safe.directory
// entry for every element of sourcePaths (workspace + --mount directories),
// and the per-sandbox branch name (D-PD-03).
//
// seeder must be a GuestSeeder that targets GuestGitconfigPath. Callers
// should use NewGuestFileSeeder(client, GuestGitconfigPath) to produce the
// appropriate seeder. If seeder is nil, SeedGitIdentity is a no-op and the
// branch name is still returned (for logging).
//
// Returns the per-sandbox branch name (e.g. "nexus3/my-feature/ab12cd34").
// The branch name is deterministic: repeated calls with the same inputs
// return the same string.
//
// # Failure on missing host identity
//
// When seeder is non-nil, HostGitIdentity() is called to resolve the
// operator's name and email. If either is absent from the host's global git
// config, SeedGitIdentity returns an actionable error. A silent fallback to
// a synthetic identity is deliberately not provided: wrong attribution is
// worse than a failed create.
//
// # Security invariant
//
// The gitconfig payload contains only the operator's name/email, git
// configuration, and an environment-driven GitHub credential helper.
// No token, SSH key, or static credential is written into the file.
// The credential helper reads $GH_TOKEN from the guest environment at push
// time (the 64-hex MITM placeholder); it never embeds the value in the file.
// D-PD-22: an AGENT sandbox must never list github.com in AllowedHosts.
// See N-AC1 (TestN_AC1_NoGitHubEgressPermitted).
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

// SeedGitCredentialHelper pushes GuestGitCredentialHelperScript to
// GuestGitCredentialHelperPath inside the guest via seeder. The script must
// be present before any git push that relies on the credential helper written
// into GuestGitconfigPath by SeedGitIdentity.
//
// seeder should be NewGuestFileSeeder(client, GuestGitCredentialHelperPath).
// When seeder is nil, SeedGitCredentialHelper is a no-op.
func SeedGitCredentialHelper(ctx context.Context, id domain.SandboxID, seeder GuestSeeder) error {
	if seeder == nil {
		return nil
	}
	if err := seeder(ctx, id, []byte(GuestGitCredentialHelperScript)); err != nil {
		return fmt.Errorf("git_identity: write %s: %w", GuestGitCredentialHelperPath, err)
	}
	return nil
}

// HostHeadSHA returns the current HEAD commit SHA (40-hex) of the git
// repository at repoPath, suitable for recording as the sandbox's BaseRef
// (D-PD-19).
//
// BaseRef marks the shallow-clone boundary: the guest's depth-1 git clone
// starts at this commit, and G2 (nexus3 bundle) uses it as the bundle anchor.
//
// Returns ("", nil) when repoPath is empty. Returns an error if git is not
// available or the path is not a git repository.
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

// isGitHubHost reports whether h is any GitHub or GitHub-adjacent hostname.
// Delegates to domain.IsGitHubHost, which is the authoritative definition
// shared with the MITM proxy layer.
func isGitHubHost(h string) bool {
	return domain.IsGitHubHost(h)
}
