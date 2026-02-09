package coordination

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nexus/nexus/pkg/paths"
)

// ServerInfo contains server runtime information
type ServerInfo struct {
	StartTime   time.Time `json:"start_time"`
	PID         int       `json:"pid"`
	Version     string    `json:"version"`
	GoVersion   string    `json:"go_version"`
	Host        string    `json:"host"`
	Port        int       `json:"port"`
	NodeCount   int       `json:"node_count"`
	ClientCount int       `json:"client_count"`
}

// StartServer starts the coordination server with proper lifecycle management
func StartServer(configPath string) error {
	projectRoot := paths.GetProjectRoot()

	if dbPath := os.Getenv("DB_PATH"); dbPath == "" {
		dbPath = paths.GetDatabasePath(projectRoot)
		os.Setenv("DB_PATH", dbPath)

		if err := paths.EnsureDir(paths.GetDataDir(projectRoot)); err != nil {
			return fmt.Errorf("failed to create data directory: %w", err)
		}
	}

	// Load configuration
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Create server instance
	srv := NewServer(cfg)

	// Start server in goroutine
	serverErr := make(chan error, 1)
	go func() {
		log.Printf("Starting coordination server on %s:%d", cfg.Server.Host, cfg.Server.Port)
		if err := srv.Start(); err != nil && err != http.ErrServerClosed {
			serverErr <- fmt.Errorf("server failed to start: %w", err)
		}
	}()

	// Setup signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Wait for either server error or shutdown signal
	select {
	case err := <-serverErr:
		return err
	case sig := <-sigCh:
		log.Printf("Received signal %v, shutting down gracefully", sig)

		// Create shutdown context with timeout
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		// Shutdown server
		if err := srv.Stop(shutdownCtx); err != nil {
			log.Printf("Error during server shutdown: %v", err)
			return err
		}

		log.Println("Server shutdown complete")
		return nil
	}
}

// GetServerInfo returns server runtime information
func (s *Server) GetServerInfo() (*ServerInfo, error) {
	nodes, err := s.registry.List()
	if err != nil {
		return nil, err
	}

	s.clientsMu.Lock()
	clientCount := len(s.clients)
	s.clientsMu.Unlock()

	info := &ServerInfo{
		StartTime:   time.Now(), // This should be set at server start
		PID:         os.Getpid(),
		Version:     "1.0.0",
		GoVersion:   "1.24",
		Host:        s.config.Server.Host,
		Port:        s.config.Server.Port,
		NodeCount:   len(nodes),
		ClientCount: clientCount,
	}

	return info, nil
}

// ValidateConfig validates the server configuration
func ValidateConfig(cfg *Config) error {
	if cfg.Server.Port < 1 || cfg.Server.Port > 65535 {
		return fmt.Errorf("server port must be between 1 and 65535, got %d", cfg.Server.Port)
	}

	if cfg.Server.Host == "" {
		return fmt.Errorf("server host cannot be empty")
	}

	if cfg.Registry.MaxRetries < 0 {
		return fmt.Errorf("registry max_retries must be non-negative, got %d", cfg.Registry.MaxRetries)
	}

	if cfg.Auth.Enabled {
		if cfg.Auth.JWTSecret == "" {
			return fmt.Errorf("JWT secret is required when auth is enabled")
		}
		if len(cfg.Auth.JWTSecret) < 16 {
			return fmt.Errorf("JWT secret must be at least 16 characters long")
		}
	}

	return nil
}

// CheckPortAvailable checks if a port is available for binding
func CheckPortAvailable(host string, port int) error {
	address := fmt.Sprintf("%s:%d", host, port)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("port %d is not available: %w", port, err)
	}
	listener.Close()
	return nil
}

// GenerateDefaultConfig generates a default configuration file
func GenerateDefaultConfig(path string) error {
	cfg := &Config{}
	cfg.Server.Host = "0.0.0.0"
	cfg.Server.Port = 3001
	cfg.Server.AuthToken = "nexus-coordination-token"
	cfg.Server.ReadTimeout = "30s"
	cfg.Server.WriteTimeout = "30s"
	cfg.Server.IdleTimeout = "120s"

	cfg.Registry.Provider = "memory"
	cfg.Registry.SyncInterval = "30s"
	cfg.Registry.HealthCheckInterval = "10s"
	cfg.Registry.NodeTimeout = "60s"
	cfg.Registry.MaxRetries = 3

	cfg.WebSocket.Enabled = true
	cfg.WebSocket.Path = "/ws"
	cfg.WebSocket.Origins = []string{"*"}
	cfg.WebSocket.PingPeriod = "30s"

	cfg.Auth.Enabled = false
	cfg.Auth.JWTSecret = "nexus-jwt-secret-key-minimum-16-chars"
	cfg.Auth.TokenExpiry = "24h"
	cfg.Auth.AllowedIPs = []string{}

	cfg.Logging.Level = "info"
	cfg.Logging.Format = "json"
	cfg.Logging.Output = "stdout"
	cfg.Logging.MaxSize = 100
	cfg.Logging.MaxBackups = 3
	cfg.Logging.MaxAge = 28

	return SaveConfig(cfg, path)
}

