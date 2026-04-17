package workspacemgr

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/config"
	"github.com/inizio/nexus/packages/nexus/pkg/projectmgr"
	"github.com/inizio/nexus/packages/nexus/pkg/store"
)

type Manager struct {
	root          string
	workspaceRepo workspaceStore
	mu            sync.RWMutex
	workspaces    map[string]*Workspace
	projectMgr    *projectmgr.Manager
}

type workspaceStore interface {
	store.WorkspaceRepository
	store.ProjectRepository
	store.SpotlightRepository
	store.SandboxResourceSettingsRepository
}

func NewManager(root string) *Manager {
	m := &Manager{
		root:       root,
		workspaces: make(map[string]*Workspace),
	}
	storePath := nodeStorePathForRoot(root, config.NodeDBPath())
	if st, err := store.Open(storePath); err == nil {
		m.workspaceRepo = st
	} else {
		fmt.Fprintf(os.Stderr, "workspacemgr: warning: sqlite store disabled (%v)\n", err)
	}
	_ = m.loadAll()
	return m
}

func nodeStorePathForRoot(root string, defaultPath string) string {
	cleanRoot := filepath.Clean(root)
	if cleanRoot == "" || defaultPath == "" {
		return defaultPath
	}

	resolvedRoot := cleanRoot
	if real, err := filepath.EvalSymlinks(cleanRoot); err == nil {
		resolvedRoot = filepath.Clean(real)
	}

	resolvedTemp := filepath.Clean(os.TempDir())
	if real, err := filepath.EvalSymlinks(resolvedTemp); err == nil {
		resolvedTemp = filepath.Clean(real)
	}

	tmpPrefix := resolvedTemp + string(filepath.Separator)
	if strings.HasPrefix(resolvedRoot+string(filepath.Separator), tmpPrefix) {
		return filepath.Join(cleanRoot, ".nexus", "state", "node.db")
	}

	return defaultPath
}

func (m *Manager) SetProjectManager(pm *projectmgr.Manager) {
	m.projectMgr = pm
}

func (m *Manager) loadAll() error {
	if m.workspaceRepo == nil {
		return nil
	}

	all, err := m.workspaceRepo.ListWorkspaceRows()
	if err != nil {
		return fmt.Errorf("list sqlite workspaces: %w", err)
	}
	for _, row := range all {
		if len(row.Payload) == 0 {
			continue
		}
		var ws Workspace
		if err := json.Unmarshal(row.Payload, &ws); err != nil {
			continue
		}
		if ws.RepoID == "" {
			ws.RepoID = deriveRepoID(ws.Repo)
		}
		if ws.RepoKind == "" {
			ws.RepoKind = deriveRepoKind(ws.Repo)
		}
		if strings.TrimSpace(ws.TargetBranch) == "" {
			ws.TargetBranch = normalizeWorkspaceRef(ws.Ref)
		}
		if strings.TrimSpace(ws.CurrentRef) == "" {
			ws.CurrentRef = normalizeWorkspaceRef(ws.Ref)
		}
		if strings.TrimSpace(ws.HostWorkspacePath) == "" {
			ws.HostWorkspacePath = strings.TrimSpace(ws.LocalWorktreePath)
		}
		if ws.LineageRootID == "" {
			if ws.ParentWorkspaceID == "" {
				ws.LineageRootID = ws.ID
			} else {
				ws.LineageRootID = ws.ParentWorkspaceID
			}
		}
		if normalized := normalizeLegacyWorkspacePath(&ws); normalized {
			_ = m.persistWorkspace(&ws)
		}
		copy := ws
		m.workspaces[ws.ID] = &copy
	}
	return nil
}

func (m *Manager) persistWorkspace(ws *Workspace) error {
	if m.workspaceRepo == nil {
		return fmt.Errorf("sqlite workspace store unavailable")
	}

	payload, err := json.Marshal(ws)
	if err != nil {
		return fmt.Errorf("marshal sqlite workspace payload: %w", err)
	}
	if err := m.workspaceRepo.UpsertWorkspaceRow(store.WorkspaceRow{
		ID:        ws.ID,
		Payload:   payload,
		CreatedAt: ws.CreatedAt,
		UpdatedAt: ws.UpdatedAt,
	}); err != nil {
		return fmt.Errorf("upsert sqlite workspace: %w", err)
	}

	return nil
}

