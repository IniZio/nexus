package workspace

import (
	"context"
	"testing"
	"time"
)

// TestWorkspaceManager_CreateWorkspace tests creating workspaces.
func TestWorkspaceManager_CreateWorkspace(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	req := &CreateWorkspaceRequest{
		Name:        "test-workspace",
		Description: "Test workspace",
		Project:     "test-project",
		Owner:       "test-user",
		Labels:      map[string]string{"env": "test"},
		Tags:        []string{"test"},
	}

	ws, err := manager.CreateWorkspace(context.Background(), req)
	if err != nil {
		t.Fatalf("CreateWorkspace() error = %v", err)
	}

	if ws.ID == "" {
		t.Error("Workspace ID should be set")
	}
	if ws.Name != "test-workspace" {
		t.Errorf("Workspace name = %v, want test-workspace", ws.Name)
	}
	if ws.State != StatePending {
		t.Errorf("Workspace state = %v, want pending", ws.State)
	}
	if ws.Project != "test-project" {
		t.Errorf("Workspace project = %v, want test-project", ws.Project)
	}
}

// TestWorkspaceManager_GetWorkspace tests retrieving workspaces.
func TestWorkspaceManager_GetWorkspace(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	req := &CreateWorkspaceRequest{
		Name:   "test-ws",
		Project: "test",
		Owner:  "user",
	}

	ws, _ := manager.CreateWorkspace(context.Background(), req)

	// Test getting existing workspace
	got, err := manager.GetWorkspace(ws.ID)
	if err != nil {
		t.Errorf("GetWorkspace() error = %v", err)
		return
	}
	if got.ID != ws.ID {
		t.Errorf("GetWorkspace() ID = %v, want %v", got.ID, ws.ID)
	}

	// Test getting non-existent workspace
	_, err = manager.GetWorkspace("nonexistent")
	if err == nil {
		t.Error("GetWorkspace() should return error for non-existent workspace")
	}
}

// TestWorkspaceManager_ListWorkspaces tests listing workspaces.
func TestWorkspaceManager_ListWorkspaces(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	// Create multiple workspaces
	for i := 0; i < 5; i++ {
		req := &CreateWorkspaceRequest{
			Name:   "test-ws",
			Project: "test",
			Owner:  "user",
		}
		manager.CreateWorkspace(context.Background(), req)
	}

	workspaces := manager.ListWorkspaces()
	if len(workspaces) != 5 {
		t.Errorf("ListWorkspaces() count = %v, want 5", len(workspaces))
	}
}

// TestWorkspaceManager_ListWorkspacesByProject tests filtering by project.
func TestWorkspaceManager_ListWorkspacesByProject(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	// Create workspaces for different projects
	projects := []string{"proj-a", "proj-a", "proj-b", "proj-c"}
	for _, proj := range projects {
		req := &CreateWorkspaceRequest{
			Name:   "test-ws",
			Project: proj,
			Owner:  "user",
		}
		manager.CreateWorkspace(context.Background(), req)
	}

	workspaces := manager.ListWorkspacesByProject("proj-a")
	if len(workspaces) != 2 {
		t.Errorf("ListWorkspacesByProject() count = %v, want 2", len(workspaces))
	}

	workspaces = manager.ListWorkspacesByProject("proj-b")
	if len(workspaces) != 1 {
		t.Errorf("ListWorkspacesByProject() count = %v, want 1", len(workspaces))
	}

	workspaces = manager.ListWorkspacesByProject("nonexistent")
	if len(workspaces) != 0 {
		t.Errorf("ListWorkspacesByProject() count = %v, want 0", len(workspaces))
	}
}

// TestWorkspaceManager_ListWorkspacesByOwner tests filtering by owner.
func TestWorkspaceManager_ListWorkspacesByOwner(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	// Create workspaces for different owners
	owners := []string{"alice", "alice", "bob", "charlie"}
	for _, owner := range owners {
		req := &CreateWorkspaceRequest{
			Name:   "test-ws",
			Project: "test",
			Owner:  owner,
		}
		manager.CreateWorkspace(context.Background(), req)
	}

	workspaces := manager.ListWorkspacesByOwner("alice")
	if len(workspaces) != 2 {
		t.Errorf("ListWorkspacesByOwner() count = %v, want 2", len(workspaces))
	}

	workspaces = manager.ListWorkspacesByOwner("bob")
	if len(workspaces) != 1 {
		t.Errorf("ListWorkspacesByOwner() count = %v, want 1", len(workspaces))
	}
}

