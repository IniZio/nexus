package workspacemgr

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Manager struct {
	root       string
	mu         sync.RWMutex
	workspaces map[string]*Workspace
}

func NewManager(root string) *Manager {
	return &Manager{
		root:       root,
		workspaces: make(map[string]*Workspace),
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

	ws := &Workspace{
		ID:            id,
		Repo:          spec.Repo,
		Ref:           spec.Ref,
		WorkspaceName: spec.WorkspaceName,
		AgentProfile:  spec.AgentProfile,
		Policy:        spec.Policy,
		State:         StateSetup,
		RootPath:      rootPath,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	m.mu.Lock()
	m.workspaces[id] = ws
	m.mu.Unlock()

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
	}

	return ok
}

func cloneWorkspace(in *Workspace) *Workspace {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
