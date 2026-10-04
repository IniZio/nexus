//go:build spriteslive

package mcp

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
	"github.com/IniZio/nexus/internal/core/driver/sprites/broker"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/herdragent"
	"github.com/IniZio/nexus/internal/hubclient"
	"github.com/IniZio/nexus/internal/hubclient/journal"
)

type noopSetter struct{}

func (noopSetter) SetRealToken(domain.SandboxID, string, string) error { return nil }

type liveSpritesSvc struct {
	*stubService
	drv driver.Driver
	id  domain.SandboxID
}

func (s *liveSpritesSvc) Exec(ctx context.Context, _ string, argv []string, env map[string]string, cwd, stdin string) (int32, string, string, error) {
	var out, errb bytes.Buffer
	opts := driver.ExecOptions{Argv: argv, Env: env, Cwd: cwd, Stdout: &out, Stderr: &errb}
	if stdin != "" {
		opts.Stdin = strings.NewReader(stdin)
	}
	code, err := s.drv.Exec(ctx, s.id, opts)
	return code, out.String(), errb.String(), err
}

// The agent is simulated with `claude -p --model haiku` run through driver Exec
// with the sprites standing orders; herdr dispatch into a pane is not exercised.
func TestDelegateDoneLive_Sprites(t *testing.T) {
	if os.Getenv("SPRITES_TOKEN") == "" {
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
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	emitDelegateDoneEvent = func(ctx context.Context, ev hubclient.Event) { _ = journal.Emit(ctx, ev) }

	bin := os.Getenv("NEXUS_LIVE_BIN")
	if bin == "" {
		t.Skip("set NEXUS_LIVE_BIN to a real nexus binary (the sprites broker is spawned from it)")
	}
	stateDir := t.TempDir()
	drv, err := sprites.New(sprites.Config{StateDir: stateDir, Broker: &broker.Manager{
		StateDir: stateDir, Launcher: broker.ExecLauncher{Exe: bin, StateDir: stateDir}, ReadyTimeout: 60 * time.Second,
	}})
	if err != nil {
		t.Fatal(err)
	}
	id := domain.NewSandboxID()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	if err := drv.Provision(ctx, id, sprites.Spec{OpenEgress: true, SecretNames: []string{sprites.SecretClaudeOAuth}}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	defer func() {
		if err := drv.Deprovision(context.Background(), id); err != nil {
			t.Errorf("Deprovision: %v", err)
		}
	}()
	svc := &liveSpritesSvc{stubService: &stubService{listResult: []domain.Sandbox{{ID: id, Backend: spritesBackend}}}, drv: drv, id: id}
	ref := id.String()
	scrub := func(s string) string { return strings.ReplaceAll(s, tok, "***") }

	if code, o, e, err := svc.Exec(ctx, ref, []string{"sh", "-c", "command -v claude >/dev/null || npm i -g @anthropic-ai/claude-code >/dev/null 2>&1; claude --version"}, nil, "/tmp", ""); err != nil || code != 0 {
		t.Fatalf("claude install: code=%d err=%v %s %s", code, err, scrub(o), scrub(e))
	}

	res, err := buildPollResult(ctx, svc, ref, herdragent.State{})
	if err != nil || res.DoneVia == "marker" {
		t.Fatalf("pre-run poll: %+v %v", res, err)
	}

	prompt := spritesStandingOrders + "Sandbox id: " + ref + "\n\nTask: there is no repo and nothing to commit. Run `echo live-ok > $HOME/hello.txt`, then finish per the Completion contract: write the one-line summary to the marker.\n"
	code, o, e, err := svc.Exec(ctx, ref, []string{"claude", "-p", "--model", "haiku", "--permission-mode", "bypassPermissions", prompt}, nil, "/home/sprite", "")
	t.Logf("claude -p: code=%d err=%v out=%q", code, err, scrub(strings.TrimSpace(o+e)))
	if err != nil || code != 0 {
		t.Fatal("agent run failed")
	}

	for i := 0; i < 3; i++ {
		res, err = buildPollResult(ctx, svc, ref, herdragent.State{})
		if err != nil || res.DoneVia != "marker" {
			t.Fatalf("poll %d: %+v %v", i, res, err)
		}
		t.Logf("poll %d: via=%s marker=%q", i, res.DoneVia, res.MarkerContent)
	}

	time.Sleep(2 * time.Second)
	out, err := exec.Command("journalctl", "--user", "-o", "json", "--since", "-15min", "CE_TYPE=delegate.done").Output()
	if err != nil {
		t.Fatalf("journalctl: %v", err)
	}
	n := 0
	for _, l := range strings.Split(string(out), "\n") {
		if strings.Contains(l, ref) {
			n++
			t.Logf("journal: %s", l)
		}
	}
	if n != 1 {
		t.Fatalf("delegate.done journal entries for %s = %d, want 1", ref, n)
	}
}
