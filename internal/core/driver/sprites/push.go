package sprites

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/IniZio/nexus/internal/core/domain"
)

// Sync modes: bundle (default) moves work as git bundles over exec and needs
// no credentials; push clones origin in the sprite and pushes a task branch
// through the host broker (placeholder GH_TOKEN only).
const (
	SyncBundle = "bundle"
	SyncPush   = "push"
)

// PushHosts are the hosts push mode adds to the egress allowlist.
var PushHosts = []string{"github.com", "api.github.com"}

// CredentialHelper reads $GH_TOKEN from the environment of the git call; the
// token is never written to disk.
const (
	CredentialHelperKey   = "credential.https://github.com.helper"
	CredentialHelperValue = `!f() { echo username=x-access-token; echo password=$GH_TOKEN; }; f`
)

// NormalizeSyncMode maps "" to bundle and rejects unknown modes.
func NormalizeSyncMode(m string) (string, error) {
	switch m {
	case "", SyncBundle:
		return SyncBundle, nil
	case SyncPush:
		return SyncPush, nil
	}
	return "", fmt.Errorf("sprites: sync mode %q unsupported (want %s or %s)", m, SyncBundle, SyncPush)
}

// stripURLCreds drops any userinfo from an http(s) URL.
func stripURLCreds(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.User == nil {
		return u
	}
	p.User = nil
	return p.String()
}

// SSHToHTTPS rewrites git@host:path and ssh://git@host/path to https; sprites
// hold no ssh keys. Other URLs pass through.
func SSHToHTTPS(u string) string {
	if rest, ok := strings.CutPrefix(u, "ssh://git@"); ok {
		return "https://" + rest
	}
	if rest, ok := strings.CutPrefix(u, "git@"); ok {
		if host, path, ok := strings.Cut(rest, ":"); ok && !strings.Contains(host, "/") {
			return "https://" + host + "/" + path
		}
	}
	return u
}

const pushPrepScript = `set -e
cd "$1"; b=$2
git check-ref-format --branch "$b" >/dev/null
git config "$3" "$4"
if git fetch -q origin "refs/heads/$b:refs/remotes/origin/$b" 2>/dev/null; then
  git checkout -q -B "$b" "origin/$b"
else
  git checkout -q -B "$b"
fi
git config "branch.$b.remote" origin
git config "branch.$b.merge" "refs/heads/$b"`

// validPushBranch applies git check-ref-format rules plus no glob characters:
// the mitm allowlist matches branches as globs, so a metacharacter would widen it.
func validPushBranch(b string) error {
	bad := func(why string) error { return fmt.Errorf("branch %q invalid: %s", b, why) }
	switch {
	case b == "" || b == "@":
		return bad("empty")
	case strings.HasPrefix(b, "-"):
		return bad("leading -")
	case strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".") || strings.HasSuffix(b, ".lock"):
		return bad("bad suffix")
	case strings.Contains(b, "..") || strings.Contains(b, "@{"):
		return bad("contains .. or @{")
	}
	for _, r := range b {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("*?[]\\~^:", r) {
			return bad("forbidden character")
		}
	}
	for _, c := range strings.Split(b, "/") {
		if c == "" || strings.HasPrefix(c, ".") || strings.HasSuffix(c, ".lock") {
			return bad("bad path component")
		}
	}
	return nil
}

// PreparePushBranch checks out the task branch in the cloned sprite repo
// (from origin/<branch> when it exists, else from the clone's HEAD) and wires
// its upstream and the env-only credential helper.
func (d *Driver) PreparePushBranch(ctx context.Context, id domain.SandboxID, guestDir, branch string) error {
	if guestDir == "" || branch == "" {
		return fmt.Errorf("sprites push prepare: guest dir and branch required")
	}
	if err := validPushBranch(branch); err != nil {
		return fmt.Errorf("sprites push prepare: %w", err)
	}
	spec, specErr := d.Spec(id)
	if want := []string{"refs/heads/" + branch}; specErr == nil && !slices.Equal(spec.AllowedBranches, want) {
		spec.AllowedBranches = want
		if err := d.writeSpec(id, spec); err != nil {
			return fmt.Errorf("sprites push prepare: %w", err)
		}
		if err := d.stopBroker(id); err != nil {
			return fmt.Errorf("sprites push prepare: %w", err)
		}
	}
	genv, err := d.brokerEnv(ctx, id, spec, map[string]string{"GIT_TERMINAL_PROMPT": "0"})
	if err != nil {
		return fmt.Errorf("sprites push prepare: %w", err)
	}
	stderr := &tailBuffer{max: copyStderrCap}
	code, err := d.api.Exec(ctx, SpriteName(id), ExecRequest{
		Argv:   []string{"sh", "-c", pushPrepScript, "sh", guestDir, branch, CredentialHelperKey, CredentialHelperValue},
		Env:    genv,
		Stderr: stderr,
	})
	if err != nil {
		return fmt.Errorf("sprites push prepare: %s", err.Error())
	}
	if code != 0 {
		return fmt.Errorf("sprites push prepare: exit %d: %s", code, string(bytes.TrimSpace(stderr.buf)))
	}
	if hasGuestGoMod(ctx, d, id, guestDir) {
		return d.warmGo(ctx, id, guestDir)
	}
	return nil
}

func hasGuestGoMod(ctx context.Context, d *Driver, id domain.SandboxID, guestDir string) bool {
	return d.guestRun(ctx, id, ExecRequest{Argv: []string{"git", "-C", guestDir, "cat-file", "-e", "HEAD:go.mod"}}) == nil
}

// SyncMode returns the persisted sync mode of id; sandboxes provisioned before
// push mode existed are bundle.
func (d *Driver) SyncMode(id domain.SandboxID) (string, error) {
	s, err := d.Spec(id)
	if err != nil {
		return "", err
	}
	return NormalizeSyncMode(s.Sync)
}

// GuestUnpushed returns a description of what is not on the remote branch;
// empty means HEAD is pushed to its upstream.
func (d *Driver) GuestUnpushed(ctx context.Context, id domain.SandboxID, guestDir string) (string, error) {
	var out bytes.Buffer
	err := d.guestRun(ctx, id, ExecRequest{
		Argv:   []string{"git", "-C", guestDir, "rev-list", "--count", "@{u}..HEAD"},
		Stdout: &out,
	})
	if err != nil {
		return "branch has no pushed upstream (" + err.Error() + ")", nil
	}
	n, perr := strconv.Atoi(strings.TrimSpace(out.String()))
	if perr != nil {
		return "", fmt.Errorf("sprites unpushed: parse %q", out.String())
	}
	if n > 0 {
		return fmt.Sprintf("%d commit(s) not pushed to the upstream branch", n), nil
	}
	return "", nil
}
