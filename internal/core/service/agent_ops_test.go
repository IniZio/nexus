package service_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/agent/agentpb"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/fake"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/service"
)

// noGuestDialDriver implements driver.Driver but NOT driver.GuestDialer.
// It is used to test that the service correctly detects missing capabilities.
type noGuestDialDriver struct {
	driver.NoGuest
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

func TestServiceExecGoesThroughDriverPort(t *testing.T) {
	fd := fake.New()
	fd.SetExecHook(func(_ context.Context, _ domain.SandboxID, _ driver.ExecOptions) (int32, error) {
		return 7, nil
	})
	svc := newSvcWithDriver(t, fd)
	ref := createSandbox(t, svc)

	code, err := svc.Exec(context.Background(), ref, agent.ExecOptions{Argv: []string{"echo", "hi"}})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}
	calls := fd.ExecCalls()
	if len(calls) != 1 {
		t.Fatalf("ExecCalls = %d, want 1", len(calls))
	}
	if calls[0].ID.String() != ref {
		t.Errorf("sandbox id = %s, want %s", calls[0].ID, ref)
	}
	if want := []string{"echo", "hi"}; !reflect.DeepEqual(calls[0].Opts.Argv, want) {
		t.Errorf("argv = %v, want %v", calls[0].Opts.Argv, want)
	}
}

func TestServiceCopyGoesThroughDriverPort(t *testing.T) {
	fd := fake.New()
	sentinel := errors.New("copy hook")
	fd.SetCopyHook(func(_ context.Context, _ domain.SandboxID, _ driver.CopyOptions) error {
		return sentinel
	})
	svc := newSvcWithDriver(t, fd)
	ref := createSandbox(t, svc)

	err := svc.Copy(context.Background(), ref, agent.CopyOptions{
		Direction: agentpb.CopyDirection_COPY_DIRECTION_PULL,
		GuestPath: "/workspace",
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Copy err = %v, want hook error", err)
	}
	calls := fd.CopyCalls()
	if len(calls) != 1 {
		t.Fatalf("CopyCalls = %d, want 1", len(calls))
	}
	if calls[0].ID.String() != ref || calls[0].Opts.GuestPath != "/workspace" {
		t.Errorf("call = %+v", calls[0])
	}
}

func TestServiceExecUnsupportedDriver(t *testing.T) {
	svc := newSvcWithDriver(t, &noGuestDialDriver{name: "no-guest"})
	ref := createSandbox(t, svc)

	_, err := svc.Exec(context.Background(), ref, agent.ExecOptions{Argv: []string{"true"}})
	if !errors.Is(err, service.ErrNoSubstrate) || !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("err = %v, want ErrNoSubstrate and ErrUnsupported", err)
	}
}
