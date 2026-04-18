package server

import (
	"context"
	"fmt"
	"sort"
	"strings"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
	"github.com/inizio/nexus/packages/nexus/pkg/spotlight"
	"github.com/inizio/nexus/packages/nexus/pkg/workspacemgr"
)

type WorkspacePortState struct {
	Port       int    `json:"port"`
	RemotePort int    `json:"remotePort"`
	Process    string `json:"process,omitempty"`
	Preferred  bool   `json:"preferred"`
	Tunneled   bool   `json:"tunneled"`
}

func (s *Server) WorkspacePortStates(workspaceID string) ([]WorkspacePortState, string) {
	stateByPort := map[int]*WorkspacePortState{}

	if s.portMonitor != nil {
		for _, p := range s.portMonitor.ListDiscovered(workspaceID) {
			if p.Port <= 0 || p.Port > 65535 {
				continue
			}
			stateByPort[p.Port] = &WorkspacePortState{
				Port:       p.Port,
				RemotePort: p.Port,
				Process:    strings.TrimSpace(p.Process),
			}
		}
	}

	s.mu.RLock()
	for hostPort, targetPort := range s.composePortHints[workspaceID] {
		if hostPort <= 0 || hostPort > 65535 || targetPort <= 0 || targetPort > 65535 {
			continue
		}
		existing, ok := stateByPort[hostPort]
		if !ok {
			stateByPort[hostPort] = &WorkspacePortState{
				Port:       hostPort,
				RemotePort: targetPort,
			}
			continue
		}
		existing.RemotePort = targetPort
	}
	activeWorkspaceID := s.activeTunnelWorkspace
	s.mu.RUnlock()

	if ws, ok := s.workspaceMgr.Get(workspaceID); ok {
		if len(ws.TunnelPorts) == 0 && len(stateByPort) > 0 {
			defaultPorts := make([]int, 0, len(stateByPort))
			for p := range stateByPort {
				if p > 0 && p <= 65535 {
					defaultPorts = append(defaultPorts, p)
				}
			}
			sort.Slice(defaultPorts, func(i, j int) bool { return defaultPorts[i] < defaultPorts[j] })
			if len(defaultPorts) > 0 {
				if err := s.workspaceMgr.SetTunnelPorts(workspaceID, defaultPorts); err == nil {
					ws.TunnelPorts = defaultPorts
				}
			}
		}
		for _, p := range ws.TunnelPorts {
			if p <= 0 || p > 65535 {
				continue
			}
			entry, ok := stateByPort[p]
			if !ok {
				entry = &WorkspacePortState{Port: p, RemotePort: s.composeTargetPort(workspaceID, p)}
				stateByPort[p] = entry
			}
			entry.Preferred = true
		}
	}

	for _, fwd := range s.spotlightMgr.List(workspaceID) {
		entry, ok := stateByPort[fwd.LocalPort]
		if !ok {
			entry = &WorkspacePortState{Port: fwd.LocalPort, RemotePort: fwd.RemotePort}
			stateByPort[fwd.LocalPort] = entry
		}
		entry.Tunneled = true
		if entry.RemotePort == 0 {
			entry.RemotePort = fwd.RemotePort
		}
	}

	items := make([]WorkspacePortState, 0, len(stateByPort))
	for _, st := range stateByPort {
		items = append(items, *st)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Port < items[j].Port })
	return items, activeWorkspaceID
}

func (s *Server) SetWorkspaceTunnelPreference(workspaceID string, port int, enabled bool) error {
	ws, ok := s.workspaceMgr.Get(workspaceID)
	if !ok {
		return fmt.Errorf("workspace not found")
	}
	ports := append([]int(nil), ws.TunnelPorts...)
	if enabled {
		ports = append(ports, port)
	} else {
		next := make([]int, 0, len(ports))
		for _, p := range ports {
			if p != port {
				next = append(next, p)
			}
		}
		ports = next
	}
	if err := s.workspaceMgr.SetTunnelPorts(workspaceID, ports); err != nil {
		return err
	}

	s.mu.RLock()
	activeWorkspace := s.activeTunnelWorkspace
	s.mu.RUnlock()
	if activeWorkspace == workspaceID {
		if enabled {
			return s.ensureTunnelPort(workspaceID, port)
		}
		s.closeTunnelPort(workspaceID, port)
	}
	return nil
}

func (s *Server) ensureTunnelPort(workspaceID string, port int) error {
	remotePort := s.composeTargetPort(workspaceID, port)
	_, err := s.spotlightMgr.Expose(context.Background(), spotlight.ExposeSpec{
		WorkspaceID: workspaceID,
		Service:     "",
		RemotePort:  remotePort,
		LocalPort:   port,
		Host:        "127.0.0.1",
	})
	if err != nil && !strings.Contains(err.Error(), "already in use") {
		return err
	}
	return nil
}

func (s *Server) closeTunnelPort(workspaceID string, port int) {
	for _, fwd := range s.spotlightMgr.List(workspaceID) {
		if fwd.LocalPort == port {
			_ = s.spotlightMgr.Close(fwd.ID)
		}
	}
}

func (s *Server) StartWorkspaceTunnels(workspaceID string) error {
	s.mu.Lock()
	existing := s.activeTunnelWorkspace
	s.mu.Unlock()

	if existing != "" && existing != workspaceID {
		s.StopWorkspaceTunnels(existing)
	}

	s.mu.Lock()
	s.activeTunnelWorkspace = workspaceID
	s.mu.Unlock()

	ws, ok := s.workspaceMgr.Get(workspaceID)
	if !ok {
		return fmt.Errorf("workspace not found")
	}
	for _, p := range ws.TunnelPorts {
		if p <= 0 || p > 65535 {
			continue
		}
		if err := s.ensureTunnelPort(workspaceID, p); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) StopWorkspaceTunnels(workspaceID string) {
	for _, fwd := range s.spotlightMgr.List(workspaceID) {
		_ = s.spotlightMgr.Close(fwd.ID)
	}
	s.mu.Lock()
	if s.activeTunnelWorkspace == workspaceID {
		s.activeTunnelWorkspace = ""
	}
	s.mu.Unlock()
}

func (s *Server) requireWorkspaceStarted(workspaceID string) *rpckit.RPCError {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return rpckit.ErrInvalidParams
	}

	wsRecord, ok := s.workspaceMgr.Get(workspaceID)
	if !ok {
		return rpckit.ErrWorkspaceNotFound
	}
	if wsRecord.State != workspacemgr.StateRunning {
		return rpckit.ErrWorkspaceNotStarted
	}

	return nil
}
