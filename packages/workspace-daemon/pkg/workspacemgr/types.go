package workspacemgr

import "time"

type WorkspaceState string

const (
	StateSetup    WorkspaceState = "setup"
	StateStart    WorkspaceState = "start"
	StateReady    WorkspaceState = "ready"
	StateActive   WorkspaceState = "active"
	StateTeardown WorkspaceState = "teardown"
)

type CreateSpec struct {
	Repo          string `json:"repo"`
	Ref           string `json:"ref"`
	WorkspaceName string `json:"workspaceName"`
	AgentProfile  string `json:"agentProfile"`
	Policy        Policy `json:"policy"`
}

type Workspace struct {
	ID            string         `json:"id"`
	Repo          string         `json:"repo"`
	Ref           string         `json:"ref"`
	WorkspaceName string         `json:"workspaceName"`
	AgentProfile  string         `json:"agentProfile"`
	Policy        Policy         `json:"policy"`
	State         WorkspaceState `json:"state"`
	RootPath      string         `json:"rootPath"`
	CreatedAt     time.Time      `json:"createdAt"`
	UpdatedAt     time.Time      `json:"updatedAt"`
}
