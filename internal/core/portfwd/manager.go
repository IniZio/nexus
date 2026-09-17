package portfwd

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
)

type fwdKey struct {
	sandboxID string
	port      uint16
}

type persistedEntry struct {
	SandboxID string `json:"sandbox_id"`
	Port      uint16 `json:"port"`
}

type persistedApplied struct {
	Entries []persistedEntry `json:"entries"`
}

type Manager struct {
	fw               *Forwarder
	applied          map[fwdKey]struct{}
	persistPath      string
	cancelWarnedPort map[uint16]struct{}
}

// NewManager loads a persisted applied-set from <controlpath>.applied.json so
// a restarted client can cancel orphaned forwards from the previous process.
func NewManager(fw *Forwarder) *Manager {
	m := &Manager{
		fw:               fw,
		applied:          make(map[fwdKey]struct{}),
		cancelWarnedPort: make(map[uint16]struct{}),
	}
	if fw.ControlPath != "" {
		m.persistPath = fw.ControlPath + ".applied.json"
		m.loadApplied()
	}
	return m
}

func (m *Manager) loadApplied() {
	data, err := os.ReadFile(m.persistPath)
	if err != nil {
		return
	}
	var pa persistedApplied
	if err := json.Unmarshal(data, &pa); err != nil {
		return
	}
	for _, e := range pa.Entries {
		m.applied[fwdKey{sandboxID: e.SandboxID, port: e.Port}] = struct{}{}
	}
}

func (m *Manager) saveApplied() {
	if m.persistPath == "" {
		return
	}
	pa := persistedApplied{Entries: make([]persistedEntry, 0, len(m.applied))}
	for k := range m.applied {
		pa.Entries = append(pa.Entries, persistedEntry{SandboxID: k.sandboxID, Port: k.port})
	}
	data, err := json.Marshal(pa)
	if err != nil {
		slog.Warn("portfwd: marshal applied set", "err", err)
		return
	}
	tmp := m.persistPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		slog.Warn("portfwd: write applied set", "err", err)
		return
	}
	if err := os.Rename(tmp, m.persistPath); err != nil {
		slog.Warn("portfwd: rename applied set", "err", err)
		_ = os.Remove(tmp)
	}
}

// AdoptMasterForwards seeds the applied set with every port our ControlMaster
// currently forwards that no applied.json entry covers. Those forwards were
// left by a previous client (possibly one that never wrote applied.json);
// adopting them under an unknown sandbox makes them cancellable by Reconcile.
func (m *Manager) AdoptMasterForwards(ctx context.Context) error {
	ports, err := m.fw.MasterForwards(ctx)
	if err != nil {
		return err
	}
	added := 0
	for _, p := range ports {
		if m.hasPort(p) {
			continue
		}
		m.applied[fwdKey{port: p}] = struct{}{}
		added++
	}
	if added > 0 {
		slog.Info("portfwd: adopted forwards left on the control master", "count", added)
		m.saveApplied()
	}
	return nil
}

func (m *Manager) hasPort(port uint16) bool {
	for k := range m.applied {
		if k.port == port {
			return true
		}
	}
	return false
}

func (m *Manager) Reconcile(ctx context.Context, desired []Listener) error {
	desiredSet := make(map[fwdKey]struct{}, len(desired))
	for _, l := range desired {
		if l.Sandbox.Status == SandboxStatusRunning {
			desiredSet[fwdKey{sandboxID: l.Sandbox.ID, port: l.Port}] = struct{}{}
		}
	}
	for want := range desiredSet {
		if _, ok := m.applied[fwdKey{port: want.port}]; ok && want.sandboxID != "" {
			delete(m.applied, fwdKey{port: want.port})
			m.applied[want] = struct{}{}
		}
	}
	for key := range m.applied {
		if _, ok := desiredSet[key]; !ok {
			if err := m.fw.Cancel(ctx, key.port); err != nil {
				if _, warned := m.cancelWarnedPort[key.port]; !warned {
					slog.Warn("portfwd: cancel forward failed, entry retained", "port", key.port, "err", err)
					m.cancelWarnedPort[key.port] = struct{}{}
				}
				continue
			}
			delete(m.applied, key)
		}
	}
	m.saveApplied()

	for _, l := range desired {
		if l.Sandbox.Status != SandboxStatusRunning {
			continue
		}
		key := fwdKey{sandboxID: l.Sandbox.ID, port: l.Port}
		if _, ok := m.applied[key]; ok {
			continue
		}
		presence, err := m.fw.Present(ctx, l.Port)
		if err != nil {
			return err
		}
		switch presence {
		case PresenceForeign:
			slog.Warn("portfwd: port in use by another process, forward skipped",
				"port", l.Port, "sandbox", l.Sandbox.ID)
			continue
		case PresenceOurs:
			slog.Info("portfwd: adopted forward already on the control master",
				"port", l.Port, "sandbox", l.Sandbox.ID)
		default:
			if err := m.fw.Apply(ctx, l.Port); err != nil {
				return err
			}
		}
		m.applied[key] = struct{}{}
	}
	m.saveApplied()
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
	m.saveApplied()
	return nil
}
