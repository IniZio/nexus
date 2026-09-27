package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/IniZio/nexus/internal/controller"
	controllerconfig "github.com/IniZio/nexus/internal/controller/config"
	"github.com/IniZio/nexus/internal/controller/sandbox"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/vault"
)

// stubChat implements slackChat without network access.
type stubChat struct{ teamID string }

func (s stubChat) Run(_ context.Context, _ controller.Handler) error              { return nil }
func (s stubChat) Post(_ context.Context, _ controller.ThreadRef, _ string) error { return nil }
func (s stubChat) PostFile(_ context.Context, _ controller.ThreadRef, _ string, _ []byte) error {
	return nil
}
func (s stubChat) React(_ context.Context, _ controller.ThreadRef, _ string) error { return nil }
func (s stubChat) Mention(user string) string                                      { return "<@" + user + ">" }
func (s stubChat) TeamID() string                                                  { return s.teamID }

// stubLifecycleSvc satisfies sandbox.LifecycleService without a real store.
type stubLifecycleSvc struct{}

func (stubLifecycleSvc) Pause(_ context.Context, _ string) (domain.Sandbox, error) {
	return domain.Sandbox{}, nil
}
func (stubLifecycleSvc) Resume(_ context.Context, _ string) (domain.Sandbox, error) {
	return domain.Sandbox{}, nil
}
func (stubLifecycleSvc) Stop(_ context.Context, _ string) (domain.Sandbox, error) {
	return domain.Sandbox{}, nil
}
func (stubLifecycleSvc) Start(_ context.Context, _ string) (domain.Sandbox, error) {
	return domain.Sandbox{}, nil
}

// setupFactory replaces the two network/substrate-bound package vars with stubs
// and returns a cleanup function that restores the originals.
func setupFactory(t *testing.T) func() {
	t.Helper()
	origSlack := newSlackAdapter
	origLifecycle := newSandboxLifecycle

	newSlackAdapter = func(_, _ string) (slackChat, error) {
		return stubChat{teamID: "TTEST"}, nil
	}
	newSandboxLifecycle = func(v vault.Vault) (*sandbox.ServiceLifecycle, error) {
		return sandbox.NewServiceLifecycle(stubLifecycleSvc{}), nil
	}

	return func() {
		newSlackAdapter = origSlack
		newSandboxLifecycle = origLifecycle
	}
}

// minimalConfig returns a Config with no channels; sufficient for NewResolver.
func minimalConfig() *controllerconfig.Config {
	return &controllerconfig.Config{}
}

func TestRealDepsFactory_AllFieldsNonNil(t *testing.T) {
	cleanup := setupFactory(t)
	defer cleanup()

	tmpState := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmpState)

	deps, handler, err := realDepsFactory(minimalConfig(), "xapp-token", "xoxb-token", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if handler == nil {
		t.Error("handler is nil")
	}
	if deps.Chat == nil {
		t.Error("Deps.Chat is nil")
	}
	if deps.Store == nil {
		t.Error("Deps.Store is nil")
	}
	if deps.Backend == nil {
		t.Error("Deps.Backend is nil")
	}
	if deps.Lifecycle == nil {
		t.Error("Deps.Lifecycle is nil")
	}
	if deps.Linker == nil {
		t.Error("Deps.Linker is nil")
	}
	if deps.Projects == nil {
		t.Error("Deps.Projects is nil")
	}
	// IdleFor must not be set here; serve.go fills it from config.
	if deps.IdleFor != nil {
		t.Error("Deps.IdleFor must not be set by realDepsFactory")
	}
}

func TestRealDepsFactory_TasksDBCreated(t *testing.T) {
	cleanup := setupFactory(t)
	defer cleanup()

	tmpState := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmpState)

	if _, _, err := realDepsFactory(minimalConfig(), "xapp-token", "xoxb-token", nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	dbPath := filepath.Join(tmpState, "nexus", "controller", "tasks.db")
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("tasks.db not created at %s: %v", dbPath, err)
	}
	// Dir must have 0700 permissions.
	dirInfo, err := os.Stat(filepath.Dir(dbPath))
	if err != nil {
		t.Fatalf("stat state dir: %v", err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Errorf("state dir perms = %o, want 0700", perm)
	}
}

func TestRealDepsFactory_HonoursModelEnv(t *testing.T) {
	cleanup := setupFactory(t)
	defer cleanup()

	t.Setenv("XDG_STATE_HOME", t.TempDir())
	const wantModel = "claude-sonnet-4-5"
	t.Setenv("NEXUS_CONTROLLER_MODEL", wantModel)

	deps, _, err := realDepsFactory(minimalConfig(), "xapp-token", "xoxb-token", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Inspect Backend.cfg.Model via reflection (field is unexported).
	bv := reflect.ValueOf(deps.Backend)
	if bv.IsNil() {
		t.Fatal("Backend is nil")
	}
	bv = bv.Elem()
	cfgField := bv.FieldByName("cfg")
	if !cfgField.IsValid() {
		t.Skip("cannot inspect Backend.cfg via reflection; skipping model check")
	}
	modelField := cfgField.FieldByName("Model")
	if !modelField.IsValid() {
		t.Skip("cannot inspect Config.Model via reflection; skipping model check")
	}
	if got := modelField.String(); got != wantModel {
		t.Errorf("Backend model = %q, want %q", got, wantModel)
	}
}

func TestRealDepsFactory_PropagatesSlackError(t *testing.T) {
	origSlack := newSlackAdapter
	origLifecycle := newSandboxLifecycle
	defer func() {
		newSlackAdapter = origSlack
		newSandboxLifecycle = origLifecycle
	}()

	wantErr := errors.New("auth.test: invalid token")
	newSlackAdapter = func(_, _ string) (slackChat, error) { return nil, wantErr }
	newSandboxLifecycle = func(v vault.Vault) (*sandbox.ServiceLifecycle, error) {
		return sandbox.NewServiceLifecycle(stubLifecycleSvc{}), nil
	}

	t.Setenv("XDG_STATE_HOME", t.TempDir())

	_, _, err := realDepsFactory(minimalConfig(), "bad", "bad", nil)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("error = %v, want to wrap %v", err, wantErr)
	}
}
