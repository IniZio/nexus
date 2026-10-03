package sprites

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir, file string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", file)
	git(t, dir, "commit", "-q", "-m", file)
}

func syncFixture(t *testing.T) (*Driver, domain.SandboxID, string, string) {
	t.Helper()
	host := filepath.Join(t.TempDir(), "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, host, "init", "-q", "-b", "main")
	commit(t, host, "a")
	commit(t, host, "b") // unpushed: no remote at all
	d, err := New(Config{StateDir: t.TempDir(), API: &localExecAPI{}})
	if err != nil {
		t.Fatal(err)
	}
	return d, domain.NewSandboxID(), host, filepath.Join(t.TempDir(), "guest")
}

func TestSeedExportRoundTrip(t *testing.T) {
	d, id, host, guest := syncFixture(t)
	ctx := context.Background()
	if err := d.SeedWorktree(ctx, id, host, "HEAD", guest); err != nil {
		t.Fatal(err)
	}
	if got, want := git(t, guest, "rev-parse", "HEAD"), git(t, host, "rev-parse", "HEAD"); got != want {
		t.Fatalf("guest head %s != host %s", got, want)
	}
	if out := git(t, guest, "remote"); out != "" {
		t.Fatalf("guest has remotes: %q", out)
	}
	if git(t, host, "for-each-ref", "refs/nx") != "" {
		t.Fatal("temp refs leaked on host")
	}
	// re-seed is idempotent
	if err := d.SeedWorktree(ctx, id, host, "main", guest); err != nil {
		t.Fatal(err)
	}
	commit(t, guest, "c")
	want := git(t, guest, "rev-parse", "HEAD")
	got, err := d.ExportWorktree(ctx, id, guest, host, "main")
	if err != nil || got != want {
		t.Fatalf("export = %q, %v; want %s", got, err, want)
	}
	if h := git(t, host, "rev-parse", "main"); h != want {
		t.Fatalf("host main = %s want %s", h, want)
	}
	if _, err := os.Stat(filepath.Join(host, "c")); err != nil {
		t.Fatalf("checked-out worktree not advanced: %v", err)
	}
	if git(t, host, "for-each-ref", "refs/nx") != "" {
		t.Fatal("temp refs leaked on host")
	}
}

func TestExportNoChange(t *testing.T) {
	d, id, host, guest := syncFixture(t)
	ctx := context.Background()
	if err := d.SeedWorktree(ctx, id, host, "HEAD", guest); err != nil {
		t.Fatal(err)
	}
	got, err := d.ExportWorktree(ctx, id, guest, host, "main")
	if err != nil || got != git(t, host, "rev-parse", "HEAD") {
		t.Fatalf("export = %q, %v", got, err)
	}
}

func TestExportRefusesNonFF(t *testing.T) {
	d, id, host, guest := syncFixture(t)
	ctx := context.Background()
	if err := d.SeedWorktree(ctx, id, host, "HEAD", guest); err != nil {
		t.Fatal(err)
	}
	commit(t, guest, "g")
	commit(t, host, "h") // host moved on
	before := git(t, host, "rev-parse", "main")
	_, err := d.ExportWorktree(ctx, id, guest, host, "main")
	if err == nil || !strings.Contains(err.Error(), "not a fast-forward") {
		t.Fatalf("err = %v", err)
	}
	if after := git(t, host, "rev-parse", "main"); after != before {
		t.Fatal("host branch moved on refused export")
	}
}

func TestExportNonCheckedOutBranch(t *testing.T) {
	d, id, host, guest := syncFixture(t)
	ctx := context.Background()
	git(t, host, "branch", "feat")
	if err := d.SeedWorktree(ctx, id, host, "feat", guest); err != nil {
		t.Fatal(err)
	}
	commit(t, guest, "f")
	want := git(t, guest, "rev-parse", "HEAD")
	if _, err := d.ExportWorktree(ctx, id, guest, host, "feat"); err != nil {
		t.Fatal(err)
	}
	if git(t, host, "rev-parse", "feat") != want {
		t.Fatal("feat not advanced")
	}
	if git(t, host, "rev-parse", "main") == want {
		t.Fatal("checked-out main moved")
	}
}

func TestExportNoGuestCommitsHostAdvanced(t *testing.T) {
	d, id, host, guest := syncFixture(t)
	ctx := context.Background()
	if err := d.SeedWorktree(ctx, id, host, "HEAD", guest); err != nil {
		t.Fatal(err)
	}
	commit(t, host, "h")
	before := git(t, host, "rev-parse", "main")
	if _, err := d.ExportWorktree(ctx, id, guest, host, "main"); err != nil {
		t.Fatal(err)
	}
	if git(t, host, "rev-parse", "main") != before {
		t.Fatal("host branch moved")
	}
}

func TestGuestStatus(t *testing.T) {
	d, id, host, guest := syncFixture(t)
	ctx := context.Background()
	if err := d.SeedWorktree(ctx, id, host, "HEAD", guest); err != nil {
		t.Fatal(err)
	}
	if out, err := d.GuestStatus(ctx, id, guest); err != nil || out != "" {
		t.Fatalf("clean = %q, %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(guest, "u"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := d.GuestStatus(ctx, id, guest); err != nil || !strings.Contains(out, "u") {
		t.Fatalf("dirty = %q, %v", out, err)
	}
}
