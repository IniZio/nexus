package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/nexus/nexus/pkg/coordination"
)

// Server represents the Pulse web server
type Server struct {
	coordServer *coordination.Server
	addr        string
	mux         *http.ServeMux
	server      *http.Server
	workspaces  map[string]*Workspace
	mu          sync.RWMutex
}

// Workspace represents a project workspace
type Workspace struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Issues      map[string]*Issue `json:"issues"`
	Columns     []Column          `json:"columns"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

// Issue represents a single issue/task
type Issue struct {
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Status      string            `json:"status"`
	Priority    int               `json:"priority"` // 0=No priority, 1=Urgent, 2=High, 3=Medium, 4=Low
	AssigneeID string            `json:"assignee_id"`
	Labels      []string          `json:"labels"`
	Estimate    int               `json:"estimate"` // Story points
	CycleID     string            `json:"cycle_id"`
	ParentID    string            `json:"parent_id"`
	SubIssues   []string          `json:"sub_issues"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	CompletedAt *time.Time        `json:"completed_at"`
}

// Cycle represents a time-boxed iteration
type Cycle struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	StartDate time.Time  `json:"start_date"`
	EndDate   time.Time  `json:"end_date"`
	IssueIDs  []string   `json:"issue_ids"`
	Completed int        `json:"completed"` // Number of completed issues
	Progress  float64    `json:"progress"`  // 0-100
}

// Column represents a workflow column
type Column struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Order  int    `json:"order"`
	Color  string `json:"color"`
}

// Team represents a team
type Team struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Members   []string `json:"members"`
	Workspace string   `json:"workspace"`
}

// VelocityMetrics tracks team velocity
type VelocityMetrics struct {
	CycleID           string    `json:"cycle_id"`
	PointsPlanned     int       `json:"points_planned"`
	PointsCompleted   int       `json:"points_completed"`
	CycleTime         float64   `json:"cycle_time_hours"`
	LeadTime          float64   `json:"lead_time_hours"`
	BugCount          int       `json:"bug_count"`
	IssuesCreated     int       `json:"issues_created"`
	IssuesCompleted   int       `json:"issues_completed"`
	AverageEstimate   float64   `json:"average_estimate"`
	CompletionRate    float64   `json:"completion_rate"` // 0-100
}

