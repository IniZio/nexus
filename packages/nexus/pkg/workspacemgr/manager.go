package workspacemgr

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/config"
	"github.com/inizio/nexus/packages/nexus/pkg/git/worktree"
	"github.com/inizio/nexus/packages/nexus/pkg/store"
)

type Manager struct {
	root          string
	workspaceRepo workspaceStore
	mu            sync.RWMutex
	workspaces    map[string]*Workspace
}

type workspaceStore interface {
	store.WorkspaceRepository
	store.SpotlightRepository
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
		if ws.LineageRootID == "" {
			if ws.ParentWorkspaceID == "" {
				ws.LineageRootID = ws.ID
			} else {
				ws.LineageRootID = ws.ParentWorkspaceID
			}
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

func (m *Manager) Create(_ context.Context, spec CreateSpec) (*Workspace, error) {
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
	rootPath := filepath.Join(m.root, "instances", id)
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace root: %w", err)
	}

	localWorktreePath := ""
	if isLikelyLocalPath(spec.Repo) {
		worktreePath, wtErr := worktree.Create(spec.Repo, spec.Ref, spec.WorkspaceName)
		if wtErr != nil {
			_ = os.RemoveAll(rootPath)
			return nil, wtErr
		}
		localWorktreePath = worktreePath
	}

	authBinding := spec.AuthBinding
	if authBinding == nil {
		authBinding = make(map[string]string)
	}
	ws := &Workspace{
		ID:                id,
		RepoID:            deriveRepoID(spec.Repo),
		RepoKind:          deriveRepoKind(spec.Repo),
		Repo:              spec.Repo,
		Ref:               spec.Ref,
		WorkspaceName:     spec.WorkspaceName,
		AgentProfile:      spec.AgentProfile,
		Policy:            spec.Policy,
		State:             StateCreated,
		RootPath:          rootPath,
		Backend:           spec.Backend,
		AuthBinding:       authBinding,
		LocalWorktreePath: localWorktreePath,
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
		worktree.CleanupCreatedWorktree(spec.Repo, localWorktreePath)
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

func (m *Manager) Remove(id string) bool {
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
		m.deleteRecord(id)
	}

	return ok
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
	ws.MutagenSessionID = mutagenSessionID
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		return fmt.Errorf("persist local worktree: %w", err)
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

func (m *Manager) Pause(id string) error {
	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	if ws.State == StateRemoved {
		m.mu.Unlock()
		return fmt.Errorf("cannot pause removed workspace: %s", id)
	}
	ws.State = StatePaused
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		return fmt.Errorf("persist pause: %w", err)
	}
	return nil
}

func (m *Manager) Resume(id string) error {
	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	if ws.State == StateRemoved {
		m.mu.Unlock()
		return fmt.Errorf("cannot resume removed workspace: %s", id)
	}
	ws.State = StateRunning
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		return fmt.Errorf("persist resume: %w", err)
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
	if strings.TrimSpace(childRef) == "" {
		return nil, fmt.Errorf("child ref is required")
	}

	now := time.Now().UTC()
	childID := fmt.Sprintf("ws-%d", now.UnixNano())
	childRootPath := filepath.Join(m.root, "instances", childID)
	if err := os.MkdirAll(childRootPath, 0o755); err != nil {
		return nil, fmt.Errorf("create child workspace root: %w", err)
	}

	childLocalWorktreePath := ""
	parentForkBase := worktree.ResolveForkBasePath(worktree.ForkParentInput{
		Repo:              parent.Repo,
		WorkspaceName:     parent.WorkspaceName,
		LocalWorktreePath: parent.LocalWorktreePath,
	})
	if parentForkBase != "" {
		worktreePath, wtErr := worktree.CreateFork(parentForkBase, childRef, childWorkspaceName)
		if wtErr != nil {
			_ = os.RemoveAll(childRootPath)
			return nil, wtErr
		}
		childLocalWorktreePath = worktreePath
	}

	child := &Workspace{
		ID:                childID,
		RepoID:            parent.RepoID,
		RepoKind:          parent.RepoKind,
		Repo:              parent.Repo,
		Ref:               childRef,
		WorkspaceName:     childWorkspaceName,
		AgentProfile:      parent.AgentProfile,
		Policy:            parent.Policy,
		State:             StateCreated,
		RootPath:          childRootPath,
		ParentWorkspaceID: parent.ID,
		LineageRootID:     parent.LineageRootID,
		DerivedFromRef:    parent.Ref,
		Backend:           parent.Backend,
		AuthBinding:       make(map[string]string, len(parent.AuthBinding)),
		LocalWorktreePath: childLocalWorktreePath,
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
	return &out
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

