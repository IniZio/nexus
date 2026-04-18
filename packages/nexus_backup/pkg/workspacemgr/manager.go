package workspacemgr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/config"
	"github.com/inizio/nexus/packages/nexus/pkg/project"
	"github.com/inizio/nexus/packages/nexus/pkg/store"
)

type Manager struct {
	root          string
	workspaceRepo workspaceStore
	mu            sync.RWMutex
	workspaces    map[string]*Workspace
	projectMgr    *project.Manager
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

func (m *Manager) SetProjectManager(pm *project.Manager) {
	m.projectMgr = pm
}

func (m *Manager) Root() string {
	return m.root
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
