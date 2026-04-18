package workspacemgr

import (
	"fmt"
	"strings"
	"time"
)

func (m *Manager) CopyDirtyStateFromWorkspace(sourceWorkspaceID string, targetWorkspaceID string) error {
	sourceWorkspaceID = strings.TrimSpace(sourceWorkspaceID)
	targetWorkspaceID = strings.TrimSpace(targetWorkspaceID)
	if sourceWorkspaceID == "" || targetWorkspaceID == "" || sourceWorkspaceID == targetWorkspaceID {
		return nil
	}

	m.mu.RLock()
	source, sourceOK := m.workspaces[sourceWorkspaceID]
	target, targetOK := m.workspaces[targetWorkspaceID]
	m.mu.RUnlock()
	if !sourceOK {
		return fmt.Errorf("source workspace not found: %s", sourceWorkspaceID)
	}
	if !targetOK {
		return fmt.Errorf("target workspace not found: %s", targetWorkspaceID)
	}

	sourcePath := strings.TrimSpace(source.LocalWorktreePath)
	targetPath := strings.TrimSpace(target.LocalWorktreePath)
	if sourcePath == "" || targetPath == "" || sourcePath == targetPath {
		return nil
	}
	if strings.TrimSpace(source.RepoID) != "" && strings.TrimSpace(target.RepoID) != "" && strings.TrimSpace(source.RepoID) != strings.TrimSpace(target.RepoID) {
		return fmt.Errorf("workspace repo mismatch: source %s target %s", source.RepoID, target.RepoID)
	}
	if !looksLikeGitRepo(sourcePath) || !looksLikeGitRepo(targetPath) {
		return nil
	}
	if err := copyDirtyStateFromParent(sourcePath, targetPath); err != nil {
		return fmt.Errorf("copy dirty state from %s to %s: %w", sourceWorkspaceID, targetWorkspaceID, err)
	}

	m.mu.Lock()
	ws, ok := m.workspaces[targetWorkspaceID]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("target workspace not found: %s", targetWorkspaceID)
	}
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()
	if err := m.persistWorkspace(ws); err != nil {
		return fmt.Errorf("persist target workspace after dirty sync: %w", err)
	}
	return nil
}

func (m *Manager) Checkout(id string, targetRef string) (*Workspace, error) {
	normalizedTarget := normalizeWorkspaceRef(targetRef)
	if normalizedTarget == "" {
		return nil, fmt.Errorf("target ref is required")
	}

	m.mu.RLock()
	current, ok := m.workspaces[id]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("workspace not found: %s", id)
	}
	if current.State == StateRemoved {
		return nil, fmt.Errorf("cannot checkout removed workspace: %s", id)
	}

	if conflictID := m.branchConflictWorkspaceID(current.ProjectID, current.RepoID, normalizedTarget, id); conflictID != "" {
		return nil, fmt.Errorf("workspace already exists for branch %q (workspace %s)", normalizedTarget, conflictID)
	}

	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("workspace not found: %s", id)
	}
	if ws.State == StateRemoved {
		m.mu.Unlock()
		return nil, fmt.Errorf("cannot checkout removed workspace: %s", id)
	}
	ws.Ref = normalizedTarget
	ws.TargetBranch = normalizedTarget
	ws.CurrentRef = normalizedTarget
	ws.CurrentCommit = ""
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		return nil, fmt.Errorf("persist checkout: %w", err)
	}
	return cloneWorkspace(ws), nil
}

func (m *Manager) CanCheckout(id string, targetRef string) error {
	normalizedTarget := normalizeWorkspaceRef(targetRef)
	if normalizedTarget == "" {
		return fmt.Errorf("target ref is required")
	}

	m.mu.RLock()
	current, ok := m.workspaces[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("workspace not found: %s", id)
	}
	if current.State == StateRemoved {
		return fmt.Errorf("cannot checkout removed workspace: %s", id)
	}
	if conflictID := m.branchConflictWorkspaceID(current.ProjectID, current.RepoID, normalizedTarget, id); conflictID != "" {
		return fmt.Errorf("workspace already exists for branch %q (workspace %s)", normalizedTarget, conflictID)
	}
	return nil
}
