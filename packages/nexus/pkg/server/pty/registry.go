package pty

import (
	"sort"
	"sync"
)

// Registry provides global tracking of PTY sessions across all connections.
// This enables workspace-scoped session management and multi-tab support.
type Registry struct {
	mu          sync.RWMutex
	sessions    map[string]*Session        // sessionID -> Session
	byWorkspace map[string]map[string]bool // workspaceID -> set of sessionIDs
	subscribers map[string]map[Conn]bool   // sessionID -> subscriber connections
}

// NewRegistry creates a new global PTY session registry
func NewRegistry() *Registry {
	return &Registry{
		sessions:    make(map[string]*Session),
		byWorkspace: make(map[string]map[string]bool),
		subscribers: make(map[string]map[Conn]bool),
	}
}

// Register adds a session to the global registry
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

// Unregister removes a session from the registry
func (r *Registry) Unregister(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.sessions[sessionID]
	if !ok {
		return
	}

	delete(r.sessions, sessionID)
	delete(r.subscribers, sessionID)

	if s.WorkspaceID != "" && r.byWorkspace[s.WorkspaceID] != nil {
		delete(r.byWorkspace[s.WorkspaceID], sessionID)
		if len(r.byWorkspace[s.WorkspaceID]) == 0 {
			delete(r.byWorkspace, s.WorkspaceID)
		}
	}
}

// Subscribe registers a connection to receive PTY events for a session.
func (r *Registry) Subscribe(sessionID string, conn Conn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[sessionID]; !ok {
		return false
	}
	if r.subscribers[sessionID] == nil {
		r.subscribers[sessionID] = make(map[Conn]bool)
	}
	r.subscribers[sessionID][conn] = true
	return true
}

// Unsubscribe removes a connection from a session's subscribers.
func (r *Registry) Unsubscribe(sessionID string, conn Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if subs, ok := r.subscribers[sessionID]; ok {
		delete(subs, conn)
		if len(subs) == 0 {
			delete(r.subscribers, sessionID)
		}
	}
}

// UnsubscribeConn removes a connection from all session subscriber sets.
func (r *Registry) UnsubscribeConn(conn Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for sessionID, subs := range r.subscribers {
		delete(subs, conn)
		if len(subs) == 0 {
			delete(r.subscribers, sessionID)
		}
	}
}

// Broadcast delivers a pre-encoded JSON-RPC notification to all subscribers.
func (r *Registry) Broadcast(sessionID string, encoded []byte) {
	r.mu.RLock()
	subs := r.subscribers[sessionID]
	targets := make([]Conn, 0, len(subs))
	for conn := range subs {
		targets = append(targets, conn)
	}
	r.mu.RUnlock()
	for _, conn := range targets {
		conn.Enqueue(encoded)
	}
}

// Get retrieves a session by ID
func (r *Registry) Get(sessionID string) *Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.sessions[sessionID]
}

// ListByWorkspace returns all sessions for a given workspace
func (r *Registry) ListByWorkspace(workspaceID string) []SessionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]SessionInfo, 0)
	if ids, ok := r.byWorkspace[workspaceID]; ok {
		for id := range ids {
			if s, ok := r.sessions[id]; ok {
				result = append(result, s.Info())
			}
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if !result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].CreatedAt.Before(result[j].CreatedAt)
		}
		if result[i].Name != result[j].Name {
			return result[i].Name < result[j].Name
		}
		return result[i].ID < result[j].ID
	})
	return result
}

// ListAll returns all active sessions
func (r *Registry) ListAll() []SessionInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]SessionInfo, 0, len(r.sessions))
	for _, s := range r.sessions {
		result = append(result, s.Info())
	}
	return result
}

// Rename updates a session's name
func (r *Registry) Rename(sessionID, newName string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.sessions[sessionID]
	if !ok {
		return false
	}
	s.Name = newName
	return true
}

// Count returns the total number of registered sessions
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sessions)
}

// CountByWorkspace returns the number of sessions for a workspace
func (r *Registry) CountByWorkspace(workspaceID string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if ids, ok := r.byWorkspace[workspaceID]; ok {
		return len(ids)
	}
	return 0
}
