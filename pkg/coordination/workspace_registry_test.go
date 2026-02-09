package coordination

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewInMemoryWorkspaceRegistry(t *testing.T) {
	reg := NewInMemoryWorkspaceRegistry()
	assert.NotNil(t, reg)
}

func TestWorkspaceRegistryCreate(t *testing.T) {
	reg := NewInMemoryWorkspaceRegistry()

	tests := []struct {
		name    string
		ws      *DBWorkspace
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid workspace",
			ws: &DBWorkspace{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test",
				UserID:        "user-1",
				Status:        "active",
			},
			wantErr: false,
		},
		{
			name: "empty workspace id",
			ws: &DBWorkspace{
				WorkspaceID:   "",
				WorkspaceName: "test",
			},
			wantErr: true,
			errMsg:  "workspace ID cannot be empty",
		},
		{
			name: "duplicate workspace",
			ws: &DBWorkspace{
				WorkspaceID:   "ws-123",
				WorkspaceName: "test2",
			},
			wantErr: true,
			errMsg:  "workspace already exists",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reg.Create(tt.ws)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestWorkspaceRegistryGet(t *testing.T) {
	reg := NewInMemoryWorkspaceRegistry()

	ws := &DBWorkspace{
		WorkspaceID:   "ws-123",
		WorkspaceName: "test",
		UserID:        "user-1",
	}
	reg.Create(ws)

	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{
			name:    "existing workspace",
			id:      "ws-123",
			wantErr: false,
		},
		{
			name:    "nonexistent workspace",
			id:      "nonexistent",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, err := reg.Get(tt.id)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, ws)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, ws)
				assert.Equal(t, tt.id, ws.WorkspaceID)
			}
		})
	}
}

func TestWorkspaceRegistryGetByUserAndName(t *testing.T) {
	reg := NewInMemoryWorkspaceRegistry()

	ws := &DBWorkspace{
		WorkspaceID:   "ws-123",
		WorkspaceName: "test",
		UserID:        "user-1",
	}
	reg.Create(ws)

	tests := []struct {
		name    string
		userID  string
		wsName  string
		wantErr bool
	}{
		{
			name:    "existing workspace",
			userID:  "user-1",
			wsName:  "test",
			wantErr: false,
		},
		{
			name:    "wrong user",
			userID:  "user-2",
			wsName:  "test",
			wantErr: true,
		},
		{
			name:    "wrong name",
			userID:  "user-1",
			wsName:  "other",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws, err := reg.GetByUserAndName(tt.userID, tt.wsName)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, ws)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, ws)
			}
		})
	}
}

func TestWorkspaceRegistryList(t *testing.T) {
	reg := NewInMemoryWorkspaceRegistry()

	ws1 := &DBWorkspace{WorkspaceID: "ws-1", WorkspaceName: "test1", UserID: "user-1"}
	ws2 := &DBWorkspace{WorkspaceID: "ws-2", WorkspaceName: "test2", UserID: "user-1"}

	reg.Create(ws1)
	reg.Create(ws2)

	workspaces, err := reg.List()
	assert.NoError(t, err)
	assert.Equal(t, 2, len(workspaces))
}

func TestWorkspaceRegistryListByUser(t *testing.T) {
	reg := NewInMemoryWorkspaceRegistry()

	ws1 := &DBWorkspace{WorkspaceID: "ws-1", WorkspaceName: "test1", UserID: "user-1"}
	ws2 := &DBWorkspace{WorkspaceID: "ws-2", WorkspaceName: "test2", UserID: "user-1"}
	ws3 := &DBWorkspace{WorkspaceID: "ws-3", WorkspaceName: "test3", UserID: "user-2"}

	reg.Create(ws1)
	reg.Create(ws2)
	reg.Create(ws3)

	workspaces, err := reg.ListByUser("user-1")
	assert.NoError(t, err)
	assert.Equal(t, 2, len(workspaces))

	workspaces, err = reg.ListByUser("user-2")
	assert.NoError(t, err)
	assert.Equal(t, 1, len(workspaces))
}

func TestWorkspaceRegistryUpdate(t *testing.T) {
	reg := NewInMemoryWorkspaceRegistry()

	ws := &DBWorkspace{
		WorkspaceID:   "ws-1",
		WorkspaceName: "test",
		UserID:        "user-1",
		Status:        "creating",
	}
	reg.Create(ws)

	err := reg.Update("ws-1", map[string]interface{}{
		"status": "running",
	})
	assert.NoError(t, err)

	updated, _ := reg.Get("ws-1")
	assert.Equal(t, "running", updated.Status)
}

func TestWorkspaceRegistryUpdateStatus(t *testing.T) {
	reg := NewInMemoryWorkspaceRegistry()

	ws := &DBWorkspace{
		WorkspaceID:   "ws-1",
		WorkspaceName: "test",
		Status:        "creating",
	}
	reg.Create(ws)

	err := reg.UpdateStatus("ws-1", "running")
	assert.NoError(t, err)

	updated, _ := reg.Get("ws-1")
	assert.Equal(t, "running", updated.Status)
}

func TestWorkspaceRegistryUpdateSSHPort(t *testing.T) {
	reg := NewInMemoryWorkspaceRegistry()

	ws := &DBWorkspace{
		WorkspaceID:   "ws-1",
		WorkspaceName: "test",
	}
	reg.Create(ws)

	err := reg.UpdateSSHPort("ws-1", 2222, "localhost")
	assert.NoError(t, err)

	updated, _ := reg.Get("ws-1")
	assert.NotNil(t, updated.SSHPort)
	assert.Equal(t, 2222, *updated.SSHPort)
	assert.NotNil(t, updated.SSHHost)
	assert.Equal(t, "localhost", *updated.SSHHost)
}

func TestWorkspaceRegistryDelete(t *testing.T) {
	reg := NewInMemoryWorkspaceRegistry()

	ws := &DBWorkspace{
		WorkspaceID:   "ws-1",
		WorkspaceName: "test",
	}
	reg.Create(ws)

	err := reg.Delete("ws-1")
	assert.NoError(t, err)

	_, err = reg.Get("ws-1")
	assert.Error(t, err)
}

func TestWorkspaceRegistryUpdateNotFound(t *testing.T) {
	reg := NewInMemoryWorkspaceRegistry()

	err := reg.Update("nonexistent", map[string]interface{}{
		"status": "running",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "workspace not found")
}

func TestWorkspaceRegistryUpdateSSHPortNotFound(t *testing.T) {
	reg := NewInMemoryWorkspaceRegistry()

	err := reg.UpdateSSHPort("nonexistent", 2222, "localhost")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "workspace not found")
}