// TestWorkspaceManager_UpdateWorkspace tests updating workspaces.
func TestWorkspaceManager_UpdateWorkspace(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	req := &CreateWorkspaceRequest{
		Name:   "original-name",
		Project: "test",
		Owner:  "user",
	}
	ws, _ := manager.CreateWorkspace(context.Background(), req)

	// Update the workspace
	err := manager.UpdateWorkspace(ws.ID, map[string]interface{}{
		"name":        "updated-name",
		"description": "updated description",
		"labels":      map[string]string{"env": "updated"},
	})

	if err != nil {
		t.Errorf("UpdateWorkspace() error = %v", err)
		return
	}

	got, _ := manager.GetWorkspace(ws.ID)
	if got.Name != "updated-name" {
		t.Errorf("UpdateWorkspace() name = %v, want updated-name", got.Name)
	}
	if got.Description != "updated description" {
		t.Errorf("UpdateWorkspace() description = %v, want updated description", got.Description)
	}
}

// TestWorkspaceManager_StartWorkspace tests starting workspaces.
func TestWorkspaceManager_StartWorkspace(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	// Set up a no-op start hook
	manager.StartHook = func(ctx context.Context, ws *Workspace) error {
		return nil
	}

	req := &CreateWorkspaceRequest{
		Name:   "test-ws",
		Project: "test",
		Owner:  "user",
	}
	ws, _ := manager.CreateWorkspace(context.Background(), req)

	// Start the workspace
	err := manager.StartWorkspace(context.Background(), ws.ID)
	if err != nil {
		t.Errorf("StartWorkspace() error = %v", err)
		return
	}

	got, _ := manager.GetWorkspace(ws.ID)
	if got.State != StateRunning {
		t.Errorf("StartWorkspace() state = %v, want running", got.State)
	}

	// Test starting already running workspace
	err = manager.StartWorkspace(context.Background(), ws.ID)
	if err == nil {
		t.Error("StartWorkspace() should return error for already running workspace")
	}
}

// TestWorkspaceManager_StopWorkspace tests stopping workspaces.
func TestWorkspaceManager_StopWorkspace(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	manager.StartHook = func(ctx context.Context, ws *Workspace) error {
		return nil
	}
	manager.StopHook = func(ctx context.Context, ws *Workspace) error {
		return nil
	}

	req := &CreateWorkspaceRequest{
		Name:   "test-ws",
		Project: "test",
		Owner:  "user",
	}
	ws, _ := manager.CreateWorkspace(context.Background(), req)
	manager.StartWorkspace(context.Background(), ws.ID)

	// Stop the workspace
	err := manager.StopWorkspace(context.Background(), ws.ID)
	if err != nil {
		t.Errorf("StopWorkspace() error = %v", err)
		return
	}

	got, _ := manager.GetWorkspace(ws.ID)
	if got.State != StateStopped {
		t.Errorf("StopWorkspace() state = %v, want stopped", got.State)
	}
}

// TestWorkspaceManager_DeleteWorkspace tests deleting workspaces.
func TestWorkspaceManager_DeleteWorkspace(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	manager.DeleteHook = func(ctx context.Context, ws *Workspace) error {
		return nil
	}

	req := &CreateWorkspaceRequest{
		Name:   "test-ws",
		Project: "test",
		Owner:  "user",
	}
	ws, _ := manager.CreateWorkspace(context.Background(), req)

	// Delete the workspace
	err := manager.DeleteWorkspace(context.Background(), ws.ID)
	if err != nil {
		t.Errorf("DeleteWorkspace() error = %v", err)
		return
	}

	// Verify it's gone
	_, err = manager.GetWorkspace(ws.ID)
	if err == nil {
		t.Error("DeleteWorkspace() should remove the workspace")
	}
}

// TestWorkspaceManager_ArchiveWorkspace tests archiving workspaces.
func TestWorkspaceManager_ArchiveWorkspace(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	req := &CreateWorkspaceRequest{
		Name:   "test-ws",
		Project: "test",
		Owner:  "user",
	}
	ws, _ := manager.CreateWorkspace(context.Background(), req)

	err := manager.ArchiveWorkspace(ws.ID)
	if err != nil {
		t.Errorf("ArchiveWorkspace() error = %v", err)
		return
	}

	got, _ := manager.GetWorkspace(ws.ID)
	if got.State != StateArchived {
		t.Errorf("ArchiveWorkspace() state = %v, want archived", got.State)
	}
}

