package projectmgr

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/store"
)

type projectStore interface {
	store.ProjectRepository
	store.WorkspaceRepository
}

type Manager struct {
	root        string
	projectRepo projectStore
	mu          sync.RWMutex
	projects    map[string]*Project
}

func NewManager(root string, repo projectStore) *Manager {
	m := &Manager{
		root:        root,
		projectRepo: repo,
		projects:    make(map[string]*Project),
	}
	_ = m.loadAll()
	return m
}

func (m *Manager) loadAll() error {
	if m.projectRepo == nil {
		return nil
	}

	all, err := m.projectRepo.ListProjectRows()
	if err != nil {
		return fmt.Errorf("list sqlite projects: %w", err)
	}
	for _, row := range all {
		if len(row.Payload) == 0 {
			continue
		}
		var p Project
		if err := json.Unmarshal(row.Payload, &p); err != nil {
			continue
		}
		copy := p
		m.projects[p.ID] = &copy
	}
	return nil
}

func (m *Manager) persistProject(p *Project) error {
	if m.projectRepo == nil {
		return fmt.Errorf("sqlite project store unavailable")
	}

	payload, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("marshal sqlite project payload: %w", err)
	}
	if err := m.projectRepo.UpsertProjectRow(store.ProjectRow{
		ID:        p.ID,
		Payload:   payload,
		CreatedAt: p.CreatedAt,
		UpdatedAt: p.UpdatedAt,
	}); err != nil {
		return fmt.Errorf("upsert sqlite project: %w", err)
	}

	return nil
}

func (m *Manager) GetOrCreateForRepo(repo string, repoID string) (*Project, error) {
	if repo == "" {
		return nil, fmt.Errorf("repo is required")
	}

	// Check if project exists for this repo
	m.mu.RLock()
	for _, p := range m.projects {
		if p.PrimaryRepo == repo {
			m.mu.RUnlock()
			return p, nil
		}
	}
	m.mu.RUnlock()

	// Create new project
	now := time.Now().UTC()
	id := fmt.Sprintf("proj-%d", now.UnixNano())
	name := deriveProjectName(repo)
	rootPath := filepath.Join(m.root, "projects", id)
	if err := os.MkdirAll(rootPath, 0o755); err != nil {
		return nil, fmt.Errorf("create project root: %w", err)
	}

	p := &Project{
		ID:          id,
		Name:        name,
		PrimaryRepo: repo,
		RepoIDs:     []string{repoID},
		RootPath:    rootPath,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	m.mu.Lock()
	m.projects[id] = p
	m.mu.Unlock()

	if err := m.persistProject(p); err != nil {
		m.mu.Lock()
		delete(m.projects, id)
		m.mu.Unlock()
		_ = os.RemoveAll(rootPath)
		return nil, fmt.Errorf("persist project: %w", err)
	}

	return p, nil
}

// WorkspaceMigrationRecord describes a workspace row for project backfill.
type WorkspaceMigrationRecord struct {
	ID        string
	ProjectID string
	Repo      string
	RepoID    string
}

// WorkspaceMigrationSource enumerates workspaces without importing workspacemgr (avoids an import cycle).
type WorkspaceMigrationSource interface {
	ListWorkspaceMigrationRecords() []WorkspaceMigrationRecord
	UpdateProjectID(workspaceID, projectID string) error
}

// MigrateWorkspacesWithoutProject creates projects for existing workspaces that do not have a project_id. Call on startup.
func (m *Manager) MigrateWorkspacesWithoutProject(src WorkspaceMigrationSource) error {
	if m.projectRepo == nil {
		return nil
	}
	for _, ws := range src.ListWorkspaceMigrationRecords() {
		if ws.ProjectID != "" {
			continue
		}
		project, err := m.GetOrCreateForRepo(ws.Repo, ws.RepoID)
		if err != nil {
			log.Printf("project migration: failed to create project for workspace %s: %v", ws.ID, err)
			continue
		}
		if err := src.UpdateProjectID(ws.ID, project.ID); err != nil {
			log.Printf("project migration: failed to update workspace %s: %v", ws.ID, err)
		}
	}
	return nil
}

func (m *Manager) Get(id string) (*Project, bool) {
	m.mu.RLock()
	p, ok := m.projects[id]
	m.mu.RUnlock()
	if !ok {
		return nil, false
	}
	return cloneProject(p), true
}

func (m *Manager) List() []*Project {
	m.mu.RLock()
	all := make([]*Project, 0, len(m.projects))
	for _, p := range m.projects {
		all = append(all, cloneProject(p))
	}
	m.mu.RUnlock()
	return all
}

func (m *Manager) Remove(id string) bool {
	m.mu.Lock()
	p, ok := m.projects[id]
	if ok {
		delete(m.projects, id)
	}
	m.mu.Unlock()

	if ok {
		_ = os.RemoveAll(p.RootPath)
		if m.projectRepo != nil {
			_ = m.projectRepo.DeleteProject(id)
		}
	}
	return ok
}

func deriveProjectName(repo string) string {
	// Extract repo name from path or URL
	base := filepath.Base(repo)
	if base == "" || base == "." || base == "/" {
		return "project"
	}
	// Remove .git suffix if present
	if strings.HasSuffix(base, ".git") {
		base = base[:len(base)-4]
	}
	return base
}

func cloneProject(in *Project) *Project {
	if in == nil {
		return nil
	}
	out := *in
	if in.RepoIDs != nil {
		out.RepoIDs = make([]string, len(in.RepoIDs))
		copy(out.RepoIDs, in.RepoIDs)
	}
	return &out
}
