package workspace

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/inizio/nexus/packages/nexus/internal/domain/workspace"
)

func (s *Service) PortsList(ctx context.Context, id string) ([]int, error) {
	ws, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, workspace.ErrNotFound
	}
	ports := make([]int, len(ws.TunnelPorts))
	copy(ports, ws.TunnelPorts)
	return ports, nil
}

func (s *Service) PortsAdd(ctx context.Context, id string, port int) ([]int, error) {
	ws, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, workspace.ErrNotFound
	}
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid port: %d", port)
	}
	for _, p := range ws.TunnelPorts {
		if p == port {
			return ws.TunnelPorts, nil
		}
	}
	ws.TunnelPorts = append(ws.TunnelPorts, port)
	sort.Ints(ws.TunnelPorts)
	ws.UpdatedAt = time.Now().UTC()
	if err := s.repo.Update(ctx, ws); err != nil {
		return nil, fmt.Errorf("persist ports add: %w", err)
	}
	return ws.TunnelPorts, nil
}

func (s *Service) PortsRemove(ctx context.Context, id string, port int) ([]int, error) {
	ws, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, workspace.ErrNotFound
	}
	updated := make([]int, 0, len(ws.TunnelPorts))
	for _, p := range ws.TunnelPorts {
		if p != port {
			updated = append(updated, p)
		}
	}
	ws.TunnelPorts = updated
	ws.UpdatedAt = time.Now().UTC()
	if err := s.repo.Update(ctx, ws); err != nil {
		return nil, fmt.Errorf("persist ports remove: %w", err)
	}
	return ws.TunnelPorts, nil
}

func (s *Service) TunnelsStart(ctx context.Context, id string) error {
	_, err := s.repo.Get(ctx, id)
	if err != nil {
		return workspace.ErrNotFound
	}
	return nil
}

func (s *Service) TunnelsStop(ctx context.Context, id string) error {
	_, err := s.repo.Get(ctx, id)
	if err != nil {
		return workspace.ErrNotFound
	}
	return nil
}