// TestWorkspaceManager_GetWorkspaceEvents tests event history.
func TestWorkspaceManager_GetWorkspaceEvents(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	req := &CreateWorkspaceRequest{
		Name:   "test-ws",
		Project: "test",
		Owner:  "user",
	}
	ws, _ := manager.CreateWorkspace(context.Background(), req)

	// Get events
	events := manager.GetWorkspaceEvents(ws.ID)
	if len(events) == 0 {
		t.Error("GetWorkspaceEvents() should return at least one event")
	}

	// Verify event structure
	if len(events) > 0 {
		event := events[0]
		if event.WorkspaceID != ws.ID {
			t.Errorf("Event workspace ID = %v, want %v", event.WorkspaceID, ws.ID)
		}
		if event.Type == "" {
			t.Error("Event type should be set")
		}
	}
}

// TestWorkspaceManager_RegisterTemplate tests template registration.
func TestWorkspaceManager_RegisterTemplate(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	template := &WorkspaceTemplate{
		Name:        "test-template",
		Description: "Test template",
		Category:    "development",
		Config:      &WorkspaceConfig{},
		Resources:   &ResourceQuota{},
		IsPublic:   true,
	}

	err := manager.RegisterTemplate(template)
	if err != nil {
		t.Errorf("RegisterTemplate() error = %v", err)
		return
	}

	if template.ID == "" {
		t.Error("Template ID should be set")
	}

	// Retrieve template
	got, err := manager.GetTemplate(template.ID)
	if err != nil {
		t.Errorf("GetTemplate() error = %v", err)
		return
	}
	if got.Name != "test-template" {
		t.Errorf("Template name = %v, want test-template", got.Name)
	}
}

// TestWorkspaceManager_ListTemplates tests listing templates.
func TestWorkspaceManager_ListTemplates(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	// Register templates
	for i := 0; i < 3; i++ {
		template := &WorkspaceTemplate{
			Name:     "test-template",
			Category: "development",
		}
		manager.RegisterTemplate(template)
	}

	templates := manager.ListTemplates()
	if len(templates) != 3 {
		t.Errorf("ListTemplates() count = %v, want 3", len(templates))
	}
}

// TestWorkspaceManager_GetStats tests statistics.
func TestWorkspaceManager_GetStats(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	manager.StartHook = func(ctx context.Context, ws *Workspace) error {
		return nil
	}

	// Create and start some workspaces
	for i := 0; i < 3; i++ {
		req := &CreateWorkspaceRequest{
			Name:   "running-ws",
			Project: "test",
			Owner:  "user",
		}
		ws, _ := manager.CreateWorkspace(context.Background(), req)
		if i < 2 {
			manager.StartWorkspace(context.Background(), ws.ID)
		}
	}

	// Create stopped workspaces
	for i := 0; i < 2; i++ {
		req := &CreateWorkspaceRequest{
			Name:   "stopped-ws",
			Project: "test",
			Owner:  "user",
		}
		ws, _ := manager.CreateWorkspace(context.Background(), req)
		manager.StartWorkspace(context.Background(), ws.ID)
		manager.StopWorkspace(context.Background(), ws.ID)
	}

	stats := manager.GetStats()

	if stats.Total != 5 {
		t.Errorf("Stats total = %v, want 5", stats.Total)
	}
	if stats.Running != 2 {
		t.Errorf("Stats running = %v, want 2", stats.Running)
	}
	if stats.Stopped != 2 {
		t.Errorf("Stats stopped = %v, want 2", stats.Stopped)
	}
}

// TestWorkspaceManager_UpdateUsage tests resource usage updates.
func TestWorkspaceManager_UpdateUsage(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	req := &CreateWorkspaceRequest{
		Name:   "test-ws",
		Project: "test",
		Owner:  "user",
	}
	ws, _ := manager.CreateWorkspace(context.Background(), req)

	usage := &ResourceUsage{
		CPUUsage:    0.5,
		MemoryBytes: 1024 * 1024 * 1024, // 1GB
		UpdatedAt:   time.Now(),
	}

	err := manager.UpdateUsage(ws.ID, usage)
	if err != nil {
		t.Errorf("UpdateUsage() error = %v", err)
		return
	}

	got, _ := manager.GetWorkspace(ws.ID)
	if got.Usage.CPUUsage != 0.5 {
		t.Errorf("UpdateUsage() CPU = %v, want 0.5", got.Usage.CPUUsage)
	}
}

