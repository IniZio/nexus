package sprites

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
)

const brokerEnsureTimeout = 30 * time.Second

var errNoBroker = errors.New("sprites: broker mode but no broker configured")

// ensureBroker starts (or confirms) the sandbox's credential broker.
func (d *Driver) ensureBroker(ctx context.Context, id domain.SandboxID) error {
	m := d.cfg.Broker
	if m == nil {
		return errNoBroker
	}
	ectx, cancel := context.WithTimeout(ctx, brokerEnsureTimeout)
	defer cancel()
	if err := m.Ensure(ectx, id.String()); err != nil {
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

// brokerMode reports whether the persisted spec for id selects the broker.
func (d *Driver) brokerMode(id domain.SandboxID) bool {
	s, err := d.Spec(id)
	return err == nil && s.CredMode == CredModeBroker
}
