package types

import (
	"testing"
	"time"
)

func TestWorkspaceStatusString(t *testing.T) {
	tests := []struct {
		status WorkspaceStatus
		want   string
	}{
		{StatusUnknown, "unknown"},
		{StatusCreating, "creating"},
		{StatusRunning, "running"},
		{StatusSleeping, "sleeping"},
		{StatusStopped, "stopped"},
		{StatusError, "error"},
		{999, "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.status.String(); got != tt.want {
				t.Errorf("WorkspaceStatus.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWorkspaceStatusFromString(t *testing.T) {
	tests := []struct {
		str  string
		want WorkspaceStatus
	}{
		{"creating", StatusCreating},
		{"running", StatusRunning},
		{"sleeping", StatusSleeping},
		{"stopped", StatusStopped},
		{"error", StatusError},
		{"unknown", StatusStopped},
		{"", StatusStopped},
	}

	for _, tt := range tests {
		t.Run(tt.str, func(t *testing.T) {
			if got := WorkspaceStatusFromString(tt.str); got != tt.want {
				t.Errorf("WorkspaceStatusFromString(%q) = %v, want %v", tt.str, got, tt.want)
			}
		})
	}
}

func TestBackendTypeString(t *testing.T) {
	tests := []struct {
		backend BackendType
		want    string
	}{
		{BackendDocker, "docker"},
		{BackendSprite, "sprite"},
		{BackendKubernetes, "kubernetes"},
		{BackendDaytona, "daytona"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if got := tt.backend.String(); got != tt.want {
				t.Errorf("BackendType.String() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBackendTypeFromString(t *testing.T) {
	tests := []struct {
		str  string
		want BackendType
	}{
		{"docker", BackendDocker},
		{"sprite", BackendSprite},
		{"kubernetes", BackendKubernetes},
		{"daytona", BackendDaytona},
		{"unknown", BackendUnknown},
		{"", BackendUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.str, func(t *testing.T) {
			if got := BackendTypeFromString(tt.str); got != tt.want {
				t.Errorf("BackendTypeFromString(%q) = %v, want %v", tt.str, got, tt.want)
			}
		})
	}
}

func TestDaytonaConfigMarshaling(t *testing.T) {
	config := DaytonaConfig{
		Enabled: true,
		APIURL:  "https://app.daytona.io/api",
	}

	if !config.Enabled {
		t.Error("Expected Enabled to be true")
	}
	if config.APIURL != "https://app.daytona.io/api" {
		t.Errorf("Expected APIURL to be 'https://app.daytona.io/api', got %q", config.APIURL)
	}
}

func TestWorkspaceTypes(t *testing.T) {
	repo := &Repository{
		URL:           "https://github.com/test/repo",
		Provider:      "github",
		LocalPath:     "/workspaces/repo",
		DefaultBranch: "main",
		CurrentCommit: "abc123",
	}

	resources := &ResourceAllocation{
		CPUCores:     2.0,
		MemoryBytes:  4 * 1024 * 1024 * 1024,
		StorageBytes: 20 * 1024 * 1024 * 1024,
	}

	ports := []PortMapping{
		{Name: "http", Protocol: "tcp", ContainerPort: 8080, HostPort: 32800, Visibility: "public"},
	}

	config := &WorkspaceConfig{
		Image:       "ubuntu:22.04",
		Env:         map[string]string{"FOO": "bar"},
		IdleTimeout: 30,
	}

	ws := Workspace{
		ID:         "ws-123",
		Name:       "test-workspace",
		Status:     StatusRunning,
		Backend:    BackendDocker,
		Repository: repo,
		Branch:     "main",
		Resources:  resources,
		Ports:      ports,
		Config:     config,
		Labels:     map[string]string{"env": "dev"},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}

	if ws.ID != "ws-123" {
		t.Errorf("Expected ID ws-123, got %s", ws.ID)
	}
	if ws.Status != StatusRunning {
		t.Errorf("Expected Status Running, got %v", ws.Status)
	}
	if ws.Backend != BackendDocker {
		t.Errorf("Expected Backend Docker, got %v", ws.Backend)
	}
	if len(ws.Ports) != 1 {
		t.Errorf("Expected 1 port, got %d", len(ws.Ports))
	}
	if ws.Ports[0].HostPort != 32800 {
		t.Errorf("Expected host port 32800, got %d", ws.Ports[0].HostPort)
	}
}

func TestWorkspaceEventTypes(t *testing.T) {
	evt := WorkspaceEvent{
		ID:          "evt-123",
		WorkspaceID: "ws-456",
		EventType:   "started",
		Data:        `{"reason": "user initiated"}`,
		ActorType:   "user",
		ActorID:     "user-789",
		OccurredAt:  time.Now(),
	}

	if evt.ID != "evt-123" {
		t.Errorf("Expected ID evt-123, got %s", evt.ID)
	}
	if evt.EventType != "started" {
		t.Errorf("Expected EventType started, got %s", evt.EventType)
	}
}

func TestResourceStatsTypes(t *testing.T) {
	stats := ResourceStats{
		WorkspaceID:      "ws-123",
		CPUUsagePercent:  45.5,
		MemoryUsedBytes:  2 * 1024 * 1024 * 1024,
		MemoryLimitBytes: 4 * 1024 * 1024 * 1024,
		DiskUsedBytes:    10 * 1024 * 1024 * 1024,
		NetworkRxBytes:   1024,
		NetworkTxBytes:   2048,
		Timestamp:        time.Now(),
	}

	if stats.WorkspaceID != "ws-123" {
		t.Errorf("Expected WorkspaceID ws-123, got %s", stats.WorkspaceID)
	}
	if stats.CPUUsagePercent != 45.5 {
		t.Errorf("Expected CPUUsagePercent 45.5, got %f", stats.CPUUsagePercent)
	}
}

func TestCreateWorkspaceRequestTypes(t *testing.T) {
	req := CreateWorkspaceRequest{
		Name:          "my-workspace",
		DisplayName:   "My Workspace",
		Backend:       BackendDocker,
		RepositoryURL: "https://github.com/test/repo",
		Branch:        "feature-branch",
		ResourceClass: "medium",
		Config: &WorkspaceConfig{
			Image: "ubuntu:22.04",
		},
		Labels:     map[string]string{"team": "engineering"},
		ForwardSSH: true,
		ID:         "ws-new",
	}

	if req.Name != "my-workspace" {
		t.Errorf("Expected Name my-workspace, got %s", req.Name)
	}
	if req.Backend != BackendDocker {
		t.Errorf("Expected Backend Docker, got %v", req.Backend)
	}
	if !req.ForwardSSH {
		t.Error("Expected ForwardSSH to be true")
	}
}

func TestDaytonaMetadataTypes(t *testing.T) {
	meta := DaytonaMetadata{
		SandboxID:     "sb-abc123",
		SSHHost:       "host.daytona.io",
		SSHPort:       22,
		SSHUsername:   "daytona",
		SSHPrivateKey: "-----BEGIN RSA PRIVATE KEY-----",
	}

	if meta.SandboxID != "sb-abc123" {
		t.Errorf("Expected SandboxID sb-abc123, got %s", meta.SandboxID)
	}
	if meta.SSHPort != 22 {
		t.Errorf("Expected SSHPort 22, got %d", meta.SSHPort)
	}
}

func TestWorkspaceConfigTypes(t *testing.T) {
	config := WorkspaceConfig{
		Image:            "ubuntu:22.04",
		DevcontainerPath: ".devcontainer/devcontainer.json",
		Env:              map[string]string{"DEBUG": "true", "PORT": "8080"},
		EnvFiles:         []string{".env.local"},
		IdleTimeout:      60,
		ShutdownBehavior: "stop",
	}

	if config.Image != "ubuntu:22.04" {
		t.Errorf("Expected Image ubuntu:22.04, got %s", config.Image)
	}
	if config.IdleTimeout != 60 {
		t.Errorf("Expected IdleTimeout 60, got %d", config.IdleTimeout)
	}
	if len(config.Env) != 2 {
		t.Errorf("Expected 2 env vars, got %d", len(config.Env))
	}
}

func TestPortMappingTypes(t *testing.T) {
	port := PortMapping{
		Name:          "web",
		Protocol:      "tcp",
		ContainerPort: 3000,
		HostPort:      32800,
		Visibility:    "public",
		URL:           "https://example.com",
	}

	if port.Name != "web" {
		t.Errorf("Expected Name web, got %s", port.Name)
	}
	if port.ContainerPort != 3000 {
		t.Errorf("Expected ContainerPort 3000, got %d", port.ContainerPort)
	}
}

func TestVolumeConfigTypes(t *testing.T) {
	vol := VolumeConfig{
		Type:     "bind",
		Source:   "/local/path",
		Target:   "/container/path",
		ReadOnly: true,
	}

	if vol.Type != "bind" {
		t.Errorf("Expected Type bind, got %s", vol.Type)
	}
	if !vol.ReadOnly {
		t.Error("Expected ReadOnly to be true")
	}
}

func TestServiceConfigTypes(t *testing.T) {
	svc := ServiceConfig{
		Name:  "postgres",
		Image: "postgres:15",
		Ports: []PortMapping{
			{Name: "db", Protocol: "tcp", ContainerPort: 5432, HostPort: 32801},
		},
		Env:       map[string]string{"POSTGRES_PASSWORD": "secret"},
		DependsOn: []string{"redis"},
	}

	if svc.Name != "postgres" {
		t.Errorf("Expected Name postgres, got %s", svc.Name)
	}
	if len(svc.Ports) != 1 {
		t.Errorf("Expected 1 port, got %d", len(svc.Ports))
	}
	if len(svc.DependsOn) != 1 {
		t.Errorf("Expected 1 dependency, got %d", len(svc.DependsOn))
	}
}

func TestWorkspaceHooksTypes(t *testing.T) {
	hooks := WorkspaceHooks{
		PreCreate:  []string{"./scripts/pre-create.sh"},
		PostCreate: []string{"./scripts/post-create.sh"},
		PreStart:   []string{"./scripts/pre-start.sh"},
		PostStart:  []string{"./scripts/post-start.sh"},
		PreStop:    []string{"./scripts/pre-stop.sh"},
		PostStop:   []string{"./scripts/post-stop.sh"},
	}

	if len(hooks.PreCreate) != 1 {
		t.Errorf("Expected 1 PreCreate hook, got %d", len(hooks.PreCreate))
	}
	if len(hooks.PostStop) != 1 {
		t.Errorf("Expected 1 PostStop hook, got %d", len(hooks.PostStop))
	}
}

func TestOperationTypes(t *testing.T) {
	op := Operation{
		ID:           "op-123",
		Status:       "completed",
		ErrorMessage: "",
		CreatedAt:    time.Now(),
		CompletedAt:  time.Now(),
	}

	if op.ID != "op-123" {
		t.Errorf("Expected ID op-123, got %s", op.ID)
	}
	if op.Status != "completed" {
		t.Errorf("Expected Status completed, got %s", op.Status)
	}
}

func TestSnapshotTypes(t *testing.T) {
	snap := Snapshot{
		ID:          "snap-123",
		WorkspaceID: "ws-456",
		Name:        "backup-1",
		Description: "Initial snapshot",
		SizeBytes:   1024 * 1024 * 100,
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(24 * time.Hour),
	}

	if snap.ID != "snap-123" {
		t.Errorf("Expected ID snap-123, got %s", snap.ID)
	}
	if snap.SizeBytes != 104857600 {
		t.Errorf("Expected SizeBytes 104857600, got %d", snap.SizeBytes)
	}
}
