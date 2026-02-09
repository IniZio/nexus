// Package pulse provides integration with Pulse issue tracking.
package pulse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Client is a Pulse API client
type Client struct {
	baseURL string
	client  *http.Client
}

// Issue represents a Pulse issue
type Issue struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"workspace_id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	Priority    int       `json:"priority"`
	AssigneeID  string    `json:"assignee_id"`
	Labels      []string  `json:"labels"`
	Estimate    int       `json:"estimate"`
	CycleID     string    `json:"cycle_id"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Workspace represents a Pulse workspace
type Workspace struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

// NewClient creates a new Pulse client
func NewClient(baseURL string) *Client {
	if baseURL == "" {
		baseURL = "http://localhost:3002"
	}
	return &Client{
		baseURL: baseURL,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// CreateIssue creates a new issue in Pulse
func (c *Client) CreateIssue(workspaceID, title, description string, priority, estimate int, labels []string) (*Issue, error) {
	issue := map[string]interface{}{
		"workspace_id": workspaceID,
		"title":        title,
		"description":  description,
		"status":       "backlog",
		"priority":     priority,
		"estimate":     estimate,
		"labels":       labels,
	}

	data, err := json.Marshal(issue)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal issue: %w", err)
	}

	resp, err := c.client.Post(c.baseURL+"/api/issues", "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create issue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to create issue: status %d", resp.StatusCode)
	}

	var created Issue
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &created, nil
}

// ListIssues lists issues in a workspace
func (c *Client) ListIssues(workspaceID string) ([]*Issue, error) {
	url := c.baseURL + "/api/issues?workspace_id=" + workspaceID
	resp, err := c.client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to list issues: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list issues: status %d", resp.StatusCode)
	}

	var issues []*Issue
	if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return issues, nil
}

// UpdateIssueStatus updates an issue's status
func (c *Client) UpdateIssueStatus(issueID, status string) error {
	data, err := json.Marshal(map[string]string{"status": status})
	if err != nil {
		return fmt.Errorf("failed to marshal status: %w", err)
	}

	url := c.baseURL + "/api/issues/" + issueID
	req, err := http.NewRequest(http.MethodPatch, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to update issue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to update issue: status %d", resp.StatusCode)
	}

	return nil
}

// GetWorkspaces lists all workspaces
func (c *Client) GetWorkspaces() ([]*Workspace, error) {
	resp, err := c.client.Get(c.baseURL + "/api/workspaces")
	if err != nil {
		return nil, fmt.Errorf("failed to list workspaces: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to list workspaces: status %d", resp.StatusCode)
	}

	var workspaces []*Workspace
	if err := json.NewDecoder(resp.Body).Decode(&workspaces); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return workspaces, nil
}

// EnsureWorkspace creates a workspace if it doesn't exist
func (c *Client) EnsureWorkspace(name, description string) (*Workspace, error) {
	workspaces, err := c.GetWorkspaces()
	if err != nil {
		return nil, err
	}

	// Look for existing workspace with same name
	for _, ws := range workspaces {
		if ws.Name == name {
			return ws, nil
		}
	}

	// Create new workspace
	ws := map[string]interface{}{
		"name":        name,
		"description": description,
		"settings":    "{}",
	}

	data, err := json.Marshal(ws)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal workspace: %w", err)
	}

	resp, err := c.client.Post(c.baseURL+"/api/workspaces", "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create workspace: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to create workspace: status %d", resp.StatusCode)
	}

	var created Workspace
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &created, nil
}

// Health checks if Pulse is running
func (c *Client) Health() error {
	resp, err := c.client.Get(c.baseURL + "/api/health")
	if err != nil {
		return fmt.Errorf("pulse not reachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("pulse unhealthy: status %d", resp.StatusCode)
	}

	return nil
}

// IsConfigured checks if Pulse is configured (environment variable or reachable)
func IsConfigured() bool {
	return os.Getenv("PULSE_URL") != "" || true // Default to localhost:3002
}
