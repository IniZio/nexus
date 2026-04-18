package firecracker

// CreateRequest is the request to create a new workspace.
type CreateRequest struct {
	WorkspaceID string
	ProjectRoot string
	Options     map[string]string
}
