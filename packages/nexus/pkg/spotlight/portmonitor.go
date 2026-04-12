package spotlight

import (
	"context"
	"encoding/json"
	"fmt"
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
	mgr      *Manager
	scanner  PortScanner
	interval time.Duration

	mu         sync.RWMutex
	workspaces map[string]*workspaceMonitor
}

type workspaceMonitor struct {
	workspaceID string
	ctx         context.Context
	cancel      context.CancelFunc
	lastScan    time.Time
}

// NewPortMonitor creates a new port monitor.
func NewPortMonitor(mgr *Manager, scanner PortScanner, interval time.Duration) *PortMonitor {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	return &PortMonitor{
		mgr:        mgr,
		scanner:    scanner,
		interval:   interval,
		workspaces: make(map[string]*workspaceMonitor),
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	discovered, err := pm.scanner.ScanPorts(ctx, workspaceID)
	if err != nil {
		// Log error but don't stop monitoring
		return
	}

	// Track which forwards we've seen this scan
	seenIDs := make(map[string]bool)

	for _, port := range discovered {
		if port.Port <= 0 {
			continue
		}

		// Try to auto-expose this port
		// For auto-detected ports, we use the same local and remote port by default
		spec := ExposeSpec{
			WorkspaceID: workspaceID,
			Service:     port.Process,
			RemotePort:  port.Port,
			LocalPort:   port.Port,
			Host:        "127.0.0.1",
		}

		fwd, err := pm.mgr.AutoExpose(spec)
		if err != nil {
			// Port might already be exposed or in use, try to find existing
			continue
		}
		seenIDs[fwd.ID] = true
	}

	// Update last seen for existing auto-detected forwards
	existing := pm.mgr.List(workspaceID)
	for _, fwd := range existing {
		if fwd.Source == ForwardSourceAutoDetected && seenIDs[fwd.ID] {
			pm.mgr.UpdateLastSeen(fwd.ID)
		}
	}

	// Cleanup stale auto-detected forwards (not seen in last 2 scan intervals)
	cutoff := time.Now().UTC().Add(-2 * pm.interval)
	pm.mgr.CleanupStaleAutoDetected(workspaceID, cutoff)

	pm.mu.Lock()
	if mon, exists := pm.workspaces[workspaceID]; exists {
		mon.lastScan = time.Now().UTC()
	}
	pm.mu.Unlock()
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
		if addr, ok := portMap["address"].(string); ok {
			port.Address = addr
			// Extract port from address
			if idx := strings.LastIndex(addr, ":"); idx >= 0 {
				if p, err := strconv.Atoi(addr[idx+1:]); err == nil {
					port.Port = p
				}
			}
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
