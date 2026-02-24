package daytona

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/nexus/nexus/packages/nexusd/internal/types"
)

type mockStateStore struct {
	mappings  map[string]string
	saveErr   error
	getErr    error
	deleteErr error
}

func newMockStateStore() *mockStateStore {
	return &mockStateStore{
		mappings: make(map[string]string),
	}
}

func (m *mockStateStore) SaveDaytonaMapping(nexusID, daytonaID string) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.mappings[nexusID] = daytonaID
	return nil
}

func (m *mockStateStore) GetDaytonaMapping(nexusID string) (string, error) {
	if m.getErr != nil {
		return "", m.getErr
	}
	if id, ok := m.mappings[nexusID]; ok {
		return id, nil
	}
	return "", errors.New("mapping not found")
}

func (m *mockStateStore) DeleteDaytonaMapping(nexusID string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	delete(m.mappings, nexusID)
	return nil
}

func TestIDMappingPersistence(t *testing.T) {
	store := newMockStateStore()
	backend := &DaytonaBackend{
		client:     &Client{},
		idMapping:  make(map[string]string),
		stateStore: store,
	}

	backend.setDaytonaID("nexus-ws-1", "daytona-ws-1")

	if id := backend.idMapping["nexus-ws-1"]; id != "daytona-ws-1" {
		t.Errorf("expected daytona-ws-1, got %s", id)
	}

	if store.mappings["nexus-ws-1"] != "daytona-ws-1" {
		t.Error("mapping not persisted to state store")
	}
}

func TestIDMappingLookup(t *testing.T) {
	store := newMockStateStore()
	store.mappings["nexus-ws-1"] = "daytona-ws-1"

	backend := &DaytonaBackend{
		client:     &Client{},
		idMapping:  make(map[string]string),
		stateStore: store,
	}

	daytonaID, err := backend.getDaytonaID("nexus-ws-1")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if daytonaID != "daytona-ws-1" {
		t.Errorf("expected daytona-ws-1, got %s", daytonaID)
	}
}

func TestIDMappingLookupNotFound(t *testing.T) {
	store := newMockStateStore()
	backend := &DaytonaBackend{
		client:     &Client{},
		idMapping:  make(map[string]string),
		stateStore: store,
	}

	_, err := backend.getDaytonaID("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent workspace")
	}
}

func TestIDMappingRemove(t *testing.T) {
	store := newMockStateStore()
	backend := &DaytonaBackend{
		client:     &Client{},
		idMapping:  make(map[string]string),
		stateStore: store,
	}

	backend.setDaytonaID("nexus-ws-1", "daytona-ws-1")
	backend.removeDaytonaID("nexus-ws-1")

	if _, ok := backend.idMapping["nexus-ws-1"]; ok {
		t.Error("mapping should be removed from in-memory cache")
	}

	if _, ok := store.mappings["nexus-ws-1"]; ok {
		t.Error("mapping should be removed from state store")
	}
}

func TestMapSandboxState(t *testing.T) {
	tests := []struct {
		state      string
		wantStatus types.WorkspaceStatus
	}{
		{"creating", types.StatusCreating},
		{"pending", types.StatusCreating},
		{"started", types.StatusRunning},
		{"running", types.StatusRunning},
		{"stopped", types.StatusStopped},
		{"error", types.StatusError},
		{"unknown", types.StatusUnknown},
		{"", types.StatusUnknown},
	}

	for _, tt := range tests {
		status := mapSandboxState(tt.state)
		if status != tt.wantStatus {
			t.Errorf("mapSandboxState(%q) = %v, want %v", tt.state, status, tt.wantStatus)
		}
	}
}

func TestGetResourcesForClass(t *testing.T) {
	tests := []struct {
		class    string
		wantCPU  int
		wantMem  int
		wantDisk int
	}{
		{"small", 1, 1, 3},
		{"medium", 2, 4, 20},
		{"large", 4, 8, 40},
		{"unknown", 1, 1, 3},
		{"", 1, 1, 3},
	}

	for _, tt := range tests {
		r := getResourcesForClass(tt.class)
		if r.CPU != tt.wantCPU {
			t.Errorf("getResourcesForClass(%q).CPU = %d, want %d", tt.class, r.CPU, tt.wantCPU)
		}
		if r.Memory != tt.wantMem {
			t.Errorf("getResourcesForClass(%q).Memory = %d, want %d", tt.class, r.Memory, tt.wantMem)
		}
		if r.Disk != tt.wantDisk {
			t.Errorf("getResourcesForClass(%q).Disk = %d, want %d", tt.class, r.Disk, tt.wantDisk)
		}
	}
}