// BackupRegistry creates a backup of the current registry state
func (s *Server) BackupRegistry() ([]byte, error) {
	nodes, err := s.registry.List()
	if err != nil {
		return nil, err
	}

	backup := map[string]interface{}{
		"timestamp": time.Now(),
		"version":   "1.0",
		"nodes":     nodes,
	}

	return json.MarshalIndent(backup, "", "  ")
}

// RestoreRegistry restores registry state from backup
func (s *Server) RestoreRegistry(backupData []byte) error {
	var backup map[string]interface{}
	if err := json.Unmarshal(backupData, &backup); err != nil {
		return fmt.Errorf("failed to parse backup data: %w", err)
	}

	nodesData, ok := backup["nodes"].([]interface{})
	if !ok {
		return fmt.Errorf("invalid backup format: missing nodes data")
	}

	// Clear existing registry
	if memRegistry, ok := s.registry.(*InMemoryRegistry); ok {
		memRegistry.nodeMutex.Lock()
		memRegistry.nodes = make(map[string]*Node)
		memRegistry.nodeMutex.Unlock()
	}

	// Restore nodes
	for _, nodeData := range nodesData {
		nodeJSON, _ := json.Marshal(nodeData)
		var node Node
		if err := json.Unmarshal(nodeJSON, &node); err != nil {
			log.Printf("Failed to restore node from backup: %v", err)
			continue
		}

		if err := s.registry.Register(&node); err != nil {
			log.Printf("Failed to register restored node %s: %v", node.ID, err)
			continue
		}
	}

	log.Printf("Registry restored from backup with %d nodes", len(nodesData))
	return nil
}

// GetStats returns runtime statistics
func (s *Server) GetStats() map[string]interface{} {
	nodes, _ := s.registry.List()

	statusCounts := make(map[string]int)
	providerCounts := make(map[string]int)

	for _, node := range nodes {
		statusCounts[node.Status]++
		providerCounts[node.Provider]++
	}

	s.clientsMu.Lock()
	clientCount := len(s.clients)
	s.clientsMu.Unlock()

	stats := map[string]interface{}{
		"timestamp":         time.Now(),
		"uptime":            time.Since(time.Now()), // Should track actual start time
		"total_nodes":       len(nodes),
		"connected_clients": clientCount,
		"nodes_by_status":   statusCounts,
		"nodes_by_provider": providerCounts,
		"server_version":    "1.0.0",
		"go_version":        "1.24",
	}

	return stats
}

// HealthCheck performs a comprehensive health check
func (s *Server) HealthCheck() map[string]interface{} {
	health := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now(),
		"checks":    make(map[string]interface{}),
	}

	checks := health["checks"].(map[string]interface{})

	// Check registry
	nodes, err := s.registry.List()
	if err != nil {
		health["status"] = "unhealthy"
		checks["registry"] = map[string]interface{}{
			"status": "error",
			"error":  err.Error(),
		}
	} else {
		checks["registry"] = map[string]interface{}{
			"status":       "ok",
			"total_nodes":  len(nodes),
			"active_nodes": s.countActiveNodes(nodes),
		}
	}

	// Check HTTP server (basic connectivity)
	if s.httpSrv != nil {
		checks["http_server"] = map[string]interface{}{
			"status": "ok",
			"addr":   s.httpSrv.Addr,
		}
	} else {
		health["status"] = "unhealthy"
		checks["http_server"] = map[string]interface{}{
			"status": "error",
			"error":  "HTTP server not initialized",
		}
	}

	// Check client connections
	s.clientsMu.Lock()
	clientCount := len(s.clients)
	s.clientsMu.Unlock()
	checks["websocket"] = map[string]interface{}{
		"status":            "ok",
		"connected_clients": clientCount,
		"websocket_enabled": s.config.WebSocket.Enabled,
	}

	return health
}

func (s *Server) countActiveNodes(nodes []*Node) int {
	count := 0
	for _, node := range nodes {
		if node.Status == "active" {
			count++
		}
	}
	return count
}
