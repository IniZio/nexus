package workspace

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockProvider struct {
	created    []string
	started    []string
	stopped    []string
	destroyed  []string
	executeds  [][]string
	workspaces []WorkspaceInfo
	exists     map[string]bool
}

func (m *mockProvider) Create(ctx context.Context, name string, worktreePath string) error {
	m.created = append(m.created, name)
	return nil
}

func (m *mockProvider) CreateWithDinD(ctx context.Context, name string, worktreePath string) error {
	m.created = append(m.created, name)
	return nil
}

func (m *mockProvider) Start(ctx context.Context, name string) error {
	m.started = append(m.started, name)
	return nil
}

func (m *mockProvider) Stop(ctx context.Context, name string) error {
	m.stopped = append(m.stopped, name)
	return nil
}

func (m *mockProvider) Destroy(ctx context.Context, name string) error {
	m.destroyed = append(m.destroyed, name)
	return nil
}

func (m *mockProvider) Shell(ctx context.Context, name string) error {
	return nil
}

func (m *mockProvider) Exec(ctx context.Context, name string, command []string) error {
	m.executeds = append(m.executeds, command)
	return nil
}

func (m *mockProvider) List(ctx context.Context) ([]WorkspaceInfo, error) {
	return m.workspaces, nil
}

func (m *mockProvider) Close() error {
	return nil
}

func (m *mockProvider) ContainerExists(ctx context.Context, name string) (bool, error) {
	return m.exists[name], nil
}

func (m *mockProvider) StartSync(ctx context.Context, workspaceName, worktreePath string) (string, error) {
	return "sync-session-id", nil
}

func (m *mockProvider) PauseSync(ctx context.Context, workspaceName string) error {
	return nil
}

func (m *mockProvider) ResumeSync(ctx context.Context, workspaceName string) error {
	return nil
}

func (m *mockProvider) StopSync(ctx context.Context, workspaceName string) error {
	return nil
}

func (m *mockProvider) GetSyncStatus(ctx context.Context, workspaceName string) (interface{}, error) {
	return "synced", nil
}

func (m *mockProvider) FlushSync(ctx context.Context, workspaceName string) error {
	return nil
}

