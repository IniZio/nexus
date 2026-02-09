package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateCreateWorkspaceCommand(t *testing.T) {
	tests := []struct {
		name    string
		cmd     *CreateWorkspaceCommand
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid command",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository: RepositoryInfo{
					Owner:  "owner",
					Name:   "repo",
					URL:    "https://github.com/owner/repo",
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
				Services: []ServiceDefinition{
					{
						Name:    "web",
						Command: "npm start",
						Port:    3000,
					},
				},
			},
			wantErr: false,
		},
		{
			name: "missing workspace id",
			cmd: &CreateWorkspaceCommand{
				WorkspaceName: "test",
			},
			wantErr: true,
			errMsg:  "workspace_id is required",
		},
		{
			name: "missing workspace name",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID: "ws-123",
			},
			wantErr: true,
			errMsg:  "workspace_name is required",
		},
		{
			name: "missing provider",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
			},
			wantErr: true,
			errMsg:  "provider is required",
		},
		{
			name: "missing image",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
			},
			wantErr: true,
			errMsg:  "image is required",
		},
		{
			name: "missing repository owner",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository:    RepositoryInfo{},
			},
			wantErr: true,
			errMsg:  "repository.owner is required",
		},
		{
			name: "missing repository name",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository: RepositoryInfo{
					Owner: "owner",
				},
			},
			wantErr: true,
			errMsg:  "repository.name is required",
		},
		{
			name: "missing repository url",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository: RepositoryInfo{
					Owner: "owner",
					Name:  "repo",
				},
			},
			wantErr: true,
			errMsg:  "repository.url is required",
		},
		{
			name: "missing repository branch",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository: RepositoryInfo{
					Owner: "owner",
					Name:  "repo",
					URL:   "https://github.com/owner/repo",
				},
			},
			wantErr: true,
			errMsg:  "repository.branch is required",
		},
		{
			name: "missing ssh port",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository: RepositoryInfo{
					Owner:  "owner",
					Name:   "repo",
					URL:    "https://github.com/owner/repo",
					Branch: "main",
				},
				SSH: SSHConfig{},
			},
			wantErr: true,
			errMsg:  "ssh.port is required",
		},
		{
			name: "missing ssh user",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository: RepositoryInfo{
					Owner:  "owner",
					Name:   "repo",
					URL:    "https://github.com/owner/repo",
					Branch: "main",
				},
				SSH: SSHConfig{
					Port: 2222,
				},
			},
			wantErr: true,
			errMsg:  "ssh.user is required",
		},
		{
			name: "missing ssh pubkey",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository: RepositoryInfo{
					Owner:  "owner",
					Name:   "repo",
					URL:    "https://github.com/owner/repo",
					Branch: "main",
				},
				SSH: SSHConfig{
					Port: 2222,
					User: "dev",
				},
			},
			wantErr: true,
			errMsg:  "ssh.pub_key is required",
		},
		{
			name: "missing cpu",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository: RepositoryInfo{
					Owner:  "owner",
					Name:   "repo",
					URL:    "https://github.com/owner/repo",
					Branch: "main",
				},
				SSH: SSHConfig{
					Port:   2222,
					User:   "dev",
					PubKey: "ssh-ed25519 AAAA...",
				},
				Resources: ResourceConfig{},
			},
			wantErr: true,
			errMsg:  "resources.cpu is required",
		},
		{
			name: "missing memory",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository: RepositoryInfo{
					Owner:  "owner",
					Name:   "repo",
					URL:    "https://github.com/owner/repo",
					Branch: "main",
				},
				SSH: SSHConfig{
					Port:   2222,
					User:   "dev",
					PubKey: "ssh-ed25519 AAAA...",
				},
				Resources: ResourceConfig{
					CPU: 2,
				},
			},
			wantErr: true,
			errMsg:  "resources.memory is required",
		},
		{
			name: "missing disk",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository: RepositoryInfo{
					Owner:  "owner",
					Name:   "repo",
					URL:    "https://github.com/owner/repo",
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
				},
			},
			wantErr: true,
			errMsg:  "resources.disk is required",
		},
		{
			name: "missing service name",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository: RepositoryInfo{
					Owner:  "owner",
					Name:   "repo",
					URL:    "https://github.com/owner/repo",
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
				Services: []ServiceDefinition{
					{
						Command: "npm start",
						Port:    3000,
					},
				},
			},
			wantErr: true,
			errMsg:  "service name is required",
		},
		{
			name: "missing service command",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository: RepositoryInfo{
					Owner:  "owner",
					Name:   "repo",
					URL:    "https://github.com/owner/repo",
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
				Services: []ServiceDefinition{
					{
						Name: "web",
						Port: 3000,
					},
				},
			},
			wantErr: true,
			errMsg:  "service web: command is required",
		},
		{
			name: "missing service port",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository: RepositoryInfo{
					Owner:  "owner",
					Name:   "repo",
					URL:    "https://github.com/owner/repo",
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
				Services: []ServiceDefinition{
					{
						Name:    "web",
						Command: "npm start",
					},
				},
			},
			wantErr: true,
			errMsg:  "service web: port is required",
		},
		{
			name: "duplicate service name",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository: RepositoryInfo{
					Owner:  "owner",
					Name:   "repo",
					URL:    "https://github.com/owner/repo",
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
				Services: []ServiceDefinition{
					{
						Name:    "web",
						Command: "npm start",
						Port:    3000,
					},
					{
						Name:    "web",
						Command: "npm test",
						Port:    3001,
					},
				},
			},
			wantErr: true,
			errMsg:  "duplicate service name: web",
		},
		{
			name: "undefined dependency",
			cmd: &CreateWorkspaceCommand{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				Provider:      "docker",
				Image:         "ubuntu:22.04",
				Repository: RepositoryInfo{
					Owner:  "owner",
					Name:   "repo",
					URL:    "https://github.com/owner/repo",
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
				Services: []ServiceDefinition{
					{
						Name:      "web",
						Command:   "npm start",
						Port:      3000,
						DependsOn: []string{"db"},
					},
				},
			},
			wantErr: true,
			errMsg:  "depends on undefined service db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cmd.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestWorkspaceStatusConstants(t *testing.T) {
	assert.Equal(t, WorkspaceStatus("creating"), WorkspaceStatusCreating)
	assert.Equal(t, WorkspaceStatus("running"), WorkspaceStatusRunning)
	assert.Equal(t, WorkspaceStatus("stopped"), WorkspaceStatusStopped)
	assert.Equal(t, WorkspaceStatus("error"), WorkspaceStatusError)
	assert.Equal(t, WorkspaceStatus("deleting"), WorkspaceStatusDeleting)
}

func TestServiceStatusConstants(t *testing.T) {
	assert.Equal(t, ServiceStatus("pending"), ServiceStatusPending)
	assert.Equal(t, ServiceStatus("starting"), ServiceStatusStarting)
	assert.Equal(t, ServiceStatus("running"), ServiceStatusRunning)
	assert.Equal(t, ServiceStatus("unhealthy"), ServiceStatusUnhealthy)
	assert.Equal(t, ServiceStatus("stopped"), ServiceStatusStopped)
}

func TestHealthCheckTypeConstants(t *testing.T) {
	assert.Equal(t, HealthCheckType("http"), HealthCheckHTTP)
	assert.Equal(t, HealthCheckType("tcp"), HealthCheckTCP)
	assert.Equal(t, HealthCheckType("exec"), HealthCheckExec)
	assert.Equal(t, HealthCheckType("custom"), HealthCheckCustom)
}
