package workspace

import "time"

// Workspace is the core domain entity representing a remote development environment.
type Workspace struct {
	ID                string            `json:"id"`
	ProjectID         string            `json:"projectId,omitempty"`
	RepoID            string            `json:"repoId,omitempty"`
	Repo              string            `json:"repo"`
	Ref               string            `json:"ref"`
	WorkspaceName     string            `json:"workspaceName"`
	AgentProfile      string            `json:"agentProfile"`
	Policy            Policy            `json:"policy"`
	State             State             `json:"state"`
	RootPath          string            `json:"rootPath"`
	AuthBinding       map[string]string `json:"authBinding,omitempty"`
	TunnelPorts       []int             `json:"tunnelPorts,omitempty"`
	ParentWorkspaceID string            `json:"parentWorkspaceId,omitempty"`
	LineageRootID     string            `json:"lineageRootId,omitempty"`
	Backend           string            `json:"backend,omitempty"`
	ConfigBundle      string            `json:"configBundle,omitempty"`
	CreatedAt         time.Time         `json:"created_at,omitempty"`
	UpdatedAt         time.Time         `json:"updated_at,omitempty"`
}

// CreateSpec holds parameters for creating a new workspace.
type CreateSpec struct {
	Repo          string            `json:"repo"`
	Ref           string            `json:"ref"`
	WorkspaceName string            `json:"workspaceName"`
	AgentProfile  string            `json:"agentProfile"`
	Policy        Policy            `json:"policy"`
	Backend       string            `json:"backend,omitempty"`
	AuthBinding   map[string]string `json:"authBinding,omitempty"`
	ConfigBundle  string            `json:"configBundle,omitempty"`
	ProjectID     string            `json:"projectId,omitempty"`
}
