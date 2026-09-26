package sandbox

import (
	"context"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

type fakeSvc struct {
	paused  []string
	resumed []string
	stopped []string
}

func (f *fakeSvc) Pause(_ context.Context, ref string) (domain.Sandbox, error) {
	f.paused = append(f.paused, ref)
	return domain.Sandbox{}, nil
}

func (f *fakeSvc) Resume(_ context.Context, ref string) (domain.Sandbox, error) {
	f.resumed = append(f.resumed, ref)
	return domain.Sandbox{}, nil
}

func (f *fakeSvc) Stop(_ context.Context, ref string) (domain.Sandbox, error) {
	f.stopped = append(f.stopped, ref)
	return domain.Sandbox{}, nil
}

func TestServiceLifecycleDelegates(t *testing.T) {
	ctx := context.Background()
	svc := &fakeSvc{}
	lc := NewServiceLifecycle(svc)

	if err := lc.Pause(ctx, "sb-1"); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if err := lc.Resume(ctx, "sb-2"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := lc.Stop(ctx, "sb-3"); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if len(svc.paused) != 1 || svc.paused[0] != "sb-1" {
		t.Errorf("Pause delegation: %v", svc.paused)
	}
	if len(svc.resumed) != 1 || svc.resumed[0] != "sb-2" {
		t.Errorf("Resume delegation: %v", svc.resumed)
	}
	if len(svc.stopped) != 1 || svc.stopped[0] != "sb-3" {
		t.Errorf("Stop delegation: %v", svc.stopped)
	}
}
