package sandbox

import (
	"context"

	"github.com/IniZio/nexus/internal/controller"
	"github.com/IniZio/nexus/internal/core/domain"
)

// LifecycleService abstracts the Pause/Resume/Stop methods of service.Service.
type LifecycleService interface {
	Pause(ctx context.Context, ref string) (domain.Sandbox, error)
	Resume(ctx context.Context, ref string) (domain.Sandbox, error)
	Stop(ctx context.Context, ref string) (domain.Sandbox, error)
}

// ServiceLifecycle wraps a LifecycleService as a controller.SandboxLifecycle.
type ServiceLifecycle struct {
	svc LifecycleService
}

var _ controller.SandboxLifecycle = (*ServiceLifecycle)(nil)

func NewServiceLifecycle(svc LifecycleService) *ServiceLifecycle {
	return &ServiceLifecycle{svc: svc}
}

func (l *ServiceLifecycle) Pause(ctx context.Context, sandboxID string) error {
	_, err := l.svc.Pause(ctx, sandboxID)
	return err
}

func (l *ServiceLifecycle) Resume(ctx context.Context, sandboxID string) error {
	_, err := l.svc.Resume(ctx, sandboxID)
	return err
}

func (l *ServiceLifecycle) Stop(ctx context.Context, sandboxID string) error {
	_, err := l.svc.Stop(ctx, sandboxID)
	return err
}
