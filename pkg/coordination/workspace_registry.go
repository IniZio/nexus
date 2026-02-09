package coordination

import (
	"fmt"
	"sync"
	"time"
)

type WorkspaceRegistry interface {
	Create(ws *DBWorkspace) error
	Get(id string) (*DBWorkspace, error)
	GetByUserAndName(userID, name string) (*DBWorkspace, error)
	List() ([]*DBWorkspace, error)
	ListByUser(userID string) ([]*DBWorkspace, error)
	Update(id string, updates map[string]interface{}) error
	UpdateStatus(id, status string) error
	UpdateSSHPort(id string, port int, host string) error
	UpdateServices(workspaceID string, services map[string]DBService) error
	UpdateServiceHealth(workspaceID, serviceName, health string) error
	GetServices(workspaceID string) (map[string]DBService, error)
	Delete(id string) error
}

type InMemoryWorkspaceRegistry struct {
	workspaces map[string]*DBWorkspace
	services   map[string]map[string]DBService
	mu         sync.RWMutex
}

func NewInMemoryWorkspaceRegistry() WorkspaceRegistry {
	return &InMemoryWorkspaceRegistry{
		workspaces: make(map[string]*DBWorkspace),
		services:   make(map[string]map[string]DBService),
	}
}

func (r *InMemoryWorkspaceRegistry) Create(ws *DBWorkspace) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if ws.WorkspaceID == "" {
		return fmt.Errorf("workspace ID cannot be empty")
	}

	if _, exists := r.workspaces[ws.WorkspaceID]; exists {
		return fmt.Errorf("workspace already exists: %s", ws.WorkspaceID)
	}

	now := time.Now()
	ws.CreatedAt = now
	ws.UpdatedAt = now

	r.workspaces[ws.WorkspaceID] = ws
	return nil
}

func (r *InMemoryWorkspaceRegistry) Get(id string) (*DBWorkspace, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ws, exists := r.workspaces[id]
	if !exists {
		return nil, fmt.Errorf("workspace not found: %s", id)
	}
	return ws, nil
}

func (r *InMemoryWorkspaceRegistry) GetByUserAndName(userID, name string) (*DBWorkspace, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, ws := range r.workspaces {
		if ws.UserID == userID && ws.WorkspaceName == name {
			return ws, nil
		}
	}
	return nil, fmt.Errorf("workspace not found for user %s: %s", userID, name)
}

func (r *InMemoryWorkspaceRegistry) List() ([]*DBWorkspace, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	workspaces := make([]*DBWorkspace, 0, len(r.workspaces))
	for _, ws := range r.workspaces {
		workspaces = append(workspaces, ws)
	}
	return workspaces, nil
}

func (r *InMemoryWorkspaceRegistry) ListByUser(userID string) ([]*DBWorkspace, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	workspaces := make([]*DBWorkspace, 0)
	for _, ws := range r.workspaces {
		if ws.UserID == userID {
			workspaces = append(workspaces, ws)
		}
	}
	return workspaces, nil
}

func (r *InMemoryWorkspaceRegistry) Update(id string, updates map[string]interface{}) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	ws, exists := r.workspaces[id]
	if !exists {
		return fmt.Errorf("workspace not found: %s", id)
	}

	for key, value := range updates {
		switch key {
		case "status":
			ws.Status = value.(string)
		case "ssh_port":
			port := value.(int)
			ws.SSHPort = &port
		case "ssh_host":
			host := value.(string)
			ws.SSHHost = &host
		case "node_id":
			nodeID := value.(string)
			ws.NodeID = &nodeID
		}
	}

	ws.UpdatedAt = time.Now()
	return nil
}

func (r *InMemoryWorkspaceRegistry) UpdateStatus(id, status string) error {
	return r.Update(id, map[string]interface{}{"status": status})
}

func (r *InMemoryWorkspaceRegistry) UpdateSSHPort(id string, port int, host string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	ws, exists := r.workspaces[id]
	if !exists {
		return fmt.Errorf("workspace not found: %s", id)
	}

	ws.SSHPort = &port
	ws.SSHHost = &host
	ws.UpdatedAt = time.Now()
	return nil
}

func (r *InMemoryWorkspaceRegistry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.workspaces, id)
	delete(r.services, id)
	return nil
}

func (r *InMemoryWorkspaceRegistry) UpdateServices(workspaceID string, services map[string]DBService) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.workspaces[workspaceID]; !exists {
		return fmt.Errorf("workspace not found: %s", workspaceID)
	}

	r.services[workspaceID] = services
	return nil
}

func (r *InMemoryWorkspaceRegistry) UpdateServiceHealth(workspaceID, serviceName, health string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	services, exists := r.services[workspaceID]
	if !exists {
		return fmt.Errorf("workspace not found: %s", workspaceID)
	}

	service, exists := services[serviceName]
	if !exists {
		return fmt.Errorf("service not found: %s", serviceName)
	}

	service.HealthStatus = health
	now := time.Now()
	service.LastHealthCheck = &now
	services[serviceName] = service
	r.services[workspaceID] = services
	return nil
}

func (r *InMemoryWorkspaceRegistry) GetServices(workspaceID string) (map[string]DBService, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	services, exists := r.services[workspaceID]
	if !exists {
		return make(map[string]DBService), nil
	}
	return services, nil
}
