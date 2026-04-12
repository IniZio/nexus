package store

import "time"

type ProjectRow struct {
	ID        string
	Payload   []byte
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ProjectRepository interface {
	UpsertProjectRow(row ProjectRow) error
	DeleteProject(id string) error
	ListProjectRows() ([]ProjectRow, error)
	GetProjectRow(id string) (ProjectRow, bool, error)
}
