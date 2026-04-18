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
	Repo               string            `json:"repo"`
	Ref                string            `json:"ref"`
	WorkspaceName      string            `json:"workspaceName"`
	AgentProfile       string            `json:"agentProfile"`
	Policy             Policy            `json:"policy"`
	Backend            string            `json:"backend,omitempty"`
	AuthBinding        map[string]string `json:"authBinding,omitempty"`
	ConfigBundle       string            `json:"configBundle,omitempty"`
	UseProjectRootPath bool              `json:"useProjectRootPath,omitempty"`
}

type RemoveOptions struct {
	DeleteHostPath bool
}

type Workspace struct {
	ID                string         `json:"id"`
	ProjectID         string         `json:"projectId,omitempty"`
	RepoID            string         `json:"repoId,omitempty"`
	RepoKind          string         `json:"repoKind,omitempty"`
	Repo              string         `json:"repo"`
	Ref               string         `json:"ref"`
	TargetBranch      string         `json:"targetBranch,omitempty"`
	CurrentRef        string         `json:"currentRef,omitempty"`
	CurrentCommit     string         `json:"currentCommit,omitempty"`
	WorkspaceName     string         `json:"workspaceName"`
	AgentProfile      string         `json:"agentProfile"`
	Policy            Policy         `json:"policy"`
	State             WorkspaceState `json:"state"`
	RootPath          string         `json:"rootPath"`
	ParentWorkspaceID string         `json:"parentWorkspaceId,omitempty"`
	LineageRootID     string         `json:"lineageRootId,omitempty"`
	DerivedFromRef    string         `json:"derivedFromRef,omitempty"`
	Backend           string         `json:"backend,omitempty"`
	// RuntimeLabel is a short summary of backend, isolation level, and effective
	// vm/process mode for clients and logs. It is derived from repo workspace.json
	// and not persisted in node storage.
	RuntimeLabel string `json:"runtimeLabel,omitempty"`
	// LineageSnapshotID is the preferred runtime snapshot identifier for
	// creating descendants of this workspace.
	LineageSnapshotID string            `json:"lineageSnapshotId,omitempty"`
	AuthBinding       map[string]string `json:"authBinding,omitempty"`
	// LocalWorktreePath is the path of the git worktree on the host machine
	// that is synced with this workspace inside the sandbox.
	LocalWorktreePath string `json:"localWorktreePath,omitempty"`
	// HostWorkspacePath is the canonical host workspace root
	// (.worktrees/<sanitized-branch>) for this workspace.
	HostWorkspacePath string `json:"hostWorkspacePath,omitempty"`
	// MutagenSessionID is the mutagen sync session name for this workspace.
	// Empty if no sync session has been established.
	MutagenSessionID string `json:"mutagenSessionId,omitempty"`
	// TunnelPorts stores user-selected host ports that should be tunnelable.
	// Tunnels are only activated when this workspace holds the global tunnel lease.
	TunnelPorts []int `json:"tunnelPorts,omitempty"`

	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}
