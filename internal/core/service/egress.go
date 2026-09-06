package service

import (
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter"
)

// GetPerimeterSupervisor returns the live PerimeterSupervisor for id, or nil
// when no supervisor is registered (e.g. the sandbox uses no egress perimeter,
// or Start has not yet been called).
//
// The caller must not call Close on the returned supervisor — ownership stays
// with the Service. Read-only operations (AllowEgress, MitmAddr, CACert) are
// safe to call on the returned pointer while the sandbox is running.
func (s *Service) GetPerimeterSupervisor(id domain.SandboxID) *perimeter.PerimeterSupervisor {
	s.supervisorsMu.Lock()
	defer s.supervisorsMu.Unlock()
	return s.supervisors[id]
}
