package runtime

// CreateRequest holds parameters for creating a new workspace runtime.
type CreateRequest struct {
	WorkspaceID   string
	WorkspaceName string
	ProjectRoot   string
	ConfigBundle  string
	Options       map[string]string
}

// Snapshot represents a point-in-time snapshot of a workspace runtime.
type Snapshot struct {
	ID          string
	WorkspaceID string
	Path        string
}
