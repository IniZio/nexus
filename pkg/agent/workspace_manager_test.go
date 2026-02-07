package agent

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/nexus/nexus/pkg/provider"
)

func createTestWorkspaceManager() *WorkspaceManager {
	// Create workspace manager with empty providers map for testing
	// The tests that need providers should provide mock providers
	return NewWorkspaceManager(make(map[string]provider.Provider))
}

func TestPortAllocation(t *testing.T) {
	t.Run("allocate SSH port", func(t *testing.T) {
		pr := &PortAllocationRange{
			SSHStart:       2222,
			SSHEnd:         2299,
			allocatedPorts: make(map[int]bool),
		}

		port1, err := pr.AllocateSSHPort()
		require.NoError(t, err)
		assert.Equal(t, 2222, port1)
		assert.True(t, pr.allocatedPorts[2222])

		port2, err := pr.AllocateSSHPort()
		require.NoError(t, err)
		assert.Equal(t, 2223, port2)
		assert.NotEqual(t, port1, port2)
	})

	t.Run("release port", func(t *testing.T) {
		pr := &PortAllocationRange{
			SSHStart:       2222,
			SSHEnd:         2299,
			allocatedPorts: make(map[int]bool),
		}

		port1, err := pr.AllocateSSHPort()
		require.NoError(t, err)
		assert.True(t, pr.allocatedPorts[port1])

		pr.ReleasePort(port1)
		assert.False(t, pr.allocatedPorts[port1])

		port2, err := pr.AllocateSSHPort()
		require.NoError(t, err)
		assert.Equal(t, port1, port2)
	})

	t.Run("exhaust SSH ports", func(t *testing.T) {
		pr := &PortAllocationRange{
			SSHStart:       2222,
			SSHEnd:         2223,
			allocatedPorts: make(map[int]bool),
		}

		_, err := pr.AllocateSSHPort()
		require.NoError(t, err)

		_, err = pr.AllocateSSHPort()
		require.NoError(t, err)

		_, err = pr.AllocateSSHPort()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "no available SSH ports")
	})
}

func TestWorkspaceServiceDependencyResolution(t *testing.T) {
	t.Run("linear dependency chain", func(t *testing.T) {
		wm := createTestWorkspaceManager()

		services := []ServiceDefinition{
			{Name: "web", Command: "npm run dev", Port: 3000},
			{Name: "api", Command: "npm run server", Port: 4000, DependsOn: []string{"web"}},
			{Name: "db", Command: "postgres", Port: 5432, DependsOn: []string{"api"}},
		}

		ordered, err := wm.resolveServiceDependencies(services)
		require.NoError(t, err)
		assert.Equal(t, 3, len(ordered))

		assert.Equal(t, "web", ordered[0].Name)
		assert.Equal(t, "api", ordered[1].Name)
		assert.Equal(t, "db", ordered[2].Name)
	})

	t.Run("no dependencies", func(t *testing.T) {
		wm := createTestWorkspaceManager()

		services := []ServiceDefinition{
			{Name: "web", Command: "npm run dev", Port: 3000},
			{Name: "api", Command: "npm run server", Port: 4000},
		}

		ordered, err := wm.resolveServiceDependencies(services)
		require.NoError(t, err)
		assert.Equal(t, 2, len(ordered))
	})

	t.Run("circular dependency detection", func(t *testing.T) {
		wm := createTestWorkspaceManager()

		services := []ServiceDefinition{
			{Name: "a", Command: "cmd", Port: 1000, DependsOn: []string{"b"}},
			{Name: "b", Command: "cmd", Port: 1001, DependsOn: []string{"a"}},
		}

		_, err := wm.resolveServiceDependencies(services)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "circular dependency")
	})

	t.Run("complex dependency graph", func(t *testing.T) {
		wm := createTestWorkspaceManager()

		services := []ServiceDefinition{
			{Name: "db", Command: "postgres", Port: 5432},
			{Name: "cache", Command: "redis", Port: 6379},
			{Name: "api", Command: "node server.js", Port: 4000, DependsOn: []string{"db", "cache"}},
			{Name: "web", Command: "npm run dev", Port: 3000, DependsOn: []string{"api"}},
		}

		ordered, err := wm.resolveServiceDependencies(services)
		require.NoError(t, err)
		assert.Equal(t, 4, len(ordered))

		assert.Equal(t, "db", ordered[0].Name)
		assert.Equal(t, "cache", ordered[1].Name)
		assert.Equal(t, "api", ordered[2].Name)
		assert.Equal(t, "web", ordered[3].Name)
	})

	t.Run("empty service list", func(t *testing.T) {
		wm := createTestWorkspaceManager()

		ordered, err := wm.resolveServiceDependencies([]ServiceDefinition{})
		require.NoError(t, err)
		assert.Equal(t, 0, len(ordered))
	})
}

