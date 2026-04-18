package workspacemgr

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func (m *Manager) SetBackend(id string, backend string) error {
	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	if ws.State == StateRemoved {
		m.mu.Unlock()
		return fmt.Errorf("cannot update backend for removed workspace: %s", id)
	}
	ws.Backend = backend
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		return fmt.Errorf("persist backend: %w", err)
	}

	return nil
}

func (m *Manager) SetLineageSnapshot(id string, snapshotID string) error {
	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	if ws.State == StateRemoved {
		m.mu.Unlock()
		return fmt.Errorf("cannot update snapshot for removed workspace: %s", id)
	}
	ws.LineageSnapshotID = strings.TrimSpace(snapshotID)
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		return fmt.Errorf("persist lineage snapshot: %w", err)
	}

	return nil
}

func (m *Manager) SetLocalWorktree(id, worktreePath, mutagenSessionID string) error {
	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	ws.LocalWorktreePath = worktreePath
	ws.HostWorkspacePath = worktreePath
	ws.MutagenSessionID = mutagenSessionID
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		return fmt.Errorf("persist local worktree: %w", err)
	}
	return nil
}

func (m *Manager) SetTunnelPorts(id string, ports []int) error {
	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	ws.TunnelPorts = normalizeTunnelPorts(ports)
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()
	if err := m.persistWorkspace(ws); err != nil {
		return fmt.Errorf("persist tunnel ports: %w", err)
	}
	return nil
}

func (m *Manager) UpdateProjectID(id string, projectID string) error {
	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	ws.ProjectID = projectID
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		return fmt.Errorf("persist project id update: %w", err)
	}
	return nil
}

func (m *Manager) SetParentWorkspace(id string, parentWorkspaceID string) error {
	parentWorkspaceID = strings.TrimSpace(parentWorkspaceID)

	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	if parentWorkspaceID != "" && parentWorkspaceID == ws.ID {
		m.mu.Unlock()
		return fmt.Errorf("workspace cannot be its own parent: %s", id)
	}
	ws.ParentWorkspaceID = parentWorkspaceID
	if parentWorkspaceID == "" {
		ws.LineageRootID = ws.ID
	} else if parent, ok := m.workspaces[parentWorkspaceID]; ok && parent != nil {
		if strings.TrimSpace(parent.LineageRootID) != "" {
			ws.LineageRootID = strings.TrimSpace(parent.LineageRootID)
		} else {
			ws.LineageRootID = strings.TrimSpace(parent.ID)
		}
	} else {
		ws.LineageRootID = parentWorkspaceID
	}
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		return fmt.Errorf("persist parent workspace: %w", err)
	}
	return nil
}

func (m *Manager) SetCurrentCommit(id string, commit string) error {
	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	ws.CurrentCommit = strings.TrimSpace(commit)
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		return fmt.Errorf("persist current commit: %w", err)
	}
	return nil
}

func (m *Manager) SetDerivedFromRef(id string, ref string) error {
	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	ws.DerivedFromRef = strings.TrimSpace(ref)
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		return fmt.Errorf("persist derived ref: %w", err)
	}
	return nil
}

func normalizeTunnelPorts(ports []int) []int {
	if len(ports) == 0 {
		return nil
	}
	seen := make(map[int]struct{}, len(ports))
	out := make([]int, 0, len(ports))
	for _, p := range ports {
		if p <= 0 || p > 65535 {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i] < out[j]
	})
	return out
}
