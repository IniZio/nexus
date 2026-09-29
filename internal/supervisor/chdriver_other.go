//go:build !linux

package supervisor

import (
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver/registry"
	"github.com/IniZio/nexus/internal/core/resize"
)

func newSupervisorDriver(Config, uint32, uint32) (supervisorDriver, error) {
	return nil, registry.Unsupported(registry.CloudHypervisor, "supervisor")
}

func newSandboxResizer(supervisorDriver, domain.SandboxID, resize.Bounds, int64, int32) (supervisorResizer, error) {
	return nil, registry.Unsupported(registry.CloudHypervisor, "supervisor")
}
