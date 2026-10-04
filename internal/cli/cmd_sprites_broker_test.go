package cli

import (
	"context"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
	"github.com/IniZio/nexus/internal/core/driver/sprites/broker"
)

var spritesBrokerTestID = domain.NewSandboxID().String()

func spritesBrokerTestDeps(t *testing.T, spec sprites.Spec, run func(context.Context, broker.RunConfig) error) spritesBrokerDeps {
	t.Helper()
	return spritesBrokerDeps{
		stateDir: t.TempDir(),
		loadSpec: func(domain.SandboxID) (sprites.Spec, error) { return spec, nil },
		resolve: func(_ context.Context, names []string) (map[string]string, error) {
			m := map[string]string{}
			for _, n := range names {
				m[n] = "REAL-" + n
			}
			return m, nil
		},
		exec: func(context.Context, string, broker.ExecRequest) (int32, error) { return 0, nil },
		run:  run,
	}
}

func TestSpritesBrokerBadIDDoesNotRun(t *testing.T) {
	ran := false
	d := spritesBrokerTestDeps(t, sprites.Spec{}, func(context.Context, broker.RunConfig) error { ran = true; return nil })
	for _, args := range [][]string{nil, {"../x"}, {"a", "b"}} {
		if err := runSpritesBroker(context.Background(), args, d); err == nil {
			t.Fatalf("args %v: want error", args)
		}
	}
	if ran {
		t.Fatal("Run called on bad args")
	}
}

func TestSpritesBrokerSecretsInConfigNotEnvOrArgv(t *testing.T) {
	spec := sprites.Spec{Repo: "https://github.com/acme/widget.git", SecretNames: []string{sprites.SecretGitHub}}
	var got broker.RunConfig
	d := spritesBrokerTestDeps(t, spec, func(_ context.Context, c broker.RunConfig) error { got = c; return nil })
	if err := runSpritesBroker(context.Background(), []string{spritesBrokerTestID}, d); err != nil {
		t.Fatal(err)
	}
	if len(got.Creds.Secrets) != 1 {
		t.Fatalf("secrets = %+v", got.Creds.Secrets)
	}
	s := got.Creds.Secrets[0]
	if s.Name != "GH_TOKEN" || s.Value != "REAL-GH_TOKEN" || s.GitHubRepo != "acme/widget" {
		t.Fatalf("secret = %+v", s)
	}
	if got.Sprite != sprites.SpriteName(got.Creds.SandboxID) || got.StateDir != d.stateDir || got.Exec == nil {
		t.Fatalf("cfg = %+v", got)
	}
}

func TestSpritesBrokerGitHubWithoutRepoFailsClosed(t *testing.T) {
	spec := sprites.Spec{Repo: "https://gitlab.com/a/b.git", SecretNames: []string{sprites.SecretGitHub}}
	var got broker.RunConfig
	d := spritesBrokerTestDeps(t, spec, func(_ context.Context, c broker.RunConfig) error { got = c; return nil })
	if err := runSpritesBroker(context.Background(), []string{spritesBrokerTestID}, d); err != nil {
		t.Fatal(err)
	}
	if len(got.Creds.Secrets) != 0 {
		t.Fatalf("GH_TOKEN must be omitted: %+v", got.Creds.Secrets)
	}
}

func TestSpritesBrokerClaudeRefreshWired(t *testing.T) {
	spec := sprites.Spec{SecretNames: []string{sprites.SecretClaudeOAuth}}
	var got broker.RunConfig
	d := spritesBrokerTestDeps(t, spec, func(_ context.Context, c broker.RunConfig) error { got = c; return nil })
	if err := runSpritesBroker(context.Background(), []string{spritesBrokerTestID}, d); err != nil {
		t.Fatal(err)
	}
	if got.Creds.RefreshStore == "" || got.Creds.RefreshSecret != sprites.SecretClaudeOAuth {
		t.Fatalf("refresh not wired: %+v", got.Creds)
	}
	if len(got.Creds.Secrets) != 1 || got.Creds.Secrets[0].Hosts[0] != "api.anthropic.com" {
		t.Fatalf("secrets = %+v", got.Creds.Secrets)
	}
}

func TestSpritesBrokerSIGTERMCancelsContext(t *testing.T) {
	started := make(chan struct{})
	cancelled := make(chan struct{})
	d := spritesBrokerTestDeps(t, sprites.Spec{}, func(ctx context.Context, _ broker.RunConfig) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- runSpritesBroker(context.Background(), []string{spritesBrokerTestID}, d) }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("run not reached: %v", err)
	}
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx not cancelled by SIGTERM")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSpritesBrokerGithubOwnerName(t *testing.T) {
	for in, want := range map[string]string{
		"acme/widget":                    "acme/widget",
		"https://github.com/acme/widget": "acme/widget",
		"https://github.com/acme/w.git":  "acme/w",
		"git@github.com:acme/widget.git": "acme/widget",
		"https://gitlab.com/acme/widget": "",
		"":                               "",
		"https://github.com/acme":        "",
		"https://github.com/acme/x/y":    "",
	} {
		if got := githubOwnerName(in); got != want {
			t.Errorf("%q = %q, want %q", in, got, want)
		}
	}
}
