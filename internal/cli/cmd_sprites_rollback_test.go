package cli

import (
	"context"
	"net/http"
	"sync"
	"testing"

	sdk "github.com/superfly/sprites-go"

	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
)

// conflictAPI is a fake Sprites API whose create always 409s: the sprite
// already exists and was not created by nexus.
type conflictAPI struct {
	mu      sync.Mutex
	deleted []string
}

func (a *conflictAPI) CreateSprite(context.Context, string) error {
	return &sdk.APIError{StatusCode: http.StatusConflict, Message: "exists"}
}
func (a *conflictAPI) SpriteExists(context.Context, string) (bool, error) { return true, nil }
func (a *conflictAPI) DeleteSprite(_ context.Context, name string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.deleted = append(a.deleted, name)
	return nil
}
func (a *conflictAPI) Exec(context.Context, string, sprites.ExecRequest) (int32, error) {
	return 0, nil
}
func (a *conflictAPI) SetNetworkPolicy(context.Context, string, *sdk.NetworkPolicy) error {
	return nil
}

func TestSpritesCreateRollbackKeepsPreexistingSprite(t *testing.T) {
	api := &conflictAPI{}
	stateDir := t.TempDir()
	orig := newSpritesDriver
	newSpritesDriver = func() (driver.Driver, error) {
		return sprites.New(sprites.Config{StateDir: stateDir, API: api})
	}
	t.Cleanup(func() { newSpritesDriver = orig })

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	svc := service.New(st, fake.New(), lifecycle.New()).WithBackendDriverFactory(backendDriverFactory)

	out, _, _ := capture(false)
	err = runSpritesCreate(context.Background(), sandboxCreateFlags{positionals: []string{"p/n"}}, out, svc)
	if err == nil {
		t.Fatal("create succeeded; want 409 failure")
	}
	if len(api.deleted) != 0 {
		t.Errorf("rollback deleted pre-existing sprite(s) %v; want none", api.deleted)
	}
	all, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("store still has %d record(s) after rollback; want 0", len(all))
	}
}
