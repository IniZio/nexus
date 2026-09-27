package controller_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/controller"
	herdrbackend "github.com/IniZio/nexus/internal/controller/backend/herdr"
	"github.com/IniZio/nexus/internal/controller/chattest"
	controllerconfig "github.com/IniZio/nexus/internal/controller/config"
	"github.com/IniZio/nexus/internal/controller/store/sqlite"
	"github.com/IniZio/nexus/internal/controller/storetest"
	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/core/vault/vaulttest"
	"github.com/IniZio/nexus/internal/herdragent"
)

const serveTestConfigYAML = `
slack:
  app_token:
    env: SERVE_TEST_SLACK_APP_TOKEN
  bot_token:
    env: SERVE_TEST_SLACK_BOT_TOKEN
channels:
  C_TEST:
    repo: github.com/example/repo
    idle_pause: 200ms
    idle_stop: 1h
deployment_mode: laptop
`

func writeTempConfig(t *testing.T, yaml string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "nexus-controller-*.yaml")
	if err != nil {
		t.Fatalf("create temp config: %v", err)
	}
	if _, err := f.WriteString(yaml); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	f.Close()
	return f.Name()
}

func fakeVaultOpener(t *testing.T) func() (vault.Vault, error) {
	t.Helper()
	fv := vaulttest.NewFake()
	impl := vault.NewVaultImpl(fv, vault.NewRegistry())
	return func() (vault.Vault, error) { return impl, nil }
}

type serveTestSandboxLC struct{}

func (s *serveTestSandboxLC) Pause(_ context.Context, _ string) error  { return nil }
func (s *serveTestSandboxLC) Resume(_ context.Context, _ string) error { return nil }
func (s *serveTestSandboxLC) Stop(_ context.Context, _ string) error   { return nil }
func (s *serveTestSandboxLC) Start(_ context.Context, _ string) error  { return nil }

func (b *serveTestNopBackend) Restart(_ context.Context, _, _ string) (string, error) {
	return "", controller.ErrNotImplemented
}

type serveTestNopBackend struct{}

func (b *serveTestNopBackend) Provision(_ context.Context, _ string, _ controller.ThreadRef, _ string) (string, string, error) {
	return "", "", controller.ErrNotImplemented
}
func (b *serveTestNopBackend) Prompt(_ context.Context, _, _ string) error {
	return controller.ErrNotImplemented
}
func (b *serveTestNopBackend) Observe(_ context.Context, _ string, _ bool) (herdragent.State, error) {
	return herdragent.State{}, controller.ErrNotImplemented
}
func (b *serveTestNopBackend) Answer(_ context.Context, _ string, _ controller.AgentInput) error {
	return controller.ErrNotImplemented
}
func (b *serveTestNopBackend) ReadAnswer(_ context.Context, _ string) (string, error) {
	return "", controller.ErrNotImplemented
}
func (b *serveTestNopBackend) Teardown(_ context.Context, _ string) error {
	return controller.ErrNotImplemented
}

type serveTestNopLinker struct{}

func (l *serveTestNopLinker) Require(_ context.Context, _ string) error { return nil }
func (l *serveTestNopLinker) StartLink(_ context.Context, _, _ string) (string, error) {
	return "", controller.ErrNotImplemented
}

type spyStore struct {
	*storetest.Fake
	listIdleCalls chan time.Time
}

func newSpyStore() *spyStore {
	return &spyStore{
		Fake:          storetest.New(),
		listIdleCalls: make(chan time.Time, 16),
	}
}

func (s *spyStore) ListIdle(ctx context.Context, before time.Time) ([]controller.Task, error) {
	select {
	case s.listIdleCalls <- before:
	default:
	}
	return s.Fake.ListIdle(ctx, before)
}