func TestMapIdleTimeout(t *testing.T) {
	backend := &DaytonaBackend{}

	tests := []struct {
		config    *types.WorkspaceConfig
		wantValue int
	}{
		{nil, 15},
		{&types.WorkspaceConfig{}, 15},
		{&types.WorkspaceConfig{IdleTimeout: 0}, 15},
		{&types.WorkspaceConfig{IdleTimeout: 30}, 30},
		{&types.WorkspaceConfig{IdleTimeout: 60}, 60},
	}

	for _, tt := range tests {
		got := backend.mapIdleTimeout(tt.config)
		if got != tt.wantValue {
			t.Errorf("mapIdleTimeout(%v) = %d, want %d", tt.config, got, tt.wantValue)
		}
	}
}

func TestMapResources(t *testing.T) {
	backend := &DaytonaBackend{}

	t.Run("with resource class", func(t *testing.T) {
		req := &types.CreateWorkspaceRequest{
			ResourceClass: "large",
		}
		r := backend.mapResources(req)
		if r.CPU != 4 {
			t.Errorf("expected CPU 4, got %d", r.CPU)
		}
	})

	t.Run("with empty resource class", func(t *testing.T) {
		req := &types.CreateWorkspaceRequest{
			ResourceClass: "",
		}
		r := backend.mapResources(req)
		if r.CPU != 1 {
			t.Errorf("expected default CPU 1, got %d", r.CPU)
		}
	})

	t.Run("with nil config", func(t *testing.T) {
		req := &types.CreateWorkspaceRequest{}
		r := backend.mapResources(req)
		if r.CPU != 1 {
			t.Errorf("expected default CPU 1, got %d", r.CPU)
		}
	})
}

func TestAllocatePort(t *testing.T) {
	backend := &DaytonaBackend{}

	port, err := backend.AllocatePort()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if port != 0 {
		t.Errorf("expected port 0 for Daytona backend, got %d", port)
	}
}

func TestReleasePort(t *testing.T) {
	backend := &DaytonaBackend{}

	err := backend.ReleasePort(32800)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestNotImplementedMethods(t *testing.T) {
	backend := &DaytonaBackend{}
	ctx := context.Background()

	methods := []struct {
		name string
		fn   func() error
	}{
		{"GetLogs", func() error {
			_, err := backend.GetLogs(ctx, "test", 10)
			return err
		}},
		{"CopyFiles", func() error {
			return backend.CopyFiles(ctx, "test", io.Reader(nil), "/dst")
		}},
		{"CommitContainer", func() error {
			return backend.CommitContainer(ctx, "test", &types.CommitContainerRequest{})
		}},
		{"RemoveImage", func() error {
			return backend.RemoveImage(ctx, "test-image")
		}},
		{"RestoreFromImage", func() error {
			return backend.RestoreFromImage(ctx, "test", "image")
		}},
	}

	for _, m := range methods {
		t.Run(m.name, func(t *testing.T) {
			err := m.fn()
			if err == nil {
				t.Error("expected error for unimplemented method")
			}
		})
	}
}

func TestSyncMethods(t *testing.T) {
	backend := &DaytonaBackend{}
	ctx := context.Background()

	t.Run("PauseSync returns nil", func(t *testing.T) {
		err := backend.PauseSync(ctx, "test-ws")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("ResumeSync returns nil", func(t *testing.T) {
		err := backend.ResumeSync(ctx, "test-ws")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("FlushSync returns nil", func(t *testing.T) {
		err := backend.FlushSync(ctx, "test-ws")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("GetSyncStatus returns unknown state", func(t *testing.T) {
		status, err := backend.GetSyncStatus(ctx, "test-ws")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if status.State != "unknown" {
			t.Errorf("expected state 'unknown', got %q", status.State)
		}
	})
}

func TestAddPortBinding(t *testing.T) {
	backend := &DaytonaBackend{}
	ctx := context.Background()

	err := backend.AddPortBinding(ctx, "test-ws", 8080, 32800)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGetStatusWithNoMapping(t *testing.T) {
	backend := &DaytonaBackend{}

	_, err := backend.GetStatus(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for workspace with no mapping")
	}
}

func TestGetWorkspaceStatus(t *testing.T) {
	backend := &DaytonaBackend{}

	_, err := backend.GetWorkspaceStatus(context.Background(), "nonexistent")
	if err == nil {
		t.Error("expected error for workspace with no mapping")
	}
}
