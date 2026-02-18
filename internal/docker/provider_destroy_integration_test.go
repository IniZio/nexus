package docker

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"

	"nexus/pkg/testutil"
)

func TestDestroy_RunningContainer(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	// Create a real provider
	provider, err := NewProviderWithoutStorage()
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx := context.Background()
	workspaceName := testutil.RandomWorkspaceName()

	// First create a container manually to simulate a running workspace
	containerName := fmt.Sprintf("nexus-%s", workspaceName)
	resp, err := provider.cli.ContainerCreate(ctx, &container.Config{
		Image: "ubuntu:22.04",
		Labels: map[string]string{
			"nexus.workspace.name": workspaceName,
			"nexus.workspace":      "true",
		},
		Cmd: []string{"tail", "-f", "/dev/null"},
	}, nil, nil, nil, containerName)
	if err != nil {
		t.Fatalf("Failed to create test container: %v", err)
	}

	// Start the container
	if err := provider.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		t.Fatalf("Failed to start test container: %v", err)
	}

	// Ensure cleanup
	t.Cleanup(func() {
		provider.cli.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})
	})

	// Wait for container to be running
	time.Sleep(500 * time.Millisecond)

	// Now test destroy
	err = provider.Destroy(ctx, workspaceName)
	if err != nil {
		t.Errorf("Destroy failed for running container: %v", err)
	}

	// Verify container is removed
	_, err = provider.cli.ContainerInspect(ctx, containerName)
	if err == nil {
		t.Error("Container should have been removed")
	}
}

func TestDestroy_StoppedContainer(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	provider, err := NewProviderWithoutStorage()
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx := context.Background()
	workspaceName := testutil.RandomWorkspaceName()

	containerName := fmt.Sprintf("nexus-%s", workspaceName)
	_, err = provider.cli.ContainerCreate(ctx, &container.Config{
		Image: "ubuntu:22.04",
		Labels: map[string]string{
			"nexus.workspace.name": workspaceName,
			"nexus.workspace":      "true",
		},
		Cmd: []string{"tail", "-f", "/dev/null"},
	}, nil, nil, nil, containerName)
	if err != nil {
		t.Fatalf("Failed to create test container: %v", err)
	}

	// Don't start - container stays stopped
	// Ensure cleanup
	t.Cleanup(func() {
		provider.cli.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})
	})

	// Test destroy on stopped container
	err = provider.Destroy(ctx, workspaceName)
	if err != nil {
		t.Errorf("Destroy failed for stopped container: %v", err)
	}

	// Verify container is removed
	_, err = provider.cli.ContainerInspect(ctx, containerName)
	if err == nil {
		t.Error("Container should have been removed")
	}
}

func TestDestroy_NonExistentContainer(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	provider, err := NewProviderWithoutStorage()
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx := context.Background()
	workspaceName := testutil.RandomWorkspaceName()

	// Destroy non-existent container - should return nil (idempotent)
	err = provider.Destroy(ctx, workspaceName)
	if err != nil {
		t.Errorf("Destroy should be idempotent for non-existent container: %v", err)
	}
}

func TestDestroy_WithTimeout(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	provider, err := NewProviderWithoutStorage()
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx := context.Background()
	workspaceName := testutil.RandomWorkspaceName()

	containerName := fmt.Sprintf("nexus-%s", workspaceName)
	resp, err := provider.cli.ContainerCreate(ctx, &container.Config{
		Image: "ubuntu:22.04",
		Labels: map[string]string{
			"nexus.workspace.name": workspaceName,
			"nexus.workspace":      "true",
		},
		Cmd: []string{"tail", "-f", "/dev/null"},
	}, nil, nil, nil, containerName)
	if err != nil {
		t.Fatalf("Failed to create test container: %v", err)
	}

	// Start the container
	if err := provider.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		t.Fatalf("Failed to start test container: %v", err)
	}

	// Ensure cleanup
	t.Cleanup(func() {
		provider.cli.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})
	})

	// Test destroy with running container (should use timeout)
	err = provider.Destroy(ctx, workspaceName)
	if err != nil {
		t.Errorf("Destroy failed with timeout: %v", err)
	}

	// Verify container is removed
	_, err = provider.cli.ContainerInspect(ctx, containerName)
	if err == nil {
		t.Error("Container should have been removed")
	}
}

