// Package tunnel multiplexes guest-initiated streams over one exec stdio pair
// using yamux. Guest opens a stream per TCP conn; host accepts and serves.
package tunnel

import (
	"errors"
	"io"
	"net"
	"time"

	"github.com/hashicorp/yamux"
)

// Config returns the explicit yamux config. Never pass nil to yamux: a past
// nil-config keepalive killed large git transfers. Keepalive is generous so a
// busy multiplexed link is not declared dead mid-transfer.
func Config() *yamux.Config {
	c := yamux.DefaultConfig()
	c.EnableKeepAlive = true
	c.KeepAliveInterval = 30 * time.Second
	c.ConnectionWriteTimeout = 5 * time.Minute
	c.StreamOpenTimeout = 30 * time.Second
	c.StreamCloseTimeout = 5 * time.Minute
	c.LogOutput = io.Discard
	c.Logger = nil
	return c
}

// HostSession accepts guest-initiated streams. It is a net.Listener.
type HostSession struct{ s *yamux.Session }

// Host runs the yamux server side over rwc.
func Host(rwc io.ReadWriteCloser) (*HostSession, error) {
	s, err := yamux.Server(rwc, Config())
	if err != nil {
		return nil, err
	}
	return &HostSession{s}, nil
}

// Accept returns the next guest stream; errors once the session dies.
func (h *HostSession) Accept() (net.Conn, error) { return h.s.Accept() }

// Close tears down the session and the underlying rwc.
func (h *HostSession) Close() error { return h.s.Close() }

// Addr implements net.Listener.
func (h *HostSession) Addr() net.Addr { return h.s.LocalAddr() }

// Closed is closed when the session dies; callers fail closed on it.
func (h *HostSession) Closed() <-chan struct{} { return h.s.CloseChan() }

// GuestSession opens streams toward the host.
type GuestSession struct{ s *yamux.Session }

// Guest runs the yamux client side over rwc.
func Guest(rwc io.ReadWriteCloser) (*GuestSession, error) {
	s, err := yamux.Client(rwc, Config())
	if err != nil {
		return nil, err
	}
	return &GuestSession{s}, nil
}

// Open opens one stream. Close on it half-closes (FIN); peer reads continue.
func (g *GuestSession) Open() (net.Conn, error) { return g.s.OpenStream() }

// Close tears down the session and the underlying rwc.
func (g *GuestSession) Close() error { return g.s.Close() }

// Closed is closed when the session dies.
func (g *GuestSession) Closed() <-chan struct{} { return g.s.CloseChan() }

type joined struct {
	w io.WriteCloser
	r io.ReadCloser
}

// Join merges an exec's stdin writer and stdout reader into one ReadWriteCloser.
func Join(w io.WriteCloser, r io.ReadCloser) io.ReadWriteCloser { return &joined{w, r} }

func (j *joined) Read(p []byte) (int, error)  { return j.r.Read(p) }
func (j *joined) Write(p []byte) (int, error) { return j.w.Write(p) }
func (j *joined) Close() error                { return errors.Join(j.w.Close(), j.r.Close()) }