func (m *Manager) deleteRecord(id string) {
	if m.workspaceRepo != nil {
		_ = m.workspaceRepo.DeleteWorkspace(id)
	}
}

func (m *Manager) Create(ctx context.Context, spec CreateSpec) (*Workspace, error) {
	if spec.Repo == "" {
		return nil, fmt.Errorf("repo is required")
	}
	if spec.WorkspaceName == "" {
		return nil, fmt.Errorf("workspaceName is required")
	}
	if err := ValidatePolicy(spec.Policy); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	id := fmt.Sprintf("ws-%d", now.UnixNano())
	repoID := deriveRepoID(spec.Repo)
	targetRef := normalizeWorkspaceRef(spec.Ref)
	projectID := ""
	if m.projectMgr != nil {
		project, err := m.projectMgr.GetOrCreateForRepo(spec.Repo, repoID)
		if err != nil {
			return nil, fmt.Errorf("get or create project: %w", err)
		}
		projectID = project.ID
	}

	if conflictID := m.branchConflictWorkspaceID(projectID, repoID, targetRef, ""); conflictID != "" {
		return nil, fmt.Errorf("workspace already exists for branch %q (workspace %s)", targetRef, conflictID)
	}

	rootPath := filepath.Join(m.root, "instances", id)
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace root: %w", err)
	}

	localWorktreePath := ""
	createdDetachedWorktree := false
	if hostWorkspaceRoot := resolveHostWorkspaceRoot(spec.Repo); hostWorkspaceRoot != "" {
		if gitignoreErr := EnsureNexusGitignore(hostWorkspaceRoot); gitignoreErr != nil {
			_ = os.RemoveAll(rootPath)
			return nil, fmt.Errorf("ensure .nexus gitignore: %w", gitignoreErr)
		}
		if spec.UseProjectRootPath {
			localWorktreePath = strings.TrimSpace(spec.Repo)
			if !filepath.IsAbs(localWorktreePath) {
				if absRepoPath, absErr := filepath.Abs(localWorktreePath); absErr == nil {
					localWorktreePath = absRepoPath
				}
			}
		} else {
			localWorktreePath = resolveHostWorkspacePath(hostWorkspaceRoot, targetRef, id)
			if mkErr := os.MkdirAll(localWorktreePath, 0o755); mkErr != nil {
				_ = os.RemoveAll(rootPath)
				return nil, fmt.Errorf("create host workspace path: %w", mkErr)
			}
			if setupErr := setupLocalWorkspaceCheckout(spec.Repo, localWorktreePath, targetRef); setupErr != nil {
				_ = os.RemoveAll(rootPath)
				cleanupLocalWorkspaceCheckout(spec.Repo, localWorktreePath)
				return nil, fmt.Errorf("setup host workspace checkout: %w", setupErr)
			}
			createdDetachedWorktree = true
			if markerErr := WriteHostWorkspaceMarker(localWorktreePath, id); markerErr != nil {
				_ = os.RemoveAll(rootPath)
				cleanupLocalWorkspaceCheckout(spec.Repo, localWorktreePath)
				return nil, fmt.Errorf("write workspace marker: %w", markerErr)
			}
		}
	}

	authBinding := spec.AuthBinding
	if authBinding == nil {
		authBinding = make(map[string]string)
	}
	ws := &Workspace{
		ID:                id,
		ProjectID:         projectID,
		RepoID:            repoID,
		RepoKind:          deriveRepoKind(spec.Repo),
		Repo:              spec.Repo,
		Ref:               targetRef,
		TargetBranch:      targetRef,
		CurrentRef:        targetRef,
		WorkspaceName:     spec.WorkspaceName,
		AgentProfile:      spec.AgentProfile,
		Policy:            spec.Policy,
		State:             StateCreated,
		RootPath:          rootPath,
		Backend:           spec.Backend,
		AuthBinding:       authBinding,
		LocalWorktreePath: localWorktreePath,
		HostWorkspacePath: localWorktreePath,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	ws.LineageRootID = ws.ID

	m.mu.Lock()
	m.workspaces[id] = ws
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		m.mu.Lock()
		delete(m.workspaces, id)
		m.mu.Unlock()
		_ = os.RemoveAll(rootPath)
		if createdDetachedWorktree && localWorktreePath != "" {
			cleanupLocalWorkspaceCheckout(spec.Repo, localWorktreePath)
		}
		return nil, fmt.Errorf("persist workspace: %w", err)
	}

	return cloneWorkspace(ws), nil
}

