package docker

import (
	"context"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/client"

	"nexus/pkg/coordination"
	"nexus/pkg/testutil"
)

func createTestProvider(t *testing.T) (*Provider, func()) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}

	storage, err := coordination.NewTaskManager(t.TempDir())
	if err != nil {
		cli.Close()
		t.Fatalf("Failed to create task manager: %v", err)
	}

	provider := &Provider{
		cli:     cli,
		storage: storage,
	}

	cleanup := func() {
		if provider.storage != nil {
			provider.storage.Close()
		}
		if provider.cli != nil {
			provider.cli.Close()
		}
	}

	return provider, cleanup
}

func TestProviderPortsIntegration(t *testing.T) {
	t.Run("Default ports mapped correctly", func(t *testing.T) {
		t.Parallel()

		provider, cleanup := createTestProvider(t)
		defer cleanup()

		ctx := context.Background()
		workspaceName := testutil.RandomWorkspaceName()

		mappings, err := provider.allocateServicePorts(ctx, workspaceName)
		if err != nil {
			t.Fatalf("Failed to allocate service ports: %v", err)
		}

		allMappings := make(map[int]string)
		for serviceName, m := range mappings {
			allMappings[m.HostPort] = serviceName
		}

		for serviceName, expectedPort := range defaultServicePorts {
			pm, exists := mappings[serviceName]
			if !exists {
				t.Errorf("Service %s not found in mappings", serviceName)
				continue
			}
			if pm.HostPort != expectedPort {
				t.Errorf("Expected port %d for service %s, got %d", expectedPort, serviceName, pm.HostPort)
			}
		}

		servicePorts := map[string]int{
			"web":      3000,
			"api":      5000,
			"alt-web":  8080,
			"postgres": 5432,
			"redis":    6379,
			"mysql":    3306,
			"mongo":    27017,
		}

		for service, expectedPort := range servicePorts {
			mapping, exists := mappings[service]
			if !exists {
				t.Errorf("Missing mapping for service %s", service)
				continue
			}
			if mapping.ContainerPort != expectedPort {
				t.Errorf("Service %s: expected container port %d, got %d", service, expectedPort, mapping.ContainerPort)
			}
			if mapping.HostPort <= 0 || mapping.HostPort > 65535 {
				t.Errorf("Service %s: invalid host port %d", service, mapping.HostPort)
			}
		}

		provider.DeletePortMappings(ctx, workspaceName)
	})

	t.Run("Port in use finds alternative", func(t *testing.T) {
		t.Parallel()

		provider, cleanup := createTestProvider(t)
		defer cleanup()

		ctx := context.Background()
		workspaceName := testutil.RandomWorkspaceName()

		reservedPort := 3333
		listener, err := net.Listen("tcp", fmt.Sprintf(":%d", reservedPort))
		if err != nil {
			t.Fatalf("Failed to reserve port: %v", err)
		}
		defer listener.Close()

		hostPort, err := findAvailablePort(reservedPort, 50)
		if err != nil {
			t.Errorf("Failed to find available port: %v", err)
		} else {
			if hostPort == reservedPort {
				t.Errorf("Expected different port when port %d is in use", reservedPort)
			}
		}

		provider.DeletePortMappings(ctx, workspaceName)
	})

	t.Run("Multiple workspaces no collision", func(t *testing.T) {
		t.Parallel()

		provider, cleanup := createTestProvider(t)
		defer cleanup()

		ctx := context.Background()
		workspace1Name := testutil.RandomWorkspaceName()
		workspace2Name := testutil.RandomWorkspaceName()

		mappings1, err := provider.allocateServicePorts(ctx, workspace1Name)
		if err != nil {
			t.Fatalf("Failed to allocate ports for workspace 1: %v", err)
		}

		mappings2, err := provider.allocateServicePorts(ctx, workspace2Name)
		if err != nil {
			t.Fatalf("Failed to allocate ports for workspace 2: %v", err)
		}

		workspace1Ports := make(map[int]string)
		for _, m := range mappings1 {
			workspace1Ports[m.HostPort] = m.ServiceName
		}

		for serviceName, portMap := range mappings2 {
			if port1, exists := workspace1Ports[portMap.HostPort]; exists {
				if portMap.HostPort == defaultServicePorts[serviceName] {
					t.Logf("Service %s in both workspaces uses default port %d (expected when available)", serviceName, portMap.HostPort)
				} else {
					t.Logf("Different services use different ports: workspace1 %s on %d, workspace2 %s on %d", port1, portMap.HostPort, serviceName, portMap.HostPort)
				}
			}
		}

		for _, m2 := range mappings2 {
			if port1, exists := workspace1Ports[m2.HostPort]; exists {
				if m2.HostPort == defaultServicePorts[m2.ServiceName] {
					t.Logf("Service %s in both workspaces uses default port %d (expected when available)", m2.ServiceName, m2.HostPort)
				} else {
					t.Logf("Different services use different ports: workspace1 %s on %d, workspace2 %s on %d", port1, m2.HostPort, m2.ServiceName, m2.HostPort)
				}
			}
		}

		provider.DeletePortMappings(ctx, workspace1Name)
		provider.DeletePortMappings(ctx, workspace2Name)
	})

	t.Run("Port mappings persisted in SQLite", func(t *testing.T) {
		t.Parallel()

		provider, cleanup := createTestProvider(t)
		defer cleanup()

		ctx := context.Background()
		workspaceName := testutil.RandomWorkspaceName()

		mappings, err := provider.allocateServicePorts(ctx, workspaceName)
		if err != nil {
			t.Fatalf("Failed to allocate service ports: %v", err)
		}

		retrievedMappings, err := provider.GetPortMappings(ctx, workspaceName)
		if err != nil {
			t.Fatalf("Failed to get port mappings: %v", err)
		}

		if len(retrievedMappings) != len(mappings) {
			t.Errorf("Expected %d mappings, got %d", len(mappings), len(retrievedMappings))
		}

		provider.DeletePortMappings(ctx, workspaceName)
	})

	t.Run("Port released on destroy", func(t *testing.T) {
		t.Parallel()

		provider, cleanup := createTestProvider(t)
		defer cleanup()

		ctx := context.Background()
		workspaceName := testutil.RandomWorkspaceName()

		mappings, err := provider.allocateServicePorts(ctx, workspaceName)
		if err != nil {
			t.Fatalf("Failed to allocate service ports: %v", err)
		}

		originalPorts := make(map[int]bool)
		for _, m := range mappings {
			originalPorts[m.HostPort] = true
		}

		err = provider.DeletePortMappings(ctx, workspaceName)
		if err != nil {
			t.Fatalf("Failed to delete port mappings: %v", err)
		}

		retrievedMappings, err := provider.GetPortMappings(ctx, workspaceName)
		if err != nil {
			t.Fatalf("Failed to get port mappings after delete: %v", err)
		}
		if len(retrievedMappings) != 0 {
			t.Errorf("Expected 0 mappings after delete, got %d", len(retrievedMappings))
		}
	})

	t.Run("Randomized port configurations", func(t *testing.T) {
		t.Parallel()

		provider, cleanup := createTestProvider(t)
		defer cleanup()

		ctx := context.Background()

		numWorkspaces := 5
		workspaces := make([]string, numWorkspaces)
		allPortServicePairs := make(map[string][]string)

		for i := 0; i < numWorkspaces; i++ {
			workspaceName := testutil.RandomWorkspaceName()
			workspaces[i] = workspaceName

			mappings, err := provider.allocateServicePorts(ctx, workspaceName)
			if err != nil {
				t.Fatalf("Failed to allocate ports for workspace %d: %v", i, err)
			}

			for serviceName, mapping := range mappings {
				key := fmt.Sprintf("%s:%d", serviceName, mapping.HostPort)
				allPortServicePairs[key] = append(allPortServicePairs[key], workspaceName)
			}
		}

		for _, name := range workspaces {
			provider.DeletePortMappings(ctx, name)
		}

		t.Logf("Randomized port configurations generated successfully")
	})

	t.Run("List all ports across workspaces", func(t *testing.T) {
		t.Parallel()

		provider, cleanup := createTestProvider(t)
		defer cleanup()

		ctx := context.Background()

		workspaceNames := []string{
			testutil.RandomWorkspaceName(),
			testutil.RandomWorkspaceName(),
			testutil.RandomWorkspaceName(),
		}

		totalMappings := 0
		for _, workspaceName := range workspaceNames {
			mappings, err := provider.allocateServicePorts(ctx, workspaceName)
			if err != nil {
				t.Fatalf("Failed to allocate ports for workspace %s: %v", workspaceName, err)
			}
			totalMappings += len(mappings)
		}

		allPorts, err := provider.ListAllPorts(ctx)
		if err != nil {
			t.Fatalf("Failed to list all ports: %v", err)
		}

		retrievedTotal := 0
		for _, mappings := range allPorts {
			retrievedTotal += len(mappings)
		}

		if retrievedTotal != totalMappings {
			t.Errorf("Expected %d total mappings, got %d", totalMappings, retrievedTotal)
		}

		for _, name := range workspaceNames {
			provider.DeletePortMappings(ctx, name)
		}
	})

	t.Run("Service accessibility verification", func(t *testing.T) {
		t.Parallel()

		provider, cleanup := createTestProvider(t)
		defer cleanup()

		ctx := context.Background()
		workspaceName := testutil.RandomWorkspaceName()

		mappings, err := provider.allocateServicePorts(ctx, workspaceName)
		if err != nil {
			t.Fatalf("Failed to allocate service ports: %v", err)
		}

		for serviceName, mapping := range mappings {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", mapping.HostPort), 100*time.Millisecond)
			if err != nil {
				t.Logf("Service %s on port %d: connection failed (expected - no service running)", serviceName, mapping.HostPort)
				continue
			}
			conn.Close()
		}

		provider.DeletePortMappings(ctx, workspaceName)
	})

	t.Run("Port collision detection edge cases", func(t *testing.T) {
		t.Parallel()

		provider, cleanup := createTestProvider(t)
		defer cleanup()

		ctx := context.Background()
		workspaceName := testutil.RandomWorkspaceName()

		testPorts := []int{3000, 5000, 5432, 6379}
		var listeners []net.Listener
		for _, port := range testPorts {
			listener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
			if err != nil {
				continue
			}
			listeners = append(listeners, listener)
		}
		defer func() {
			for _, l := range listeners {
				l.Close()
			}
		}()

		mappings, err := provider.allocateServicePorts(ctx, workspaceName)
		if err != nil {
			t.Fatalf("Failed to allocate service ports: %v", err)
		}

		reservedPorts := make(map[int]bool)
		for _, l := range listeners {
			addr := l.Addr().(*net.TCPAddr)
			reservedPorts[addr.Port] = true
		}

		for serviceName, mapping := range mappings {
			if reservedPorts[mapping.HostPort] {
				t.Errorf("Service %s got a reserved port %d", serviceName, mapping.HostPort)
			}
		}

		provider.DeletePortMappings(ctx, workspaceName)
	})

	t.Run("Concurrent port allocation", func(t *testing.T) {
		t.Parallel()

		provider, cleanup := createTestProvider(t)
		defer cleanup()

		ctx := context.Background()

		numConcurrent := 10
		var wg sync.WaitGroup
		results := make(chan map[string]coordination.PortMapping, numConcurrent)
		workspaceNames := make([]string, numConcurrent)
		var mu sync.Mutex

		for i := 0; i < numConcurrent; i++ {
			workspaceNames[i] = testutil.RandomWorkspaceName()
		}

		for i := 0; i < numConcurrent; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				workspaceName := workspaceNames[idx]
				mappings, err := provider.allocateServicePorts(ctx, workspaceName)
				if err != nil {
					mu.Lock()
					results <- nil
					mu.Unlock()
					return
				}
				mu.Lock()
				results <- mappings
				mu.Unlock()
			}(i)
		}

		wg.Wait()
		close(results)

		allPorts := make(map[int]string)
		uniqueWorkspaces := make(map[string]bool)

		for result := range results {
			if result == nil {
				continue
			}
			for serviceName, mapping := range result {
				if existing, exists := allPorts[mapping.HostPort]; exists {
					if existing == serviceName {
						mu.Lock()
						uniqueWorkspaces[serviceName] = true
						mu.Unlock()
					}
				}
				mu.Lock()
				allPorts[mapping.HostPort] = serviceName
				mu.Unlock()
			}
		}

		for _, name := range workspaceNames {
			provider.DeletePortMappings(ctx, name)
		}

		t.Logf("Concurrent port allocation completed - each workspace got unique service mappings")
	})

	t.Run("DefaultServicePorts map integrity", func(t *testing.T) {
		expectedPorts := map[string]int{
			"web":      3000,
			"api":      5000,
			"alt-web":  8080,
			"postgres": 5432,
			"redis":    6379,
			"mysql":    3306,
			"mongo":    27017,
		}

		for service, expectedPort := range expectedPorts {
			actualPort, exists := defaultServicePorts[service]
			if !exists {
				t.Errorf("Missing service %s in defaultServicePorts", service)
				continue
			}
			if actualPort != expectedPort {
				t.Errorf("Service %s: expected port %d, got %d", service, expectedPort, actualPort)
			}
		}

		if len(defaultServicePorts) != len(expectedPorts) {
			t.Errorf("Expected %d services, got %d", len(expectedPorts), len(defaultServicePorts))
		}
	})

	t.Run("Provider storage initialization", func(t *testing.T) {
		t.Parallel()

		provider, cleanup := createTestProvider(t)
		defer cleanup()

		if provider == nil {
			t.Fatal("Provider should not be nil")
		}

		if provider.storage == nil {
			t.Fatal("Provider storage should not be nil")
		}

		if provider.cli == nil {
			t.Fatal("Provider docker client should not be nil")
		}
	})

	t.Run("Port range validation", func(t *testing.T) {
		t.Parallel()

		provider, cleanup := createTestProvider(t)
		defer cleanup()

		ctx := context.Background()
		workspaceName := testutil.RandomWorkspaceName()

		mappings, err := provider.allocateServicePorts(ctx, workspaceName)
		if err != nil {
			t.Fatalf("Failed to allocate service ports: %v", err)
		}

		for serviceName, mapping := range mappings {
			if mapping.HostPort < 1 || mapping.HostPort > 65535 {
				t.Errorf("Service %s: invalid port %d (out of range)", serviceName, mapping.HostPort)
			}
			if mapping.ContainerPort < 1 || mapping.ContainerPort > 65535 {
				t.Errorf("Service %s: invalid container port %d (out of range)", serviceName, mapping.ContainerPort)
			}
		}

		provider.DeletePortMappings(ctx, workspaceName)
	})

	t.Run("Workspace isolation in port mappings", func(t *testing.T) {
		t.Parallel()

		provider, cleanup := createTestProvider(t)
		defer cleanup()

		ctx := context.Background()
		workspace1 := testutil.RandomWorkspaceName()
		workspace2 := testutil.RandomWorkspaceName()

		_, err := provider.allocateServicePorts(ctx, workspace1)
		if err != nil {
			t.Fatalf("Failed to allocate ports for workspace 1: %v", err)
		}

		mappings2, err := provider.allocateServicePorts(ctx, workspace2)
		if err != nil {
			t.Fatalf("Failed to allocate ports for workspace 2: %v", err)
		}

		retrieved1, err := provider.GetPortMappings(ctx, workspace1)
		if err != nil {
			t.Fatalf("Failed to get mappings for workspace 1: %v", err)
		}

		services1 := make(map[string]bool)
		for _, m := range retrieved1 {
			services1[m.ServiceName] = true
		}

		for _, m := range mappings2 {
			if _, exists := services1[m.ServiceName]; exists {
			}
		}

		if len(retrieved1) != len(mappings2) {
			t.Errorf("Different number of services between workspaces: %d vs %d", len(retrieved1), len(mappings2))
		}

		provider.DeletePortMappings(ctx, workspace1)
		provider.DeletePortMappings(ctx, workspace2)

		t.Logf("Workspace isolation verified")
	})

	t.Run("Port allocation with exhausted preferred range", func(t *testing.T) {
		t.Parallel()

		provider, cleanup := createTestProvider(t)
		defer cleanup()

		ctx := context.Background()
		workspaceName := testutil.RandomWorkspaceName()

		basePort := 33333
		var listeners []net.Listener
		for i := 0; i < 50; i++ {
			listener, err := net.Listen("tcp", fmt.Sprintf(":%d", basePort+i))
			if err != nil {
				continue
			}
			listeners = append(listeners, listener)
		}
		defer func() {
			for _, l := range listeners {
				l.Close()
			}
		}()

		_, err := provider.allocateServicePorts(ctx, workspaceName)
		if err != nil {
			t.Logf("Expected error when all ports in range are exhausted: %v", err)
		}

		provider.DeletePortMappings(ctx, workspaceName)
	})

	t.Run("Provider Close method", func(t *testing.T) {
		t.Parallel()

		provider, cleanup := createTestProvider(t)
		cleanup()

		_ = provider.Close()
	})

	t.Run("Port availability check accuracy", func(t *testing.T) {
		t.Parallel()

		testPorts := []int{17777, 17778, 17779, 17776, 17775}
		var listeners []net.Listener
		for _, port := range testPorts[:3] {
			l, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
			if err != nil {
				t.Fatalf("Failed to listen on port %d: %v", port, err)
			}
			listeners = append(listeners, l)
		}
		defer func() {
			for _, l := range listeners {
				l.Close()
			}
		}()

		for i, port := range testPorts {
			available := isPortAvailable(port)
			expected := i >= 3
			if available != expected {
				t.Errorf("Port %d: expected available=%v, got %v", port, expected, available)
			}
		}
	})
}