func TestServeWiresConfiguredAdapters(t *testing.T) {
	cfgPath := writeTempConfig(t, serveTestConfigYAML)
	t.Setenv("SERVE_TEST_SLACK_APP_TOKEN", "xapp-fake-token")
	t.Setenv("SERVE_TEST_SLACK_BOT_TOKEN", "xoxb-fake-token")

	type builtDeps struct {
		deps controller.Deps
	}
	built := make(chan builtDeps, 1)

	chat := chattest.New()
	factory := func(cfg *controllerconfig.Config, appToken, botToken string, v vault.Vault) (controller.Deps, controller.Handler, error) {
		tmpDB := filepath.Join(t.TempDir(), "tasks.db")
		st, err := sqlite.Open(tmpDB)
		if err != nil {
			return controller.Deps{}, nil, err
		}
		t.Cleanup(func() { st.Close() })

		backend := herdrbackend.New(herdrbackend.Config{Model: "claude-haiku-4-5"})
		reg := vault.NewRegistry()
		linker := controller.NewVaultLinker(v, reg, chat, "T_TEST")
		projects := controllerconfig.NewResolver(cfg)

		deps := controller.Deps{
			Chat:      chat,
			Store:     st,
			Backend:   backend,
			Lifecycle: &serveTestSandboxLC{},
			Linker:    linker,
			Projects:  projects,
		}
		built <- builtDeps{deps: deps}
		return deps, nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- controller.Serve(ctx, cfgPath,
			controller.WithDepsFactory(factory),
			controller.WithVaultOpener(fakeVaultOpener(t)),
		)
	}()

	var deps controller.Deps
	select {
	case bd := <-built:
		deps = bd.deps
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: DepsFactory was not called")
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned error: %v", err)
	}

	if _, ok := deps.Store.(*sqlite.Store); !ok {
		t.Errorf("Store: expected *sqlite.Store, got %T", deps.Store)
	}
	if _, ok := deps.Backend.(*herdrbackend.Backend); !ok {
		t.Errorf("Backend: expected *herdrbackend.Backend, got %T", deps.Backend)
	}
	if _, ok := deps.Linker.(*controller.VaultLinker); !ok {
		t.Errorf("Linker: expected *controller.VaultLinker, got %T", deps.Linker)
	}
	if _, ok := deps.Projects.(*controllerconfig.Resolver); !ok {
		t.Errorf("Projects: expected *config.Resolver, got %T", deps.Projects)
	}
	if deps.Chat == nil {
		t.Error("Chat: expected non-nil ChatAdapter")
	}
	if deps.Lifecycle == nil {
		t.Error("Lifecycle: expected non-nil SandboxLifecycle")
	}
}

func TestServeFailsClosedWithoutSlackTokens(t *testing.T) {
	cfgPath := writeTempConfig(t, serveTestConfigYAML)

	neverCalledFactory := func(_ *controllerconfig.Config, _, _ string, _ vault.Vault) (controller.Deps, controller.Handler, error) {
		t.Fatal("DepsFactory must not be called when Slack tokens are missing")
		return controller.Deps{}, nil, nil
	}

	err := controller.Serve(context.Background(), cfgPath,
		controller.WithDepsFactory(neverCalledFactory),
		controller.WithVaultOpener(fakeVaultOpener(t)),
	)
	if err == nil {
		t.Fatal("expected error when Slack tokens are missing, got nil")
	}
}

func TestServeFailsClosedWithoutVaultKey(t *testing.T) {
	cfgPath := writeTempConfig(t, serveTestConfigYAML)
	t.Setenv("SERVE_TEST_SLACK_APP_TOKEN", "xapp-fake-token")
	t.Setenv("SERVE_TEST_SLACK_BOT_TOKEN", "xoxb-fake-token")

	vaultErr := errors.New("vault: key not available")
	failingVault := func() (vault.Vault, error) { return nil, vaultErr }

	neverCalledFactory := func(_ *controllerconfig.Config, _, _ string, _ vault.Vault) (controller.Deps, controller.Handler, error) {
		t.Fatal("DepsFactory must not be called when vault key is unavailable")
		return controller.Deps{}, nil, nil
	}

	err := controller.Serve(context.Background(), cfgPath,
		controller.WithDepsFactory(neverCalledFactory),
		controller.WithVaultOpener(failingVault),
	)
	if err == nil {
		t.Fatal("expected error when vault key is unavailable, got nil")
	}
	if !errors.Is(err, vaultErr) {
		t.Errorf("error chain does not wrap vaultErr: %v", err)
	}
}