func (m *Manager) Get(id string) (*Workspace, bool) {
	m.mu.RLock()
	ws, ok := m.workspaces[id]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return cloneWorkspace(ws), true
}

func (m *Manager) List() []*Workspace {
	m.mu.RLock()
	all := make([]*Workspace, 0, len(m.workspaces))
	for _, ws := range m.workspaces {
		all = append(all, cloneWorkspace(ws))
	}
	m.mu.RUnlock()

	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})

	return all
}

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

// SetLocalWorktree stores the host-side worktree path and mutagen session ID
// on the workspace record. Both fields are optional; pass empty strings to clear them.
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
	normalized := normalizeTunnelPorts(ports)
	ws.TunnelPorts = normalized
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

// CopyDirtyStateFromWorkspace copies tracked dirty changes and untracked files
// from source workspace into target workspace when both are local git worktrees.
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

func (m *Manager) Root() string {
	return m.root
}

func (m *Manager) SpotlightRepository() store.SpotlightRepository {
	if m == nil {
		return nil
	}
	return m.workspaceRepo
}

func (m *Manager) ProjectRepository() store.ProjectRepository {
	if m == nil {
		return nil
	}
	return m.workspaceRepo
}

func (m *Manager) SandboxResourceSettingsRepository() store.SandboxResourceSettingsRepository {
	if m == nil {
		return nil
	}
	return m.workspaceRepo
}

func cloneWorkspace(in *Workspace) *Workspace {
	if in == nil {
		return nil
	}
	out := *in
	if in.AuthBinding != nil {
		out.AuthBinding = make(map[string]string, len(in.AuthBinding))
		for k, v := range in.AuthBinding {
			out.AuthBinding[k] = v
		}
	}
	if in.Policy.AuthProfiles != nil {
		out.Policy.AuthProfiles = make([]AuthProfile, len(in.Policy.AuthProfiles))
		copy(out.Policy.AuthProfiles, in.Policy.AuthProfiles)
	}
	if in.TunnelPorts != nil {
		out.TunnelPorts = make([]int, len(in.TunnelPorts))
		copy(out.TunnelPorts, in.TunnelPorts)
	}
	return &out
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

func deriveRepoKind(repo string) string {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "unknown"
	}
	if isLikelyRemoteRepo(repo) {
		return "hosted"
	}
	if strings.HasPrefix(repo, "/") || strings.HasPrefix(repo, "./") || strings.HasPrefix(repo, "../") {
		return "local"
	}
	if strings.HasPrefix(repo, "~/") {
		return "local"
	}
	if strings.Contains(repo, string(filepath.Separator)) {
		return "local"
	}
	if info, err := os.Stat(repo); err == nil && info.IsDir() {
		return "local"
	}
	return "unknown"
}

func deriveRepoID(repo string) string {
	normalized := strings.ToLower(strings.TrimSpace(repo))
	if normalized == "" {
		return "repo-unknown"
	}
	sum := sha1.Sum([]byte(normalized))
	return fmt.Sprintf("repo-%x", sum[:8])
}

func isLikelyLocalPath(repo string) bool {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return false
	}
	if isLikelyRemoteRepo(repo) {
		return false
	}
	if strings.HasPrefix(repo, "./repos/") || strings.HasPrefix(repo, "repos/") {
		return false
	}
	if strings.HasPrefix(repo, "/") || strings.HasPrefix(repo, "./") || strings.HasPrefix(repo, "../") {
		return true
	}

	if strings.HasPrefix(repo, "~/") {
		return true
	}

	if strings.Contains(repo, string(filepath.Separator)) {
		return true
	}

	if info, err := os.Stat(repo); err == nil && info.IsDir() {
		return true
	}

	return false
}