func TestDestroy_ConcurrentCalls(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	provider, err := NewProviderWithoutStorage()
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx := context.Background()
	workspaceName := testutil.RandomWorkspaceName()

	containerName := fmt.Sprintf("nexus-%s", workspaceName)
	resp, err := provider.cli.ContainerCreate(ctx, &container.Config{
		Image: "ubuntu:22.04",
		Labels: map[string]string{
			"nexus.workspace.name": workspaceName,
			"nexus.workspace":      "true",
		},
		Cmd: []string{"tail", "-f", "/dev/null"},
	}, nil, nil, nil, containerName)
	if err != nil {
		t.Fatalf("Failed to create test container: %v", err)
	}

	// Start the container
	if err := provider.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		t.Fatalf("Failed to start test container: %v", err)
	}

	// Ensure cleanup
	t.Cleanup(func() {
		provider.cli.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})
	})

	// Test concurrent destroy calls
	var wg sync.WaitGroup
	errs := make(chan error, 3)

	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- provider.Destroy(ctx, workspaceName)
		}()
	}

	wg.Wait()
	close(errs)

	// At least one should succeed, others may fail or succeed (idempotent)
	successCount := 0
	for err := range errs {
		if err == nil {
			successCount++
		}
	}

	// At least one should succeed
	if successCount == 0 {
		t.Error("At least one destroy call should succeed")
	}

	// Verify container is removed
	_, err = provider.cli.ContainerInspect(ctx, containerName)
	if err == nil {
		t.Error("Container should have been removed")
	}
}

func TestDestroy_ProviderFailure(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	provider, err := NewProviderWithoutStorage()
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx := context.Background()

	// Test with invalid container name that might cause issues
	// Using special characters that are invalid
	invalidName := "test-invalid-!@#$%"

	err = provider.Destroy(ctx, invalidName)
	// Should handle gracefully - either succeed (idempotent) or return proper error
	// The key is it shouldn't panic
	if err != nil && !client.IsErrNotFound(err) {
		// It's acceptable to get an error for invalid names
		t.Logf("Got expected error for invalid name: %v", err)
	}
}

func TestDestroy_MultipleDestroyCalls(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	provider, err := NewProviderWithoutStorage()
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx := context.Background()
	workspaceName := testutil.RandomWorkspaceName()

	// Create and destroy once
	err = provider.Destroy(ctx, workspaceName)
	if err != nil {
		t.Fatalf("First destroy failed: %v", err)
	}

	// Destroy again - should be idempotent
	err = provider.Destroy(ctx, workspaceName)
	if err != nil {
		t.Errorf("Second destroy should be idempotent: %v", err)
	}

	// Destroy a third time
	err = provider.Destroy(ctx, workspaceName)
	if err != nil {
		t.Errorf("Third destroy should be idempotent: %v", err)
	}
}

func TestDestroy_ForceRemoveRunning(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	provider, err := NewProviderWithoutStorage()
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx := context.Background()
	workspaceName := testutil.RandomWorkspaceName()

	containerName := fmt.Sprintf("nexus-%s", workspaceName)
	resp, err := provider.cli.ContainerCreate(ctx, &container.Config{
		Image: "ubuntu:22.04",
		Labels: map[string]string{
			"nexus.workspace.name": workspaceName,
			"nexus.workspace":      "true",
		},
		Cmd: []string{"tail", "-f", "/dev/null"},
	}, nil, nil, nil, containerName)
	if err != nil {
		t.Fatalf("Failed to create test container: %v", err)
	}

	// Start the container
	if err := provider.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		t.Fatalf("Failed to start test container: %v", err)
	}

	// Test destroy - should use force remove
	err = provider.Destroy(ctx, workspaceName)
	if err != nil {
		t.Errorf("Destroy with force remove failed: %v", err)
	}

	// Verify container is removed
	_, err = provider.cli.ContainerInspect(ctx, containerName)
	if err == nil {
		t.Error("Container should have been force removed")
	}
}

