package coordination

import (
	"strings"
	"time"
)

// DBUser represents a registered user in the database (distinct from registry.User)
type DBUser struct {
	UserID               string    `json:"user_id"`
	GitHubUsername       string    `json:"github_username"`
	GitHubID             int64     `json:"github_id"`
	SSHPubkey            string    `json:"ssh_pubkey"`
	SSHPubkeyFingerprint string    `json:"ssh_pubkey_fingerprint"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

// DBWorkspace represents an isolated development environment in the database
type DBWorkspace struct {
	WorkspaceID   string    `json:"workspace_id"`
	UserID        string    `json:"user_id"`
	WorkspaceName string    `json:"workspace_name"`
	Status        string    `json:"status"`   // pending, creating, running, stopped, error
	Provider      string    `json:"provider"` // lxc, docker, qemu
	Image         string    `json:"image"`
	SSHPort       *int      `json:"ssh_port,omitempty"`
	SSHHost       *string   `json:"ssh_host,omitempty"`
	NodeID        *string   `json:"node_id,omitempty"`
	RepoOwner     string    `json:"repo_owner"`
	RepoName      string    `json:"repo_name"`
	RepoURL       string    `json:"repo_url"`
	RepoBranch    string    `json:"repo_branch"`
	RepoCommit    *string   `json:"repo_commit,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// DBService represents a service running in a workspace in the database
type DBService struct {
	ServiceID       string     `json:"service_id"`
	WorkspaceID     string     `json:"workspace_id"`
	ServiceName     string     `json:"service_name"`
	Command         string     `json:"command"`
	Port            int        `json:"port"`                 // Internal port
	LocalPort       *int       `json:"local_port,omitempty"` // Mapped port
	Status          string     `json:"status"`               // pending, starting, running, stopped, error
	HealthStatus    string     `json:"health_status"`        // healthy, unhealthy, unknown, timeout
	LastHealthCheck *time.Time `json:"last_health_check,omitempty"`
	DependsOn       []string   `json:"depends_on"` // Service names this depends on
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// ValidationError represents a validation error
type ValidationError struct {
	Field   string
	Message string
}

func (e ValidationError) Error() string {
	return "validation failed for field " + e.Field + ": " + e.Message
}

// Validate validates user data
func (u *DBUser) Validate() error {
	if u.UserID == "" {
		return ValidationError{Field: "user_id", Message: "user_id is required"}
	}
	if u.GitHubUsername == "" {
		return ValidationError{Field: "github_username", Message: "github_username is required"}
	}
	if u.GitHubID == 0 {
		return ValidationError{Field: "github_id", Message: "github_id is required"}
	}
	if u.SSHPubkey == "" {
		return ValidationError{Field: "ssh_pubkey", Message: "ssh_pubkey is required"}
	}
	if u.SSHPubkeyFingerprint == "" {
		return ValidationError{Field: "ssh_pubkey_fingerprint", Message: "ssh_pubkey_fingerprint is required"}
	}
	return nil
}

// Validate validates workspace data
func (w *DBWorkspace) Validate() error {
	if w.WorkspaceID == "" {
		return ValidationError{Field: "workspace_id", Message: "workspace_id is required"}
	}
	if w.UserID == "" {
		return ValidationError{Field: "user_id", Message: "user_id is required"}
	}
	if w.WorkspaceName == "" {
		return ValidationError{Field: "workspace_name", Message: "workspace_name is required"}
	}
	if w.Status == "" {
		return ValidationError{Field: "status", Message: "status is required"}
	}
	if !isValidWorkspaceStatus(w.Status) {
		return ValidationError{Field: "status", Message: "status must be one of: pending, creating, running, stopped, error"}
	}
	if w.Provider == "" {
		return ValidationError{Field: "provider", Message: "provider is required"}
	}
	if w.Image == "" {
		return ValidationError{Field: "image", Message: "image is required"}
	}
	if w.RepoOwner == "" {
		return ValidationError{Field: "repo_owner", Message: "repo_owner is required"}
	}
	if w.RepoName == "" {
		return ValidationError{Field: "repo_name", Message: "repo_name is required"}
	}
	if w.RepoURL == "" {
		return ValidationError{Field: "repo_url", Message: "repo_url is required"}
	}
	return nil
}

// Validate validates service data
func (s *DBService) Validate() error {
	if s.ServiceID == "" {
		return ValidationError{Field: "service_id", Message: "service_id is required"}
	}
	if s.WorkspaceID == "" {
		return ValidationError{Field: "workspace_id", Message: "workspace_id is required"}
	}
	if s.ServiceName == "" {
		return ValidationError{Field: "service_name", Message: "service_name is required"}
	}
	if s.Command == "" {
		return ValidationError{Field: "command", Message: "command is required"}
	}
	if s.Port <= 0 || s.Port > 65535 {
		return ValidationError{Field: "port", Message: "port must be between 1 and 65535"}
	}
	if s.Status == "" {
		return ValidationError{Field: "status", Message: "status is required"}
	}
	if !isValidServiceStatus(s.Status) {
		return ValidationError{Field: "status", Message: "status must be one of: pending, starting, running, stopped, error, unhealthy"}
	}
	return nil
}

func isValidWorkspaceStatus(status string) bool {
	validStatuses := map[string]bool{
		"pending":  true,
		"creating": true,
		"running":  true,
		"stopped":  true,
		"error":    true,
	}
	return validStatuses[status]
}

func isValidServiceStatus(status string) bool {
	validStatuses := map[string]bool{
		"pending":   true,
		"starting":  true,
		"running":   true,
		"stopped":   true,
		"error":     true,
		"unhealthy": true,
	}
	return validStatuses[status]
}

// ParseDependsOn parses comma-separated dependencies
func ParseDependsOn(dependsOnStr string) []string {
	if dependsOnStr == "" {
		return []string{}
	}
	deps := strings.Split(dependsOnStr, ",")
	for i, dep := range deps {
		deps[i] = strings.TrimSpace(dep)
	}
	return deps
}

// StringifyDependsOn converts dependencies to comma-separated string
func StringifyDependsOn(deps []string) string {
	return strings.Join(deps, ", ")
}

// GitHubInstallation represents a GitHub App installation for a user
type GitHubInstallation struct {
	InstallationID int64     `json:"installation_id"`
	UserID         string    `json:"user_id"`
	GitHubUserID   int64     `json:"github_user_id"`
	GitHubUsername string    `json:"github_username"`
	RepoFullName   string    `json:"repo_full_name"`
	Token          string    `json:"token"`
	TokenExpiresAt time.Time `json:"token_expires_at"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// Validate validates GitHub installation data
// For user-based auth (no installation), installation_id can be 0
func (gi *GitHubInstallation) Validate() error {
	if gi.UserID == "" {
		return ValidationError{Field: "user_id", Message: "user_id is required"}
	}
	if gi.GitHubUserID == 0 {
		return ValidationError{Field: "github_user_id", Message: "github_user_id is required"}
	}
	if gi.GitHubUsername == "" {
		return ValidationError{Field: "github_username", Message: "github_username is required"}
	}
	if gi.Token == "" {
		return ValidationError{Field: "token", Message: "token is required"}
	}
	return nil
}

// GitHubFork represents a forked repository for a user
type GitHubFork struct {
	UserID        string    `json:"user_id"`
	OriginalOwner string    `json:"original_owner"`
	OriginalRepo  string    `json:"original_repo"`
	ForkOwner     string    `json:"fork_owner"`
	ForkURL       string    `json:"fork_url"`
	CreatedAt     time.Time `json:"created_at"`
}

// Validate validates GitHub fork data
func (gf *GitHubFork) Validate() error {
	if gf.UserID == "" {
		return ValidationError{Field: "user_id", Message: "user_id is required"}
	}
	if gf.OriginalOwner == "" {
		return ValidationError{Field: "original_owner", Message: "original_owner is required"}
	}
	if gf.OriginalRepo == "" {
		return ValidationError{Field: "original_repo", Message: "original_repo is required"}
	}
	if gf.ForkOwner == "" {
		return ValidationError{Field: "fork_owner", Message: "fork_owner is required"}
	}
	if gf.ForkURL == "" {
		return ValidationError{Field: "fork_url", Message: "fork_url is required"}
	}
	return nil
}