func resolveHostWorkspaceRoot(repo string) string {
	if !isLikelyLocalPath(repo) {
		return ""
	}
	cleanRepo := strings.TrimSpace(repo)
	if strings.HasPrefix(cleanRepo, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			cleanRepo = filepath.Join(home, strings.TrimPrefix(cleanRepo, "~/"))
		}
	}
	absRepo, err := filepath.Abs(cleanRepo)
	if err != nil {
		return ""
	}
	return filepath.Join(absRepo, ".worktrees")
}

func normalizeWorkspaceRef(ref string) string {
	normalized := strings.TrimSpace(ref)
	if normalized == "" {
		return "main"
	}
	return normalized
}

func workspaceScopeKey(projectID, repoID string) string {
	if strings.TrimSpace(projectID) != "" {
		return "project:" + projectID
	}
	return "repo:" + repoID
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

func isLikelyRemoteRepo(repo string) bool {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return false
	}
	if strings.HasPrefix(repo, "git@") || strings.HasPrefix(repo, "ssh://") {
		return true
	}
	if u, err := url.Parse(repo); err == nil && u.Scheme != "" && u.Host != "" {
		return true
	}
	if strings.Contains(repo, "@") && strings.Contains(repo, ":") {
		return true
	}
	return false
}

func setupLocalWorkspaceCheckout(repoPath, workspacePath, targetRef string) error {
	repoPath = strings.TrimSpace(repoPath)
	workspacePath = strings.TrimSpace(workspacePath)
	targetRef = normalizeWorkspaceRef(targetRef)
	if repoPath == "" || workspacePath == "" {
		return nil
	}
	if !looksLikeGitRepo(repoPath) {
		// Keep non-git local-path behavior unchanged for tests and custom directories.
		return nil
	}
	if !isDirEmpty(workspacePath) {
		return fmt.Errorf("workspace path must be empty before checkout: %s", workspacePath)
	}

	startRef := targetRef
	if !localBranchExists(repoPath, targetRef) {
		startRef = "HEAD"
	}

	if _, err := runGit(repoPath, "worktree", "add", "--force", "--detach", workspacePath, startRef); err != nil {
		return err
	}
	if localBranchExists(repoPath, targetRef) {
		if _, err := runGit(workspacePath, "checkout", "--ignore-other-worktrees", targetRef); err != nil {
			cleanupLocalWorkspaceCheckout(repoPath, workspacePath)
			return err
		}
		return nil
	}
	if _, err := runGit(workspacePath, "checkout", "--ignore-other-worktrees", "-B", targetRef); err != nil {
		cleanupLocalWorkspaceCheckout(repoPath, workspacePath)
		return err
	}
	return nil
}

func setupForkLocalWorkspaceCheckout(repoPath, parentWorkspacePath, childWorkspacePath, targetRef string) error {
	if err := setupLocalWorkspaceCheckout(repoPath, childWorkspacePath, targetRef); err != nil {
		return err
	}
	parentWorkspacePath = strings.TrimSpace(parentWorkspacePath)
	if parentWorkspacePath == "" || !looksLikeGitRepo(parentWorkspacePath) {
		return nil
	}
	if err := copyDirtyStateFromParent(parentWorkspacePath, childWorkspacePath); err != nil {
		cleanupLocalWorkspaceCheckout(repoPath, childWorkspacePath)
		return err
	}
	return nil
}

func copyDirtyStateFromParent(parentWorkspacePath, childWorkspacePath string) error {
	diffOut, err := runGitRaw(parentWorkspacePath, "diff", "--binary", "HEAD")
	if err != nil {
		return err
	}
	if strings.TrimSpace(diffOut) != "" {
		if err := runGitWithInput(childWorkspacePath, diffOut, "apply", "--whitespace=nowarn", "--binary"); err != nil {
			return err
		}
	}
	return copyUntrackedFiles(parentWorkspacePath, childWorkspacePath)
}

