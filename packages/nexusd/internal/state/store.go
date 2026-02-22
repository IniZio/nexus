package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nexus/nexus/packages/nexusd/internal/types"
)

var (
	ErrWorkspaceNotFound = errors.New("workspace not found")
	ErrWorkspaceExists  = errors.New("workspace already exists")
	ErrInvalidState     = errors.New("invalid state")
)

type StateStore struct {
	baseDir string
	mu      sync.RWMutex
}

func NewStateStore(baseDir string) (*StateStore, error) {
	absPath, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	if err := os.MkdirAll(absPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create state directory: %w", err)
	}

	return &StateStore{
		baseDir: absPath,
	}, nil
}

func (s *StateStore) BaseDir() string {
	return s.baseDir
}

func (s *StateStore) GetWorkspace(id string) (*types.Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.workspacePath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrWorkspaceNotFound
		}
		return nil, fmt.Errorf("failed to read workspace: %w", err)
	}

	var ws types.Workspace
	if err := json.Unmarshal(data, &ws); err != nil {
		return nil, fmt.Errorf("failed to unmarshal workspace: %w", err)
	}

	return &ws, nil
}

func (s *StateStore) SaveWorkspace(w *types.Workspace) error {
	if w == nil {
		return ErrInvalidState
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	w.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal workspace: %w", err)
	}

	path := s.workspacePath(w.ID)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create workspace directory: %w", err)
	}

	if err := s.writeFileAtomic(path, data); err != nil {
		return fmt.Errorf("failed to write workspace: %w", err)
	}

	return nil
}

func (s *StateStore) writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, ".workspace-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	tmpFile.Close()

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}

func (s *StateStore) ListWorkspaces() ([]*types.Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read state directory: %w", err)
	}

	var workspaces []*types.Workspace
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		id := entry.Name()
		path := s.workspacePath(id)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var ws types.Workspace
		if err := json.Unmarshal(data, &ws); err != nil {
			continue
		}

		workspaces = append(workspaces, &ws)
	}

	return workspaces, nil
}

func (s *StateStore) DeleteWorkspace(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.workspacePath(id)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return ErrWorkspaceNotFound
	}

	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		return fmt.Errorf("failed to delete workspace: %w", err)
	}

	return nil
}

func (s *StateStore) WorkspaceExists(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.workspacePath(id)
	_, err := os.Stat(path)
	return err == nil
}

func (s *StateStore) workspacePath(id string) string {
	return filepath.Join(s.baseDir, id, "workspace.json")
}
