# Project/Workspace Hierarchy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce a Project layer that implicitly groups workspaces by repository, with hierarchical listing and project management commands.

**Architecture:** Projects are auto-created when first workspace for a repo is created. Workspaces link to projects via `projectId`. CLI shows hierarchical view. SQLite stores projects with Goose migrations.

**Tech Stack:** Go (nexus daemon), TypeScript (SDK), SQLite with Goose migrations, Cobra (CLI).

---

## File Structure

### New Files
- `packages/nexus/pkg/store/migrations/00002_create_projects.sql` - Database migration
- `packages/nexus/pkg/store/project_repo.go` - Project repository interface
- `packages/nexus/pkg/projectmgr/types.go` - Project types
- `packages/nexus/pkg/projectmgr/manager.go` - Project manager
- `packages/nexus/pkg/handlers/project_manager.go` - Project RPC handlers
- `packages/nexus/cmd/nexus/project.go` - CLI project commands

### Modified Files
- `packages/nexus/pkg/workspacemgr/types.go` - Add ProjectID to Workspace
- `packages/nexus/pkg/workspacemgr/manager.go` - Link workspaces to projects
- `packages/nexus/pkg/store/node_store.go` - Add project persistence methods
- `packages/nexus/pkg/store/workspace_repo.go` - Add ProjectRepository interface methods
- `packages/nexus/pkg/handlers/workspace_manager.go` - Auto-create projects
- `packages/nexus/cmd/nexus/workspace.go` - Modify list command
- `packages/sdk/js/src/types/workspace.ts` - Add Project types
- `packages/sdk/js/src/types/project.ts` - New project types file
- `packages/sdk/js/src/rpc/schema.ts` - Add project RPC methods

---

## Task 1: Database Migration

**Files:**
- Create: `packages/nexus/pkg/store/migrations/00002_create_projects.sql`

- [ ] **Step 1: Create migration file with projects table and workspace column**

```sql
-- +goose Up
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  payload_json TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_projects_created_at ON projects(created_at);

-- Add project_id to workspaces (nullable initially for migration)
ALTER TABLE workspaces ADD COLUMN project_id TEXT;

CREATE INDEX IF NOT EXISTS idx_workspaces_project_id ON workspaces(project_id);

-- +goose Down
DROP INDEX IF EXISTS idx_workspaces_project_id;
DROP INDEX IF EXISTS idx_projects_created_at;
ALTER TABLE workspaces DROP COLUMN project_id;
DROP TABLE IF EXISTS projects;
```

- [ ] **Step 2: Test migration by building nexus**

Run: `cd packages/nexus && go build ./...`
Expected: Build succeeds

- [ ] **Step 3: Commit**

```bash
git add packages/nexus/pkg/store/migrations/00002_create_projects.sql
git commit -m "feat: add projects table migration"
```

---

## Task 2: Project Types

**Files:**
- Create: `packages/nexus/pkg/projectmgr/types.go`

- [ ] **Step 1: Create Project type**

```go
package projectmgr

import "time"

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	PrimaryRepo string    `json:"primaryRepo"`
	RepoIDs     []string  `json:"repoIds"`
	RootPath    string    `json:"rootPath"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
```

- [ ] **Step 2: Commit**

```bash
git add packages/nexus/pkg/projectmgr/types.go
git commit -m "feat: add Project type"
```

---

## Task 3: Project Store Repository

**Files:**
- Create: `packages/nexus/pkg/store/project_repo.go`

- [ ] **Step 1: Create project repository interface**

```go
package store

import "time"