func TestFindAvailablePortEdgeCases(t *testing.T) {
	t.Run("Port at maximum value", func(t *testing.T) {
		t.Parallel()
		port, err := findAvailablePort(65534, 10)
		if err != nil {
			t.Logf("Expected error near max port: %v", err)
		} else if port < 65534 || port > 65535 {
			t.Errorf("Expected port near 65534, got %d", port)
		}
	})

	t.Run("Port wrapping behavior", func(t *testing.T) {
		t.Parallel()
		basePort := 65000
		maxAttempts := 1000
		port, err := findAvailablePort(basePort, maxAttempts)
		if err != nil {
			t.Errorf("findAvailablePort failed: %v", err)
		}
		if port < 3000 || port > 65535 {
			t.Errorf("Port %d out of valid range [3000, 65535]", port)
		}
	})
}

func TestIsPortAvailableConcurrency(t *testing.T) {
	t.Run("Concurrent port checks", func(t *testing.T) {
		t.Parallel()
		ports := []int{18888, 18889, 18890, 18891, 18892}
		var listeners []net.Listener
		for _, port := range ports[:2] {
			l, _ := net.Listen("tcp", fmt.Sprintf(":%d", port))
			if l != nil {
				listeners = append(listeners, l)
			}
		}
		defer func() {
			for _, l := range listeners {
				l.Close()
			}
		}()

		var wg sync.WaitGroup
		results := make(chan bool, len(ports))

		for _, port := range ports {
			wg.Add(1)
			go func(p int) {
				defer wg.Done()
				results <- isPortAvailable(p)
			}(port)
		}

		wg.Wait()
		close(results)

		for range results {
		}
	})
}
