package spotlight

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

type Forward struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspaceId"`
	Service     string    `json:"service"`
	RemotePort  int       `json:"remotePort"`
	LocalPort   int       `json:"localPort"`
	Host        string    `json:"host"`
	CreatedAt   time.Time `json:"createdAt"`
}

type ExposeSpec struct {
	WorkspaceID string `json:"workspaceId"`
	Service     string `json:"service"`
	RemotePort  int    `json:"remotePort"`
	LocalPort   int    `json:"localPort"`
	Host        string `json:"host,omitempty"`
}

type Manager struct {
	mu        sync.RWMutex
	forwards  map[string]*Forward
	localToID map[int]string
}

func NewManager() *Manager {
	return &Manager{
		forwards:  make(map[string]*Forward),
		localToID: make(map[int]string),
	}
}

func (m *Manager) Expose(_ context.Context, spec ExposeSpec) (*Forward, error) {
	if spec.RemotePort <= 0 || spec.LocalPort <= 0 {
		return nil, fmt.Errorf("remotePort and localPort must be > 0")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if existing, ok := m.localToID[spec.LocalPort]; ok {
		return nil, fmt.Errorf("local port %d already in use by %s", spec.LocalPort, existing)
	}

	host := spec.Host
	if host == "" {
		host = "127.0.0.1"
	}

	now := time.Now().UTC()
	id := fmt.Sprintf("spot-%d", now.UnixNano())
	fwd := &Forward{
		ID:          id,
		WorkspaceID: spec.WorkspaceID,
		Service:     spec.Service,
		RemotePort:  spec.RemotePort,
		LocalPort:   spec.LocalPort,
		Host:        host,
		CreatedAt:   now,
	}

	m.forwards[id] = fwd
	m.localToID[spec.LocalPort] = id

	copy := *fwd
	return &copy, nil
}

func (m *Manager) List(workspaceID string) []*Forward {
	m.mu.RLock()
	all := make([]*Forward, 0, len(m.forwards))
	for _, fwd := range m.forwards {
		if workspaceID == "" || fwd.WorkspaceID == workspaceID {
			copy := *fwd
			all = append(all, &copy)
		}
	}
	m.mu.RUnlock()

	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})

	return all
}

func (m *Manager) Close(id string) bool {
	m.mu.Lock()
	fwd, ok := m.forwards[id]
	if ok {
		delete(m.forwards, id)
		delete(m.localToID, fwd.LocalPort)
	}
	m.mu.Unlock()
	return ok
}