type ProjectRow struct {
	ID        string
	Payload   []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ProjectRepository interface {
	UpsertProjectRow(row ProjectRow) error
	DeleteProject(id string) error
	ListProjectRows() ([]ProjectRow, error)
	GetProjectRow(id string) (ProjectRow, bool, error)
}
```

- [ ] **Step 2: Commit**

```bash
git add packages/nexus/pkg/store/project_repo.go
git commit -m "feat: add ProjectRepository interface"
```

---

## Task 4: NodeStore Project Methods

**Files:**
- Modify: `packages/nexus/pkg/store/node_store.go`

- [ ] **Step 1: Add imports**

Add to imports:
```go
"database/sql"
"encoding/json"
```

- [ ] **Step 2: Add UpsertProjectRow method**

```go
func (s *NodeStore) UpsertProjectRow(row ProjectRow) error {
	if row.ID == "" {
		return fmt.Errorf("project id is required")
	}
	if len(row.Payload) == 0 {
		return fmt.Errorf("project payload is required")
	}
	created := row.CreatedAt.UTC().Format(time.RFC3339Nano)
	updated := row.UpdatedAt.UTC().Format(time.RFC3339Nano)

	_, err := s.db.Exec(
		`INSERT INTO projects(id, payload_json, created_at, updated_at)
		 VALUES(?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			payload_json=excluded.payload_json,
			updated_at=excluded.updated_at`,
		row.ID,
		string(row.Payload),
		created,
		updated,
	)
	if err != nil {
		return fmt.Errorf("upsert project: %w", err)
	}

	return nil
}
```

- [ ] **Step 3: Add DeleteProject method**

```go
func (s *NodeStore) DeleteProject(id string) error {
	if id == "" {
		return nil
	}
	if _, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Add ListProjectRows method**

```go
func (s *NodeStore) ListProjectRows() ([]ProjectRow, error) {
	rows, err := s.db.Query(`SELECT id, payload_json, created_at, updated_at FROM projects ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list projects query: %w", err)
	}
	defer rows.Close()

	all := make([]ProjectRow, 0)
	for rows.Next() {
		var (
			id      string
			payload string
			created string
			updated string
		)
		if err := rows.Scan(&id, &payload, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan project row: %w", err)
		}
		createdAt, _ := time.Parse(time.RFC3339Nano, created)
		updatedAt, _ := time.Parse(time.RFC3339Nano, updated)
		all = append(all, ProjectRow{
			ID:        id,
			Payload:   []byte(payload),
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project rows: %w", err)
	}

	return all, nil
}
```

- [ ] **Step 5: Add GetProjectRow method**

```go
func (s *NodeStore) GetProjectRow(id string) (ProjectRow, bool, error) {
	if id == "" {
		return ProjectRow{}, false, nil
	}
	var (
		payload string
		created string
		updated string
	)
	err := s.db.QueryRow(
		`SELECT payload_json, created_at, updated_at FROM projects WHERE id = ?`,
		id,
	).Scan(&payload, &created, &updated)
	if err == sql.ErrNoRows {
		return ProjectRow{}, false, nil
	}
	if err != nil {
		return ProjectRow{}, false, fmt.Errorf("get project: %w", err)
	}
	createdAt, _ := time.Parse(time.RFC3339Nano, created)
	updatedAt, _ := time.Parse(time.RFC3339Nano, updated)
	return ProjectRow{
		ID:        id,
		Payload:   []byte(payload),
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, true, nil
}
```

- [ ] **Step 6: Modify UpsertWorkspaceRow to include project_id**

Replace existing `UpsertWorkspaceRow` with:
```go
func (s *NodeStore) UpsertWorkspaceRow(row WorkspaceRow) error {
	if row.ID == "" {
		return fmt.Errorf("workspace id is required")
	}
	if len(row.Payload) == 0 {
		return fmt.Errorf("workspace payload is required")
	}
	created := row.CreatedAt.UTC().Format(time.RFC3339Nano)
	updated := row.UpdatedAt.UTC().Format(time.RFC3339Nano)

	// Extract project_id from payload JSON
	type workspacePayload struct {
		ProjectID string `json:"projectId"`
	}
	var payloadData workspacePayload
	projectID := ""
	if err := json.Unmarshal(row.Payload, &payloadData); err == nil {
		projectID = payloadData.ProjectID
	}

	_, err := s.db.Exec(
		`INSERT INTO workspaces(id, payload_json, project_id, created_at, updated_at)
		 VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
			payload_json=excluded.payload_json,
			project_id=excluded.project_id,
			updated_at=excluded.updated_at`,
		row.ID,
		string(row.Payload),
		projectID,
		created,
		updated,
	)
	if err != nil {
		return fmt.Errorf("upsert workspace: %w", err)
	}

	return nil
}
```

- [ ] **Step 7: Add ListWorkspaceRowsByProject method**

```go
func (s *NodeStore) ListWorkspaceRowsByProject(projectID string) ([]WorkspaceRow, error) {
	if projectID == "" {
		return s.ListWorkspaceRows()
	}
	rows, err := s.db.Query(
		`SELECT id, payload_json, created_at, updated_at FROM workspaces WHERE project_id = ? ORDER BY created_at ASC`,
		projectID,
	)
	if err != nil {
		return nil, fmt.Errorf("list workspaces by project query: %w", err)
	}
	defer rows.Close()

	all := make([]WorkspaceRow, 0)
	for rows.Next() {
		var (
			id      string
			payload string
			created string
			updated string
		)
		if err := rows.Scan(&id, &payload, &created, &updated); err != nil {
			return nil, fmt.Errorf("scan workspace row: %w", err)
		}
		createdAt, _ := time.Parse(time.RFC3339Nano, created)
		updatedAt, _ := time.Parse(time.RFC3339Nano, updated)
		all = append(all, WorkspaceRow{
			ID:        id,
			Payload:   []byte(payload),
			CreatedAt: createdAt,
			UpdatedAt: updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate workspace rows: %w", err)
	}

	return all, nil
}
```

- [ ] **Step 8: Commit**

```bash
git add packages/nexus/pkg/store/node_store.go
git commit -m "feat: add project persistence methods to NodeStore"
```

---

## Task 5: Add ProjectID to Workspace Type

**Files:**
- Modify: `packages/nexus/pkg/workspacemgr/types.go`

- [ ] **Step 1: Add ProjectID field to Workspace struct**

Add to Workspace struct after ID field:
```go
ProjectID string `json:"projectId,omitempty"`
```

- [ ] **Step 2: Commit**

```bash
git add packages/nexus/pkg/workspacemgr/types.go
git commit -m "feat: add ProjectID to Workspace type"
```

---

## Task 6: Create Project Manager

**Files:**
- Create: `packages/nexus/pkg/projectmgr/manager.go`

- [ ] **Step 1: Create project manager implementation**

```go
package projectmgr

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/store"
)

type projectStore interface {
	store.ProjectRepository
	store.WorkspaceRepository
}

type Manager struct {
	root         string
	projectRepo  projectStore
	mu           sync.RWMutex
	projects     map[string]*Project
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
	if len(base) > 4 && base[len(base)-4:] == ".git" {
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
```

- [ ] **Step 2: Commit**

```bash
git add packages/nexus/pkg/projectmgr/manager.go
git commit -m "feat: add Project manager with GetOrCreateForRepo"
```

---

## Task 7: Update Workspace Manager to Link Projects

**Files:**
- Modify: `packages/nexus/pkg/workspacemgr/manager.go`

- [ ] **Step 1: Add imports**

Add to imports:
```go
"github.com/inizio/nexus/packages/nexus/pkg/projectmgr"
```

- [ ] **Step 2: Add ProjectManager field to Manager struct**

Add to Manager struct:
```go
projectMgr *projectmgr.Manager
```

- [ ] **Step 3: Add SetProjectManager method**

Add to manager.go:
```go
func (m *Manager) SetProjectManager(pm *projectmgr.Manager) {
	m.projectMgr = pm
}
```

- [ ] **Step 4: Modify Create to link project**

In the `Create` method, after deriving repoID, add:
```go
repoID := deriveRepoID(spec.Repo)

// Get or create project for this repo
projectID := ""
if m.projectMgr != nil {
	project, err := m.projectMgr.GetOrCreateForRepo(spec.Repo, repoID)
	if err != nil {
		return nil, fmt.Errorf("get or create project: %w", err)
	}
	projectID = project.ID
}
```

Then add `ProjectID: projectID,` to the Workspace struct initialization.

- [ ] **Step 5: Modify persistWorkspace to handle project_id**

The persistWorkspace method already extracts project_id from JSON payload (we updated NodeStore in Task 4), so no changes needed there.

- [ ] **Step 6: Commit**

```bash
git add packages/nexus/pkg/workspacemgr/manager.go
git commit -m "feat: link workspaces to projects on creation"
```

---

## Task 8: Project RPC Handlers

**Files:**
- Create: `packages/nexus/pkg/handlers/project_manager.go`

- [ ] **Step 1: Create project handlers**

```go
package handlers

import (
	"context"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/projectmgr"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

type ProjectListParams struct{}

type ProjectGetParams struct {
	ID string `json:"id"`
}

type ProjectRemoveParams struct {
	ID string `json:"id"`
}

type ProjectListResult struct {
	Projects []*projectmgr.Project `json:"projects"`
}

type ProjectGetResult struct {
	Project    *projectmgr.Project       `json:"project"`
	Workspaces []*workspacemgr.Workspace `json:"workspaces,omitempty"`
}

type ProjectRemoveResult struct {
	Removed bool `json:"removed"`
}

func HandleProjectList(_ context.Context, _ ProjectListParams, mgr *projectmgr.Manager) (*ProjectListResult, *rpckit.RPCError) {
	all := mgr.List()
	return &ProjectListResult{Projects: all}, nil
}

func HandleProjectGet(_ context.Context, req ProjectGetParams, projMgr *projectmgr.Manager, wsMgr *workspacemgr.Manager) (*ProjectGetResult, *rpckit.RPCError) {
	p, ok := projMgr.Get(req.ID)
	if !ok {
		return nil, rpckit.ErrWorkspaceNotFound // Using existing error type
	}

	// Get workspaces for this project
	var workspaces []*workspacemgr.Workspace
	allWorkspaces := wsMgr.List()
	for _, ws := range allWorkspaces {
		if ws.ProjectID == p.ID {
			workspaces = append(workspaces, ws)
		}
	}

	return &ProjectGetResult{
		Project:    p,
		Workspaces: workspaces,
	}, nil
}

func HandleProjectRemove(_ context.Context, req ProjectRemoveParams, projMgr *projectmgr.Manager, wsMgr *workspacemgr.Manager) (*ProjectRemoveResult, *rpckit.RPCError) {
	// First remove all workspaces in this project
	allWorkspaces := wsMgr.List()
	for _, ws := range allWorkspaces {
		if ws.ProjectID == req.ID {
			_ = wsMgr.Remove(ws.ID)
		}
	}

	removed := projMgr.Remove(req.ID)
	if !removed {
		return nil, rpckit.ErrWorkspaceNotFound
	}

	return &ProjectRemoveResult{Removed: true}, nil
}
```

- [ ] **Step 2: Commit**

```bash
git add packages/nexus/pkg/handlers/project_manager.go
git commit -m "feat: add project RPC handlers"
```

---

## Task 9: Register Project RPC Methods

**Files:**
- Find and modify the RPC registration file (likely in `packages/nexus/pkg/server/`)

- [ ] **Step 1: Find RPC registration file**

Run: `grep -r "workspace.create" packages/nexus/pkg/server/ --include="*.go" -l`

- [ ] **Step 2: Add project method registration**

Look for the file that registers `workspace.create` and add similar registrations for:
- `project.list` -> HandleProjectList
- `project.get` -> HandleProjectGet
- `project.remove` -> HandleProjectRemove

- [ ] **Step 3: Commit**

```bash
git add packages/nexus/pkg/server/<modified-file>.go
git commit -m "feat: register project RPC methods"
```

---

## Task 10: Modify CLI List Command

**Files:**
- Modify: `packages/nexus/cmd/nexus/workspace.go`

- [ ] **Step 1: Add flat flag variable**

Add near other flag variables:
```go
var listFlat bool
```

- [ ] **Step 2: Modify listCmd to add flag**

In `init()` function, add:
```go
listCmd.Flags().BoolVar(&listFlat, "flat", false, "show flat list instead of hierarchical")
```

- [ ] **Step 3: Modify listWorkspaces function**

Replace `listWorkspaces` function with:
```go
func listWorkspaces() {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus list: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	if listFlat {
		listWorkspacesFlat(conn)
		return
	}
	listWorkspacesHierarchical(conn)
}

func listWorkspacesFlat(conn *websocket.Conn) {
	var result struct {
		Workspaces []workspacemgr.Workspace `json:"workspaces"`
	}
	if err := daemonRPC(conn, "workspace.list", map[string]any{}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus list: %v\n", err)
		os.Exit(1)
	}

	if len(result.Workspaces) == 0 {
		fmt.Println("no workspaces")
		return
	}
	fmt.Printf("%-36s  %-20s  %-10s  %-10s  %s\n", "ID", "NAME", "STATE", "BACKEND", "WORKTREE")
	fmt.Printf("%-36s  %-20s  %-10s  %-10s  %s\n",
		"------------------------------------", "--------------------",
		"----------", "----------", "--------")
	for _, ws := range result.Workspaces {
		wt := ws.LocalWorktreePath
		if wt == "" {
			wt = "—"
		}
		fmt.Printf("%-36s  %-20s  %-10s  %-10s  %s\n",
			ws.ID, ws.WorkspaceName, ws.State, ws.Backend, wt)
	}
}

func listWorkspacesHierarchical(conn *websocket.Conn) {
	// Get projects
	var projectsResult struct {
		Projects []projectmgr.Project `json:"projects"`
	}
	if err := daemonRPC(conn, "project.list", map[string]any{}, &projectsResult); err != nil {
		fmt.Fprintf(os.Stderr, "nexus list: %v\n", err)
		os.Exit(1)
	}

	// Get all workspaces
	var workspacesResult struct {
		Workspaces []workspacemgr.Workspace `json:"workspaces"`
	}
	if err := daemonRPC(conn, "workspace.list", map[string]any{}, &workspacesResult); err != nil {
		fmt.Fprintf(os.Stderr, "nexus list: %v\n", err)
		os.Exit(1)
	}

	if len(projectsResult.Projects) == 0 {
		fmt.Println("no projects")
		return
	}

	// Group workspaces by project
	workspacesByProject := make(map[string][]workspacemgr.Workspace)
	for _, ws := range workspacesResult.Workspaces {
		pid := ws.ProjectID
		if pid == "" {
			pid = "orphan"
		}
		workspacesByProject[pid] = append(workspacesByProject[pid], ws)
	}

	// Display hierarchical view
	for _, p := range projectsResult.Projects {
		fmt.Printf("PROJECT: %s (%s)\n", p.Name, p.PrimaryRepo)
		workspaces := workspacesByProject[p.ID]
		if len(workspaces) == 0 {
			fmt.Println("  (no workspaces)")
			continue
		}
		for _, ws := range workspaces {
			fmt.Printf("  %-20s  %-10s  %-10s  %s\n",
				ws.WorkspaceName, ws.State, ws.Backend, ws.Ref)
		}
		fmt.Println()
	}

	// Handle orphaned workspaces (legacy without project)
	if orphans, ok := workspacesByProject["orphan"]; ok && len(orphans) > 0 {
		fmt.Println("PROJECT: (legacy workspaces)")
		for _, ws := range orphans {
			fmt.Printf("  %-20s  %-10s  %-10s  %s\n",
				ws.WorkspaceName, ws.State, ws.Backend, ws.Ref)
		}
	}

	totalWs := len(workspacesResult.Workspaces)
	fmt.Printf("%d projects, %d workspaces total\n", len(projectsResult.Projects), totalWs)
}
```

- [ ] **Step 4: Add projectmgr import**

Add to imports:
```go
"github.com/inizio/nexus/packages/nexus/pkg/projectmgr"
```

- [ ] **Step 5: Commit**

```bash
git add packages/nexus/cmd/nexus/workspace.go
git commit -m "feat: add hierarchical project/workspace listing to nexus list"
```

---

## Task 11: Add CLI Project Commands

**Files:**
- Create: `packages/nexus/cmd/nexus/project.go`

- [ ] **Step 1: Create project commands file**

```go
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/inizio/nexus/packages/nexus/pkg/projectmgr"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
	"github.com/spf13/cobra"
)

var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "Manage projects",
}

var projectListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all projects",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		listProjects()
	},
}

var projectShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show project details and workspaces",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		showProject(strings.TrimSpace(args[0]))
	},
}

var projectRemoveCmd = &cobra.Command{
	Use:   "remove <id>",
	Short: "Remove a project and all its workspaces",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		removeProject(strings.TrimSpace(args[0]))
	},
}

