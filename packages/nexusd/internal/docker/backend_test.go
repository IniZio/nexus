package docker

import (
	"testing"
)

func TestPortAllocation(t *testing.T) {
	pm := NewPortManager(32800, 32900)

	port1, err := pm.Allocate()
	if err != nil {
		t.Fatalf("Failed to allocate first port: %v", err)
	}

	if port1 < 32800 || port1 > 32900 {
		t.Errorf("Port %d out of range", port1)
	}

	port2, err := pm.Allocate()
	if err != nil {
		t.Fatalf("Failed to allocate second port: %v", err)
	}

	if port1 == port2 {
		t.Errorf("Allocated same port twice: %d", port1)
	}

	if err := pm.Release(port1); err != nil {
		t.Errorf("Failed to release port: %v", err)
	}

	allocatedPorts := pm.GetAllocatedPorts()
	if len(allocatedPorts) != 1 {
		t.Errorf("Expected 1 allocated port after release, got %d", len(allocatedPorts))
	}

	if allocatedPorts[0] != port2 {
		t.Errorf("Expected port %d to remain allocated, got %d", port2, allocatedPorts[0])
	}
}

func TestPortAllocationRange(t *testing.T) {
	pm := NewPortManager(32800, 32802)

	for i := 0; i < 3; i++ {
		_, err := pm.Allocate()
		if err != nil {
			t.Fatalf("Failed to allocate port %d: %v", i, err)
		}
	}

	_, err := pm.Allocate()
	if err == nil {
		t.Error("Expected error when all ports allocated")
	}
}

func TestPortAllocationSpecific(t *testing.T) {
	pm := NewPortManager(32800, 32900)

	if err := pm.AllocateSpecific(32850); err != nil {
		t.Fatalf("Failed to allocate specific port: %v", err)
	}

	if err := pm.AllocateSpecific(32850); err == nil {
		t.Error("Expected error when allocating same port twice")
	}

	if !pm.IsAllocated(32850) {
		t.Error("Port should be allocated")
	}

	if pm.IsAllocated(32851) {
		t.Error("Port should not be allocated")
	}
}

func TestWorkspaceLifecycle(t *testing.T) {
	pm := NewPortManager(32800, 32900)

	workspaces := make(map[string]int32)

	for i := 0; i < 3; i++ {
		port, err := pm.Allocate()
		if err != nil {
			t.Fatalf("Failed to allocate port: %v", err)
		}

		wsID := "workspace-1"
		workspaces[wsID] = port
	}

	if len(workspaces) != 1 {
		t.Errorf("Expected 1 workspace, got %d", len(workspaces))
	}

	port := workspaces["workspace-1"]
	if err := pm.Release(port); err != nil {
		t.Errorf("Failed to release workspace port: %v", err)
	}

	if pm.IsAllocated(port) {
		t.Error("Port should be released")
	}

	allocatedPorts := pm.GetAllocatedPorts()
	if len(allocatedPorts) != 2 {
		t.Errorf("Expected 2 remaining ports after release, got %d", len(allocatedPorts))
	}
}

func TestPortManagerConcurrency(t *testing.T) {
	pm := NewPortManager(32800, 33000)

	done := make(chan bool, 10)

	for i := 0; i < 10; i++ {
		go func() {
			_, err := pm.Allocate()
			if err != nil {
				t.Errorf("Concurrent allocation failed: %v", err)
			}
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	ports := pm.GetAllocatedPorts()
	if len(ports) != 10 {
		t.Errorf("Expected 10 allocated ports, got %d", len(ports))
	}
}
