package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
)

// noGuestDialDriver implements driver.Driver but NOT driver.GuestDialer.
// It is used to test that the service correctly detects missing capabilities.
type noGuestDialDriver struct {
	name string
}

func (d *noGuestDialDriver) Name() string { return d.name }

func (d *noGuestDialDriver) Observe(_ context.Context, _ domain.SandboxID) (driver.Observation, error) {
	return driver.Observation{State: driver.Absent}, nil
}

func (d *noGuestDialDriver) Start(_ context.Context, _ driver.StartRequest) (string, error) {
	return "iid-test", nil
}

func (d *noGuestDialDriver) Stop(_ context.Context, _ domain.SandboxID) error {
	return nil
}

// newSvcWithDriver creates a Service backed by a real file store, the given
// driver, and the default lifecycle machine. Used for capability-check tests
// that don't need a functional driver.
func newSvcWithDriver(t *testing.T, drv driver.Driver) *service.Service {
	t.Helper()
	return service.New(newFileStore(t), drv, lifecycle.New())
}

// createSandbox is a helper that creates a sandbox and returns its ref string.
func createSandbox(t *testing.T, svc *service.Service) string {
	t.Helper()
	sb, err := svc.Create(context.Background(), "testproj", "testsb", service.CreateOptions{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return sb.ID.String()
}

// TestExec_NoGuestDialer verifies that Service.Exec returns an error wrapping
// ErrNoSubstrate when the driver does not implement driver.GuestDialer.
func TestExec_NoGuestDialer(t *testing.T) {
	drv := &noGuestDialDriver{name: "no-dial"}
	svc := newSvcWithDriver(t, drv)
	ref := createSandbox(t, svc)

	_, err := svc.Exec(context.Background(), ref, agent.ExecOptions{
		Argv: []string{"/bin/sh"},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, service.ErrNoSubstrate) {
		t.Errorf("error %v does not wrap ErrNoSubstrate", err)
	}
}

// TestAttach_NoGuestDialer verifies that Service.Attach returns an error
// wrapping ErrNoSubstrate when the driver does not implement GuestDialer.
func TestAttach_NoGuestDialer(t *testing.T) {
	drv := &noGuestDialDriver{name: "no-dial"}
	svc := newSvcWithDriver(t, drv)
	ref := createSandbox(t, svc)

	_, err := svc.Attach(context.Background(), ref, agent.AttachOptions{
		SessionID: "some-session",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, service.ErrNoSubstrate) {
		t.Errorf("error %v does not wrap ErrNoSubstrate", err)
	}
}

// TestCopy_NoGuestDialer verifies that Service.Copy returns an error wrapping
// ErrNoSubstrate when the driver does not implement GuestDialer.
func TestCopy_NoGuestDialer(t *testing.T) {
	drv := &noGuestDialDriver{name: "no-dial"}
	svc := newSvcWithDriver(t, drv)
	ref := createSandbox(t, svc)

	err := svc.Copy(context.Background(), ref, agent.CopyOptions{
		Direction: agentpb.CopyDirection_COPY_DIRECTION_PULL,
		GuestPath: "/workspace",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, service.ErrNoSubstrate) {
		t.Errorf("error %v does not wrap ErrNoSubstrate", err)
	}
}