func init() {
	projectCmd.AddCommand(projectListCmd, projectShowCmd, projectRemoveCmd)
	rootCmd.AddCommand(projectCmd)
}

func listProjects() {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus project list: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	var result struct {
		Projects []projectmgr.Project `json:"projects"`
	}
	if err := daemonRPC(conn, "project.list", map[string]any{}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus project list: %v\n", err)
		os.Exit(1)
	}

	if len(result.Projects) == 0 {
		fmt.Println("no projects")
		return
	}

	fmt.Printf("%-24s  %-20s  %s\n", "ID", "NAME", "PRIMARY REPO")
	fmt.Printf("%-24s  %-20s  %s\n",
		"------------------------", "--------------------", "------------------------------")
	for _, p := range result.Projects {
		fmt.Printf("%-24s  %-20s  %s\n", p.ID, p.Name, p.PrimaryRepo)
	}
}

func showProject(id string) {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus project show: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	var result struct {
		Project    projectmgr.Project         `json:"project"`
		Workspaces []workspacemgr.Workspace   `json:"workspaces"`
	}
	if err := daemonRPC(conn, "project.get", map[string]any{"id": id}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus project show: %v\n", err)
		os.Exit(1)
	}

	p := result.Project
	fmt.Printf("ID:             %s\n", p.ID)
	fmt.Printf("Name:           %s\n", p.Name)
	fmt.Printf("Primary Repo:   %s\n", p.PrimaryRepo)
	fmt.Printf("Root Path:      %s\n", p.RootPath)
	fmt.Printf("Created:        %s\n", p.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Printf("\nWorkspaces (%d):\n", len(result.Workspaces))

	if len(result.Workspaces) == 0 {
		fmt.Println("  (none)")
		return
	}

	fmt.Printf("  %-36s  %-20s  %-10s  %s\n", "ID", "NAME", "STATE", "REF")
	fmt.Printf("  %-36s  %-20s  %-10s  %s\n",
		"------------------------------------", "--------------------",
		"----------", "----------")
	for _, ws := range result.Workspaces {
		fmt.Printf("  %-36s  %-20s  %-10s  %s\n",
			ws.ID, ws.WorkspaceName, ws.State, ws.Ref)
	}
}

func removeProject(id string) {
	conn, err := ensureDaemon()
	if err != nil {
		fmt.Fprintf(os.Stderr, "nexus project remove: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	var result struct {
		Removed bool `json:"removed"`
	}
	if err := daemonRPC(conn, "project.remove", map[string]any{"id": id}, &result); err != nil {
		fmt.Fprintf(os.Stderr, "nexus project remove: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("removed project %s\n", id)
}
```

- [ ] **Step 2: Commit**

```bash
git add packages/nexus/cmd/nexus/project.go
git commit -m "feat: add nexus project subcommands (list, show, remove)"
```

---

## Task 12: SDK TypeScript Types

**Files:**
- Create: `packages/sdk/js/src/types/project.ts`
- Modify: `packages/sdk/js/src/types/workspace.ts`

- [ ] **Step 1: Create project types file**

```typescript
export interface Project {
  id: string;
  name: string;
  primaryRepo: string;
  repoIds: string[];
  rootPath: string;
  createdAt: string;
  updatedAt: string;
}

export interface ProjectListResult {
  projects: Project[];
}

export interface ProjectWithWorkspaces extends Project {
  workspaces: import('./workspace').WorkspaceRecord[];
}

export interface ProjectGetResult {
  project: Project;
  workspaces?: import('./workspace').WorkspaceRecord[];
}

export interface ProjectRemoveResult {
  removed: boolean;
}
```

- [ ] **Step 2: Add ProjectID to WorkspaceRecord**

In `packages/sdk/js/src/types/workspace.ts`, add to WorkspaceRecord:
```typescript
projectId?: string;
```

- [ ] **Step 3: Commit**

```bash
git add packages/sdk/js/src/types/project.ts packages/sdk/js/src/types/workspace.ts
git commit -m "feat(sdk): add Project types and ProjectID to WorkspaceRecord"
```

---

## Task 13: SDK RPC Schema

**Files:**
- Modify: `packages/sdk/js/src/rpc/schema.ts`

- [ ] **Step 1: Add import**

Add to imports:
```typescript
import type { ProjectListResult, ProjectGetResult, ProjectRemoveResult } from '../types/project';
```

- [ ] **Step 2: Add project RPC methods**

Add to RPCSchema interface:
```typescript
'project.list': [Record<string, never>, ProjectListResult];
'project.get': [{ id: string }, ProjectGetResult];
'project.remove': [{ id: string }, ProjectRemoveResult];
```

- [ ] **Step 3: Commit**

```bash
git add packages/sdk/js/src/rpc/schema.ts
git commit -m "feat(sdk): add project RPC methods to schema"
```

---

## Task 14: Initialize Project Manager in Daemon

**Files:**
- Find where workspace manager is initialized (likely in daemon main or server setup)

- [ ] **Step 1: Find daemon initialization**

Run: `grep -r "workspacemgr.NewManager" packages/nexus/ --include="*.go" -l`

- [ ] **Step 2: Add project manager initialization**

After creating workspace manager, add:
```go
projectMgr := projectmgr.NewManager(root, store)
wsMgr.SetProjectManager(projectMgr)
```

Pass `projectMgr` to RPC handlers that need it.

- [ ] **Step 3: Commit**

```bash
git add packages/nexus/<daemon-init-file>.go
git commit -m "feat: initialize project manager in daemon"
```

---

## Task 15: Migration for Existing Workspaces

**Files:**
- Modify: `packages/nexus/pkg/projectmgr/manager.go`

- [ ] **Step 1: Add migration method**

Add to Manager:
```go
// MigrateWorkspacesWithoutProject creates projects for existing workspaces
// that don't have a project_id assigned. Call this on startup.
func (m *Manager) MigrateWorkspacesWithoutProject(wsMgr *workspacemgr.Manager) error {
	if m.projectRepo == nil {
		return nil
	}

	allWorkspaces := wsMgr.List()
	for _, ws := range allWorkspaces {
		if ws.ProjectID != "" {
			continue // Already has a project
		}

		// Create or get project for this workspace's repo
		project, err := m.GetOrCreateForRepo(ws.Repo, ws.RepoID)
		if err != nil {
			log.Printf("project migration: failed to create project for workspace %s: %v", ws.ID, err)
			continue
		}

		// Update workspace with project ID
		ws.ProjectID = project.ID
		// Re-persist workspace with new project_id
		if err := wsMgr.UpdateProjectID(ws.ID, project.ID); err != nil {
			log.Printf("project migration: failed to update workspace %s: %v", ws.ID, err)
		}
	}

	return nil
}
```

- [ ] **Step 2: Add UpdateProjectID method to workspace manager**

In `packages/nexus/pkg/workspacemgr/manager.go`, add:
```go
func (m *Manager) UpdateProjectID(id string, projectID string) error {
	m.mu.Lock()
	ws, ok := m.workspaces[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("workspace not found: %s", id)
	}
	ws.ProjectID = projectID
	ws.UpdatedAt = time.Now().UTC()
	m.mu.Unlock()

	if err := m.persistWorkspace(ws); err != nil {
		return fmt.Errorf("persist project id update: %w", err)
	}
	return nil
}
```

- [ ] **Step 3: Call migration on daemon startup**

In daemon initialization, after creating both managers:
```go
// Migrate existing workspaces to projects
if err := projectMgr.MigrateWorkspacesWithoutProject(wsMgr); err != nil {
	log.Printf("warning: project migration failed: %v", err)
}
```

- [ ] **Step 4: Commit**

```bash
git add packages/nexus/pkg/projectmgr/manager.go packages/nexus/pkg/workspacemgr/manager.go
git commit -m "feat: migrate existing workspaces to projects on startup"
```

---

## Task 16: Testing

- [ ] **Step 1: Build nexus**

Run: `cd packages/nexus && go build ./...`
Expected: Build succeeds

- [ ] **Step 2: Run unit tests**

Run: `cd packages/nexus && go test ./pkg/store/... ./pkg/projectmgr/... ./pkg/workspacemgr/... -v`
Expected: Tests pass

- [ ] **Step 3: Build SDK**

Run: `cd packages/sdk/js && npm run build`
Expected: Build succeeds

- [ ] **Step 4: Commit**

```bash
git commit -m "test: verify project/workspace hierarchy builds and tests pass"
```

---

## Task 17: Documentation Update

- [ ] **Step 1: Update CLI docs**

Add to relevant documentation files describing:
- New `nexus project` commands
- Modified `nexus list` behavior
- Project/workspace hierarchy concept

- [ ] **Step 2: Commit**

```bash
git add docs/
git commit -m "docs: document project/workspace hierarchy feature"
```

---

## Final Review Checklist

- [ ] All code changes follow AGENTS.md guidelines
- [ ] File size limits respected (core/domain ≤300 lines, etc.)
- [ ] All new files have proper package declarations
- [ ] Database migration uses Goose format correctly
- [ ] Project manager follows workspace manager patterns
- [ ] CLI commands follow existing patterns
- [ ] SDK types mirror Go types
- [ ] RPC methods registered properly
- [ ] Migration handles existing workspaces
