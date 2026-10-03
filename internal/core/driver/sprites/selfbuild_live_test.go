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

func TestSelfBuildLive_Sprites(t *testing.T) {
	if os.Getenv("SPRITES_TOKEN") == "" && os.Getenv("SPRITES_API_TOKEN") == "" {
		t.Skip("set SPRITES_TOKEN")
	}
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatal(err)
	}
	repo := strings.TrimSpace(string(top))

	drv, err := sprites.New(sprites.Config{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	id := domain.NewSandboxID()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
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

	const guestDir = "/tmp/nexus"
	t0 := time.Now()
	if err := drv.SeedWorktree(ctx, id, repo, "HEAD", guestDir); err != nil {
		t.Fatalf("SeedWorktree: %v", err)
	}
	t.Logf("SeedWorktree+warm took %s", time.Since(t0))

	run := func(cwd string, argv ...string) (int32, string) {
		var out bytes.Buffer
		t1 := time.Now()
		code, err := drv.Exec(ctx, id, driver.ExecOptions{Argv: argv, Cwd: cwd, Stdout: &out, Stderr: &out})
		if err != nil {
			t.Fatalf("exec %v: %v", argv, err)
		}
		t.Logf("%v took %s code=%d", argv, time.Since(t1), code)
		return code, out.String()
	}

	_, o := run("/", "sh", "-c", "stat -c '%F %u %n' /.sprite/api.sock; cat /proc/1/comm; ls -la /.sprite; command -v make gcc")
	t.Logf("marker:\n%s", o)

	code, o := run(guestDir, "make", "build")
	t.Logf("make build output:\n%s", tail(o))
	if code != 0 {
		t.Fatalf("make build exit %d", code)
	}
	if !strings.Contains(o, "nexus-make: sprite guest") {
		t.Fatalf("missing guard notice")
	}

	code, o = run(guestDir, "make", "test", "GOTEST_PKGS=./internal/hubstate/")
	t.Logf("make test output:\n%s", tail(o))
	if code != 0 {
		t.Fatalf("make test exit %d", code)
	}
	if !strings.Contains(o, "ok") {
		t.Fatalf("make test output lacks ok")
	}
}

func tail(s string) string {
	if len(s) > 4000 {
		return s[len(s)-4000:]
	}
	return s
}
