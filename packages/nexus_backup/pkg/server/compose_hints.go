package server

import (
	"context"
	"log"

	"github.com/inizio/nexus/packages/nexus/pkg/compose"
)

func (s *Server) ensureComposeHints(ctx context.Context, workspaceID, rootPath string) {
	if workspaceID == "" || rootPath == "" {
		return
	}

	s.mu.Lock()
	if s.autoComposeForwards[workspaceID] {
		s.mu.Unlock()
		return
	}
	s.autoComposeForwards[workspaceID] = true
	s.mu.Unlock()

	published, err := compose.DiscoverPublishedPorts(ctx, rootPath)
	if err != nil {
		if err == compose.ErrComposeFileNotFound {
			return
		}
		log.Printf("[ports] failed to inspect compose ports for %s: %v", workspaceID, err)
		s.mu.Lock()
		s.autoComposeForwards[workspaceID] = false
		s.mu.Unlock()
		return
	}
	hints := make(map[int]int, len(published))
	for _, p := range published {
		if p.HostPort <= 0 || p.HostPort > 65535 || p.TargetPort <= 0 || p.TargetPort > 65535 {
			continue
		}
		hints[p.HostPort] = p.TargetPort
	}
	s.mu.Lock()
	if len(hints) > 0 {
		s.composePortHints[workspaceID] = hints
	}
	s.mu.Unlock()
}

func (s *Server) composeTargetPort(workspaceID string, port int) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if byWorkspace := s.composePortHints[workspaceID]; byWorkspace != nil {
		if target, ok := byWorkspace[port]; ok && target > 0 {
			return target
		}
	}
	return port
}
