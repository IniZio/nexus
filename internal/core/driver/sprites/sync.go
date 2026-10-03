package sprites

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
)

var _ driver.WorktreeSyncer = (*Driver)(nil)

const (
	seedRef      = "refs/nx/seed"
	bundleSrcRef = "refs/nx/bundle-src"
	exportRef    = "refs/nx/export"
)

const seedScript = `set -e
d=$1; sha=$2
t=$(mktemp); trap 'rm -f "$t"' EXIT
cat > "$t"
[ -d "$d/.git" ] || git init -q -- "$d"
cd "$d"
git fetch -q "$t" "+` + bundleSrcRef + `:` + seedRef + `"
[ "$(git rev-parse ` + seedRef + `)" = "$sha" ]
git checkout -q -f --detach "$sha"`

func hostGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func (d *Driver) guestRun(ctx context.Context, id domain.SandboxID, req ExecRequest) error {
	stderr := &tailBuffer{max: copyStderrCap}
	req.Stderr = stderr
	code, err := d.api.Exec(ctx, SpriteName(id), req)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("remote exit %d: %s", code, bytes.TrimSpace(stderr.buf))
	}
	return nil
}

// SeedWorktree streams a git bundle of ref (full history, unpushed commits
// included) into the sprite and checks out the identical commit detached.
// No remote and no credentials are involved.
func (d *Driver) SeedWorktree(ctx context.Context, id domain.SandboxID, hostRepoDir, ref, guestDir string) error {
	if guestDir == "" || strings.HasPrefix(ref, "-") || ref == "" {
		return errors.New("sprites seed: guest dir and ref required")
	}
	sha, err := hostGit(ctx, hostRepoDir, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return fmt.Errorf("sprites seed: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// a bundle needs a ref name, so pin the sha under a temp ref
	tmp := bundleSrcRef
	if _, err := hostGit(ctx, hostRepoDir, "update-ref", tmp, sha); err != nil {
		return fmt.Errorf("sprites seed: %w", err)
	}
	defer hostGit(context.WithoutCancel(ctx), hostRepoDir, "update-ref", "-d", tmp)
	cmd := exec.CommandContext(ctx, "git", "-C", hostRepoDir, "bundle", "create", "-", tmp)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	berr := &tailBuffer{max: copyStderrCap}
	cmd.Stderr = berr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("sprites seed: bundle: %w", err)
	}
	gerr := d.guestRun(ctx, id, ExecRequest{
		Argv:  []string{"sh", "-c", seedScript, "sh", guestDir, sha},
		Stdin: pipe,
	})
	if gerr != nil {
		cancel()
	}
	werr := cmd.Wait()
	if gerr != nil {
		return fmt.Errorf("sprites seed: %w", gerr)
	}
	if werr != nil {
		return fmt.Errorf("sprites seed: bundle: %w: %s", werr, bytes.TrimSpace(berr.buf))
	}
	return nil
}

// ExportWorktree bundles the commits made in the sprite since the seed,
// fetches them into hostRepoDir and fast-forwards branch to the new head.
func (d *Driver) ExportWorktree(ctx context.Context, id domain.SandboxID, guestDir, hostRepoDir, branch string) (string, error) {
	if guestDir == "" || branch == "" || strings.HasPrefix(branch, "-") {
		return "", errors.New("sprites export: guest dir and branch required")
	}
	var head bytes.Buffer
	if err := d.guestRun(ctx, id, ExecRequest{
		Argv:   []string{"git", "-C", guestDir, "rev-parse", "HEAD"},
		Stdout: &head,
	}); err != nil {
		return "", fmt.Errorf("sprites export: %w", err)
	}
	newHead := strings.TrimSpace(head.String())
	oldTip, err := hostGit(ctx, hostRepoDir, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		return "", fmt.Errorf("sprites export: %w", err)
	}
	if newHead == oldTip {
		return newHead, nil
	}
	f, err := os.CreateTemp("", "nx-export-*.bundle")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := d.guestRun(ctx, id, ExecRequest{
		Argv:   []string{"git", "-C", guestDir, "bundle", "create", "-", seedRef + "..HEAD"},
		Stdout: f,
	}); err != nil {
		return "", fmt.Errorf("sprites export: %w", err)
	}
	if _, err := hostGit(ctx, hostRepoDir, "fetch", "-q", f.Name(), "+HEAD:"+exportRef); err != nil {
		return "", fmt.Errorf("sprites export: %w", err)
	}
	defer hostGit(context.WithoutCancel(ctx), hostRepoDir, "update-ref", "-d", exportRef)
	if got, err := hostGit(ctx, hostRepoDir, "rev-parse", exportRef); err != nil || got != newHead {
		return "", fmt.Errorf("sprites export: fetched head %q != guest head %q (%v)", got, newHead, err)
	}
	if _, err := hostGit(ctx, hostRepoDir, "merge-base", "--is-ancestor", oldTip, newHead); err != nil {
		return "", fmt.Errorf("sprites export: not a fast-forward: host %s has commits the sprite lacks (host %s, sprite %s)", branch, oldTip, newHead)
	}
	cur, _ := hostGit(ctx, hostRepoDir, "symbolic-ref", "--short", "-q", "HEAD")
	if cur == branch {
		_, err = hostGit(ctx, hostRepoDir, "merge", "--ff-only", "-q", newHead)
	} else {
		_, err = hostGit(ctx, hostRepoDir, "update-ref", "refs/heads/"+branch, newHead, oldTip)
	}
	if err != nil {
		return "", fmt.Errorf("sprites export: %w", err)
	}
	return newHead, nil
}
