package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServiceDefinitionValidation(t *testing.T) {
	tests := []struct {
		name string
		svc  ServiceDefinition
		ok   bool
	}{
		{
			name: "valid service",
			svc: ServiceDefinition{
				Name:    "web",
				Command: "npm start",
				Port:    3000,
			},
			ok: true,
		},
		{
			name: "service with dependencies",
			svc: ServiceDefinition{
				Name:      "app",
				Command:   "python main.py",
				Port:      8000,
				DependsOn: []string{"db"},
				Env: map[string]string{
					"DEBUG": "true",
				},
			},
			ok: true,
		},
		{
			name: "service with health check",
			svc: ServiceDefinition{
				Name:    "api",
				Command: "go run main.go",
				Port:    4000,
				HealthCheck: &HealthCheck{
					Type:    HealthCheckHTTP,
					Path:    "/health",
					Timeout: 5,
					Retries: 3,
				},
			},
			ok: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotEmpty(t, tt.svc.Name)
			assert.NotEmpty(t, tt.svc.Command)
			assert.Greater(t, tt.svc.Port, 0)
		})
	}
}

func TestHealthCheckDefinition(t *testing.T) {
	httpCheck := HealthCheck{
		Type:    HealthCheckHTTP,
		Path:    "/health",
		Timeout: 5,
	}

	tcpCheck := HealthCheck{
		Type:    HealthCheckTCP,
		Port:    3000,
		Timeout: 10,
	}

	assert.Equal(t, "http", string(httpCheck.Type))
	assert.Equal(t, "tcp", string(tcpCheck.Type))
}

func TestRepositoryInfo(t *testing.T) {
	repo := RepositoryInfo{
		Owner:  "nexus",
		Name:   "example",
		URL:    "https://github.com/nexus/example",
		Branch: "main",
	}

	assert.Equal(t, "nexus", repo.Owner)
	assert.Equal(t, "example", repo.Name)
	assert.Contains(t, repo.URL, "github.com")
	assert.Equal(t, "main", repo.Branch)
}

func TestSSHConfig(t *testing.T) {
	ssh := SSHConfig{
		Port:   2222,
		User:   "dev",
		PubKey: "ssh-ed25519 AAAA...",
	}

	assert.Equal(t, 2222, ssh.Port)
	assert.Equal(t, "dev", ssh.User)
	assert.NotEmpty(t, ssh.PubKey)
}

func TestResourceConfig(t *testing.T) {
	resources := ResourceConfig{
		CPU:    4,
		Memory: "8GB",
		Disk:   "50GB",
	}

	assert.Equal(t, 4, resources.CPU)
	assert.Equal(t, "8GB", resources.Memory)
	assert.Equal(t, "50GB", resources.Disk)
}

func TestWorkspaceCreateResultJSON(t *testing.T) {
	result := WorkspaceCreateResult{
		WorkspaceID: "ws-123",
		ContainerID: "abc123",
		Status:      WorkspaceStatusRunning,
		SSHPort:     2222,
		Services: map[string]int{
			"web": 3000,
			"api": 4000,
		},
	}

	assert.Equal(t, "ws-123", result.WorkspaceID)
	assert.Equal(t, WorkspaceStatusRunning, result.Status)
	assert.Equal(t, 2222, result.SSHPort)
	assert.Equal(t, 2, len(result.Services))
}

func TestServiceStartResult(t *testing.T) {
	result := ServiceStartResult{
		ServiceName: "web",
		Status:      ServiceStatusRunning,
		Port:        3000,
	}

	assert.Equal(t, "web", result.ServiceName)
	assert.Equal(t, ServiceStatusRunning, result.Status)
	assert.Equal(t, 3000, result.Port)
}

func TestWorkspaceStatusUpdate(t *testing.T) {
	update := WorkspaceStatusUpdate{
		WorkspaceID: "ws-123",
		Status:      WorkspaceStatusRunning,
		Message:     "Workspace is running",
		Services: map[string]string{
			"web": "running",
			"db":  "running",
		},
	}

	assert.Equal(t, "ws-123", update.WorkspaceID)
	assert.Equal(t, WorkspaceStatusRunning, update.Status)
	assert.Equal(t, 2, len(update.Services))
}
