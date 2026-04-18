package spotlight

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PortScanner defines the interface for scanning ports in a workspace.
type PortScanner interface {
	ScanPorts(ctx context.Context, workspaceID string) ([]DiscoveredPort, error)
}

// DiscoveredPort represents a listening port discovered in a workspace.
type DiscoveredPort struct {
	Address string `json:"address"` // e.g., "0.0.0.0:8080"
	Port    int    `json:"port"`
	Process string `json:"process,omitempty"` // process name if available
}

// PortMonitor periodically scans workspaces for listening ports and auto-exposes them.
type PortMonitor struct {
	scanner  PortScanner
	interval time.Duration

	mu         sync.RWMutex
	workspaces map[string]*workspaceMonitor
	latest     map[string][]DiscoveredPort
}

type workspaceMonitor struct {
	workspaceID string
	ctx         context.Context
	cancel      context.CancelFunc
	lastScan    time.Time
}

// NewPortMonitor creates a new port monitor.
func NewPortMonitor(_ *Manager, scanner PortScanner, interval time.Duration) *PortMonitor {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &PortMonitor{
		scanner:    scanner,
		interval:   interval,
		workspaces: make(map[string]*workspaceMonitor),
		latest:     make(map[string][]DiscoveredPort),
	}
}

// StartWorkspace begins monitoring a workspace for listening ports.
func (pm *PortMonitor) StartWorkspace(workspaceID string) error {
	if workspaceID == "" {
		return fmt.Errorf("workspaceID is required")
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	if _, exists := pm.workspaces[workspaceID]; exists {
		return nil // already monitoring
	}

	ctx, cancel := context.WithCancel(context.Background())
	pm.workspaces[workspaceID] = &workspaceMonitor{
		workspaceID: workspaceID,
		ctx:         ctx,
		cancel:      cancel,
	}

	go pm.monitorLoop(ctx, workspaceID)
	return nil
}

// StopWorkspace stops monitoring a workspace.
func (pm *PortMonitor) StopWorkspace(workspaceID string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if mon, exists := pm.workspaces[workspaceID]; exists {
		mon.cancel()
		delete(pm.workspaces, workspaceID)
	}
}

// IsMonitoring returns true if the workspace is being monitored.
func (pm *PortMonitor) IsMonitoring(workspaceID string) bool {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	_, exists := pm.workspaces[workspaceID]
	return exists
}

// ListDiscovered returns the latest discovered ports for a workspace.
func (pm *PortMonitor) ListDiscovered(workspaceID string) []DiscoveredPort {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	ports := pm.latest[workspaceID]
	out := make([]DiscoveredPort, len(ports))
	copy(out, ports)
	return out
}

func (pm *PortMonitor) monitorLoop(ctx context.Context, workspaceID string) {
	ticker := time.NewTicker(pm.interval)
	defer ticker.Stop()

	// Initial scan immediately
	pm.scanWorkspace(workspaceID)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pm.scanWorkspace(workspaceID)
		}
	}
}

func (pm *PortMonitor) scanWorkspace(workspaceID string) {
	log.Printf("[PortMonitor] Starting scan for workspace: %s", workspaceID)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	discovered, err := pm.scanner.ScanPorts(ctx, workspaceID)
	if err != nil {
		log.Printf("[PortMonitor] ScanPorts error for workspace %s: %v", workspaceID, err)
		return
	}

	log.Printf("[PortMonitor] Discovered %d ports for workspace %s: %+v", len(discovered), workspaceID, discovered)

	filtered := make([]DiscoveredPort, 0, len(discovered))
	for _, port := range discovered {
		if port.Port <= 0 {
			continue
		}
		filtered = append(filtered, port)
	}

	pm.mu.Lock()
	if mon, exists := pm.workspaces[workspaceID]; exists {
		mon.lastScan = time.Now().UTC()
	}
	pm.latest[workspaceID] = filtered
	pm.mu.Unlock()

	log.Printf("[PortMonitor] Scan complete for workspace %s", workspaceID)
}

// ShellPortScanner implements PortScanner using the shell protocol.
type ShellPortScanner struct {
	agentConnFn func(ctx context.Context, workspaceID string) (net.Conn, error)
}

// NewShellPortScanner creates a new shell-based port scanner.
func NewShellPortScanner(agentConnFn func(ctx context.Context, workspaceID string) (net.Conn, error)) *ShellPortScanner {
	return &ShellPortScanner{agentConnFn: agentConnFn}
}

// ScanPorts scans for listening ports using the shell protocol.
func (s *ShellPortScanner) ScanPorts(ctx context.Context, workspaceID string) ([]DiscoveredPort, error) {
	log.Printf("[PortMonitor] ShellPortScanner: Getting agent connection for workspace %s", workspaceID)
	conn, err := s.agentConnFn(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("get agent connection: %w", err)
	}
	defer conn.Close()

	// Send port scan request
	req := map[string]any{
		"type": "ports.scan",
		"id":   fmt.Sprintf("scan-%d", time.Now().UnixNano()),
	}

	enc := json.NewEncoder(conn)
	dec := json.NewDecoder(conn)

	log.Printf("[PortMonitor] ShellPortScanner: Sending scan request for workspace %s", workspaceID)
	if err := enc.Encode(req); err != nil {
		return nil, fmt.Errorf("send scan request: %w", err)
	}

	// Read response with timeout
	resultCh := make(chan []DiscoveredPort, 1)
	errCh := make(chan error, 1)

	go func() {
		for {
			var resp map[string]any
			if err := dec.Decode(&resp); err != nil {
				errCh <- fmt.Errorf("decode response: %w", err)
				return
			}

			respType, _ := resp["type"].(string)
			if respType == "ports.result" {
				ports := parsePortsResult(resp)
				log.Printf("[PortMonitor] ShellPortScanner: Received %d ports from agent for workspace %s", len(ports), workspaceID)
				resultCh <- ports
				return
			}
		}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-errCh:
		return nil, err
	case ports := <-resultCh:
		return ports, nil
	}
}

func parsePortsResult(resp map[string]any) []DiscoveredPort {
	portsData, ok := resp["ports"].([]any)
	if !ok {
		return nil
	}

	var ports []DiscoveredPort
	for _, p := range portsData {
		portMap, ok := p.(map[string]any)
		if !ok {
			continue
		}

		port := DiscoveredPort{}
		
		// Check for direct port field first
		if pVal, ok := portMap["port"]; ok {
			switch v := pVal.(type) {
			case int:
				port.Port = v
			case float64:
				port.Port = int(v)
			case string:
				if parsed, err := strconv.Atoi(v); err == nil {
					port.Port = parsed
				}
			}
		}
		
		// Fall back to address parsing if port not found
		if port.Port == 0 {
			if addr, ok := portMap["address"].(string); ok {
				port.Address = addr
				// Extract port from address
				if idx := strings.LastIndex(addr, ":"); idx >= 0 {
					if p, err := strconv.Atoi(addr[idx+1:]); err == nil {
						port.Port = p
					}
				}
			}
		} else if addr, ok := portMap["address"].(string); ok {
			// Port was found directly, but still capture address
			port.Address = addr
		}
		
		if proc, ok := portMap["process"].(string); ok {
			port.Process = proc
		}
		if port.Port > 0 {
			ports = append(ports, port)
		}
	}
	return ports
}
