package service

import (
	"context"
	"fmt"
	"net"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

// Lookup resolves the sandbox identified by ref and returns it without
// changing its state. ref may be an exact ID, an ID prefix, or a
// "<project>/<name>" handle.
func (s *Service) Lookup(ctx context.Context, ref string) (domain.Sandbox, error) {
	return s.resolve(ctx, ref)
}

// DialGuest resolves the sandbox identified by ref and dials the given vsock
// port inside the running guest VM. Returns ErrNoSubstrate if the driver does
// not implement driver.GuestDialer.
func (s *Service) DialGuest(ctx context.Context, ref string, port uint32) (net.Conn, error) {
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	gd, ok := s.driver.(driver.GuestDialer)
	if !ok {
		return nil, fmt.Errorf(
			"service: dial guest %s port %d: driver %q does not support guest dialing: %w",
			ref, port, s.driver.Name(), ErrNoSubstrate,
		)
	}
	return gd.DialGuest(ctx, sb.ID, port)
}

// DialGuestByID dials the given vsock port directly by sandbox ID, without a
// store lookup. It is used by the SSH command when the sandbox has already
// been resolved.
func (s *Service) DialGuestByID(ctx context.Context, id domain.SandboxID, port uint32) (net.Conn, error) {
	gd, ok := s.driver.(driver.GuestDialer)
	if !ok {
		return nil, fmt.Errorf(
			"service: dial guest %s port %d: driver %q does not support guest dialing: %w",
			id, port, s.driver.Name(), ErrNoSubstrate,
		)
	}
	return gd.DialGuest(ctx, id, port)
}
