package workspacemgr

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type RemoveOptions struct {
	DeleteHostPath bool
}

func (m *Manager) Remove(id string) bool {
	removed, _ := m.RemoveWithOptions(id, RemoveOptions{DeleteHostPath: true})
	return removed
}

func (m *Manager) RemoveWithOptions(id string, opts RemoveOptions) (bool, error) {
	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if ok {
		delete(m.workspaces, id)
	}
	m.mu.Unlock()

	if ok {
		if err := os.RemoveAll(ws.RootPath); err != nil {
			log.Printf("workspace.remove: RemoveAll %s: %v", ws.RootPath, err)
		}
		if opts.DeleteHostPath && strings.TrimSpace(ws.LocalWorktreePath) != "" {
			cleanupLocalWorkspaceCheckout(ws.Repo, ws.LocalWorktreePath)
			if _, err := os.Stat(ws.LocalWorktreePath); err == nil {
				if err := os.RemoveAll(ws.LocalWorktreePath); err != nil {
					log.Printf("workspace.remove: RemoveAll %s: %v", ws.LocalWorktreePath, err)
				}
			}
		}
		m.deleteRecord(id)
	}

	return ok, nil
}

func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	if ws.State == StateRemoved {
		m.mu.Unlock()
		return fmt.Errorf("cannot stop removed workspace: %s", id)
	}
	ws.State = StateStopped
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		return fmt.Errorf("persist stop: %w", err)
	}
	return nil
}

func (m *Manager) Restore(id string) (*Workspace, bool) {
	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if !ok {
		m.mu.Unlock()
		return nil, false
	}
	if ws.State == StateRemoved {
		m.mu.Unlock()
		return nil, false
	}
	ws.State = StateRestored
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		return nil, false
	}
	return cloneWorkspace(ws), true
}

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

func (m *Manager) Start(id string) error {
	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	if ws.State == StateRemoved {
		m.mu.Unlock()
		return fmt.Errorf("cannot start removed workspace: %s", id)
	}
	ws.State = StateRunning
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		return fmt.Errorf("persist start: %w", err)
	}
	return nil
}

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
