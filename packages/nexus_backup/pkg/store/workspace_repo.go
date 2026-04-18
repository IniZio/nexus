package store

type WorkspaceRepository interface {
	UpsertWorkspaceRow(row WorkspaceRow) error
	DeleteWorkspace(id string) error
	ListWorkspaceRows() ([]WorkspaceRow, error)
}
