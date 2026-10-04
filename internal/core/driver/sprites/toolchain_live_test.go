//go:build spriteslive

package sprites_test

import (
	"bytes"
	"context"
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

func TestToolchainLive_Sprites(t *testing.T) {
	if os.Getenv("SPRITES_TOKEN") == "" && os.Getenv("SPRITES_API_TOKEN") == "" {
		t.Skip("set SPRITES_TOKEN")
	}
	const want = "go1.26.6"
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module x\n\ngo 1.26.0\n\ntoolchain "+want+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "go.mod"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "m"}} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}

	drv := liveBrokerDriver(t, t.TempDir())
	id := domain.NewSandboxID()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	start := time.Now()
	if err := drv.Provision(ctx, id, sprites.Spec{}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	t.Logf("Provision took %s", time.Since(start))
	defer func() {
		if err := drv.Deprovision(context.Background(), id); err != nil {
			t.Errorf("Deprovision: %v", err)
		}
	}()

	const guestDir = "/tmp/gt"
	t0 := time.Now()
	if err := drv.SeedWorktree(ctx, id, repo, "HEAD", guestDir); err != nil {
		t.Fatalf("SeedWorktree: %v", err)
	}
	t.Logf("SeedWorktree+warm took %s", time.Since(t0))

	run := func(argv ...string) (int32, string) {
		var out bytes.Buffer
		code, err := drv.Exec(ctx, id, driver.ExecOptions{Argv: argv, Cwd: guestDir, Stdout: &out, Stderr: &out})
		if err != nil {
			t.Fatalf("exec %v: %v", argv, err)
		}
		return code, out.String()
	}
	code, o := run("go", "version")
	t.Logf("go version: code=%d %q", code, o)
	if code != 0 || !strings.Contains(o, want) {
		t.Fatalf("want %s in output", want)
	}
	for _, host := range []string{"storage.googleapis.com", "example.com"} {
		code, o = run("curl", "-sS", "-m", "10", "-o", "/dev/null", "https://"+host)
		t.Logf("%s: code=%d %q", host, code, o)
		if code == 0 {
			t.Fatalf("%s must be denied after warm", host)
		}
	}
}
