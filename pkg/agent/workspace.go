package agent

import (
	"encoding/json"
	"fmt"
	"time"
)

// WorkspaceStatus represents the status of a workspace
type WorkspaceStatus string

const (
	WorkspaceStatusCreating     WorkspaceStatus = "creating"
	WorkspaceStatusRunning      WorkspaceStatus = "running"
	WorkspaceStatusStopped      WorkspaceStatus = "stopped"
	WorkspaceStatusError        WorkspaceStatus = "error"
	WorkspaceStatusDeleting     WorkspaceStatus = "deleting"
	WorkspaceStatusInitializing WorkspaceStatus = "initializing"
)

// ServiceStatus represents the status of a service
type ServiceStatus string

const (
	ServiceStatusPending   ServiceStatus = "pending"
	ServiceStatusStarting  ServiceStatus = "starting"
	ServiceStatusRunning   ServiceStatus = "running"
	ServiceStatusUnhealthy ServiceStatus = "unhealthy"
	ServiceStatusStopped   ServiceStatus = "stopped"
	ServiceStatusError     ServiceStatus = "error"
)

// HealthCheckType represents the type of health check
type HealthCheckType string

const (
	HealthCheckHTTP   HealthCheckType = "http"
	HealthCheckTCP    HealthCheckType = "tcp"
	HealthCheckExec   HealthCheckType = "exec"
	HealthCheckCustom HealthCheckType = "custom"
)

// HealthCheck defines how to check if a service is healthy
type HealthCheck struct {
	Type    HealthCheckType `json:"type"`
	Path    string          `json:"path,omitempty"`    // For HTTP checks
	Timeout int             `json:"timeout,omitempty"` // In seconds
	Command string          `json:"command,omitempty"` // For exec checks
	Port    int             `json:"port,omitempty"`    // For TCP checks
	Retries int             `json:"retries,omitempty"` // Number of retries
}

// ServiceDefinition represents a service to be started in the workspace
type ServiceDefinition struct {
	Name        string                 `json:"name"`
	Command     string                 `json:"command"`
	Port        int                    `json:"port"`
	DependsOn   []string               `json:"depends_on,omitempty"`
	Env         map[string]string      `json:"env,omitempty"`
	HealthCheck *HealthCheck           `json:"health_check,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// RepositoryInfo contains repository details
type RepositoryInfo struct {
	Owner  string `json:"owner"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	Branch string `json:"branch"`
}

// CreateWorkspaceCommand represents a request to create a new workspace
type CreateWorkspaceCommand struct {
	ID            string              `json:"id"`
	WorkspaceID   string              `json:"workspace_id"`
	WorkspaceName string              `json:"workspace_name"`
	Provider      string              `json:"provider"`
	Image         string              `json:"image"`
	Repository    RepositoryInfo      `json:"repository"`
	Services      []ServiceDefinition `json:"services"`
	SSH           SSHConfig           `json:"ssh"`
	Resources     ResourceConfig      `json:"resources"`
	CreatedAt     time.Time           `json:"created_at"`
}

// SSHConfig contains SSH configuration for the workspace
type SSHConfig struct {
	Port   int    `json:"port"`
	User   string `json:"user"`
	PubKey string `json:"pub_key"`
}

// ResourceConfig specifies resource limits for the container
type ResourceConfig struct {
	CPU    int    `json:"cpu"`    // CPU cores (1-4)
	Memory string `json:"memory"` // Memory size ("2GB", "4GB", etc.)
	Disk   string `json:"disk"`   // Disk size ("20GB", etc.)
}

// WorkspaceCreateResult represents the result of workspace creation
type WorkspaceCreateResult struct {
	WorkspaceID string          `json:"workspace_id"`
	ContainerID string          `json:"container_id"`
	Status      WorkspaceStatus `json:"status"`
	SSHPort     int             `json:"ssh_port"`
	IPAddress   string          `json:"ip_address,omitempty"`
	Services    map[string]int  `json:"services,omitempty"` // Service name -> port mapping
	Error       string          `json:"error,omitempty"`
	Timestamp   time.Time       `json:"timestamp"`
}

