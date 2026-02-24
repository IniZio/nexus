package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Checkpoint struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Name        string    `json:"name"`
	ImageName   string    `json:"image_name"`
	CreatedAt   time.Time `json:"created_at"`
	Size        int64     `json:"size"`
	Description string    `json:"description,omitempty"`
}

type CheckpointIndex struct {
	Version     int           `json:"version"`
	WorkspaceID string        `json:"workspace_id"`
	UpdatedAt   time.Time     `json:"updated_at"`
	Checkpoints []*Checkpoint `json:"checkpoints"`
}

type FileCheckpointStore struct {
	baseDir string
	indexes map[string]*CheckpointIndex
	mu      sync.RWMutex
}

func NewFileCheckpointStore(baseDir string) (*FileCheckpointStore, error) {
	absPath, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	if err := os.MkdirAll(absPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create checkpoint directory: %w", err)
	}

	return &FileCheckpointStore{
		baseDir: absPath,
		indexes: make(map[string]*CheckpointIndex),
	}, nil
}

func (s *FileCheckpointStore) BaseDir() string {
	return s.baseDir
}

func (s *FileCheckpointStore) LoadWorkspace(workspaceID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	index, err := s.loadIndexLocked(workspaceID)
	if err != nil {
		return err
	}

	s.indexes[workspaceID] = index
	return nil
}

func (s *FileCheckpointStore) loadIndexLocked(workspaceID string) (*CheckpointIndex, error) {
	indexPath := s.indexPath(workspaceID)

	data, err := os.ReadFile(indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &CheckpointIndex{
				Version:     1,
				WorkspaceID: workspaceID,
				UpdatedAt:   time.Now(),
				Checkpoints: []*Checkpoint{},
			}, nil
		}
		return nil, fmt.Errorf("failed to read index: %w", err)
	}

	var index CheckpointIndex
	if err := json.Unmarshal(data, &index); err != nil {
		return nil, fmt.Errorf("failed to unmarshal index: %w", err)
	}

	return &index, nil
}

func (s *FileCheckpointStore) ListCheckpoints(workspaceID string) ([]*Checkpoint, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	index, exists := s.indexes[workspaceID]
	if !exists {
		index, err := s.loadIndexLocked(workspaceID)
		if err != nil {
			return nil, err
		}
		s.indexes[workspaceID] = index
		return index.Checkpoints, nil
	}

	return index.Checkpoints, nil
}

func (s *FileCheckpointStore) GetCheckpoint(workspaceID, checkpointID string) (*Checkpoint, error) {
	checkpoints, err := s.ListCheckpoints(workspaceID)
	if err != nil {
		return nil, err
	}

	for _, cp := range checkpoints {
		if cp.ID == checkpointID {
			return cp, nil
		}
	}

	return nil, fmt.Errorf("checkpoint not found")
}

func (s *FileCheckpointStore) SaveCheckpoint(cp *Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	index, err := s.loadIndexLocked(cp.WorkspaceID)
	if err != nil {
		return err
	}

	cpDir := s.checkpointDir(cp.WorkspaceID)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		return fmt.Errorf("failed to create checkpoint directory: %w", err)
	}

	cpPath := filepath.Join(cpDir, cp.ID+".json")
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal checkpoint: %w", err)
	}

	if err := s.writeFileAtomic(cpPath, data); err != nil {
		return fmt.Errorf("failed to write checkpoint: %w", err)
	}

	index.Checkpoints = append(index.Checkpoints, cp)
	index.UpdatedAt = time.Now()

	if err := s.saveIndexLocked(index); err != nil {
		return fmt.Errorf("failed to save index: %w", err)
	}

	s.indexes[cp.WorkspaceID] = index

	return nil
}

func (s *FileCheckpointStore) DeleteCheckpoint(workspaceID, checkpointID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	index, err := s.loadIndexLocked(workspaceID)
	if err != nil {
		return err
	}

	var targetIdx int = -1
	for i, cp := range index.Checkpoints {
		if cp.ID == checkpointID {
			targetIdx = i
			break
		}
	}

	if targetIdx == -1 {
		return fmt.Errorf("checkpoint not found")
	}

	cpPath := s.checkpointPath(workspaceID, checkpointID)
	if err := os.Remove(cpPath); err != nil {
		return fmt.Errorf("failed to remove checkpoint file: %w", err)
	}

	index.Checkpoints = append(index.Checkpoints[:targetIdx], index.Checkpoints[targetIdx+1:]...)
	index.UpdatedAt = time.Now()

	if err := s.saveIndexLocked(index); err != nil {
		return fmt.Errorf("failed to save index: %w", err)
	}

	s.indexes[workspaceID] = index

	return nil
}

func (s *FileCheckpointStore) LoadAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return fmt.Errorf("failed to read checkpoint directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		workspaceID := entry.Name()
		index, err := s.loadIndexLocked(workspaceID)
		if err != nil {
			continue
		}

		s.indexes[workspaceID] = index
	}

	return nil
}

func (s *FileCheckpointStore) indexPath(workspaceID string) string {
	return filepath.Join(s.baseDir, workspaceID, "index.json")
}

func (s *FileCheckpointStore) checkpointDir(workspaceID string) string {
	return filepath.Join(s.baseDir, workspaceID, "checkpoints")
}

func (s *FileCheckpointStore) checkpointPath(workspaceID, checkpointID string) string {
	return filepath.Join(s.checkpointDir(workspaceID), checkpointID+".json")
}

func (s *FileCheckpointStore) saveIndexLocked(index *CheckpointIndex) error {
	indexPath := s.indexPath(index.WorkspaceID)
	dir := filepath.Dir(indexPath)

	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal index: %w", err)
	}

	if err := s.writeFileAtomic(indexPath, data); err != nil {
		return fmt.Errorf("failed to write index: %w", err)
	}

	return nil
}

func (s *FileCheckpointStore) writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, ".checkpoint-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to write temp file: %w", err)
	}
	_ = tmpFile.Close()

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}