func TestServeRegistersLinkCommand(t *testing.T) {
	cfgPath := writeTempConfig(t, serveTestConfigYAML)
	t.Setenv("SERVE_TEST_SLACK_APP_TOKEN", "xapp-fake-token")
	t.Setenv("SERVE_TEST_SLACK_BOT_TOKEN", "xoxb-fake-token")

	chat := chattest.New()
	cmdHandlerCalled := make(chan controller.Event, 1)
	cmdHandler := func(_ context.Context, ev controller.Event) error {
		cmdHandlerCalled <- ev
		return nil
	}

	factory := func(cfg *controllerconfig.Config, _, _ string, v vault.Vault) (controller.Deps, controller.Handler, error) {
		deps := controller.Deps{
			Chat:      chat,
			Store:     storetest.New(),
			Backend:   &serveTestNopBackend{},
			Lifecycle: &serveTestSandboxLC{},
			Linker:    &serveTestNopLinker{},
			Projects:  controllerconfig.NewResolver(cfg),
		}
		return deps, cmdHandler, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- controller.Serve(ctx, cfgPath,
			controller.WithDepsFactory(factory),
			controller.WithVaultOpener(fakeVaultOpener(t)),
		)
	}()

	ev := controller.Event{
		Kind:      controller.EventSlashCommand,
		ThreadRef: controller.NewThreadRef("T1", "C_TEST", "ts1"),
		User:      "U1",
		Text:      "link",
	}
	if err := chat.Inject(ctx, ev); err != nil {
		t.Fatalf("Inject: %v", err)
	}

	select {
	case got := <-cmdHandlerCalled:
		if got.Kind != controller.EventSlashCommand {
			t.Errorf("expected EventSlashCommand, got %v", got.Kind)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: /link command handler was not called")
	}
}

func TestServeIdleTickerUsesConfigDurations(t *testing.T) {
	cfgPath := writeTempConfig(t, serveTestConfigYAML)
	t.Setenv("SERVE_TEST_SLACK_APP_TOKEN", "xapp-fake-token")
	t.Setenv("SERVE_TEST_SLACK_BOT_TOKEN", "xoxb-fake-token")

	fixedNow := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	idlePause := 200 * time.Millisecond
	wantBefore := fixedNow.Add(-idlePause)

	spy := newSpyStore()
	chat := chattest.New()

	factory := func(cfg *controllerconfig.Config, _, _ string, v vault.Vault) (controller.Deps, controller.Handler, error) {
		return controller.Deps{
			Chat:      chat,
			Store:     spy,
			Backend:   &serveTestNopBackend{},
			Lifecycle: &serveTestSandboxLC{},
			Linker:    &serveTestNopLinker{},
			Projects:  controllerconfig.NewResolver(cfg),
		}, nil, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- controller.Serve(ctx, cfgPath,
			controller.WithDepsFactory(factory),
			controller.WithVaultOpener(fakeVaultOpener(t)),
			controller.WithServeClock(func() time.Time { return fixedNow }),
			controller.WithServeTickInterval(30*time.Millisecond),
		)
	}()

	select {
	case before := <-spy.listIdleCalls:
		diff := before.Sub(wantBefore)
		if diff < -5*time.Millisecond || diff > 5*time.Millisecond {
			t.Errorf("ListIdle(before=%v), want ~%v (diff %v)", before, wantBefore, diff)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: ListIdle was not called by the idle ticker")
	}

	cancel()
	<-done
}
