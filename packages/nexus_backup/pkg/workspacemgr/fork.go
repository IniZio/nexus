package workspacemgr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (m *Manager) Fork(parentID string, childWorkspaceName string, childRef string) (*Workspace, error) {
	m.mu.RLock()
	parent, ok := m.workspaces[parentID]
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("workspace not found: %s", parentID)
	}
	if parent.State == StateRemoved {
		return nil, fmt.Errorf("cannot fork removed workspace: %s", parentID)
	}

	if strings.TrimSpace(childWorkspaceName) == "" {
		childWorkspaceName = parent.WorkspaceName + "-fork"
	}
	targetRef := normalizeWorkspaceRef(childRef)
	if targetRef == "" {
		return nil, fmt.Errorf("child ref is required")
	}
	if conflictID := m.branchConflictWorkspaceID(parent.ProjectID, parent.RepoID, targetRef, ""); conflictID != "" {
		return nil, fmt.Errorf("workspace already exists for branch %q (workspace %s)", targetRef, conflictID)
	}

	now := time.Now().UTC()
	childID := fmt.Sprintf("ws-%d", now.UnixNano())
	childRootPath := filepath.Join(m.root, "instances", childID)
	if err := os.MkdirAll(childRootPath, 0o755); err != nil {
		return nil, fmt.Errorf("create child workspace root: %w", err)
	}

	childLocalWorktreePath := ""
	if hostWorkspaceRoot := resolveHostWorkspaceRoot(parent.Repo); hostWorkspaceRoot != "" {
		if gitignoreErr := EnsureNexusGitignore(hostWorkspaceRoot); gitignoreErr != nil {
			_ = os.RemoveAll(childRootPath)
			return nil, fmt.Errorf("ensure .nexus gitignore: %w", gitignoreErr)
		}
		childLocalWorktreePath = resolveHostWorkspacePath(hostWorkspaceRoot, targetRef, childID)
		if mkErr := os.MkdirAll(childLocalWorktreePath, 0o755); mkErr != nil {
			_ = os.RemoveAll(childRootPath)
			return nil, fmt.Errorf("create child host workspace path: %w", mkErr)
		}
		if setupErr := setupForkLocalWorkspaceCheckout(parent.Repo, parent.LocalWorktreePath, childLocalWorktreePath, targetRef); setupErr != nil {
			_ = os.RemoveAll(childRootPath)
			cleanupLocalWorkspaceCheckout(parent.Repo, childLocalWorktreePath)
			return nil, fmt.Errorf("setup child host workspace checkout: %w", setupErr)
		}
		if markerErr := WriteHostWorkspaceMarker(childLocalWorktreePath, childID); markerErr != nil {
			_ = os.RemoveAll(childRootPath)
			cleanupLocalWorkspaceCheckout(parent.Repo, childLocalWorktreePath)
			return nil, fmt.Errorf("write child workspace marker: %w", markerErr)
		}
	}

	child := &Workspace{
		ID:                childID,
		ProjectID:         parent.ProjectID,
		RepoID:            parent.RepoID,
		RepoKind:          parent.RepoKind,
		Repo:              parent.Repo,
		Ref:               targetRef,
		TargetBranch:      targetRef,
		CurrentRef:        targetRef,
		WorkspaceName:     childWorkspaceName,
		AgentProfile:      parent.AgentProfile,
		Policy:            parent.Policy,
		State:             StateCreated,
		RootPath:          childRootPath,
		ParentWorkspaceID: parent.ID,
		LineageRootID:     parent.LineageRootID,
		DerivedFromRef:    parent.Ref,
		Backend:           parent.Backend,
		LineageSnapshotID: parent.LineageSnapshotID,
		AuthBinding:       make(map[string]string, len(parent.AuthBinding)),
		LocalWorktreePath: childLocalWorktreePath,
		HostWorkspacePath: childLocalWorktreePath,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	if child.LineageRootID == "" {
		child.LineageRootID = parent.ID
	}
	for k, v := range parent.AuthBinding {
		child.AuthBinding[k] = v
	}

	m.mu.Lock()
	m.workspaces[childID] = child
	m.mu.Unlock()

	if err := m.persistWorkspace(child); err != nil {
		m.mu.Lock()
		delete(m.workspaces, childID)
		m.mu.Unlock()
		_ = os.RemoveAll(childRootPath)
		if childLocalWorktreePath != "" {
			cleanupLocalWorkspaceCheckout(parent.Repo, childLocalWorktreePath)
		}
		return nil, fmt.Errorf("persist child workspace: %w", err)
	}

	return cloneWorkspace(child), nil
}

func (m *Manager) branchConflictWorkspaceID(projectID, repoID, targetRef, excludeWorkspaceID string) string {
	scopeKey := workspaceScopeKey(projectID, repoID)
	normalizedTarget := normalizeWorkspaceRef(targetRef)

	m.mu.RLock()
	defer m.mu.RUnlock()
	for id, ws := range m.workspaces {
		if id == excludeWorkspaceID {
			continue
		}
		if ws.State == StateRemoved {
			continue
		}
		if workspaceScopeKey(ws.ProjectID, ws.RepoID) != scopeKey {
			continue
		}
		if normalizeWorkspaceRef(ws.Ref) != normalizedTarget {
			continue
		}
		return id
	}
	return ""
}
