package projectmgr

import "time"

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	PrimaryRepo string    `json:"primaryRepo"`
	RepoIDs     []string  `json:"repoIds"`
	RootPath    string    `json:"rootPath"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
