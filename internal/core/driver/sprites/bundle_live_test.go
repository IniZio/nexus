//go:build spriteslive

package sprites_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
)

// Bundle mode with a remote-less repo: no clone, seed by bundle, HEAD matches.
func TestBundleSyncLive_NoRemote(t *testing.T) {
	if os.Getenv("SPRITES_TOKEN") == "" && os.Getenv("SPRITES_API_TOKEN") == "" {
		t.Skip("set SPRITES_TOKEN")
	}
	repo := t.TempDir()
	for _, a := range [][]string{
		{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "seed"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	want, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	drv, err := sprites.New(sprites.Config{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	id := domain.NewSandboxID()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err := drv.Provision(ctx, id, sprites.Spec{}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	defer func() {
		if err := drv.Deprovision(context.Background(), id); err != nil {
			t.Errorf("Deprovision: %v", err)
		}
	}()
	if err := drv.SeedWorktree(ctx, id, repo, "HEAD", sprites.CloneDir); err != nil {
		t.Fatalf("SeedWorktree: %v", err)
	}
	var o bytes.Buffer
	code, err := drv.Exec(ctx, id, driver.ExecOptions{Argv: []string{"git", "rev-parse", "HEAD"}, Cwd: sprites.CloneDir, Stdout: &o, Stderr: &o})
	if err != nil || code != 0 || strings.TrimSpace(o.String()) != strings.TrimSpace(string(want)) {
		t.Fatalf("guest HEAD: code=%d err=%v %q want %q", code, err, o.String(), want)
	}
	t.Logf("guest HEAD == host HEAD == %s", strings.TrimSpace(string(want)))
}
