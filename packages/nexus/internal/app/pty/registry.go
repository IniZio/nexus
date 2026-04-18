package pty

import (
	"sort"
	"sync"
)

// Registry tracks active PTY sessions across all workspaces.
type Registry struct {
	mu          sync.RWMutex
	sessions    map[string]*Session
	byWorkspace map[string]map[string]bool
}

// NewRegistry creates a new empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		sessions:    make(map[string]*Session),
		byWorkspace: make(map[string]map[string]bool),
	}
}

// Register adds a session to the registry.
func (r *Registry) Register(s *Session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[s.ID] = s
	if s.WorkspaceID != "" {
		if r.byWorkspace[s.WorkspaceID] == nil {
			r.byWorkspace[s.WorkspaceID] = make(map[string]bool)
		}
		r.byWorkspace[s.WorkspaceID][s.ID] = true
	}
}

// Unregister removes a session from the registry.
func (r *Registry) Unregister(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[sessionID]
	if !ok {
		return
	}
	delete(r.sessions, sessionID)
	if s.WorkspaceID != "" {
		if ids := r.byWorkspace[s.WorkspaceID]; ids != nil {
			delete(ids, sessionID)
			if len(ids) == 0 {
				delete(r.byWorkspace, s.WorkspaceID)
			}
		}
	}
}

// Get retrieves a session by ID.
func (r *Registry) Get(sessionID string) *Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessions[sessionID]
}

// ListByWorkspace returns all sessions for a workspace, sorted by creation time.
func (r *Registry) ListByWorkspace(workspaceID string) []SessionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := r.byWorkspace[workspaceID]
	result := make([]SessionInfo, 0, len(ids))
	for id := range ids {
		if s, ok := r.sessions[id]; ok {
			result = append(result, s.Info())
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		}
		return result[i].ID < result[j].ID
	})
	return result
}

// CountByWorkspace returns the number of sessions for a workspace.
func (r *Registry) CountByWorkspace(workspaceID string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byWorkspace[workspaceID])
}

// Rename updates a session's display name.
func (r *Registry) Rename(sessionID, name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[sessionID]
	if !ok {
		return false
	}
	s.Name = name
	return true
}
