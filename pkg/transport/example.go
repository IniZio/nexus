package transport

import (
	"context"
	"fmt"
	"log"
	"time"
)

// IntegrationExample demonstrates transport layer usage
func IntegrationExample() {
	// Create transport manager
	manager := NewManager()

	// Register SSH configuration
	sshConfig := CreateDefaultSSHConfig("localhost:22", "user", "/home/user/.ssh/id_rsa")
	err := manager.RegisterConfig("ssh-node", sshConfig)
	if err != nil {
		log.Printf("Failed to register SSH config: %v", err)
		return
	}

	// Register HTTP configuration
	httpConfig := CreateDefaultHTTPConfig("https://api.example.com:8080", "api-token-123")
	err = manager.RegisterConfig("http-api", httpConfig)
	if err != nil {
		log.Printf("Failed to register HTTP config: %v", err)
		return
	}

	// Create transport pools for connection reuse
	sshPool, err := manager.CreatePool("ssh-node")
	if err != nil {
		log.Printf("Failed to create SSH pool: %v", err)
		return
	}
	defer sshPool.Close()

	httpPool, err := manager.CreatePool("http-api")
	if err != nil {
		log.Printf("Failed to create HTTP pool: %v", err)
		return
	}
	defer httpPool.Close()

	// Use SSH transport for command execution
	ctx := context.Background()
	sshTransport, err := sshPool.Get(ctx, "server.example.com:22")
	if err != nil {
		log.Printf("Failed to get SSH transport: %v", err)
		return
	}

	result, err := sshTransport.Execute(ctx, &Command{
		Cmd:           []string{"ls", "-la", "/tmp"},
		CaptureOutput: true,
		Timeout:       30 * time.Second,
	})
	if err != nil {
		log.Printf("SSH command failed: %v", err)
	} else {
		fmt.Printf("SSH command result: %s (exit code: %d)\n", result.Output, result.ExitCode)
	}

	// Return transport to pool
	err = sshTransport.Disconnect(ctx)
	if err != nil {
		log.Printf("Failed to return SSH transport to pool: %v", err)
	}

	// Use HTTP transport for API calls
	httpTransport, err := httpPool.Get(ctx, "")
	if err != nil {
		log.Printf("Failed to get HTTP transport: %v", err)
		return
	}

	apiResult, err := httpTransport.Execute(ctx, &Command{
		Cmd:           []string{"echo", "Hello, API!"},
		CaptureOutput: true,
	})
	if err != nil {
		log.Printf("HTTP API call failed: %v", err)
	} else {
		fmt.Printf("HTTP API result: %s (duration: %v)\n", apiResult.Output, apiResult.Duration)
	}

	// Return transport to pool
	err = httpTransport.Disconnect(ctx)
	if err != nil {
		log.Printf("Failed to return HTTP transport to pool: %v", err)
	}

	// Print pool metrics
	sshMetrics := sshPool.GetMetrics()
	fmt.Printf("SSH Pool Metrics: Created=%d, Destroyed=%d, Active=%d, Idle=%d, Reused=%d\n",
		sshMetrics.Created, sshMetrics.Destroyed, sshMetrics.Active, sshMetrics.Idle, sshMetrics.TotalReused)

	httpMetrics := httpPool.GetMetrics()
	fmt.Printf("HTTP Pool Metrics: Created=%d, Destroyed=%d, Active=%d, Idle=%d, Reused=%d\n",
		httpMetrics.Created, httpMetrics.Destroyed, httpMetrics.Active, httpMetrics.Idle, httpMetrics.TotalReused)

	// Demonstrate file operations
	sshTransport, err = sshPool.Get(ctx, "server.example.com:22")
	if err == nil {
		err = sshTransport.Upload(ctx, "/tmp/local.txt", "/tmp/remote.txt")
		if err != nil {
			log.Printf("Upload failed: %v", err)
		} else {
			fmt.Println("File uploaded successfully")
		}

		err = sshTransport.Download(ctx, "/tmp/remote.txt", "/tmp/downloaded.txt")
		if err != nil {
			log.Printf("Download failed: %v", err)
		} else {
			fmt.Println("File downloaded successfully")
		}

		sshTransport.Disconnect(ctx)
	}

	fmt.Println("Transport layer integration example completed")
}

// CoordinationServerExample shows how to integrate with coordination server
func CoordinationServerExample() {
	// Create transport for node communication
	manager := NewManager()

	// Register node configurations
	nodeConfigs := map[string]*Config{
		"node1": CreateDefaultSSHConfig("node1.example.com:22", "nexus", "/home/nexus/.ssh/id_rsa"),
		"node2": CreateDefaultSSHConfig("node2.example.com:22", "nexus", "/home/nexus/.ssh/id_rsa"),
		"api":   CreateDefaultHTTPConfig("https://coordinator.example.com:3001", "coordination-token"),
	}

	for name, config := range nodeConfigs {
		err := manager.RegisterConfig(name, config)
		if err != nil {
			log.Printf("Failed to register config for %s: %v", name, err)
			continue
		}
	}

	// Create pools for efficient communication
	pools := make(map[string]*Pool)
	for name := range nodeConfigs {
		pool, err := manager.CreatePool(name)
		if err != nil {
			log.Printf("Failed to create pool for %s: %v", name, err)
			continue
		}
		defer pool.Close()
		pools[name] = pool
	}

	// Example: Execute command on multiple nodes in parallel
	ctx := context.Background()
	results := make(chan *Result, len(nodeConfigs))

	for name, pool := range pools {
		go func(nodeName string, p *Pool) {
			transport, err := p.Get(ctx, "")
			if err != nil {
				log.Printf("Failed to get transport for %s: %v", nodeName, err)
				return
			}
			defer transport.Disconnect(ctx)

			// Execute command based on transport type
			var result *Result
			if nodeName == "api" {
				result, err = transport.Execute(ctx, &Command{
					Cmd:           []string{"GET", "/api/v1/nodes"},
					CaptureOutput: true,
				})
			} else {
				result, err = transport.Execute(ctx, &Command{
					Cmd:           []string{"ps", "aux"},
					CaptureOutput: true,
				})
			}

			if err != nil {
				log.Printf("Command failed on %s: %v", nodeName, err)
				return
			}

			results <- result
		}(name, pool)
	}

	// Collect results
	for i := 0; i < len(nodeConfigs); i++ {
		result := <-results
		fmt.Printf("Command completed with exit code %d: %.100s...\n", result.ExitCode, result.Output)
	}

	fmt.Println("Coordination server example completed")
}
