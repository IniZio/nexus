package mocks

import (
	"fmt"
	"sync"
)

type MockMutagenClient struct {
	Sessions       map[string]*MockMutagenSession
	mu             sync.RWMutex
	CreateCalls    []CreateSessionCall
	PauseCalls     []string
	ResumeCalls    []string
	TerminateCalls []string
}

type MockMutagenSession struct {
	ID     string
	Alpha  string
	Beta   string
	Paused bool
	Status string
}

type CreateSessionCall struct {
	Alpha string
	Beta  string
}

func NewMockMutagenClient() *MockMutagenClient {
	return &MockMutagenClient{
		Sessions: make(map[string]*MockMutagenSession),
	}
}

func (m *MockMutagenClient) CreateSession(alpha, beta string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.CreateCalls = append(m.CreateCalls, CreateSessionCall{
		Alpha: alpha,
		Beta:  beta,
	})

	sessionID := fmt.Sprintf("session-%d", len(m.Sessions))
	m.Sessions[sessionID] = &MockMutagenSession{
		ID:     sessionID,
		Alpha:  alpha,
		Beta:   beta,
		Status: "connected",
	}

	return sessionID, nil
}

func (m *MockMutagenClient) PauseSession(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.PauseCalls = append(m.PauseCalls, id)

	if session, ok := m.Sessions[id]; ok {
		session.Paused = true
		session.Status = "paused"
	}

	return nil
}

func (m *MockMutagenClient) ResumeSession(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.ResumeCalls = append(m.ResumeCalls, id)

	if session, ok := m.Sessions[id]; ok {
		session.Paused = false
		session.Status = "connected"
	}

	return nil
}

func (m *MockMutagenClient) TerminateSession(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.TerminateCalls = append(m.TerminateCalls, id)
	delete(m.Sessions, id)

	return nil
}

func (m *MockMutagenClient) GetStatus(id string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if session, ok := m.Sessions[id]; ok {
		if session.Paused {
			return "paused", nil
		}
		return "connected", nil
	}

	return "", fmt.Errorf("session not found")
}

func (m *MockMutagenClient) FlushSession(id string) error {
	return nil
}

func (m *MockMutagenClient) ResetSession(id string) error {
	return nil
}

func (m *MockMutagenClient) ListSessions() ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ids := make([]string, 0, len(m.Sessions))
	for id := range m.Sessions {
		ids = append(ids, id)
	}

	return ids, nil
}

func (m *MockMutagenClient) GetSession(id string) (*MockMutagenSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if session, ok := m.Sessions[id]; ok {
		return session, nil
	}

	return nil, fmt.Errorf("session not found")
}
