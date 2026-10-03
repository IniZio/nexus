//go:build spriteslive

package herdrworktree

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
)

func hgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestSpriteTeardownGuardLive(t *testing.T) {
	if os.Getenv("SPRITES_TOKEN") == "" && os.Getenv("SPRITES_API_TOKEN") == "" {
		t.Skip("set SPRITES_TOKEN")
	}
	host := filepath.Join(t.TempDir(), "host")
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatal(err)
	}
	hgit(t, host, "init", "-q", "-b", "main")
	for _, f := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(host, f), []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
		hgit(t, host, "add", f)
		hgit(t, host, "commit", "-q", "-m", f)
	}

	drv, err := sprites.New(sprites.Config{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	id := domain.NewSandboxID()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := drv.Provision(ctx, id, sprites.Spec{}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Logf("sprite %s", sprites.SpriteName(id))
	defer func() {
		if err := drv.Deprovision(context.Background(), id); err != nil {
			t.Errorf("Deprovision: %v", err)
		}
	}()
	if err := drv.SeedWorktree(ctx, id, host, "HEAD", sprites.CloneDir); err != nil {
		t.Fatalf("Seed: %v", err)
	}
	guest := func(script string) {
		t.Helper()
		var out bytes.Buffer
		code, err := drv.Exec(ctx, id, driver.ExecOptions{Argv: []string{"sh", "-c", script}, Cwd: sprites.CloneDir, Stdout: &out, Stderr: &out})
		if err != nil || code != 0 {
			t.Fatalf("guest %q: code=%d err=%v out=%s", script, code, err, out.String())
		}
	}
	commit := "git -c user.name=t -c user.email=t@t -c commit.gpgsign=false commit -q -m "

	r := Runners{
		Herdr: func(context.Context, string, ...string) (string, error) {
			return `{"result":{"worktrees":[{"path":"` + host + `","open_workspace_id":"w"}]}}`, nil
		},
		SpriteSync: func(context.Context, string) (SpriteSyncer, domain.SandboxID, bool, error) {
			return drv, id, true, nil
		},
	}
	g := func(force bool) error { return guardSprite(ctx, r, "herdr", "w", "p/a", id.String(), force) }

	guest("echo c1 > c1 && git add c1 && " + commit + "c1")
	want := hgit(t, host, "rev-parse", "main")
	if err := g(false); err != nil {
		t.Fatalf("case1 guard: %v", err)
	}
	got := hgit(t, host, "rev-parse", "main")
	if got == want {
		t.Fatal("case1: host main not advanced by export")
	}
	if _, err := os.Stat(filepath.Join(host, "c1")); err != nil {
		t.Fatalf("case1: c1 not on host: %v", err)
	}
	t.Logf("case1: exported, host main %s -> %s", want[:8], got[:8])

	guest("echo u > u")
	var ue *SpriteUnsyncedError
	err = g(false)
	if !errors.As(err, &ue) {
		t.Fatalf("uncommitted: err=%v", err)
	}
	t.Logf("uncommitted refused: %v", err)
	guest("rm u")

	guest("echo c2 > c2 && git add c2 && " + commit + "c2")
	if err := os.WriteFile(filepath.Join(host, "h"), []byte("h"), 0o644); err != nil {
		t.Fatal(err)
	}
	hgit(t, host, "add", "h")
	hgit(t, host, "commit", "-q", "-m", "h")
	before := hgit(t, host, "rev-parse", "main")
	err = g(false)
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "not a fast-forward") {
		t.Fatalf("case2: err=%v", err)
	}
	if hgit(t, host, "rev-parse", "main") != before {
		t.Fatal("case2: host moved on refusal")
	}
	t.Logf("case2 refused: %v", err)
	if err := g(true); err != nil {
		t.Fatalf("force: %v", err)
	}
}
