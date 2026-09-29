package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/driver"
)

// unsupported maps driver.ErrUnsupported to a wrapped ErrNoSubstrate.
func (s *Service) unsupported(op, ref string, err error) error {
	if errors.Is(err, driver.ErrUnsupported) {
		return fmt.Errorf("service: %s %s: driver %q: %w: %w", op, ref, s.driver.Name(), ErrNoSubstrate, err)
	}
	return err
}

// Exec resolves the sandbox identified by ref and runs a command in the guest
// through the driver port.
func (s *Service) Exec(ctx context.Context, ref string, opts agent.ExecOptions) (int32, error) {
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return 0, err
	}
	code, err := s.driver.Exec(ctx, sb.ID, opts)
	return code, s.unsupported("exec", ref, err)
}

// Attach resolves the sandbox identified by ref and reattaches to an existing
// guest session.
func (s *Service) Attach(ctx context.Context, ref string, opts agent.AttachOptions) (int32, error) {
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return 0, err
	}
	at, ok := s.driver.(driver.SessionAttacher)
	if !ok {
		return 0, fmt.Errorf(
			"service: attach %s: driver %q does not support session attach: %w",
			ref, s.driver.Name(), ErrNoSubstrate,
		)
	}
	return at.Attach(ctx, sb.ID, opts)
}

// Copy resolves the sandbox identified by ref and performs a file-transfer
// operation with the guest through the driver port.
func (s *Service) Copy(ctx context.Context, ref string, opts agent.CopyOptions) error {
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return err
	}
	return s.unsupported("copy", ref, s.driver.Copy(ctx, sb.ID, opts))
}
