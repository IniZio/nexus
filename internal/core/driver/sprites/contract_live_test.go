//go:build spriteslive

// Live contract run against real Fly.io Sprites. It creates and destroys real
// sprites (names prefixed "nx-") and needs network access.
//
//	SPRITES_TOKEN=... [SPRITES_ORG=...] make test GOTEST_PKGS=./internal/core/driver/sprites/ GOTEST_ARGS='-tags spriteslive -run Live'
//
// SPRITES_API_TOKEN is accepted in place of SPRITES_TOKEN. The Makefile has no
// tags variable; the tag rides in GOTEST_ARGS. Skips when no token is set.
package sprites_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/contract"
	"github.com/IniZio/nexus/internal/core/driver/registry"
	"github.com/IniZio/nexus/internal/core/driver/sprites"
)

const guestScratch = "/tmp/nx-contract"

func TestContractLive_Sprites(t *testing.T) {
	if os.Getenv("SPRITES_TOKEN") == "" && os.Getenv("SPRITES_API_TOKEN") == "" {
		t.Skip("set SPRITES_TOKEN (or SPRITES_API_TOKEN) to run the live sprites contract")
	}
	drv, err := registry.New(registry.Sprites, sprites.Config{StateDir: t.TempDir()})
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}
	sp, ok := drv.(*sprites.Driver)
	if !ok {
		t.Fatalf("driver is %T, want *sprites.Driver", drv)
	}
	contract.Run(t, contract.Harness{
		Backend: registry.Sprites,
		Driver:  drv,
		Net:     true,
		// The sandbox is remote: the host-side root never exists, so
		// destroy must leave it absent.
		GuestRoot:        guestScratch,
		HostShared:       false,
		DestroyKeepsRoot: false,
		Create: func(t *testing.T, allow []string) (domain.SandboxID, string, func()) {
			id := domain.NewSandboxID()
			c, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			if err := sp.Provision(c, id, sprites.Spec{AllowedHosts: allow}); err != nil {
				t.Fatalf("Provision: %v", err)
			}
			code, err := sp.Exec(c, id, driver.ExecOptions{Argv: []string{"mkdir", "-p", guestScratch}})
			if err != nil || code != 0 {
				_ = sp.Deprovision(context.Background(), id)
				t.Fatalf("mkdir %s: code=%d err=%v", guestScratch, code, err)
			}
			root := filepath.Join(t.TempDir(), "remote-root")
			return id, root, func() {
				dc, dcancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer dcancel()
				if err := sp.Deprovision(dc, id); err != nil {
					t.Errorf("Deprovision: %v", err)
				}
			}
		},
	})
}
