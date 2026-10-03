package supervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/IniZio/nexus/internal/hubclient"
)

type fakeDelegateEm struct {
	mu  sync.Mutex
	evs []hubclient.Event
}

func (f *fakeDelegateEm) emit(_ context.Context, ev hubclient.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.evs = append(f.evs, ev)
	return nil
}

func (f *fakeDelegateEm) types() []string {
	var t []string
	for _, e := range f.evs {
		t = append(t, e.Type)
	}
	return t
}

func eqStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func appendFile(t *testing.T, p, s string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestDelegateWatchFriction(t *testing.T) {
	dir := t.TempDir()
	em := &fakeDelegateEm{}
	w := &delegateWatcher{em: em, sandbox: "sb1", worktree: dir}
	ctx := context.Background()
	w.poll(ctx)
	p := filepath.Join(dir, frictionFile)
	appendFile(t, p, "- slow pull\n- BLOCKED: no registry\npart")
	w.poll(ctx)
	w.poll(ctx)
	appendFile(t, p, "ial\n")
	w.poll(ctx)
	if !eqStrs(em.types(), []string{"delegate.friction", "delegate.friction", "delegate.friction"}) {
		t.Fatalf("types=%v", em.types())
	}
	var pl hubclient.DelegatePayload
	_ = json.Unmarshal(em.evs[1].Payload, &pl)
	if !pl.Blocked || pl.Sandbox != "sb1" {
		t.Fatalf("payload=%+v", pl)
	}
	_ = json.Unmarshal(em.evs[2].Payload, &pl)
	if pl.Blocked || pl.Line != "partial" {
		t.Fatalf("payload=%+v", pl)
	}
}

func TestDelegateWatchPermissionRearms(t *testing.T) {
	em := &fakeDelegateEm{}
	dialog := "────────\nDo you want to proceed?\n❯ 1. Yes\n  2. No\nEsc to cancel"
	screen := dialog
	w := &delegateWatcher{em: em, sandbox: "sb1", worktree: t.TempDir(), paneID: "p1",
		readPane: func(context.Context, string) (string, error) { return screen, nil }}
	ctx := context.Background()
	w.poll(ctx)
	w.poll(ctx)
	screen = "working..."
	w.poll(ctx)
	screen = dialog
	w.poll(ctx)
	if !eqStrs(em.types(), []string{"delegate.permission", "delegate.permission"}) {
		t.Fatalf("types=%v", em.types())
	}
}

func TestDelegateWatchNoPaneSkips(t *testing.T) {
	em := &fakeDelegateEm{}
	called := false
	w := &delegateWatcher{em: em, worktree: t.TempDir(),
		readPane: func(context.Context, string) (string, error) { called = true; return "", nil }}
	w.poll(context.Background())
	if called || len(em.evs) != 0 {
		t.Fatal("expected skip")
	}
}
