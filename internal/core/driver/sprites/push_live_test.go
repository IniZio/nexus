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

// Read-only: clones a public repo and creates a local task branch; never pushes.
func TestPushSyncLive_CloneBranchReadOnly(t *testing.T) {
	if os.Getenv("SPRITES_TOKEN") == "" && os.Getenv("SPRITES_API_TOKEN") == "" {
		t.Skip("set SPRITES_TOKEN")
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	tok := strings.TrimSpace(string(out))
	if err != nil || tok == "" {
		t.Skip("no host gh token")
	}
	drv, err := sprites.New(sprites.Config{StateDir: t.TempDir(), EnvResolver: func(context.Context, []string) (map[string]string, error) {
		return map[string]string{sprites.SecretGitHub: tok}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	id := domain.NewSandboxID()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err := drv.Provision(ctx, id, sprites.Spec{Repo: "https://github.com/IniZio/opencode-nudge.git", Sync: sprites.SyncPush, GitToken: tok}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	defer func() {
		if err := drv.Deprovision(context.Background(), id); err != nil {
			t.Errorf("Deprovision: %v", err)
		}
	}()
	if err := drv.PreparePushBranch(ctx, id, sprites.CloneDir, "nx-sp9b-live"); err != nil {
		t.Fatalf("PreparePushBranch: %v", err)
	}
	run := func(argv ...string) (int32, string) {
		var o bytes.Buffer
		code, err := drv.Exec(ctx, id, driver.ExecOptions{Argv: argv, Cwd: sprites.CloneDir, Stdout: &o, Stderr: &o})
		if err != nil {
			t.Fatalf("exec %v: %v", argv, err)
		}
		return code, strings.ReplaceAll(o.String(), tok, "***")
	}
	if code, o := run("git", "rev-parse", "--abbrev-ref", "HEAD"); code != 0 || strings.TrimSpace(o) != "nx-sp9b-live" {
		t.Fatalf("branch: code=%d %q", code, o)
	}
	if code, o := run("sh", "-c", `echo "$GH_TOKEN" | wc -c; git ls-remote origin HEAD | wc -l`); code != 0 {
		t.Fatalf("env/ls-remote: %d %q", code, o)
	} else {
		t.Logf("GH_TOKEN len + ls-remote lines: %s", strings.Join(strings.Fields(o), " "))
	}
	if code, o := run("sh", "-c", `grep -rIlF -- "$1" .git "$HOME" 2>/dev/null | wc -l`, "sh", tok[:10]); code != 0 || strings.TrimSpace(o) != "0" {
		t.Fatalf("token found on sprite disk: %d %q", code, o)
	}
	un, err := drv.GuestUnpushed(ctx, id, sprites.CloneDir)
	t.Logf("unpushed: %q %v", un, err)
	if !strings.Contains(un, "no pushed upstream") {
		t.Fatalf("expected unpushed guard, got %q", un)
	}
}