// TestWorkspaceManager_CleanupExpired tests expiration cleanup.
func TestWorkspaceManager_CleanupExpired(t *testing.T) {
	manager := NewWorkspaceManager(nil)

	// Create workspace with short TTL
	req := &CreateWorkspaceRequest{
		Name:       "expired-ws",
		Project:    "test",
		Owner:      "user",
		TTL:        -1 * time.Hour, // Already expired
	}
	ws, _ := manager.CreateWorkspace(context.Background(), req)

	// Create non-expired workspace
	req2 := &CreateWorkspaceRequest{
		Name:   "valid-ws",
		Project: "test",
		Owner:  "user",
		TTL:    24 * time.Hour,
	}
	manager.CreateWorkspace(context.Background(), req2)

	// Cleanup
	err := manager.CleanupExpired(context.Background())
	if err != nil {
		t.Errorf("CleanupExpired() error = %v", err)
	}

	// Verify expired workspace is gone
	_, err = manager.GetWorkspace(ws.ID)
	if err == nil {
		t.Error("CleanupExpired() should remove expired workspace")
	}
}

// TestResourceComparator tests resource comparison.
func TestResourceComparator(t *testing.T) {
	comparator := &ResourceComparator{}

	tests := []struct {
		name     string
		usage    *ResourceUsage
		quota    *ResourceQuota
		exceeded []ResourceType
	}{
		{
			name: "within quota",
			usage: &ResourceUsage{
				CPUUsage:    1.0,
				MemoryBytes: 2 * 1024 * 1024 * 1024,
			},
			quota: &ResourceQuota{
				CPUCores:    2.0,
				MemoryBytes: 4 * 1024 * 1024 * 1024,
			},
			exceeded: nil,
		},
		{
			name: "exceeds CPU",
			usage: &ResourceUsage{
				CPUUsage:    3.0,
				MemoryBytes: 2 * 1024 * 1024 * 1024,
			},
			quota: &ResourceQuota{
				CPUCores:    2.0,
				MemoryBytes: 4 * 1024 * 1024 * 1024,
			},
			exceeded: []ResourceType{ResourceCPU},
		},
		{
			name: "exceeds memory",
			usage: &ResourceUsage{
				CPUUsage:    1.0,
				MemoryBytes: 5 * 1024 * 1024 * 1024,
			},
			quota: &ResourceQuota{
				CPUCores:    2.0,
				MemoryBytes: 4 * 1024 * 1024 * 1024,
			},
			exceeded: []ResourceType{ResourceMemory},
		},
		{
			name: "exceeds multiple resources",
			usage: &ResourceUsage{
				CPUUsage:    3.0,
				MemoryBytes: 5 * 1024 * 1024 * 1024,
			},
			quota: &ResourceQuota{
				CPUCores:    2.0,
				MemoryBytes: 4 * 1024 * 1024 * 1024,
			},
			exceeded: []ResourceType{ResourceCPU, ResourceMemory},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := comparator.IsOverQuota(tt.usage, tt.quota)

			if len(got) != len(tt.exceeded) {
				t.Errorf("IsOverQuota() exceeded = %v, want %v", got, tt.exceeded)
				return
			}

			for i, r := range got {
				if r != tt.exceeded[i] {
					t.Errorf("IsOverQuota()[%d] = %v, want %v", i, r, tt.exceeded[i])
				}
			}
		})
	}
}

// TestWorkspace_Structure tests workspace structure.
func TestWorkspace_Structure(t *testing.T) {
	ws := &Workspace{
		ID:          "test-id",
		Name:        "test-name",
		Description: "test-description",
		Project:     "test-project",
		Owner:       "test-owner",
		State:       StateRunning,
		Labels:      map[string]string{"key": "value"},
		Tags:        []string{"tag1", "tag2"},
	}

	if ws.ID != "test-id" {
		t.Error("Workspace ID should be set")
	}
	if ws.State != StateRunning {
		t.Error("Workspace state should be running")
	}
	if len(ws.Labels) != 1 {
		t.Error("Workspace should have one label")
	}
	if len(ws.Tags) != 2 {
		t.Error("Workspace should have two tags")
	}
}

