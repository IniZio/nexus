package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/driver"
)

// unsupported maps driver.ErrUnsupported to a wrapped ErrNoSubstrate.
func (s *Service) unsupported(op, ref string, drv driver.Driver, err error) error {
	if errors.Is(err, driver.ErrUnsupported) {
		return fmt.Errorf("service: %s %s: driver %q: %w: %w", op, ref, drv.Name(), ErrNoSubstrate, err)
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
	drv, err := s.drvFor(sb)
	if err != nil {
		return 0, err
	}
	code, err := drv.Exec(ctx, sb.ID, opts)
	return code, s.unsupported("exec", ref, drv, err)
}

// Attach resolves the sandbox identified by ref and reattaches to an existing
// guest session.
func (s *Service) Attach(ctx context.Context, ref string, opts agent.AttachOptions) (int32, error) {
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return 0, err
	}
	drv, err := s.drvFor(sb)
	if err != nil {
		return 0, err
	}
	at, ok := drv.(driver.SessionAttacher)
	if !ok {
		return 0, fmt.Errorf(
			"service: attach %s: driver %q does not support session attach: %w",
			ref, drv.Name(), ErrNoSubstrate,
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
	drv, err := s.drvFor(sb)
	if err != nil {
		return err
	}
	return s.unsupported("copy", ref, drv, drv.Copy(ctx, sb.ID, opts))
}
