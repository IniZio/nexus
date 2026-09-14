package portfwd

type AppliedEntry struct {
	SandboxID string
	Port      uint16
}

func (m *Manager) Applied() []AppliedEntry {
	out := make([]AppliedEntry, 0, len(m.applied))
	for k := range m.applied {
		out = append(out, AppliedEntry{SandboxID: k.sandboxID, Port: k.port})
	}
	return out
}
