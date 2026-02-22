package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	wsTypes "github.com/nexus/nexus/packages/workspace-daemon/internal/types"
)

type Checkpoint struct {
	ID        string    `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Name      string    `json:"name"`
	ImageName string   `json:"image_name"`
	CreatedAt time.Time `json:"created_at"`
}

type CheckpointStore struct {
	checkpoints map[string][]*Checkpoint
	mu          sync.RWMutex
	stateDir    string
}

func NewCheckpointStore(stateDir string) *CheckpointStore {
	return &CheckpointStore{
		checkpoints: make(map[string][]*Checkpoint),
		stateDir:    stateDir,
	}
}

func (s *Server) setupCheckpointRoutes() {
	s.mux.HandleFunc("/api/v1/workspaces/", s.handleCheckpointByWorkspaceID)
}

func (s *Server) handleCheckpointByWorkspaceID(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path[len("/api/v1/workspaces/"):]
	parts := strings.SplitN(path, "/", 4)
	
	workspaceID := parts[0]
	if workspaceID == "" {
		http.Error(w, "workspace ID required", http.StatusBadRequest)
		return
	}
	
	s.mu.RLock()
	ws, exists := s.workspaces[workspaceID]
	s.mu.RUnlock()
	if !exists {
		s.mu.RLock()
		for _, w := range s.workspaces {
			if w.Name == workspaceID {
				ws = w
				workspaceID = w.ID
				break
			}
		}
		s.mu.RUnlock()
	}
	
	if ws == nil {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}
	
	if len(parts) < 2 || parts[1] == "" {
		s.handleWorkspaceByID(w, r)
		return
	}
	
	subPath := ""
	if len(parts) > 1 {
		subPath = parts[1]
	}
	
	if len(parts) > 2 && parts[2] != "" {
		checkpointID := parts[2]
		
		if len(parts) > 3 && parts[3] == "restore" {
			switch r.Method {
			case http.MethodPost:
				s.handleRestoreCheckpoint(w, r, workspaceID, checkpointID)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
			return
		}
		
		switch r.Method {
		case http.MethodGet:
			s.getCheckpoint(w, r, workspaceID, checkpointID)
		case http.MethodDelete:
			s.deleteCheckpoint(w, r, workspaceID, checkpointID)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}
	
	switch subPath {
	case "checkpoints":
		switch r.Method {
		case http.MethodGet:
			s.listCheckpoints(w, r, workspaceID)
		case http.MethodPost:
			s.createCheckpoint(w, r, workspaceID)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	default:
		s.handleWorkspaceByID(w, r)
	}
}

func (s *Server) createCheckpoint(w http.ResponseWriter, r *http.Request, workspaceID string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("reading request body: %w", err))
		return
	}
	defer r.Body.Close()

	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("parsing request: %w", err))
		return
	}

	if req.Name == "" {
		req.Name = fmt.Sprintf("checkpoint-%d", time.Now().Unix())
	}

	checkpoint, err := s.doCreateCheckpoint(workspaceID, req.Name)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("creating checkpoint: %w", err))
		return
	}

	WriteSuccess(w, checkpoint)
}

func (s *Server) doCreateCheckpoint(workspaceID, name string) (*Checkpoint, error) {
	s.mu.RLock()
	ws, exists := s.workspaces[workspaceID]
	s.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("workspace not found")
	}

	if s.dockerBackend == nil || ws.Backend != "docker" {
		return nil, fmt.Errorf("docker backend not available")
	}

	ctx := context.Background()

	currentStatus, err := s.dockerBackend.GetWorkspaceStatus(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("getting workspace status: %w", err)
	}

	wasRunning := currentStatus == wsTypes.StatusRunning

	if wasRunning {
		log.Printf("[checkpoint] Pausing workspace %s for checkpoint", workspaceID)
		if _, err := s.dockerBackend.StopWorkspace(ctx, workspaceID, 30); err != nil {
			return nil, fmt.Errorf("pausing workspace: %w", err)
		}
	}

	imageName := fmt.Sprintf("nexus-checkpoint-%s-%s-%d", 
		workspaceID, strings.ReplaceAll(name, " ", "-"), time.Now().Unix())

	commitOpts := &wsTypes.CommitContainerRequest{
		WorkspaceID: workspaceID,
		ImageName:  imageName,
	}
	
	err = s.dockerBackend.CommitContainer(ctx, workspaceID, commitOpts)
	if err != nil {
		log.Printf("[checkpoint] Commit failed: %v", err)
		if wasRunning {
			s.dockerBackend.StartWorkspace(ctx, workspaceID)
		}
		return nil, fmt.Errorf("committing container: %w", err)
	}

	checkpoint := &Checkpoint{
		ID:          fmt.Sprintf("%s-%d", name, time.Now().Unix()),
		WorkspaceID: workspaceID,
		Name:        name,
		ImageName:   imageName,
		CreatedAt:   time.Now(),
	}

	s.saveCheckpoint(checkpoint)

	if wasRunning {
		log.Printf("[checkpoint] Resuming workspace %s after checkpoint", workspaceID)
		if _, err := s.dockerBackend.StartWorkspace(ctx, workspaceID); err != nil {
			log.Printf("[checkpoint] Warning: failed to resume workspace: %v", err)
		}
	}

	log.Printf("[checkpoint] Created checkpoint %s for workspace %s", checkpoint.ID, workspaceID)
	return checkpoint, nil
}

func (s *Server) listCheckpoints(w http.ResponseWriter, r *http.Request, workspaceID string) {
	s.mu.RLock()
	_, exists := s.workspaces[workspaceID]
	s.mu.RUnlock()

	if !exists {
		WriteError(w, http.StatusNotFound, fmt.Errorf("workspace not found"))
		return
	}

	checkpoints := s.getCheckpoints(workspaceID)
	WriteSuccess(w, checkpoints)
}

func (s *Server) getCheckpoint(w http.ResponseWriter, r *http.Request, workspaceID, checkpointID string) {
	checkpoints := s.getCheckpoints(workspaceID)
	
	for _, cp := range checkpoints {
		if cp.ID == checkpointID {
			WriteSuccess(w, cp)
			return
		}
	}
	
	WriteError(w, http.StatusNotFound, fmt.Errorf("checkpoint not found"))
}

func (s *Server) deleteCheckpoint(w http.ResponseWriter, r *http.Request, workspaceID, checkpointID string) {
	checkpoints := s.getCheckpoints(workspaceID)
	
	var target *Checkpoint
	var targetIdx int
	for i, cp := range checkpoints {
		if cp.ID == checkpointID {
			target = cp
			targetIdx = i
			break
		}
	}
	
	if target == nil {
		WriteError(w, http.StatusNotFound, fmt.Errorf("checkpoint not found"))
		return
	}

	ctx := context.Background()
	if err := s.dockerBackend.RemoveImage(ctx, target.ImageName); err != nil {
		log.Printf("[checkpoint] Warning: failed to remove image %s: %v", target.ImageName, err)
	}

	s.mu.Lock()
	s.checkpointStore.checkpoints[workspaceID] = append(
		checkpoints[:targetIdx],
		checkpoints[targetIdx+1:]...,
	)
	s.mu.Unlock()

	if s.stateStore != nil {
		checkpointPath := filepath.Join(s.stateStore.BaseDir(), workspaceID, "checkpoints", checkpointID+".json")
		os.Remove(checkpointPath)
	}

	WriteSuccess(w, map[string]string{"status": "deleted"})
}

func (s *Server) saveCheckpoint(cp *Checkpoint) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.checkpointStore == nil {
		s.checkpointStore = &CheckpointStore{
			checkpoints: make(map[string][]*Checkpoint),
		}
	}

	s.checkpointStore.checkpoints[cp.WorkspaceID] = append(
		s.checkpointStore.checkpoints[cp.WorkspaceID],
		cp,
	)

	if s.stateStore != nil {
		cpDir := filepath.Join(s.stateStore.BaseDir(), cp.WorkspaceID, "checkpoints")
		if err := os.MkdirAll(cpDir, 0755); err != nil {
			log.Printf("[checkpoint] failed to create checkpoint dir: %v", err)
			return
		}
		cpPath := filepath.Join(cpDir, cp.ID+".json")
		data, err := json.Marshal(cp)
		if err != nil {
			log.Printf("[checkpoint] failed to marshal checkpoint: %v", err)
			return
		}
		if err := os.WriteFile(cpPath, data, 0644); err != nil {
			log.Printf("[checkpoint] failed to write checkpoint: %v", err)
		}
	}
}

func (s *Server) getCheckpoints(workspaceID string) []*Checkpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.checkpointStore == nil {
		return nil
	}

	return s.checkpointStore.checkpoints[workspaceID]
}

func (s *Server) handleRestoreCheckpoint(w http.ResponseWriter, r *http.Request, workspaceID, checkpointID string) {
	checkpoints := s.getCheckpoints(workspaceID)
	
	var target *Checkpoint
	for _, cp := range checkpoints {
		if cp.ID == checkpointID {
			target = cp
			break
		}
	}
	
	if target == nil {
		WriteError(w, http.StatusNotFound, fmt.Errorf("checkpoint not found"))
		return
	}

	ctx := context.Background()

	ws, exists := s.workspaces[workspaceID]
	if !exists {
		WriteError(w, http.StatusNotFound, fmt.Errorf("workspace not found"))
		return
	}

	if ws.Status == "running" {
		if _, err := s.dockerBackend.StopWorkspace(ctx, workspaceID, 30); err != nil {
			WriteError(w, http.StatusInternalServerError, fmt.Errorf("stopping workspace: %w", err))
			return
		}
	}

	if err := s.dockerBackend.RestoreFromImage(ctx, workspaceID, target.ImageName); err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("restoring from image: %w", err))
		return
	}

	if _, err := s.dockerBackend.StartWorkspace(ctx, workspaceID); err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("starting workspace: %w", err))
		return
	}

	s.mu.Lock()
	if ws, exists := s.workspaces[workspaceID]; exists {
		ws.Status = "running"
		ws.UpdatedAt = time.Now()
	}
	s.mu.Unlock()

	WriteSuccess(w, map[string]string{"status": "restored"})
}