func TestManager_validateCreate(t *testing.T) {
	mockProv := &mockProvider{exists: make(map[string]bool)}
	manager := NewManager(mockProv)

	tests := []struct {
		name    string
		wantErr bool
	}{
		{"valid-workspace", false},
		{"workspace-123", false},
		{"my_workspace", false},
		{"", true},
		{"workspace with spaces", true},
		{"workspace@special", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := manager.validateCreate(tt.name)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestManager_Repair(t *testing.T) {
	tests := []struct {
		name           string
		worktreeExists bool
		containerExists bool
		wantErr        bool
		errContains    string
	}{
		{"", false, false, true, "name required"},
		{"healthy-ws", true, true, true, "already healthy"},
		{"missing-ws", false, false, true, "does not exist"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockProv := &mockProvider{
				exists: map[string]bool{tt.name: tt.containerExists},
			}
			manager := &Manager{
				provider: mockProv,
			}

			err := manager.Repair(tt.name)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestManager_Create(t *testing.T) {
	mockProv := &mockProvider{exists: make(map[string]bool)}
	manager := NewManager(mockProv)

	ctx := context.Background()
	err := manager.Create(ctx, "new-workspace", "/tmp/test-worktree")
	
	assert.NoError(t, err)
	assert.Contains(t, mockProv.created, "new-workspace")
}

func TestManager_Start(t *testing.T) {
	mockProv := &mockProvider{exists: map[string]bool{"test-ws": true}}
	manager := NewManager(mockProv)

	ctx := context.Background()
	err := manager.Start(ctx, "test-ws")
	
	assert.NoError(t, err)
	assert.Contains(t, mockProv.started, "test-ws")
}

func TestManager_Stop(t *testing.T) {
	mockProv := &mockProvider{exists: map[string]bool{"test-ws": true}}
	manager := NewManager(mockProv)

	ctx := context.Background()
	err := manager.Stop(ctx, "test-ws")
	
	assert.NoError(t, err)
	assert.Contains(t, mockProv.stopped, "test-ws")
}

func TestManager_Destroy(t *testing.T) {
	mockProv := &mockProvider{exists: map[string]bool{"test-ws": true}}
	manager := NewManager(mockProv)

	ctx := context.Background()
	err := manager.Destroy(ctx, "test-ws")
	
	assert.NoError(t, err)
	assert.Contains(t, mockProv.destroyed, "test-ws")
}

func TestManager_Exec(t *testing.T) {
	mockProv := &mockProvider{exists: map[string]bool{"test-ws": true}}
	manager := NewManager(mockProv)

	ctx := context.Background()
	err := manager.Exec(ctx, "test-ws", []string{"ls", "-la"})
	
	assert.NoError(t, err)
	require.Len(t, mockProv.executeds, 1)
	assert.Equal(t, []string{"ls", "-la"}, mockProv.executeds[0])
}

func TestManager_List(t *testing.T) {
	mockProv := &mockProvider{
		workspaces: []WorkspaceInfo{
			{Name: "ws1", Status: "running", Port: "32800"},
			{Name: "ws2", Status: "stopped", Port: "32801"},
		},
	}
	manager := NewManager(mockProv)

	ctx := context.Background()
	workspaces, err := manager.List(ctx)
	
	assert.NoError(t, err)
	assert.Len(t, workspaces, 2)
}

func TestManager_Status(t *testing.T) {
	mockProv := &mockProvider{
		workspaces: []WorkspaceInfo{
			{Name: "test-ws", Status: "running", Port: "32800"},
		},
	}
	manager := NewManager(mockProv)

	ctx := context.Background()
	status, err := manager.Status(ctx, "test-ws")
	
	assert.NoError(t, err)
	assert.Equal(t, "running", status)
}

func TestManager_StartSync(t *testing.T) {
	mockProv := &mockProvider{exists: map[string]bool{"test-ws": true}}
	manager := NewManager(mockProv)

	ctx := context.Background()
	sessionID, err := manager.StartSync(ctx, "test-ws", "/tmp/worktree")
	
	assert.NoError(t, err)
	assert.Equal(t, "sync-session-id", sessionID)
}

func TestManager_StopSync(t *testing.T) {
	mockProv := &mockProvider{exists: map[string]bool{"test-ws": true}}
	manager := NewManager(mockProv)

	ctx := context.Background()
	err := manager.StopSync(ctx, "test-ws")
	
	assert.NoError(t, err)
}

func TestManager_HealthCheck(t *testing.T) {
	mockProv := &mockProvider{
		exists: map[string]bool{"test-ws": true},
		workspaces: []WorkspaceInfo{
			{Name: "test-ws", Status: "running"},
		},
	}
	manager := NewManager(mockProv)

	ctx := context.Background()
	healthy, err := manager.HealthCheck(ctx, "test-ws")
	
	assert.NoError(t, err)
	assert.True(t, healthy)
}

func TestManager_HealthCheck_Unhealthy(t *testing.T) {
	mockProv := &mockProvider{
		exists: map[string]bool{"test-ws": false},
	}
	manager := NewManager(mockProv)

	ctx := context.Background()
	healthy, err := manager.HealthCheck(ctx, "test-ws")
	
	assert.NoError(t, err)
	assert.False(t, healthy)
}

func TestManager_Logs(t *testing.T) {
	mockProv := &mockProvider{exists: map[string]bool{"test-ws": true}}
	manager := NewManager(mockProv)

	ctx := context.Background()
	logs, err := manager.Logs(ctx, "test-ws", 100)
	
	assert.NoError(t, err)
	assert.NotNil(t, logs)
}

func TestManager_ValidateName(t *testing.T) {
	// Test via validateCreate which contains the name validation logic
	mockProv := &mockProvider{exists: make(map[string]bool)}
	manager := NewManager(mockProv)

	validNames := []string{
		"my-workspace",
		"workspace123",
		"ws_under_score",
		"WS-UPPER",
	}

	invalidNames := []string{
		"",
		"workspace with spaces",
		"workspace@special",
		"workspace!",
	}

	for _, name := range validNames {
		t.Run("valid:"+name, func(t *testing.T) {
			err := manager.validateCreate(name)
			assert.NoError(t, err)
		})
	}

	for _, name := range invalidNames {
		t.Run("invalid:"+name, func(t *testing.T) {
			err := manager.validateCreate(name)
			assert.Error(t, err)
		})
	}
}

func TestManager_CreateTimeout(t *testing.T) {
	slowProvider := &mockProvider{}
	manager := NewManager(slowProvider)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := manager.Create(ctx, "slow-workspace", "/tmp/test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "timeout")
}
