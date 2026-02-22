package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/inizio/nexus/packages/nexus/pkg/testutil"
)

type mockProvider struct {
	containers map[string]bool
	mu         sync.Mutex
}

func newMockProvider() *mockProvider {
	return &mockProvider{
		containers: make(map[string]bool),
	}
}

func (m *mockProvider) Create(ctx context.Context, name string, worktreePath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.containers[name] = true
	return nil
}

func (m *mockProvider) CreateWithDinD(ctx context.Context, name string, worktreePath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.containers[name] = true
	return nil
}

func (m *mockProvider) Start(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.containers[name] {
		return fmt.Errorf("workspace not found")
	}
	return nil
}

func (m *mockProvider) Stop(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.containers[name] {
		return fmt.Errorf("workspace not found")
	}
	return nil
}

func (m *mockProvider) Destroy(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.containers, name)
	return nil
}

func (m *mockProvider) Shell(ctx context.Context, name string) error {
	return nil
}

func (m *mockProvider) Exec(ctx context.Context, name string, command []string) error {
	return nil
}

func (m *mockProvider) List(ctx context.Context) ([]WorkspaceInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []WorkspaceInfo
	for name := range m.containers {
		result = append(result, WorkspaceInfo{Name: name, Status: "running"})
	}
	return result, nil
}

func (m *mockProvider) Close() error {
	return nil
}

func (m *mockProvider) ContainerExists(ctx context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, exists := m.containers[name]
	return exists, nil
}

func (m *mockProvider) StartSync(ctx context.Context, workspaceName, worktreePath string) (string, error) {
	return "", nil
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
	return nil, nil
}

func (m *mockProvider) FlushSync(ctx context.Context, workspaceName string) error {
	return nil
}

func TestManagerDestroy_WithName(t *testing.T) {
	provider := newMockProvider()
	manager := NewManager(provider)

	ctx := context.Background()
	provider.Create(ctx, "test-workspace", "/path/to/worktree")

	err := manager.Destroy("test-workspace")
	if err != nil {
		t.Errorf("Destroy failed: %v", err)
	}

	list, _ := provider.List(ctx)
	if len(list) != 0 {
		t.Errorf("Expected 0 workspaces, got %d", len(list))
	}
}

func TestManagerDestroy_AutoDetectFromCurrent(t *testing.T) {
	tmpDir := t.TempDir()
	nexusDir := filepath.Join(tmpDir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		t.Fatalf("Failed to create .nexus dir: %v", err)
	}

	currentFile := filepath.Join(nexusDir, "current")
	if err := os.WriteFile(currentFile, []byte("auto-detected-ws"), 0644); err != nil {
		t.Fatalf("Failed to write current file: %v", err)
	}

	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(tmpDir)

	provider := newMockProvider()
	manager := NewManager(provider)

	ctx := context.Background()
	provider.Create(ctx, "auto-detected-ws", "/path/to/worktree")

	err := manager.Destroy("")
	if err != nil {
		t.Errorf("Destroy with auto-detect failed: %v", err)
	}

	list, _ := provider.List(ctx)
	if len(list) != 0 {
		t.Errorf("Expected 0 workspaces after auto-detect destroy, got %d", len(list))
	}
}

func TestManagerDestroy_CleanupCurrentFile(t *testing.T) {
	tmpDir := t.TempDir()
	nexusDir := filepath.Join(tmpDir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		t.Fatalf("Failed to create .nexus dir: %v", err)
	}

	currentFile := filepath.Join(nexusDir, "current")
	if err := os.WriteFile(currentFile, []byte("cleanup-test-ws"), 0644); err != nil {
		t.Fatalf("Failed to write current file: %v", err)
	}

	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(tmpDir)

	provider := newMockProvider()
	manager := NewManager(provider)

	ctx := context.Background()
	provider.Create(ctx, "cleanup-test-ws", "/path/to/worktree")

	err := manager.Destroy("cleanup-test-ws")
	if err != nil {
		t.Errorf("Destroy failed: %v", err)
	}

	if _, err := os.Stat(currentFile); !os.IsNotExist(err) {
		t.Error("Expected .nexus/current to be cleaned up")
	}
}

func TestManagerDestroy_NoAutoDetect(t *testing.T) {
	provider := newMockProvider()
	manager := NewManager(provider)

	err := manager.Destroy("")
	if err != nil {
		t.Logf("Got error (may be expected): %v", err)
	}
}

func TestManagerDestroy_ProviderError(t *testing.T) {
	provider := newMockProvider()

	errorProvider := &errorMockProvider{base: provider, destroyError: fmt.Errorf("provider destroy failed")}
	managerWithError := NewManager(errorProvider)

	ctx := context.Background()
	provider.Create(ctx, "error-test-ws", "/path/to/worktree")

	err := managerWithError.Destroy("error-test-ws")
	if err == nil {
		t.Error("Expected error to be propagated")
	}
}

