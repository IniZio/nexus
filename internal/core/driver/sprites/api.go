// Package sprites is the Fly.io Sprites backend: a sandbox is one remote
// sprite driven through the official sprites-go SDK.
package sprites

import (
	"context"
	"io"

	sdk "github.com/superfly/sprites-go"

	"github.com/IniZio/nexus/internal/core/agent/wire"
)

// NamePrefix guards every sprite this backend creates or destroys.
const NamePrefix = "nx-"

// ExecRequest is one command run inside a sprite.
type ExecRequest struct {
	Argv   []string
	Env    map[string]string
	Dir    string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	TTY    bool
	Term   string // TERM for TTY runs; empty = leave to the sprite
	Rows   uint16
	Cols   uint16
	Resize <-chan wire.Winsize // may be nil
}

// API is the narrow seam over the Sprites service; sdkAPI implements it and
// tests fake it.
type API interface {
	CreateSprite(ctx context.Context, name string) error
	// SpriteExists reports false (nil error) only when the service says 404.
	SpriteExists(ctx context.Context, name string) (bool, error)
	DeleteSprite(ctx context.Context, name string) error
	// Exec runs to completion and returns the remote exit code. A non-zero
	// exit is (code, nil); transport failures are (0, err).
	Exec(ctx context.Context, name string, req ExecRequest) (int32, error)
	SetNetworkPolicy(ctx context.Context, name string, p *sdk.NetworkPolicy) error
}