func copyUntrackedFiles(parentWorkspacePath, childWorkspacePath string) error {
	out, err := runGitRaw(parentWorkspacePath, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	if out == "" {
		return nil
	}
	paths := strings.Split(out, "\x00")
	for _, rel := range paths {
		rel = strings.TrimSpace(rel)
		if rel == "" {
			continue
		}
		src := filepath.Join(parentWorkspacePath, rel)
		dst := filepath.Join(childWorkspacePath, rel)
		if err := copyPath(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyPath(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		_ = os.Remove(dst)
		return os.Symlink(target, dst)
	}
	if info.IsDir() {
		return os.MkdirAll(dst, info.Mode().Perm())
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return nil
}

func cleanupLocalWorkspaceCheckout(repoPath, workspacePath string) {
	repoPath = strings.TrimSpace(repoPath)
	workspacePath = strings.TrimSpace(workspacePath)
	if repoPath == "" || workspacePath == "" {
		return
	}
	if looksLikeGitRepo(repoPath) {
		_, _ = runGit(repoPath, "worktree", "remove", "--force", workspacePath)
		_, _ = runGit(repoPath, "worktree", "prune")
	}
	_ = os.RemoveAll(workspacePath)
}

func runGit(dir string, args ...string) (string, error) {
	out, err := runGitRaw(dir, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func runGitRaw(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s failed in %s: %s", strings.Join(args, " "), dir, msg)
	}
	return stdout.String(), nil
}

func runGitWithInput(dir string, stdin string, args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("git %s failed in %s: %s", strings.Join(args, " "), dir, msg)
	}
	return nil
}

func looksLikeGitRepo(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := runGit(path, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

func localBranchExists(repoPath, branch string) bool {
	if strings.TrimSpace(repoPath) == "" || strings.TrimSpace(branch) == "" {
		return false
	}
	_, err := runGit(repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+strings.TrimSpace(branch))
	return err == nil
}

func isDirEmpty(path string) bool {
	entries, err := os.ReadDir(path)
	if err != nil {
		return false
	}
	return len(entries) == 0
}

func resolveHostWorkspacePath(hostWorkspaceRoot, ref, workspaceID string) string {
	base := HostWorkspaceDirName(ref)
	if strings.TrimSpace(base) == "" {
		base = strings.TrimSpace(workspaceID)
	}
	candidate := filepath.Join(hostWorkspaceRoot, base)
	if _, err := os.Stat(candidate); os.IsNotExist(err) {
		return candidate
	}
	if HasValidHostWorkspaceMarker(candidate, workspaceID) {
		return candidate
	}
	fallback := strings.TrimSpace(workspaceID)
	if fallback == "" {
		fallback = "workspace"
	}
	return filepath.Join(hostWorkspaceRoot, base+"-"+fallback)
}

func normalizeLegacyWorkspacePath(ws *Workspace) bool {
	if ws == nil {
		return false
	}
	current := strings.TrimSpace(ws.LocalWorktreePath)
	if current == "" {
		return false
	}
	legacyNeedle := string(filepath.Separator) + ".nexus" + string(filepath.Separator) + "workspaces" + string(filepath.Separator)
	if !strings.Contains(current, legacyNeedle) {
		return false
	}
	hostRoot := resolveHostWorkspaceRoot(ws.Repo)
	if hostRoot == "" {
		return false
	}
	ref := strings.TrimSpace(ws.CurrentRef)
	if ref == "" {
		ref = strings.TrimSpace(ws.TargetBranch)
	}
	if ref == "" {
		ref = strings.TrimSpace(ws.Ref)
	}
	candidate := resolveHostWorkspacePath(hostRoot, ref, ws.ID)
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return false
	}
	ws.LocalWorktreePath = candidate
	ws.HostWorkspacePath = candidate
	ws.UpdatedAt = time.Now().UTC()
	return true
}

func HostWorkspaceDirName(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "main"
	}
	var b strings.Builder
	for _, r := range ref {
		isLetter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		isNumber := r >= '0' && r <= '9'
		switch {
		case isLetter || isNumber || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		case r == '/' || r == '\\' || r == ' ':
			b.WriteByte('-')
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "main"
	}
	return out
}
