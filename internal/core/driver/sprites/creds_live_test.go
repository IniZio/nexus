//go:build spriteslive

package sprites_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
)

type noopSetter struct{}

func (noopSetter) SetRealToken(domain.SandboxID, string, string) error { return nil }

func TestCredsLive_ClaudeEnvProjection(t *testing.T) {
	if os.Getenv("SPRITES_TOKEN") == "" && os.Getenv("SPRITES_API_TOKEN") == "" {
		t.Skip("set SPRITES_TOKEN")
	}
	prof := cred.MustProfileByName(cred.ClaudeCodeProfileName)
	r, err := cred.NewRefresher(service.DedicatedCredStorePathForProfile(prof), prof.CredentialedHost, noopSetter{})
	if err != nil {
		t.Skipf("no host claude credential: %v", err)
	}
	tok, _, err := r.Token(context.Background())
	if err != nil {
		t.Fatalf("host claude token: %v", err)
	}
	prefix := tok[:12]
	state := t.TempDir()
	drv, err := sprites.New(sprites.Config{StateDir: state, EnvResolver: func(context.Context, []string) (map[string]string, error) {
		return map[string]string{sprites.SecretClaudeOAuth: tok}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	id := domain.NewSandboxID()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	if err := drv.Provision(ctx, id, sprites.Spec{OpenEgress: true, SecretNames: []string{sprites.SecretClaudeOAuth}}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	defer func() {
		if err := drv.Deprovision(context.Background(), id); err != nil {
			t.Errorf("Deprovision: %v", err)
		}
	}()
	run := func(argv ...string) (int32, string) {
		var out bytes.Buffer
		code, err := drv.Exec(ctx, id, driver.ExecOptions{Argv: argv, Cwd: "/tmp", Stdout: &out, Stderr: &out})
		if err != nil {
			t.Fatalf("exec %v: %v", argv, err)
		}
		return code, strings.ReplaceAll(out.String(), tok, "***")
	}
	if code, o := run("sh", "-c", "command -v claude >/dev/null || npm i -g @anthropic-ai/claude-code >/dev/null 2>&1; claude --version"); code != 0 {
		t.Fatalf("claude install/version code=%d %q", code, o)
	}
	code, o := run("claude", "-p", "--model", "haiku", "reply with OK")
	t.Logf("claude -p: code=%d %q", code, strings.TrimSpace(o))
	if code != 0 || !strings.Contains(o, "OK") {
		t.Fatalf("claude not authenticated via env: code=%d", code)
	}

	_, cnt := run("sh", "-c", `n=0; for d in "$HOME" /tmp /root /home; do [ -d "$d" ] && c=$(grep -rIlF -- "$1" "$d" 2>/dev/null | wc -l) && n=$((n+c)); done; echo $n`, "sh", prefix)
	t.Logf("sprite disk files containing token prefix: %s", strings.TrimSpace(cnt))
	if strings.TrimSpace(cnt) != "0" {
		t.Fatalf("token prefix found on sprite disk: %s", cnt)
	}
	hostHits := 0
	_ = filepath.WalkDir(state, func(p string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), prefix) {
				hostHits++
			}
		}
		return nil
	})
	t.Logf("host state files containing token prefix: %d", hostHits)
	if hostHits != 0 {
		t.Fatal("token prefix in host state")
	}
}
