package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/service"
)

func TestEnsureDetachedSupervisor_ReconcilesRunningWithDeadSupervisor(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx := context.Background()
	svc := newTestService(t)
	sb, err := svc.Create(ctx, "proj", "dead", service.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	running, err := svc.Start(ctx, sb.ID.String())
	if err != nil {
		t.Fatal(err)
	}

	called := 0
	orig := reconcileDeadSupervisorFn
	t.Cleanup(func() { reconcileDeadSupervisorFn = orig })
	reconcileDeadSupervisorFn = func(ctx context.Context, id domain.SandboxID) error {
		called++
		_, err := svc.Stop(ctx, id.String())
		return err
	}

	err = ensureDetachedSupervisor(ctx, svc, running)
	if called != 1 {
		t.Fatalf("reconcile called %d times, want 1", called)
	}
	if err == nil || !strings.Contains(err.Error(), "no spawn spec") {
		t.Fatalf("err = %v, want to proceed past reconcile to spawn-spec check", err)
	}
}

func TestEnsureDetachedSupervisor_ErrorsWhenStillRunningAfterReconcile(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx := context.Background()
	svc := newTestService(t)
	sb, _ := svc.Create(ctx, "proj", "still", service.CreateOptions{})
	running, err := svc.Start(ctx, sb.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	orig := reconcileDeadSupervisorFn
	t.Cleanup(func() { reconcileDeadSupervisorFn = orig })
	reconcileDeadSupervisorFn = func(context.Context, domain.SandboxID) error { return nil }

	err = ensureDetachedSupervisor(ctx, svc, running)
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("err = %v, want still-running error", err)
	}
}