// TestWorkspaceTemplate_Structure tests template structure.
func TestWorkspaceTemplate_Structure(t *testing.T) {
	template := &WorkspaceTemplate{
		ID:          "test-id",
		Name:        "test-template",
		Description: "Test template",
		Category:    "development",
		IsPublic:   true,
	}

	if template.ID != "test-id" {
		t.Error("Template ID should be set")
	}
	if !template.IsPublic {
		t.Error("Template should be public")
	}
}

// TestWorkspaceEvent_Structure tests event structure.
func TestWorkspaceEvent_Structure(t *testing.T) {
	event := WorkspaceEvent{
		ID:          "event-id",
		WorkspaceID: "ws-id",
		Type:        "created",
		Reason:      "created",
		Message:     "Workspace created",
		Timestamp:   time.Now(),
	}

	if event.ID != "event-id" {
		t.Error("Event ID should be set")
	}
	if event.WorkspaceID != "ws-id" {
		t.Error("Event workspace ID should be set")
	}
	if event.Type != "created" {
		t.Error("Event type should be created")
	}
}

// TestWorkspaceConfig_Structure tests workspace configuration.
func TestWorkspaceConfig_Structure(t *testing.T) {
	config := &WorkspaceConfig{
		Environment: map[string]string{"ENV": "value"},
		Volumes: []VolumeMount{
			{Name: "data", MountPath: "/data"},
		},
		Ports: []PortMapping{
			{ContainerPort: 8080, HostPort: 8080},
		},
		InitCommand: []string{"npm install"},
		Privileged: false,
	}

	if config.Environment["ENV"] != "value" {
		t.Error("Environment should be set")
	}
	if len(config.Volumes) != 1 {
		t.Error("Should have one volume")
	}
	if len(config.Ports) != 1 {
		t.Error("Should have one port mapping")
	}
}

// TestResourceQuota_Structure tests resource quota structure.
func TestResourceQuota_Structure(t *testing.T) {
	quota := &ResourceQuota{
		CPUCores:    2.0,
		MemoryBytes: 4 * 1024 * 1024 * 1024,
		StorageBytes: 20 * 1024 * 1024 * 1024,
		GPUCount:    1,
	}

	if quota.CPUCores != 2.0 {
		t.Error("CPU cores should be 2.0")
	}
	if quota.GPUCount != 1 {
		t.Error("GPU count should be 1")
	}
}

// TestWorkspaceManager_MaxWorkspaces tests workspace limits.
func TestWorkspaceManager_MaxWorkspaces(t *testing.T) {
	config := &ManagerConfig{
		MaxWorkspaces: 3,
	}
	manager := NewWorkspaceManager(config)

	// Create up to the limit
	for i := 0; i < 3; i++ {
		req := &CreateWorkspaceRequest{
			Name:   "test-ws",
			Project: "test",
			Owner:  "user",
		}
		_, err := manager.CreateWorkspace(context.Background(), req)
		if err != nil {
			t.Errorf("CreateWorkspace() error = %v", err)
		}
	}

	// Try to exceed limit
	req := &CreateWorkspaceRequest{
		Name:   "test-ws",
		Project: "test",
		Owner:  "user",
	}
	_, err := manager.CreateWorkspace(context.Background(), req)
	if err == nil {
		t.Error("CreateWorkspace() should return error when limit exceeded")
	}
}

// BenchmarkWorkspaceManager_CreateWorkspace benchmarks workspace creation.
func BenchmarkWorkspaceManager_CreateWorkspace(b *testing.B) {
	manager := NewWorkspaceManager(nil)

	req := &CreateWorkspaceRequest{
		Name:   "bench-workspace",
		Project: "benchmark",
		Owner:  "benchmark",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		manager.CreateWorkspace(context.Background(), req)
	}
}

// BenchmarkWorkspaceManager_ListWorkspaces benchmarks listing workspaces.
func BenchmarkWorkspaceManager_ListWorkspaces(b *testing.B) {
	manager := NewWorkspaceManager(nil)

	// Create workspaces
	for i := 0; i < 100; i++ {
		req := &CreateWorkspaceRequest{
			Name:   "bench-ws",
			Project: "benchmark",
			Owner:  "benchmark",
		}
		manager.CreateWorkspace(context.Background(), req)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		manager.ListWorkspaces()
	}
}