// ServiceStartResult represents the result of starting a service
type ServiceStartResult struct {
	ServiceName string        `json:"service_name"`
	Status      ServiceStatus `json:"status"`
	Port        int           `json:"port,omitempty"`
	Error       string        `json:"error,omitempty"`
	Timestamp   time.Time     `json:"timestamp"`
}

// WorkspaceStatusUpdate represents a status update for a workspace
type WorkspaceStatusUpdate struct {
	WorkspaceID string            `json:"workspace_id"`
	Status      WorkspaceStatus   `json:"status"`
	Message     string            `json:"message,omitempty"`
	Services    map[string]string `json:"services,omitempty"` // Service name -> status
	Error       string            `json:"error,omitempty"`
	Timestamp   time.Time         `json:"timestamp"`
}

// ValidateCreateWorkspaceCommand validates the command structure
func (cmd *CreateWorkspaceCommand) Validate() error {
	if cmd.WorkspaceID == "" {
		return fmt.Errorf("workspace_id is required")
	}
	if cmd.WorkspaceName == "" {
		return fmt.Errorf("workspace_name is required")
	}
	if cmd.Provider == "" {
		return fmt.Errorf("provider is required")
	}
	if cmd.Image == "" {
		return fmt.Errorf("image is required")
	}
	if cmd.Repository.Owner == "" {
		return fmt.Errorf("repository.owner is required")
	}
	if cmd.Repository.Name == "" {
		return fmt.Errorf("repository.name is required")
	}
	if cmd.Repository.URL == "" {
		return fmt.Errorf("repository.url is required")
	}
	if cmd.Repository.Branch == "" {
		return fmt.Errorf("repository.branch is required")
	}
	if cmd.SSH.Port == 0 {
		return fmt.Errorf("ssh.port is required")
	}
	if cmd.SSH.User == "" {
		return fmt.Errorf("ssh.user is required")
	}
	if cmd.SSH.PubKey == "" {
		return fmt.Errorf("ssh.pub_key is required")
	}
	if cmd.Resources.CPU == 0 {
		return fmt.Errorf("resources.cpu is required")
	}
	if cmd.Resources.Memory == "" {
		return fmt.Errorf("resources.memory is required")
	}
	if cmd.Resources.Disk == "" {
		return fmt.Errorf("resources.disk is required")
	}

	// Validate service definitions
	seenNames := make(map[string]bool)
	for _, svc := range cmd.Services {
		if svc.Name == "" {
			return fmt.Errorf("service name is required")
		}
		if seenNames[svc.Name] {
			return fmt.Errorf("duplicate service name: %s", svc.Name)
		}
		seenNames[svc.Name] = true

		if svc.Command == "" {
			return fmt.Errorf("service %s: command is required", svc.Name)
		}
		if svc.Port == 0 {
			return fmt.Errorf("service %s: port is required", svc.Name)
		}

		// Validate depends_on references
		for _, dep := range svc.DependsOn {
			if !seenNames[dep] && dep != svc.Name {
				// Check if it will be defined later
				found := false
				for _, s := range cmd.Services {
					if s.Name == dep {
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("service %s: depends on undefined service %s", svc.Name, dep)
				}
			}
		}
	}

	return nil
}

// MarshalJSON implements json.Marshaler for CreateWorkspaceCommand
func (cmd *CreateWorkspaceCommand) MarshalJSON() ([]byte, error) {
	type Alias CreateWorkspaceCommand
	return json.Marshal(&struct {
		CreatedAt string `json:"created_at"`
		*Alias
	}{
		CreatedAt: cmd.CreatedAt.Format(time.RFC3339),
		Alias:     (*Alias)(cmd),
	})
}

// UnmarshalJSON implements json.Unmarshaler for CreateWorkspaceCommand
func (cmd *CreateWorkspaceCommand) UnmarshalJSON(data []byte) error {
	type Alias CreateWorkspaceCommand
	aux := &struct {
		CreatedAt string `json:"created_at"`
		*Alias
	}{
		Alias: (*Alias)(cmd),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return fmt.Errorf("failed to unmarshal CreateWorkspaceCommand: %w", err)
	}

	if aux.CreatedAt != "" {
		t, err := time.Parse(time.RFC3339, aux.CreatedAt)
		if err != nil {
			return fmt.Errorf("failed to parse created_at timestamp: %w", err)
		}
		cmd.CreatedAt = t
	}

	return nil
}
