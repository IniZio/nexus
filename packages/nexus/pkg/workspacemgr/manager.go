package workspacemgr

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Manager struct {
	root       string
	mu         sync.RWMutex
	workspaces map[string]*Workspace
}

func NewManager(root string) *Manager {
	m := &Manager{
		root:       root,
		workspaces: make(map[string]*Workspace),
	}
	_ = m.loadAll()
	return m
}

func (m *Manager) workspacesDir() string {
	return filepath.Join(m.root, "workspaces")
}

func (m *Manager) recordPath(id string) string {
	return filepath.Join(m.workspacesDir(), id+".json")
}

func (m *Manager) loadAll() error {
	dir := m.workspacesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read workspaces dir: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-5]
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var ws Workspace
		if err := json.Unmarshal(data, &ws); err != nil {
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
		m.workspaces[id] = &ws
	}
	return nil
}

func (m *Manager) persistWorkspace(ws *Workspace) error {
	if err := os.MkdirAll(m.workspacesDir(), 0o755); err != nil {
		return fmt.Errorf("create workspaces dir: %w", err)
	}
	data, err := json.Marshal(ws)
	if err != nil {
		return fmt.Errorf("marshal workspace: %w", err)
	}
	if err := os.WriteFile(m.recordPath(ws.ID), data, 0o644); err != nil {
		return fmt.Errorf("write workspace record: %w", err)
	}
	return nil
}

func (m *Manager) deleteRecord(id string) {
	_ = os.Remove(m.recordPath(id))
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
		worktreePath, wtErr := createWorktree(spec.Repo, spec.Ref, spec.WorkspaceName)
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
		_ = os.RemoveAll(ws.RootPath)
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
	parentForkBase := resolveForkBasePath(parent)
	if parentForkBase != "" {
		worktreePath, wtErr := createForkWorktree(parentForkBase, childRef, childWorkspaceName)
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

func createWorktree(repoPath, ref, workspaceName string) (string, error) {
	base := filepath.Clean(repoPath)
	if workspaceName == "" {
		workspaceName = "workspace"
	}
	safeName := sanitizeWorktreeName(workspaceName)
	worktreesDir := filepath.Join(base, ".worktrees")
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		return "", fmt.Errorf("create .worktrees dir: %w", err)
	}
	worktreePath := filepath.Join(worktreesDir, safeName)
	worktreePath = uniqueWorktreePath(worktreePath)

	branch := strings.TrimSpace(ref)
	if branch == "" {
		branch = safeName
	}
	branch = uniqueBranchName(base, branch)

	cmd := exec.Command("git", "-C", base, "worktree", "add", "-b", branch, worktreePath, "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		for retry := 0; retry < 5 && err != nil; retry++ {
			if strings.Contains(string(out), "already exists") {
				worktreePath = uniqueWorktreePath(worktreePath)
				branch = uniqueBranchName(base, branch)
				cmd = exec.Command("git", "-C", base, "worktree", "add", "-b", branch, worktreePath, "HEAD")
				out, err = cmd.CombinedOutput()
				continue
			}
			break
		}
		if err != nil {
			return "", fmt.Errorf("git worktree add failed: %s", strings.TrimSpace(string(out)))
		}
	}
	return worktreePath, nil
}

func createForkWorktree(parentWorktreePath, ref, childWorkspaceName string) (string, error) {
	parentPath := filepath.Clean(parentWorktreePath)
	worktreesDir := forkChildrenDir(parentPath)
	if err := os.MkdirAll(worktreesDir, 0o755); err != nil {
		return "", fmt.Errorf("create nested .worktrees dir: %w", err)
	}
	safeName := sanitizeWorktreeName(childWorkspaceName)
	childPath := filepath.Join(worktreesDir, safeName)
	childPath = uniqueWorktreePath(childPath)

	branch := strings.TrimSpace(ref)
	if branch == "" {
		branch = safeName
	}
	branch = uniqueBranchName(parentPath, branch)

	cmd := exec.Command("git", "-C", parentPath, "worktree", "add", "-b", branch, childPath, "HEAD")
	out, err := cmd.CombinedOutput()
	if err != nil {
		for retry := 0; retry < 5 && err != nil; retry++ {
			if strings.Contains(string(out), "already exists") {
				childPath = uniqueWorktreePath(childPath)
				branch = uniqueBranchName(parentPath, branch)
				cmd = exec.Command("git", "-C", parentPath, "worktree", "add", "-b", branch, childPath, "HEAD")
				out, err = cmd.CombinedOutput()
				continue
			}
			break
		}
		if err != nil {
			return "", fmt.Errorf("git nested worktree add failed: %s", strings.TrimSpace(string(out)))
		}
	}
	return childPath, nil
}

func forkChildrenDir(parentPath string) string {
	marker := string(filepath.Separator) + ".worktrees" + string(filepath.Separator)
	if idx := strings.Index(parentPath, marker); idx >= 0 {
		repoRoot := parentPath[:idx]
		return filepath.Join(repoRoot, ".worktrees")
	}
	return filepath.Join(parentPath, ".worktrees")
}

func resolveForkBasePath(parent *Workspace) string {
	if parent == nil {
		return ""
	}

	if localPath := strings.TrimSpace(parent.LocalWorktreePath); localPath != "" {
		candidate := filepath.Clean(localPath)
		if !looksLikeWorktree(candidate) {
			candidate = ""
		}
		if candidate != "" {
			if looksLikeRepoRoot(candidate) {
				nested := filepath.Join(candidate, ".worktrees", sanitizeWorktreeName(parent.WorkspaceName))
				if pathExists(nested) {
					return nested
				}
			}
			return candidate
		}
	}

	if isLikelyLocalPath(parent.Repo) {
		inferred := filepath.Join(filepath.Clean(parent.Repo), ".worktrees", sanitizeWorktreeName(parent.WorkspaceName))
		if pathExists(inferred) {
			return inferred
		}
	}

	return ""
}

func looksLikeWorktree(path string) bool {
	if path == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return true
	}
	return false
}

func looksLikeRepoRoot(path string) bool {
	if path == "" {
		return false
	}
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		return true
	}
	return false
}

func pathExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func sanitizeWorktreeName(name string) string {
	n := strings.TrimSpace(strings.ToLower(name))
	if n == "" {
		return "workspace"
	}
	n = strings.ReplaceAll(n, " ", "-")
	var b strings.Builder
	for _, r := range n {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "workspace"
	}
	return out
}

func uniqueBranchName(repoPath, desired string) string {
	branch := desired
	if !branchExists(repoPath, branch) {
		return branch
	}
	for i := 2; i < 500; i++ {
		candidate := fmt.Sprintf("%s-%d", desired, i)
		if !branchExists(repoPath, candidate) {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", desired, time.Now().Unix())
}

func branchExists(repoPath, branch string) bool {
	cmd := exec.Command("git", "-C", repoPath, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return cmd.Run() == nil
}

func uniqueWorktreePath(desired string) string {
	if _, err := os.Stat(desired); os.IsNotExist(err) {
		return desired
	}
	for i := 2; i < 500; i++ {
		candidate := fmt.Sprintf("%s-%d", desired, i)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
	return fmt.Sprintf("%s-%d", desired, time.Now().Unix())
}
