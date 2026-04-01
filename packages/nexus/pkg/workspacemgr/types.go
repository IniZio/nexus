package workspacemgr

import "time"

type WorkspaceState string

const (
	StateCreated  WorkspaceState = "created"
	StateRunning  WorkspaceState = "running"
	StatePaused   WorkspaceState = "paused"
	StateStopped  WorkspaceState = "stopped"
	StateRestored WorkspaceState = "restored"
	StateRemoved  WorkspaceState = "removed"
)

type CreateSpec struct {
	Repo          string            `json:"repo"`
	Ref           string            `json:"ref"`
	WorkspaceName string            `json:"workspaceName"`
	AgentProfile  string            `json:"agentProfile"`
	Policy        Policy            `json:"policy"`
	Backend       string            `json:"backend,omitempty"`
	AuthBinding   map[string]string `json:"authBinding,omitempty"`
}

type Workspace struct {
	ID                string            `json:"id"`
	Repo              string            `json:"repo"`
	Ref               string            `json:"ref"`
	WorkspaceName     string            `json:"workspaceName"`
	AgentProfile      string            `json:"agentProfile"`
	Policy            Policy            `json:"policy"`
	State             WorkspaceState    `json:"state"`
	RootPath          string            `json:"rootPath"`
	ParentWorkspaceID string            `json:"parentWorkspaceId,omitempty"`
	Backend           string            `json:"backend,omitempty"`
	AuthBinding       map[string]string `json:"authBinding,omitempty"`
	CreatedAt         time.Time         `json:"createdAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
}