func TestWorkspaceStatusTracking(t *testing.T) {
	t.Run("get workspace status", func(t *testing.T) {
		wm := createTestWorkspaceManager()

		cmd := &CreateWorkspaceCommand{
			ID:            "cmd-123",
			WorkspaceID:   "ws-test-123",
			WorkspaceName: "test-workspace",
			Provider:      "lxc",
			Image:         "ubuntu:22.04",
			Repository: RepositoryInfo{
				Owner:  "org",
				Name:   "repo",
				URL:    "git@github.com:org/repo.git",
				Branch: "main",
			},
			SSH: SSHConfig{
				Port:   2222,
				User:   "dev",
				PubKey: "ssh-ed25519 AAAA...",
			},
			Resources: ResourceConfig{
				CPU:    2,
				Memory: "4GB",
				Disk:   "20GB",
			},
		}

		workspace := &ManagedWorkspace{
			Command:   cmd,
			Status:    WorkspaceStatusRunning,
			StartedAt: time.Now(),
			Services:  make(map[string]*ManagedService),
			SSHPort:   2222,
		}

		wm.mu.Lock()
		wm.workspaces[cmd.WorkspaceID] = workspace
		wm.mu.Unlock()

		status := wm.GetWorkspaceStatus(cmd.WorkspaceID)
		require.NotNil(t, status)
		assert.Equal(t, cmd.WorkspaceID, status.WorkspaceID)
		assert.Equal(t, WorkspaceStatusRunning, status.Status)
	})

	t.Run("workspace not found", func(t *testing.T) {
		wm := createTestWorkspaceManager()

		status := wm.GetWorkspaceStatus("nonexistent-ws")
		assert.Nil(t, status)
	})

	t.Run("workspace with service statuses", func(t *testing.T) {
		wm := createTestWorkspaceManager()

		cmd := &CreateWorkspaceCommand{
			ID:            "cmd-123",
			WorkspaceID:   "ws-test-123",
			WorkspaceName: "test-workspace",
			Provider:      "lxc",
			Image:         "ubuntu:22.04",
			Repository: RepositoryInfo{
				Owner:  "org",
				Name:   "repo",
				URL:    "git@github.com:org/repo.git",
				Branch: "main",
			},
			SSH: SSHConfig{
				Port:   2222,
				User:   "dev",
				PubKey: "ssh-ed25519 AAAA...",
			},
			Resources: ResourceConfig{
				CPU:    2,
				Memory: "4GB",
				Disk:   "20GB",
			},
		}

		workspace := &ManagedWorkspace{
			Command:   cmd,
			Status:    WorkspaceStatusRunning,
			StartedAt: time.Now(),
			Services: map[string]*ManagedService{
				"web": {
					Definition: ServiceDefinition{Name: "web", Port: 3000},
					Status:     ServiceStatusRunning,
					Port:       3000,
					MappedPort: 23000,
				},
				"api": {
					Definition: ServiceDefinition{Name: "api", Port: 4000},
					Status:     ServiceStatusRunning,
					Port:       4000,
					MappedPort: 23001,
				},
			},
			SSHPort: 2222,
		}

		wm.mu.Lock()
		wm.workspaces[cmd.WorkspaceID] = workspace
		wm.mu.Unlock()

		status := wm.GetWorkspaceStatus(cmd.WorkspaceID)
		require.NotNil(t, status)
		assert.Equal(t, 2, len(status.Services))
		assert.Equal(t, "running", status.Services["web"])
		assert.Equal(t, "running", status.Services["api"])
	})

	t.Run("workspace error status", func(t *testing.T) {
		wm := createTestWorkspaceManager()

		cmd := &CreateWorkspaceCommand{
			ID:            "cmd-123",
			WorkspaceID:   "ws-test-123",
			WorkspaceName: "test-workspace",
			Provider:      "lxc",
			Image:         "ubuntu:22.04",
			Repository: RepositoryInfo{
				Owner:  "org",
				Name:   "repo",
				URL:    "git@github.com:org/repo.git",
				Branch: "main",
			},
			SSH: SSHConfig{
				Port:   2222,
				User:   "dev",
				PubKey: "ssh-ed25519 AAAA...",
			},
			Resources: ResourceConfig{
				CPU:    2,
				Memory: "4GB",
				Disk:   "20GB",
			},
		}

		workspace := &ManagedWorkspace{
			Command:      cmd,
			Status:       WorkspaceStatusError,
			StartedAt:    time.Now(),
			Services:     make(map[string]*ManagedService),
			SSHPort:      2222,
			ErrorMessage: "failed to start container: context deadline exceeded",
		}

		wm.mu.Lock()
		wm.workspaces[cmd.WorkspaceID] = workspace
		wm.mu.Unlock()

		status := wm.GetWorkspaceStatus(cmd.WorkspaceID)
		require.NotNil(t, status)
		assert.Equal(t, WorkspaceStatusError, status.Status)
		assert.Contains(t, status.Message, "failed to start container")
	})
}

func TestWorkspaceValidation(t *testing.T) {
	t.Run("create workspace with invalid command", func(t *testing.T) {
		wm := createTestWorkspaceManager()
		ctx := context.Background()

		cmd := &CreateWorkspaceCommand{
			WorkspaceID: "ws-123",
		}

		_, err := wm.CreateWorkspace(ctx, cmd)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid workspace command")
	})
}

func TestHealthCheckConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		check *HealthCheck
		port  int
	}{
		{
			name: "HTTP health check",
			check: &HealthCheck{
				Type:    HealthCheckHTTP,
				Path:    "/api/health",
				Timeout: 5,
			},
			port: 3000,
		},
		{
			name: "TCP health check",
			check: &HealthCheck{
				Type:    HealthCheckTCP,
				Timeout: 3,
				Retries: 2,
			},
			port: 4000,
		},
		{
			name: "Exec health check",
			check: &HealthCheck{
				Type:    HealthCheckExec,
				Command: "test -f /var/run/service.pid",
				Timeout: 10,
			},
			port: 5000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.NotNil(t, tt.check)
			assert.Equal(t, tt.port > 0, true)
		})
	}
}