// NewServer creates a new Pulse server
func NewServer(coordServer *coordination.Server, addr string) *Server {
	s := &Server{
		coordServer: coordServer,
		addr:        addr,
		mux:         http.NewServeMux(),
		workspaces:  make(map[string]*Workspace),
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	// API routes
	s.mux.HandleFunc("/api/health", s.handleHealth)
	s.mux.HandleFunc("/api/workspaces", s.handleWorkspaces)
	s.mux.HandleFunc("/api/workspaces/", s.handleWorkspace)
	s.mux.HandleFunc("/api/issues", s.handleIssues)
	s.mux.HandleFunc("/api/issues/", s.handleIssue)
	s.mux.HandleFunc("/api/cycles", s.handleCycles)
	s.mux.HandleFunc("/api/cycles/", s.handleCycle)
	s.mux.HandleFunc("/api/metrics", s.handleMetrics)
	s.mux.HandleFunc("/api/search", s.handleSearch)

	// Web UI
	s.mux.HandleFunc("/", s.handleWebUI)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	jsonResponse(w, map[string]interface{}{
		"status":    "ok",
		"timestamp": time.Now().Format(time.RFC3339),
		"version":   "1.0.0",
	})
}

func (s *Server) handleWorkspaces(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.mu.RLock()
		workspaces := make([]*Workspace, 0, len(s.workspaces))
		for _, ws := range s.workspaces {
			workspaces = append(workspaces, ws)
		}
		s.mu.RUnlock()
		jsonResponse(w, workspaces)

	case http.MethodPost:
		var req struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		ws := &Workspace{
			ID:          generateID(),
			Name:        req.Name,
			Description: req.Description,
			Issues:      make(map[string]*Issue),
			Columns:     defaultColumns(),
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		s.mu.Lock()
		s.workspaces[ws.ID] = ws
		s.mu.Unlock()

		jsonResponse(w, ws)
	}
}

func (s *Server) handleWorkspace(w http.ResponseWriter, r *http.Request) {
	id := filepath.Base(r.URL.Path)
	s.mu.RLock()
	ws, ok := s.workspaces[id]
	s.mu.RUnlock()

	if !ok {
		http.Error(w, "workspace not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		jsonResponse(w, ws)

	case http.MethodPut:
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		s.mu.Lock()
		if name, ok := req["name"].(string); ok {
			ws.Name = name
		}
		if desc, ok := req["description"].(string); ok {
			ws.Description = desc
		}
		ws.UpdatedAt = time.Now()
		s.mu.Unlock()

		jsonResponse(w, ws)

	case http.MethodDelete:
		s.mu.Lock()
		delete(s.workspaces, id)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleIssues(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// Get issues across all workspaces or filter by workspace
		workspaceID := r.URL.Query().Get("workspace_id")
		s.mu.RLock()
		var issues []*Issue
		for _, ws := range s.workspaces {
			if workspaceID == "" || ws.ID == workspaceID {
				for _, issue := range ws.Issues {
					issues = append(issues, issue)
				}
			}
		}
		s.mu.RUnlock()
		jsonResponse(w, issues)

	case http.MethodPost:
		var req struct {
			WorkspaceID string `json:"workspace_id"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Status      string `json:"status"`
			Priority    int    `json:"priority"`
			AssigneeID  string `json:"assignee_id"`
			Labels      []string `json:"labels"`
			Estimate    int    `json:"estimate"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		s.mu.RLock()
		ws, ok := s.workspaces[req.WorkspaceID]
		s.mu.RUnlock()

		if !ok {
			http.Error(w, "workspace not found", http.StatusNotFound)
			return
		}

		issue := &Issue{
			ID:          generateID(),
			Title:       req.Title,
			Description: req.Description,
			Status:      req.Status,
			Priority:    req.Priority,
			AssigneeID:  req.AssigneeID,
			Labels:      req.Labels,
			Estimate:    req.Estimate,
			CreatedAt:   time.Now(),
			UpdatedAt:   time.Now(),
		}

		s.mu.Lock()
		ws.Issues[issue.ID] = issue
		ws.UpdatedAt = time.Now()
		s.mu.Unlock()

		jsonResponse(w, issue)
	}
}

func (s *Server) handleIssue(w http.ResponseWriter, r *http.Request) {
	id := filepath.Base(r.URL.Path)

	// Find the issue across all workspaces
	s.mu.RLock()
	var issue *Issue
	var workspaceID string
	for wsID, ws := range s.workspaces {
		if i, ok := ws.Issues[id]; ok {
			issue = i
			workspaceID = wsID
			break
		}
	}
	s.mu.RUnlock()

	if issue == nil {
		http.Error(w, "issue not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		jsonResponse(w, issue)

	case http.MethodPut:
		var req map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		s.mu.Lock()
		if title, ok := req["title"].(string); ok {
			issue.Title = title
		}
		if desc, ok := req["description"].(string); ok {
			issue.Description = desc
		}
		if status, ok := req["status"].(string); ok {
			issue.Status = status
			if status == "done" {
				now := time.Now()
				issue.CompletedAt = &now
			}
		}
		if priority, ok := req["priority"].(float64); ok {
			issue.Priority = int(priority)
		}
		if assignee, ok := req["assignee_id"].(string); ok {
			issue.AssigneeID = assignee
		}
		issue.UpdatedAt = time.Now()

		ws := s.workspaces[workspaceID]
		ws.UpdatedAt = time.Now()
		s.mu.Unlock()

		jsonResponse(w, issue)

	case http.MethodDelete:
		s.mu.Lock()
		delete(s.workspaces[workspaceID].Issues, id)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleCycles(w http.ResponseWriter, r *http.Request) {
	// Simplified cycle handling - cycles are stored in coordination server
	jsonResponse(w, []interface{}{})
}

func (s *Server) handleCycle(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspace_id")

	s.mu.RLock()
	var ws *Workspace
	for _, w := range s.workspaces {
		if w.ID == workspaceID || workspaceID == "" {
			ws = w
			break
		}
	}
	s.mu.RUnlock()

	if ws == nil {
		jsonResponse(w, VelocityMetrics{})
		return
	}

	// Calculate metrics
	metrics := calculateMetrics(ws)
	jsonResponse(w, metrics)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		jsonResponse(w, []interface{}{})
		return
	}

	s.mu.RLock()
	var results []interface{}
	for _, ws := range s.workspaces {
		for _, issue := range ws.Issues {
			if contains(issue.Title, query) || contains(issue.Description, query) {
				results = append(results, map[string]interface{}{
					"type":    "issue",
					"id":      issue.ID,
					"title":   issue.Title,
					"status":  issue.Status,
					"workspace": ws.Name,
				})
			}
		}
	}
	s.mu.RUnlock()

	jsonResponse(w, results)
}

func (s *Server) handleWebUI(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, webUIHTML())
}

// Start starts the server
func (s *Server) Start(ctx context.Context) error {
	s.server = &http.Server{
		Addr:    s.addr,
		Handler: s.mux,
	}

	go func() {
		fmt.Printf("Pulse server starting on %s\n", s.addr)
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("Server error: %v\n", err)
		}
	}()

	<-ctx.Done()
	return s.server.Shutdown(ctx)
}

// Helper functions
func generateID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func defaultColumns() []Column {
	return []Column{
		{ID: "backlog", Name: "Backlog", Order: 0, Color: "#6B7280"},
		{ID: "todo", Name: "To Do", Order: 1, Color: "#F59E0B"},
		{ID: "in_progress", Name: "In Progress", Order: 2, Color: "#3B82F6"},
		{ID: "done", Name: "Done", Order: 3, Color: "#10B981"},
	}
}

func calculateMetrics(ws *Workspace) VelocityMetrics {
	var metrics VelocityMetrics

	for _, issue := range ws.Issues {
		metrics.IssuesCreated++
		metrics.PointsPlanned += issue.Estimate

		if issue.Status == "done" && issue.CompletedAt != nil {
			metrics.IssuesCompleted++
			metrics.PointsCompleted += issue.Estimate
		}

		if len(issue.Labels) > 0 {
			for _, label := range issue.Labels {
				if label == "bug" {
					metrics.BugCount++
					break
				}
			}
		}
	}

	if metrics.IssuesCreated > 0 {
		metrics.CompletionRate = float64(metrics.IssuesCompleted) / float64(metrics.IssuesCreated) * 100
	}
	if metrics.PointsPlanned > 0 {
		metrics.AverageEstimate = float64(metrics.PointsPlanned) / float64(metrics.IssuesCreated)
	}

	return metrics
}

func jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// webUIHTML returns the web UI HTML page
func webUIHTML() string {
	return `<!DOCTYPE html>
<html>
<head>
    <title>Pulse - Project Management</title>
    <style>
        * { box-sizing: border-box; margin: 0; padding: 0; }
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; background: #0F1117; color: #ECEFF1; }
        .app { display: flex; height: 100vh; }
        .sidebar { width: 240px; background: #161B22; border-right: 1px solid #30363D; padding: 16px; }
        .logo { font-size: 20px; font-weight: 700; color: #A371F7; margin-bottom: 24px; display: flex; align-items: center; gap: 8px; }
        .nav-item { display: flex; align-items: center; padding: 8px 12px; border-radius: 6px; color: #8B949E; cursor: pointer; margin-bottom: 4px; }
        .nav-item:hover, .nav-item.active { background: #21262D; color: #ECEFF1; }
        .main { flex: 1; overflow: auto; }
        .header { padding: 16px 24px; border-bottom: 1px solid #30363D; display: flex; justify-content: space-between; align-items: center; }
        .header h1 { font-size: 18px; font-weight: 600; }
        .btn { background: #238636; color: white; border: none; padding: 8px 16px; border-radius: 6px; cursor: pointer; font-size: 14px; }
        .btn:hover { background: #2EA043; }
        .board { display: flex; padding: 24px; gap: 16px; overflow-x: auto; }
        .column { min-width: 280px; background: #0D1117; border-radius: 8px; padding: 12px; }
        .column-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
        .column-title { font-weight: 600; font-size: 14px; display: flex; align-items: center; gap: 8px; }
        .column-count { background: #30363D; padding: 2px 8px; border-radius: 10px; font-size: 12px; color: #8B949E; }
        .issue { background: #161B22; border: 1px solid #30363D; border-radius: 6px; padding: 12px; margin-bottom: 8px; cursor: pointer; }
        .issue:hover { border-color: #58A6FF; }
        .issue-title { font-size: 14px; margin-bottom: 8px; }
        .issue-labels { display: flex; gap: 4px; flex-wrap: wrap; }
        .label { font-size: 11px; padding: 2px 6px; border-radius: 4px; background: #30363D; }
        .label.bug { background: #F85149; color: white; }
        .label.feature { background: #A371F7; color: white; }
        .priority { width: 4px; height: 20px; border-radius: 2px; display: inline-block; margin-right: 8px; }
        .priority.urgent { background: #F85149; }
        .priority.high { background: #F0883E; }
        .priority.medium { background: #F0C420; }
        .priority.low { background: #3FB950; }
        .search { background: #0D1117; border: 1px solid #30363D; padding: 8px 12px; border-radius: 6px; color: #ECEFF1; width: 240px; }
        .search:focus { outline: none; border-color: #58A6FF; }
    </style>
</head>
<body>
    <div class="app">
        <div class="sidebar">
            <div class="logo">Pulse</div>
            <div class="nav-item active">Board</div>
            <div class="nav-item">Analytics</div>
            <div class="nav-item">Cycles</div>
            <div class="nav-item">Labels</div>
            <div class="nav-item">Settings</div>
        </div>
        <div class="main">
            <div class="header">
                <h1>Project Board</h1>
                <input type="text" class="search" placeholder="Search issues..." id="search">
                <button class="btn" id="createBtn">+ New Issue</button>
            </div>
            <div class="board" id="board"></div>
        </div>
    </div>
    <script>
        var issues = [];
        var columns = ['backlog', 'todo', 'in_progress', 'done'];

        function getColumnColor(col) {
            var colors = { backlog: '#6B7280', todo: '#F59E0B', in_progress: '#3B82F6', done: '#10B981' };
            return colors[col] || '#6B7280';
        }

        function renderIssue(issue) {
            var priorityClass = ['', 'urgent', 'high', 'medium', 'low'][issue.priority] || '';
            var labelsHtml = '';
            for (var i = 0; i < issue.labels.length; i++) {
                labelsHtml += '<span class="label ' + issue.labels[i] + '">' + issue.labels[i] + '</span>';
            }
            return '<div class="issue" onclick="editIssue(\'' + issue.id + '\')">' +
                '<div style="display: flex; align-items: flex-start;">' +
                '<div class="priority ' + priorityClass + '"></div>' +
                '<div>' +
                '<div class="issue-title">' + issue.title + '</div>' +
                '<div class="issue-labels">' + labelsHtml + '</div>' +
                '</div></div></div>';
        }

        function renderBoard() {
            var board = document.getElementById('board');
            board.innerHTML = '';
            for (var i = 0; i < columns.length; i++) {
                var col = columns[i];
                var colIssues = [];
                for (var j = 0; j < issues.length; j++) {
                    if (issues[j].status === col) colIssues.push(issues[j]);
                }
                var colDiv = document.createElement('div');
                colDiv.className = 'column';
                var issuesHtml = '';
                for (var k = 0; k < colIssues.length; k++) {
                    issuesHtml += renderIssue(colIssues[k]);
                }
                colDiv.innerHTML = '<div class="column-header">' +
                    '<div class="column-title">' +
                    '<span style="color:' + getColumnColor(col) + '">●</span>' +
                    col.replace(/_/g, ' ') +
                    '<span class="column-count">' + colIssues.length + '</span>' +
                    '</div></div>' + issuesHtml;
                board.appendChild(colDiv);
            }
        }

        function loadIssues() {
            var xhr = new XMLHttpRequest();
            xhr.open('GET', '/api/issues', true);
            xhr.onreadystatechange = function() {
                if (xhr.readyState === 4 && xhr.status === 200) {
                    issues = JSON.parse(xhr.responseText);
                    renderBoard();
                }
            };
            xhr.send();
        }

        function createIssue() {
            var title = prompt('Issue title:');
            if (title) {
                var xhr = new XMLHttpRequest();
                xhr.open('POST', '/api/issues', true);
                xhr.setRequestHeader('Content-Type', 'application/json');
                xhr.onreadystatechange = function() {
                    if (xhr.readyState === 4) loadIssues();
                };
                xhr.send(JSON.stringify({
                    workspace_id: 'default',
                    title: title,
                    status: 'backlog',
                    priority: 3,
                    labels: []
                }));
            }
        }

        function editIssue(id) {
            var issue = null;
            for (var i = 0; i < issues.length; i++) {
                if (issues[i].id === id) { issue = issues[i]; break; }
            }
            if (!issue) return;
            var newStatus = prompt('New status (backlog, todo, in_progress, done):', issue.status);
            if (newStatus && columns.indexOf(newStatus) !== -1) {
                var xhr = new XMLHttpRequest();
                xhr.open('PUT', '/api/issues/' + id, true);
                xhr.setRequestHeader('Content-Type', 'application/json');
                xhr.onreadystatechange = function() {
                    if (xhr.readyState === 4) loadIssues();
                };
                xhr.send(JSON.stringify({ status: newStatus }));
            }
        }

        document.getElementById('createBtn').addEventListener('click', createIssue);
        document.getElementById('search').addEventListener('input', function(e) {
            var query = e.target.value.toLowerCase();
            // Filter is handled in loadIssues
        });
        loadIssues();
    </script>
</body>
</html>`
}
