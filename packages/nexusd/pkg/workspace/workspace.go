package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Workspace struct {
	id        string
	path      string
	createdAt time.Time
	mu        sync.RWMutex
}

func NewWorkspace(workspacePath string) (*Workspace, error) {
	absPath, err := filepath.Abs(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	if err := os.MkdirAll(absPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create workspace directory: %w", err)
	}

	workspaceID := fmt.Sprintf("ws-%d", time.Now().UnixNano())

	return &Workspace{
		id:        workspaceID,
		path:      absPath,
		createdAt: time.Now(),
	}, nil
}

func (w *Workspace) ID() string {
	return w.id
}

func (w *Workspace) Path() string {
	return w.path
}

func (w *Workspace) SecurePath(userPath string) (string, error) {
	if userPath == "" || userPath == "." {
		return w.path, nil
	}

	if filepath.IsAbs(userPath) {
		return "", fmt.Errorf("absolute paths not allowed: %s", userPath)
	}

	cleanPath := filepath.Clean(userPath)
	fullPath := filepath.Join(w.path, cleanPath)

	if !strings.HasPrefix(fullPath, w.path) {
		return "", fmt.Errorf("path traversal not allowed: %s", userPath)
	}

	return fullPath, nil
}

func (w *Workspace) Exists() bool {
	w.mu.RLock()
	defer w.mu.RUnlock()

	_, err := os.Stat(w.path)
	return err == nil
}

func (w *Workspace) IsValidSubPath(subPath string) bool {
	if filepath.IsAbs(subPath) {
		return false
	}
	fullPath := filepath.Join(w.path, subPath)
	cleanPath := filepath.Clean(fullPath)
	return strings.HasPrefix(cleanPath, w.path)
}

func (w *Workspace) CreatedAt() time.Time {
	return w.createdAt
}

func (w *Workspace) Stat() (WorkspaceStats, error) {
	info, err := os.Stat(w.path)
	if err != nil {
		return WorkspaceStats{}, err
	}

	return WorkspaceStats{
		Path:       w.path,
		ModifiedAt: info.ModTime(),
	}, nil
}

type WorkspaceStats struct {
	Path       string
	ModifiedAt time.Time
}
