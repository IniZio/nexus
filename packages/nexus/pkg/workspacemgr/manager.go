package workspacemgr

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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

	authBinding := spec.AuthBinding
	if authBinding == nil {
		authBinding = make(map[string]string)
	}
	ws := &Workspace{
		ID:            id,
		Repo:          spec.Repo,
		Ref:           spec.Ref,
		WorkspaceName: spec.WorkspaceName,
		AgentProfile:  spec.AgentProfile,
		Policy:        spec.Policy,
		State:         StateCreated,
		RootPath:      rootPath,
		Backend:       spec.Backend,
		AuthBinding:   authBinding,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

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

func (m *Manager) Fork(parentID string, childWorkspaceName string) (*Workspace, error) {
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

	now := time.Now().UTC()
	childID := fmt.Sprintf("ws-%d", now.UnixNano())
	childRootPath := filepath.Join(m.root, "instances", childID)
	if err := os.MkdirAll(childRootPath, 0o755); err != nil {
		return nil, fmt.Errorf("create child workspace root: %w", err)
	}

	child := &Workspace{
		ID:                childID,
		Repo:              parent.Repo,
		Ref:               parent.Ref,
		WorkspaceName:     childWorkspaceName,
		AgentProfile:      parent.AgentProfile,
		Policy:            parent.Policy,
		State:             StateCreated,
		RootPath:          childRootPath,
		ParentWorkspaceID: parent.ID,
		Backend:           parent.Backend,
		AuthBinding:       make(map[string]string, len(parent.AuthBinding)),
		CreatedAt:         now,
		UpdatedAt:         now,
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
