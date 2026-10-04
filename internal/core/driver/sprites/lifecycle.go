package sprites

import (
	"context"
	"errors"
	"fmt"

	"github.com/IniZio/nexus/internal/core/domain"
)

var errNoBroker = errors.New("sprites: broker mode but no broker configured")

// ensureBroker starts (or confirms) the sandbox's credential broker.
func (d *Driver) ensureBroker(ctx context.Context, id domain.SandboxID) error {
	m := d.cfg.Broker
	if m == nil {
		return errNoBroker
	}
	if err := m.Ensure(ctx, id.String()); err != nil {
		return fmt.Errorf("sprites: ensure broker for %s: %w", id, err)
	}
	return nil
}

// stopBroker stops the sandbox's broker (verified-PID).
func (d *Driver) stopBroker(id domain.SandboxID) error {
	m := d.cfg.Broker
	if m == nil {
		return errNoBroker
	}
	if err := m.Stop(id.String()); err != nil {
		return fmt.Errorf("sprites: stop broker for %s: %w", id, err)
	}
	return nil
}
