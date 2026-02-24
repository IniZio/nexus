package docker

import (
	"context"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/sync"
)

func TestNewDockerBackend(t *testing.T) {
	backend := NewDockerBackend(nil, "/tmp/test-state")

	if backend == nil {
		t.Fatal("Expected backend to not be nil")
	}

	if backend.portManager == nil {
		t.Error("Expected portManager to be initialized")
	}

	if backend.containerManager == nil {
		t.Error("Expected containerManager to be initialized")
	}

	if backend.stateDir != "/tmp/test-state" {
		t.Errorf("Expected stateDir to be /tmp/test-state, got %s", backend.stateDir)
	}
}

func TestGetImageForTemplate(t *testing.T) {
	tests := []struct {
		name     string
		labels   map[string]string
		expected string
	}{
		{
			name:     "node template",
			labels:   map[string]string{"template": "node"},
			expected: "node:18-alpine",
		},
		{
			name:     "python template",
			labels:   map[string]string{"template": "python"},
			expected: "python:3.11-slim",
		},
		{
			name:     "go template",
			labels:   map[string]string{"template": "go"},
			expected: "golang:1.21-alpine",
		},
		{
			name:     "rust template",
			labels:   map[string]string{"template": "rust"},
			expected: "rust:1.75-slim",
		},
		{
			name:     "blank template",
			labels:   map[string]string{"template": "blank"},
			expected: "ubuntu:22.04",
		},
		{
			name:     "empty labels",
			labels:   map[string]string{},
			expected: "ubuntu:22.04",
		},
		{
			name:     "unknown template",
			labels:   map[string]string{"template": "unknown"},
			expected: "ubuntu:22.04",
		},
		{
			name:     "nodejs template",
			labels:   map[string]string{"template": "nodejs"},
			expected: "node:18-alpine",
		},
		{
			name:     "golang template",
			labels:   map[string]string{"template": "golang"},
			expected: "golang:1.21-alpine",
		},
		{
			name:     "minimal template",
			labels:   map[string]string{"template": "minimal"},
			expected: "ubuntu:22.04",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getImageForTemplate(tt.labels)
			if result != tt.expected {
				t.Errorf("Expected %s, got %s", tt.expected, result)
			}
		})
	}
}

func TestMergeEnv(t *testing.T) {
	tests := []struct {
		name      string
		configEnv map[string]string
		sshEnv    []string
		expected  int
	}{
		{
			name:      "both empty",
			configEnv: map[string]string{},
			sshEnv:    []string{},
			expected:  0,
		},
		{
			name:      "config env only",
			configEnv: map[string]string{"KEY1": "value1"},
			sshEnv:    []string{},
			expected:  1,
		},
		{
			name:      "ssh env only",
			configEnv: map[string]string{},
			sshEnv:    []string{"SSH_AUTH_SOCK=/ssh-agent"},
			expected:  1,
		},
		{
			name:      "both envs",
			configEnv: map[string]string{"KEY1": "value1", "KEY2": "value2"},
			sshEnv:    []string{"SSH_AUTH_SOCK=/ssh-agent"},
			expected:  3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mergeEnv(tt.configEnv, tt.sshEnv)
			if len(result) != tt.expected {
				t.Errorf("Expected %d items, got %d", tt.expected, len(result))
			}
		})
	}
}

