package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/nexus/nexus/packages/workspace-daemon/internal/checkpoint"
	wsTypes "github.com/nexus/nexus/packages/workspace-daemon/internal/types"
)

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
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		WriteError(w, http.StatusBadRequest, fmt.Errorf("parsing request: %w", err))
		return
	}

	if req.Name == "" {
		req.Name = fmt.Sprintf("checkpoint-%d", time.Now().Unix())
	}

	checkpoint, err := s.doCreateCheckpoint(workspaceID, req.Name, req.Description)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("creating checkpoint: %w", err))
		return
	}

	WriteSuccess(w, checkpoint)
}

func (s *Server) doCreateCheckpoint(workspaceID, name, description string) (*checkpoint.Checkpoint, error) {
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

	cp := &checkpoint.Checkpoint{
		ID:          fmt.Sprintf("%s-%d", name, time.Now().Unix()),
		WorkspaceID: workspaceID,
		Name:        name,
		ImageName:   imageName,
		CreatedAt:   time.Now(),
		Description: description,
	}

	if err := s.checkpointStore.SaveCheckpoint(cp); err != nil {
		log.Printf("[checkpoint] Warning: failed to persist checkpoint: %v", err)
	}

	if wasRunning {
		log.Printf("[checkpoint] Resuming workspace %s after checkpoint", workspaceID)
		if _, err := s.dockerBackend.StartWorkspace(ctx, workspaceID); err != nil {
			log.Printf("[checkpoint] Warning: failed to resume workspace: %v", err)
		}
	}

	log.Printf("[checkpoint] Created checkpoint %s for workspace %s", cp.ID, workspaceID)
	return cp, nil
}

func (s *Server) listCheckpoints(w http.ResponseWriter, r *http.Request, workspaceID string) {
	s.mu.RLock()
	_, exists := s.workspaces[workspaceID]
	s.mu.RUnlock()

	if !exists {
		WriteError(w, http.StatusNotFound, fmt.Errorf("workspace not found"))
		return
	}

	checkpoints, err := s.checkpointStore.ListCheckpoints(workspaceID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("listing checkpoints: %w", err))
		return
	}

	WriteSuccess(w, checkpoints)
}

func (s *Server) getCheckpoint(w http.ResponseWriter, r *http.Request, workspaceID, checkpointID string) {
	cp, err := s.checkpointStore.GetCheckpoint(workspaceID, checkpointID)
	if err != nil {
		WriteError(w, http.StatusNotFound, fmt.Errorf("checkpoint not found"))
		return
	}
	
	WriteSuccess(w, cp)
}

func (s *Server) deleteCheckpoint(w http.ResponseWriter, r *http.Request, workspaceID, checkpointID string) {
	cp, err := s.checkpointStore.GetCheckpoint(workspaceID, checkpointID)
	if err != nil {
		WriteError(w, http.StatusNotFound, fmt.Errorf("checkpoint not found"))
		return
	}

	ctx := context.Background()
	if err := s.dockerBackend.RemoveImage(ctx, cp.ImageName); err != nil {
		log.Printf("[checkpoint] Warning: failed to remove image %s: %v", cp.ImageName, err)
	}

	if err := s.checkpointStore.DeleteCheckpoint(workspaceID, checkpointID); err != nil {
		WriteError(w, http.StatusInternalServerError, fmt.Errorf("deleting checkpoint: %w", err))
		return
	}

	WriteSuccess(w, map[string]string{"status": "deleted"})
}

func (s *Server) handleRestoreCheckpoint(w http.ResponseWriter, r *http.Request, workspaceID, checkpointID string) {
	cp, err := s.checkpointStore.GetCheckpoint(workspaceID, checkpointID)
	if err != nil {
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

	if err := s.dockerBackend.RestoreFromImage(ctx, workspaceID, cp.ImageName); err != nil {
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
