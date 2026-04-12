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
	AuthBinding  map[string]string `json:"authBinding,omitempty"`
	ConfigBundle string            `json:"configBundle,omitempty"`
}

type Workspace struct {
	ID                string            `json:"id"`
	ProjectID         string            `json:"projectId,omitempty"`
	RepoID            string            `json:"repoId,omitempty"`
	RepoKind          string            `json:"repoKind,omitempty"`
	Repo              string            `json:"repo"`
	Ref               string            `json:"ref"`
	WorkspaceName     string            `json:"workspaceName"`
	AgentProfile      string            `json:"agentProfile"`
	Policy            Policy            `json:"policy"`
	State             WorkspaceState    `json:"state"`
	RootPath          string            `json:"rootPath"`
	ParentWorkspaceID string            `json:"parentWorkspaceId,omitempty"`
	LineageRootID     string            `json:"lineageRootId,omitempty"`
	DerivedFromRef    string            `json:"derivedFromRef,omitempty"`
	Backend           string            `json:"backend,omitempty"`
	AuthBinding       map[string]string `json:"authBinding,omitempty"`
	// LocalWorktreePath is the path of the git worktree on the host machine
	// that is synced with this workspace inside the sandbox.
	LocalWorktreePath string `json:"localWorktreePath,omitempty"`
	// MutagenSessionID is the mutagen sync session name for this workspace.
	// Empty if no sync session has been established.
	MutagenSessionID string `json:"mutagenSessionId,omitempty"`

	// NEW: Optional fields for future multi-user support
	// In personal mode, OwnerUserID is "local" and TenantID is empty
	OwnerUserID string `json:"owner_user_id,omitempty"`
	TenantID    string `json:"tenant_id,omitempty"`
	CreatedBy   string `json:"created_by,omitempty"`

	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}