func TestDefaultCmd(t *testing.T) {
	tests := []struct {
		name     string
		cmd      []string
		expected []string
	}{
		{
			name:     "empty cmd",
			cmd:      []string{},
			expected: []string{"sleep", "infinity"},
		},
		{
			name:     "provided cmd",
			cmd:      []string{"bash", "-c", "echo hello"},
			expected: []string{"bash", "-c", "echo hello"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := defaultCmd(tt.cmd)
			if len(result) != len(tt.expected) {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
			if result[0] != tt.expected[0] {
				t.Errorf("Expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestGetBridgeSocketMount(t *testing.T) {
	tests := []struct {
		name          string
		bridgeSocket  string
		expectNil     bool
		checkEnvValue string
	}{
		{
			name:         "empty socket",
			bridgeSocket: "",
			expectNil:    true,
		},
		{
			name:          "valid socket",
			bridgeSocket:  "/path/to/socket",
			expectNil:     false,
			checkEnvValue: "SSH_AUTH_SOCK=/ssh-agent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			volumes, env := GetBridgeSocketMount(tt.bridgeSocket)

			if tt.expectNil {
				if volumes != nil || env != nil {
					t.Errorf("Expected nil volumes and env")
				}
				return
			}

			if volumes == nil || len(volumes) == 0 {
				t.Errorf("Expected non-nil volumes")
			}

			if env == nil || len(env) == 0 {
				t.Errorf("Expected non-nil env")
			}

			if tt.checkEnvValue != "" {
				found := false
				for _, e := range env {
					if e == tt.checkEnvValue {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected env value %s", tt.checkEnvValue)
				}
			}
		})
	}
}

func TestDecodeDockerStream(t *testing.T) {
	tests := []struct {
		name     string
		data     []byte
		expected string
	}{
		{
			name:     "empty data",
			data:     []byte{},
			expected: "",
		},
		{
			name:     "short data",
			data:     []byte("hello"),
			expected: "hello",
		},
		{
			name:     "stdout stream",
			data:     []byte{1, 0, 0, 0, 0, 0, 0, 5, 'h', 'e', 'l', 'l', 'o'},
			expected: "hello",
		},
		{
			name:     "stderr stream",
			data:     []byte{2, 0, 0, 0, 0, 0, 0, 5, 'e', 'r', 'r', 'o', 'r'},
			expected: "error",
		},
		{
			name:     "mixed streams",
			data:     []byte{1, 0, 0, 0, 0, 0, 0, 5, 'h', 'e', 'l', 'l', 'o', 2, 0, 0, 0, 0, 0, 0, 5, 'e', 'r', 'r', 'o', 'r'},
			expected: "helloerror",
		},
		{
			name:     "truncated stream",
			data:     []byte{1, 0, 0, 0, 0, 0, 0, 10, 'h', 'e', 'l', 'l', 'o'},
			expected: "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := decodeDockerStream(tt.data)
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestDockerBackend_AllocatePort(t *testing.T) {
	backend := NewDockerBackend(nil, "/tmp/test")

	port1, err := backend.AllocatePort()
	if err != nil {
		t.Fatalf("Failed to allocate port: %v", err)
	}

	if port1 < 32800 || port1 > 34999 {
		t.Errorf("Port %d out of valid range", port1)
	}

	port2, err := backend.AllocatePort()
	if err != nil {
		t.Fatalf("Failed to allocate second port: %v", err)
	}

	if port1 == port2 {
		t.Error("Expected different ports")
	}

	if err := backend.ReleasePort(port1); err != nil {
		t.Errorf("Failed to release port: %v", err)
	}
}

func TestDockerBackend_GetWorkspaceStatus(t *testing.T) {
	t.Skip("Requires Docker client - tested via integration tests")
}

func TestDockerBackend_GetResourceStats(t *testing.T) {
	ctx := context.Background()

	t.Run("container found and running", func(t *testing.T) {
		cm := NewContainerManager()
		cm.containers["ws-test"] = &ContainerInfo{
			ID:     "ws-test",
			Status: "running",
			State: ContainerState{
				Running: true,
			},
		}

		backend := &DockerBackend{
			containerManager: cm,
		}

		stats, err := backend.GetResourceStats(ctx, "ws-test")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if stats.WorkspaceID != "ws-test" {
			t.Errorf("Expected WorkspaceID to be ws-test, got %s", stats.WorkspaceID)
		}
	})

	t.Run("container not found", func(t *testing.T) {
		cm := NewContainerManager()

		backend := &DockerBackend{
			containerManager: cm,
		}

		stats, err := backend.GetResourceStats(ctx, "nonexistent")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if stats.WorkspaceID != "nonexistent" {
			t.Errorf("Expected WorkspaceID to be nonexistent, got %s", stats.WorkspaceID)
		}
	})
}

func TestDockerBackend_StartWorkspace(t *testing.T) {
	t.Skip("Requires Docker client - tested via integration tests")
}

func TestDockerBackend_StopWorkspace(t *testing.T) {
	t.Skip("Requires Docker client - tested via integration tests")
}

func TestDockerBackend_DeleteWorkspace(t *testing.T) {
	t.Skip("Requires Docker client - tested via integration tests")
}

func TestDockerBackend_GetLogs(t *testing.T) {
	ctx := context.Background()

	t.Run("get logs success", func(t *testing.T) {
		cm := NewContainerManager()
		cm.containers["ws-test"] = &ContainerInfo{
			ID:     "ws-test",
			Status: "running",
		}

		backend := &DockerBackend{
			containerManager: cm,
		}

		logs, err := backend.GetLogs(ctx, "ws-test", 100)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if logs == "" {
			t.Error("Expected logs")
		}
	})

	t.Run("container not found", func(t *testing.T) {
		cm := NewContainerManager()

		backend := &DockerBackend{
			containerManager: cm,
		}

		_, err := backend.GetLogs(ctx, "nonexistent", 100)
		if err == nil {
			t.Error("Expected error")
		}
	})
}

func TestDockerBackend_AllocateAndReleasePort(t *testing.T) {
	backend := NewDockerBackend(nil, "/tmp/test")

	t.Run("allocate multiple ports", func(t *testing.T) {
		ports := make(map[int32]bool)

		for i := 0; i < 100; i++ {
			port, err := backend.AllocatePort()
			if err != nil {
				t.Fatalf("Failed to allocate port: %v", err)
			}
			ports[port] = true
		}

		if len(ports) != 100 {
			t.Errorf("Expected 100 unique ports, got %d", len(ports))
		}
	})

	t.Run("release and reallocate", func(t *testing.T) {
		port1, _ := backend.AllocatePort()
		backend.ReleasePort(port1)

		port2, _ := backend.AllocatePort()
		backend.ReleasePort(port2)

		if port1 == port2 {
			t.Error("Expected different ports after release and reallocate")
		}
	})
}

func TestDockerBackend_GetStatus(t *testing.T) {
	t.Skip("Requires Docker client - tested via integration tests")
}

func TestDockerBackend_GetPortManager(t *testing.T) {
	backend := NewDockerBackend(nil, "/tmp/test")

	pm := backend.GetPortManager()
	if pm == nil {
		t.Error("Expected PortManager to not be nil")
	}
}

func TestDockerBackend_SetWorktreePathFunc(t *testing.T) {
	backend := NewDockerBackend(nil, "/tmp/test")

	called := false
	var receivedID string

	backend.SetWorktreePathFunc(func(workspaceID string) (string, error) {
		called = true
		receivedID = workspaceID
		return "/test/path", nil
	})

	if backend.worktreePathFunc == nil {
		t.Error("Expected worktreePathFunc to be set")
	}

	path, err := backend.worktreePathFunc("ws-123")
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if !called {
		t.Error("Expected function to be called")
	}

	if receivedID != "ws-123" {
		t.Errorf("Expected workspace ID ws-123, got %s", receivedID)
	}

	if path != "/test/path" {
		t.Errorf("Expected path /test/path, got %s", path)
	}
}

func TestContainerManager_Inspect(t *testing.T) {
	ctx := context.Background()

	t.Run("inspect existing container", func(t *testing.T) {
		cm := NewContainerManager()
		cm.containers["ws-test"] = &ContainerInfo{
			ID:     "ws-test",
			Status: "running",
		}

		info, err := cm.Inspect(ctx, "ws-test")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if info == nil {
			t.Fatal("Expected info to not be nil")
		}

		if info.ID != "ws-test" {
			t.Errorf("Expected ID ws-test, got %s", info.ID)
		}
	})

	t.Run("inspect non-existing container", func(t *testing.T) {
		cm := NewContainerManager()

		info, err := cm.Inspect(ctx, "nonexistent")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if info != nil {
			t.Error("Expected nil info")
		}
	})
}

func TestContainerManager_Start(t *testing.T) {
	ctx := context.Background()

	t.Run("start existing container", func(t *testing.T) {
		cm := NewContainerManager()
		cm.containers["ws-test"] = &ContainerInfo{
			ID:     "ws-test",
			Status: "created",
			State: ContainerState{
				Status:  "created",
				Running: false,
			},
		}

		err := cm.Start(ctx, "ws-test")
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if cm.containers["ws-test"].State.Running != true {
			t.Error("Expected container to be running")
		}
	})

	t.Run("start non-existing container", func(t *testing.T) {
		cm := NewContainerManager()

		err := cm.Start(ctx, "nonexistent")
		if err == nil {
			t.Error("Expected error")
		}
	})
}

func TestContainerManager_Stop(t *testing.T) {
	ctx := context.Background()

	t.Run("stop existing container", func(t *testing.T) {
		cm := NewContainerManager()
		cm.containers["ws-test"] = &ContainerInfo{
			ID:     "ws-test",
			Status: "running",
			State: ContainerState{
				Status:  "running",
				Running: true,
			},
		}

		err := cm.Stop(ctx, "ws-test", 0)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if cm.containers["ws-test"].State.Running != false {
			t.Error("Expected container to be stopped")
		}
	})

	t.Run("stop non-existing container", func(t *testing.T) {
		cm := NewContainerManager()

		err := cm.Stop(ctx, "nonexistent", 0)
		if err == nil {
			t.Error("Expected error")
		}
	})
}

func TestContainerManager_Remove(t *testing.T) {
	ctx := context.Background()

	t.Run("remove existing container", func(t *testing.T) {
		cm := NewContainerManager()
		cm.containers["ws-test"] = &ContainerInfo{
			ID:     "ws-test",
			Status: "running",
		}

		err := cm.Remove(ctx, "ws-test", false)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if _, exists := cm.containers["ws-test"]; exists {
			t.Error("Expected container to be removed")
		}
	})

	t.Run("remove non-existing container", func(t *testing.T) {
		cm := NewContainerManager()

		err := cm.Remove(ctx, "nonexistent", false)
		if err == nil {
			t.Error("Expected error")
		}
	})
}

func TestContainerManager_Exec(t *testing.T) {
	ctx := context.Background()

	t.Run("exec in existing container", func(t *testing.T) {
		cm := NewContainerManager()
		cm.containers["ws-test"] = &ContainerInfo{
			ID:     "ws-test",
			Status: "running",
		}

		output, err := cm.Exec(ctx, "ws-test", []string{"echo", "hello"})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if output == "" {
			t.Error("Expected output")
		}
	})

	t.Run("exec in non-existing container", func(t *testing.T) {
		cm := NewContainerManager()

		_, err := cm.Exec(ctx, "nonexistent", []string{"echo", "hello"})
		if err == nil {
			t.Error("Expected error")
		}
	})
}

func TestContainerManager_Logs(t *testing.T) {
	ctx := context.Background()

	t.Run("logs from existing container", func(t *testing.T) {
		cm := NewContainerManager()
		cm.containers["ws-test"] = &ContainerInfo{
			ID:     "ws-test",
			Status: "running",
		}

		logs, err := cm.Logs(ctx, "ws-test", 100)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if logs == "" {
			t.Error("Expected logs")
		}
	})

	t.Run("logs from non-existing container", func(t *testing.T) {
		cm := NewContainerManager()

		_, err := cm.Logs(ctx, "nonexistent", 100)
		if err == nil {
			t.Error("Expected error")
		}
	})
}

func TestContainerManager_Create(t *testing.T) {
	ctx := context.Background()

	t.Run("create container", func(t *testing.T) {
		cm := NewContainerManager()

		containerID, err := cm.Create(ctx, "test-image", &ContainerConfig{
			Name:  "test-container",
			Image: "test-image",
		})
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}

		if containerID == "" {
			t.Error("Expected container ID")
		}

		if _, exists := cm.containers[containerID]; !exists {
			t.Error("Expected container to be registered")
		}
	})
}

func TestConvertConflicts(t *testing.T) {
	t.Run("nil conflicts", func(t *testing.T) {
		result := convertConflicts(nil)
		if len(result) != 0 {
			t.Errorf("Expected 0 conflicts, got %d", len(result))
		}
	})

	t.Run("with conflicts", func(t *testing.T) {
		result := convertConflicts([]sync.Conflict{
			{Path: "file1.txt", AlphaContent: "content A", BetaContent: "content B"},
			{Path: "file2.txt", AlphaContent: "content C", BetaContent: "content D"},
		})
		if len(result) != 2 {
			t.Errorf("Expected 2 conflicts, got %d", len(result))
		}
		if result[0].Path != "file1.txt" {
			t.Errorf("Expected path file1.txt, got %s", result[0].Path)
		}
	})
}
