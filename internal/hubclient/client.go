package hubclient

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Emitter is the seam other packages mock.
type Emitter interface {
	// EmitBestEffort appends ev and never fails the caller (C1).
	EmitBestEffort(ctx context.Context, ev Event)
	// Last returns the latest event for subject, or nil when none exists.
	Last(ctx context.Context, subject string) (*Event, error)
}

// emitTimeout bounds a best-effort emit.
const emitTimeout = time.Second

var (
	transportMu      sync.RWMutex
	defaultTransport func() Transport
)

// RegisterTransport sets the factory New uses. The journal package imports
// hubclient, so the production wiring is registered from the binary side.
func RegisterTransport(f func() Transport) {
	transportMu.Lock()
	defaultTransport = f
	transportMu.Unlock()
}

// Client emits and reads hub events through a Transport. It implements
// Emitter and Transport. With no transport it is inert: emits are dropped and
// reads return ErrUnsupported.
type Client struct {
	T Transport
}

var (
	_ Emitter   = (*Client)(nil)
	_ Transport = (*Client)(nil)
)

// New returns a Client on the registered default transport.
func New() *Client {
	transportMu.RLock()
	f := defaultTransport
	transportMu.RUnlock()
	if f == nil {
		return &Client{}
	}
	return &Client{T: f()}
}

// Emit writes ev, defaulting Actor from the environment.
func (c *Client) Emit(ctx context.Context, ev Event) error {
	if c.T == nil {
		return nil
	}
	if ev.Actor == "" {
		if ev.Actor = os.Getenv(EnvSession); ev.Actor == "" {
			ev.Actor = ActorAnonymous
		}
	}
	return c.T.Emit(ctx, ev)
}

// EmitBestEffort emits ev within a 1s bound, logging a WARN on failure.
func (c *Client) EmitBestEffort(ctx context.Context, ev Event) {
	ctx, cancel := context.WithTimeout(ctx, emitTimeout)
	defer cancel()
	if err := c.Emit(ctx, ev); err != nil {
		slog.Warn("hub emit failed", "type", ev.Type, "subject", ev.Subject, "err", err)
	}
}

// Read streams items after cursor until ctx is cancelled.
func (c *Client) Read(ctx context.Context, f Filter, cursor string) (<-chan Item, error) {
	if c.T == nil {
		return nil, ErrUnsupported
	}
	return c.T.Read(ctx, f, cursor)
}

// Last returns the latest event for subject, or nil when none exists.
func (c *Client) Last(ctx context.Context, subject string) (*Event, error) {
	if c.T == nil {
		return nil, nil
	}
	ev, err := c.T.Last(ctx, subject)
	if errors.Is(err, ErrUnsupported) {
		return nil, nil
	}
	return ev, err
}

// LastAll returns the latest event of every subject.
func (c *Client) LastAll(ctx context.Context) ([]Event, error) {
	if c.T == nil {
		return nil, ErrUnsupported
	}
	return c.T.LastAll(ctx)
}