func TestManagerDestroy_ConcurrentCalls(t *testing.T) {
	provider := newMockProvider()
	manager := NewManager(provider)

	ctx := context.Background()
	provider.Create(ctx, "concurrent-ws", "/path/to/worktree")

	var wg sync.WaitGroup
	errs := make(chan error, 5)

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- manager.Destroy("concurrent-ws")
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("Concurrent destroy failed: %v", err)
		}
	}
}

func TestManagerDestroy_DifferentWorkspace(t *testing.T) {
	tmpDir := t.TempDir()
	nexusDir := filepath.Join(tmpDir, ".nexus")
	if err := os.MkdirAll(nexusDir, 0755); err != nil {
		t.Fatalf("Failed to create .nexus dir: %v", err)
	}

	currentFile := filepath.Join(nexusDir, "current")
	if err := os.WriteFile(currentFile, []byte("workspace-a"), 0644); err != nil {
		t.Fatalf("Failed to write current file: %v", err)
	}

	oldWd, _ := os.Getwd()
	defer os.Chdir(oldWd)
	os.Chdir(tmpDir)

	provider := newMockProvider()
	manager := NewManager(provider)

	ctx := context.Background()
	provider.Create(ctx, "workspace-a", "/path/to/worktree-a")
	provider.Create(ctx, "workspace-b", "/path/to/worktree-b")

	err := manager.Destroy("workspace-b")
	if err != nil {
		t.Errorf("Destroy failed: %v", err)
	}

	data, _ := os.ReadFile(currentFile)
	if string(data) != "workspace-a" {
		t.Error("Current file should still point to workspace-a")
	}
}

func TestManagerDestroy_IdempotentMultiple(t *testing.T) {
	provider := newMockProvider()
	manager := NewManager(provider)

	ctx := context.Background()
	wsName := testutil.RandomWorkspaceName()
	provider.Create(ctx, wsName, "/path/to/worktree")

	err := manager.Destroy(wsName)
	if err != nil {
		t.Errorf("First destroy failed: %v", err)
	}

	err = manager.Destroy(wsName)
	if err != nil {
		t.Errorf("Second destroy (idempotent) failed: %v", err)
	}

	err = manager.Destroy(wsName)
	if err != nil {
		t.Errorf("Third destroy (idempotent) failed: %v", err)
	}
}

type errorMockProvider struct {
	base         *mockProvider
	destroyError error
}

func (e *errorMockProvider) Create(ctx context.Context, name string, worktreePath string) error {
	return e.base.Create(ctx, name, worktreePath)
}

func (e *errorMockProvider) Start(ctx context.Context, name string) error {
	return e.base.Start(ctx, name)
}

func (e *errorMockProvider) Stop(ctx context.Context, name string) error {
	return e.base.Stop(ctx, name)
}

func (e *errorMockProvider) Destroy(ctx context.Context, name string) error {
	if e.destroyError != nil {
		return e.destroyError
	}
	return e.base.Destroy(ctx, name)
}

func (e *errorMockProvider) Shell(ctx context.Context, name string) error {
	return e.base.Shell(ctx, name)
}

func (e *errorMockProvider) Exec(ctx context.Context, name string, command []string) error {
	return e.base.Exec(ctx, name, command)
}

func (e *errorMockProvider) List(ctx context.Context) ([]WorkspaceInfo, error) {
	return e.base.List(ctx)
}

func (e *errorMockProvider) Close() error {
	return e.base.Close()
}

func (e *errorMockProvider) ContainerExists(ctx context.Context, name string) (bool, error) {
	return e.base.ContainerExists(ctx, name)
}

func (e *errorMockProvider) StartSync(ctx context.Context, workspaceName, worktreePath string) (string, error) {
	return e.base.StartSync(ctx, workspaceName, worktreePath)
}

func (e *errorMockProvider) PauseSync(ctx context.Context, workspaceName string) error {
	return e.base.PauseSync(ctx, workspaceName)
}

func (e *errorMockProvider) ResumeSync(ctx context.Context, workspaceName string) error {
	return e.base.ResumeSync(ctx, workspaceName)
}

func (e *errorMockProvider) StopSync(ctx context.Context, workspaceName string) error {
	return e.base.StopSync(ctx, workspaceName)
}

func (e *errorMockProvider) GetSyncStatus(ctx context.Context, workspaceName string) (interface{}, error) {
	return e.base.GetSyncStatus(ctx, workspaceName)
}

func (e *errorMockProvider) FlushSync(ctx context.Context, workspaceName string) error {
	return e.base.FlushSync(ctx, workspaceName)
}
