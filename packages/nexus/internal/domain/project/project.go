package project

import "time"

// ProjectConfig holds per-project configuration.
type ProjectConfig struct {
	DefaultBackend string `json:"defaultBackend,omitempty"`
	DefaultRef     string `json:"defaultRef,omitempty"`
}

// Project is the domain entity representing a tracked repository project.
type Project struct {
	ID        string        `json:"id"`
	Name      string        `json:"name"`
	RepoURL   string        `json:"repoUrl"`
	Config    ProjectConfig `json:"config"`
	CreatedAt time.Time     `json:"created_at,omitempty"`
	UpdatedAt time.Time     `json:"updated_at,omitempty"`
}
