package service

import (
	"context"
	"fmt"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

// agentClientFor type-asserts the service's driver as a [driver.GuestDialer]
// and constructs an [agent.Client] for the sandbox sb. Returns ErrNoSubstrate
// (wrapped) if the driver does not implement [driver.GuestDialer].
func (s *Service) agentClientFor(ref string, sb domain.Sandbox) (*agent.Client, error) {
	gd, ok := s.driver.(driver.GuestDialer)
	if !ok {
		return nil, fmt.Errorf(
			"service: agent %s: driver %q does not support guest dialing: %w",
			ref, s.driver.Name(), ErrNoSubstrate,
		)
	}
	return agent.NewClient(gd, sb.ID), nil
}

// Exec resolves the sandbox identified by ref, constructs an agent Client,
// and executes a command in the guest. Surfaces must call this method rather
// than building an agent.Client directly.
func (s *Service) Exec(ctx context.Context, ref string, opts agent.ExecOptions) (int32, error) {
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return 0, err
	}
	c, err := s.agentClientFor(ref, sb)
	if err != nil {
		return 0, err
	}
	return c.Exec(ctx, opts)
}

// Attach resolves the sandbox identified by ref and reattaches to an existing
// guest session. Surfaces must call this method rather than building an
// agent.Client directly.
func (s *Service) Attach(ctx context.Context, ref string, opts agent.AttachOptions) (int32, error) {
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return 0, err
	}
	c, err := s.agentClientFor(ref, sb)
	if err != nil {
		return 0, err
	}
	return c.Attach(ctx, opts)
}

// Copy resolves the sandbox identified by ref and performs a file-transfer
// operation with the guest. Surfaces must call this method rather than
// building an agent.Client directly.
func (s *Service) Copy(ctx context.Context, ref string, opts agent.CopyOptions) error {
	sb, err := s.resolve(ctx, ref)
	if err != nil {
		return err
	}
	c, err := s.agentClientFor(ref, sb)
	if err != nil {
		return err
	}
	return c.Copy(ctx, opts)
}
