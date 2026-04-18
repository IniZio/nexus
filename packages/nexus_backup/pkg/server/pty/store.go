package pty

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Store persists tmux-backed PTY metadata so sessions can be reattached after daemon restarts.
type Store struct {
	mu   sync.Mutex
	path string
}

func NewStore(workspaceDir string) *Store {
	stateDir := filepath.Join(workspaceDir, ".nexus", "state")
	return &Store{path: filepath.Join(stateDir, "pty-sessions.json")}
}

func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Store) List() ([]SessionInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

func (s *Store) Upsert(info SessionInfo) error {
	if s == nil || !info.IsTmux || info.ID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	items, err := s.loadLocked()
	if err != nil {
		return err
	}
	replaced := false
	for i := range items {
		if items[i].ID == info.ID {
			items[i] = info
			replaced = true
			break
		}
	}
	if !replaced {
		items = append(items, info)
	}
	return s.saveLocked(items)
}

func (s *Store) Delete(sessionID string) error {
	if s == nil || sessionID == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	items, err := s.loadLocked()
	if err != nil {
		return err
	}
	out := make([]SessionInfo, 0, len(items))
	for _, item := range items {
		if item.ID != sessionID {
			out = append(out, item)
		}
	}
	return s.saveLocked(out)
}

func (s *Store) loadLocked() ([]SessionInfo, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []SessionInfo{}, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return []SessionInfo{}, nil
	}
	var items []SessionInfo
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Store) saveLocked(items []SessionInfo) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return os.WriteFile(s.path, encoded, 0o644)
}
