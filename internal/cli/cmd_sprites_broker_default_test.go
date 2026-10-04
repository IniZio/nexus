package cli

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/superfly/sprites-go"

	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
	"github.com/IniZio/nexus/internal/core/driver/sprites/broker"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
)

type okAPI struct {
	mu       sync.Mutex
	policies []*sdk.NetworkPolicy
}

func (a *okAPI) CreateSprite(context.Context, string) error         { return nil }
func (a *okAPI) SpriteExists(context.Context, string) (bool, error) { return true, nil }
func (a *okAPI) DeleteSprite(context.Context, string) error         { return nil }
func (a *okAPI) Exec(context.Context, string, sprites.ExecRequest) (int32, error) {
	return 0, nil
}
func (a *okAPI) SetNetworkPolicy(_ context.Context, _ string, p *sdk.NetworkPolicy) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.policies = append(a.policies, p)
	return nil
}

type stateLauncher struct {
	t        *testing.T
	stateDir string
}

func (l stateLauncher) Spawn(_ context.Context, id string) error {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	l.t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	var argv []string
	for i := 0; i < 200; i++ {
		if a, err := broker.ProcessCmdline(cmd.Process.Pid); err == nil && len(a) > 0 && filepath.Base(a[0]) == "sleep" {
			argv = a
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return broker.WriteState(l.stateDir, id, broker.State{PID: cmd.Process.Pid, Cmdline: argv, Started: time.Now()})
}

// brokerCreate runs runSpritesCreate against a fake API and returns the
// persisted spec, the fake API and stderr.
func brokerCreate(t *testing.T, f sandboxCreateFlags, origin string) (sprites.Spec, *okAPI, string) {
	t.Helper()
	stateDir := t.TempDir()
	api := &okAPI{}
	var drv *sprites.Driver
	orig, origOrigin := newSpritesDriver, spritesOriginURL
	t.Cleanup(func() { newSpritesDriver, spritesOriginURL = orig, origOrigin })
	spritesOriginURL = func(context.Context, string) string { return origin }
	newSpritesDriver = func() (driver.Driver, error) {
		if drv != nil {
			return drv, nil
		}
		m := &broker.Manager{StateDir: stateDir, Launcher: stateLauncher{t, stateDir}, ReadyTimeout: 5 * time.Second, StopGrace: time.Second}
		var err error
		drv, err = sprites.New(sprites.Config{StateDir: stateDir, API: api, Broker: m})
		return drv, err
	}
	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := service.New(st, fake.New(), lifecycle.New()).WithBackendDriverFactory(backendDriverFactory)
	out, _, stderr := capture(false)
	f.positionals = []string{"p/n"}
	if err := runSpritesCreate(context.Background(), f, out, svc); err != nil {
		t.Fatalf("create: %v", err)
	}
	all, err := svc.List(context.Background())
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %v %v", all, err)
	}
	spec, err := drv.Spec(all[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return spec, api, stderr.String()
}

func TestSpritesCreateDefaultsToBrokerMode(t *testing.T) {
	spec, api, _ := brokerCreate(t, sandboxCreateFlags{repoURL: "https://github.com/a/b.git", syncMode: "push",
		allowHosts: []string{"github.com", "api.anthropic.com", "example.com"}}, "")
	if spec.CredMode != sprites.CredModeBroker {
		t.Fatalf("CredMode = %q, want broker", spec.CredMode)
	}
	if spec.GitHubRepo != "a/b" || !slices.Contains(spec.SecretNames, sprites.SecretGitHub) {
		t.Errorf("GitHubRepo=%q secrets=%v", spec.GitHubRepo, spec.SecretNames)
	}
	for _, p := range api.policies {
		for _, r := range p.Rules {
			if slices.Contains(sprites.SecretHosts, r.Domain) {
				t.Errorf("secret host %s in direct policy", r.Domain)
			}
		}
	}
	if !slices.Contains(spec.AllowedHosts, "example.com") {
		t.Errorf("hosts = %v", spec.AllowedHosts)
	}
}

func TestSpritesCreateGitHubWithoutRepoOrOriginFailsClosed(t *testing.T) {
	spec, _, stderr := brokerCreate(t, sandboxCreateFlags{secrets: []string{sprites.SecretGitHub}}, "")
	if slices.Contains(spec.SecretNames, sprites.SecretGitHub) || spec.GitHubRepo != "" {
		t.Errorf("GH_TOKEN brokered without binding: %+v", spec)
	}
	if !strings.Contains(stderr, "GH_TOKEN not brokered") {
		t.Errorf("stderr lacks message: %q", stderr)
	}
}

func TestSpritesCreateGitHubFromWorktreeOrigin(t *testing.T) {
	spec, _, _ := brokerCreate(t, sandboxCreateFlags{secrets: []string{sprites.SecretGitHub}}, "git@github.com:own/repo.git")
	if spec.GitHubRepo != "own/repo" || !slices.Contains(spec.SecretNames, sprites.SecretGitHub) {
		t.Errorf("spec = %+v", spec)
	}
}

func TestSpritesBrokerManagerProduction(t *testing.T) {
	m := spritesBrokerManager(t.TempDir())
	if m == nil || m.Launcher == nil || m.ReadyTimeout < 60*time.Second {
		t.Fatalf("manager = %+v", m)
	}
	if l, ok := m.Launcher.(broker.ExecLauncher); !ok || l.Exe != "" {
		t.Errorf("launcher = %#v; Exe must resolve at spawn time", m.Launcher)
	}
}
