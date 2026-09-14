package portfwd

import (
	"context"
)

type fwdKey struct {
	sandboxID string
	port      uint16
}

type Manager struct {
	fw      *Forwarder
	applied map[fwdKey]struct{}
}

func NewManager(fw *Forwarder) *Manager {
	return &Manager{fw: fw, applied: make(map[fwdKey]struct{})}
}

func (m *Manager) Reconcile(ctx context.Context, desired []Listener) error {
	desiredSet := make(map[fwdKey]struct{}, len(desired))
	for _, l := range desired {
		if l.Sandbox.Status == SandboxStatusRunning {
			desiredSet[fwdKey{sandboxID: l.Sandbox.ID, port: l.Port}] = struct{}{}
		}
	}
	for key := range m.applied {
		if _, ok := desiredSet[key]; !ok {
			if err := m.fw.Cancel(ctx, key.port); err != nil {
				return err
			}
			delete(m.applied, key)
		}
	}
	for _, l := range desired {
		if l.Sandbox.Status != SandboxStatusRunning {
			continue
		}
		key := fwdKey{sandboxID: l.Sandbox.ID, port: l.Port}
		if _, ok := m.applied[key]; ok {
			continue
		}
		present, err := m.fw.Present(ctx, l.Port)
		if err != nil {
			return err
		}
		if present {
			m.applied[key] = struct{}{}
			continue
		}
		if err := m.fw.Apply(ctx, l.Port); err != nil {
			return err
		}
		m.applied[key] = struct{}{}
	}
	return nil
}

func (m *Manager) TeardownSandbox(ctx context.Context, ref SandboxRef) error {
	for key := range m.applied {
		if key.sandboxID == ref.ID {
			if err := m.fw.Cancel(ctx, key.port); err != nil {
				return err
			}
			delete(m.applied, key)
		}
	}
	return nil
}
