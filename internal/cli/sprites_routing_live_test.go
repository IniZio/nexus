//go:build spriteslive

package cli

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/registry"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
	"github.com/IniZio/nexus/internal/core/service"
)

// A CH-default service (NEXUS_BACKEND unset) must exec on and remove a sprites sandbox by record.
func TestBackendRoutingLive_CHDefaultServiceDrivesSprite(t *testing.T) {
	if os.Getenv("SPRITES_TOKEN") == "" && os.Getenv("SPRITES_API_TOKEN") == "" {
		t.Skip("set SPRITES_TOKEN")
	}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("NEXUS_BACKEND", "")
	if b, _ := activeBackend(); b == registry.Sprites {
		t.Fatalf("process default must not be sprites, got %q", b)
	}
	svc, err := newSandboxService()
	if err != nil {
		t.Fatal(err)
	}
	drv, err := newSpritesDriver()
	if err != nil {
		t.Fatal(err)
	}
	prov := drv.(*sprites.Driver)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	sb, err := svc.Create(ctx, "route", "live", service.CreateOptions{Backend: registry.Sprites})
	if err != nil {
		t.Fatal(err)
	}
	removed := false
	defer func() {
		if !removed {
			_ = prov.Deprovision(context.Background(), sb.ID)
		}
	}()
	if err := prov.Provision(ctx, sb.ID, sprites.Spec{OpenEgress: true}); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if _, err := svc.Start(ctx, sb.ID.String()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := svc.Lookup(ctx, sb.ID.String())
	if err != nil || got.Backend != registry.Sprites {
		t.Fatalf("Lookup: backend=%q err=%v", got.Backend, err)
	}
	var out, errb bytes.Buffer
	code, err := svc.Exec(ctx, sb.ID.String(), agent.ExecOptions{Argv: []string{"echo", "ok"}, Stdout: &out, Stderr: &errb})
	if err != nil || code != 0 || strings.TrimSpace(out.String()) != "ok" {
		t.Fatalf("Exec: code=%d out=%q err=%v stderr=%q", code, out.String(), err, errb.String())
	}
	if err := svc.Remove(ctx, sb.ID.String()); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	removed = true
	if obs, err := prov.Observe(ctx, sb.ID); err != nil || obs.State != driver.Absent {
		t.Fatalf("sprite still present after Remove: state=%v err=%v", obs.State, err)
	}
}