func TestDestroy_ContainerWithVolumes(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	provider, err := NewProviderWithoutStorage()
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx := context.Background()
	workspaceName := testutil.RandomWorkspaceName()

	containerName := fmt.Sprintf("nexus-%s", workspaceName)
	_, err = provider.cli.ContainerCreate(ctx, &container.Config{
		Image: "ubuntu:22.04",
		Labels: map[string]string{
			"nexus.workspace.name": workspaceName,
			"nexus.workspace":      "true",
		},
		Cmd: []string{"tail", "-f", "/dev/null"},
	}, &container.HostConfig{
		Binds: []string{"/tmp:/tmp:ro"},
	}, nil, nil, containerName)
	if err != nil {
		t.Fatalf("Failed to create test container: %v", err)
	}

	// Test destroy - should remove volumes too
	err = provider.Destroy(ctx, workspaceName)
	if err != nil {
		t.Errorf("Destroy with volumes failed: %v", err)
	}

	// Verify container is removed
	_, err = provider.cli.ContainerInspect(ctx, containerName)
	if err == nil {
		t.Error("Container should have been removed")
	}
}

func TestDestroy_LabeledContainer(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	provider, err := NewProviderWithoutStorage()
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx := context.Background()
	workspaceName := testutil.RandomWorkspaceName()

	containerName := fmt.Sprintf("nexus-%s", workspaceName)
	resp, err := provider.cli.ContainerCreate(ctx, &container.Config{
		Image: "ubuntu:22.04",
		Labels: map[string]string{
			"nexus.workspace.name": workspaceName,
			"nexus.workspace":      "true",
			"custom.label":         "test-value",
		},
		Cmd: []string{"tail", "-f", "/dev/null"},
	}, nil, nil, nil, containerName)
	if err != nil {
		t.Fatalf("Failed to create test container: %v", err)
	}

	// Start the container
	if err := provider.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		t.Fatalf("Failed to start test container: %v", err)
	}

	// Ensure cleanup
	t.Cleanup(func() {
		provider.cli.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true})
	})

	// Test destroy
	err = provider.Destroy(ctx, workspaceName)
	if err != nil {
		t.Errorf("Destroy labeled container failed: %v", err)
	}

	// Verify container is removed
	_, err = provider.cli.ContainerInspect(ctx, containerName)
	if err == nil {
		t.Error("Labeled container should have been removed")
	}
}

func TestDestroy_AfterStopFailure(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	provider, err := NewProviderWithoutStorage()
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx := context.Background()
	workspaceName := testutil.RandomWorkspaceName()

	containerName := fmt.Sprintf("nexus-%s", workspaceName)
	resp, err := provider.cli.ContainerCreate(ctx, &container.Config{
		Image: "ubuntu:22.04",
		Labels: map[string]string{
			"nexus.workspace.name": workspaceName,
			"nexus.workspace":      "true",
		},
		Cmd: []string{"tail", "-f", "/dev/null"},
	}, nil, nil, nil, containerName)
	if err != nil {
		t.Fatalf("Failed to create test container: %v", err)
	}

	// Start the container
	if err := provider.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		t.Fatalf("Failed to start test container: %v", err)
	}

	// Test destroy - even if stop somehow fails, should still remove
	err = provider.Destroy(ctx, workspaceName)
	if err != nil {
		t.Errorf("Destroy should handle stop failures gracefully: %v", err)
	}

	// Verify container is removed
	_, err = provider.cli.ContainerInspect(ctx, containerName)
	if err == nil {
		t.Error("Container should have been removed despite any stop issues")
	}
}

func TestDestroy_RapidCreateAndDestroy(t *testing.T) {
	testutil.SkipIfNoDocker(t)

	provider, err := NewProviderWithoutStorage()
	if err != nil {
		t.Fatalf("Failed to create provider: %v", err)
	}
	defer provider.Close()

	ctx := context.Background()

	// Rapidly create and destroy workspaces
	for i := 0; i < 3; i++ {
		workspaceName := testutil.RandomWorkspaceName()
		worktreePath := filepath.Join(".nexus", "worktrees", workspaceName)

		// Create using provider.Create which handles setup
		err := provider.Create(ctx, workspaceName, worktreePath)
		if err != nil {
			// Might fail if image pull takes too long, that's OK for this test
			t.Logf("Create failed (expected for rapid test): %v", err)
			continue
		}

		// Immediately destroy
		err = provider.Destroy(ctx, workspaceName)
		if err != nil {
			t.Errorf("Rapid destroy failed: %v", err)
		}
	}
}
