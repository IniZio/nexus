package runtime

import (
	"context"
	"testing"
)

type stubDriver struct{ backend string }

func (d *stubDriver) Backend() string                                 { return d.backend }
func (d *stubDriver) Create(_ context.Context, _ CreateRequest) error { return nil }
func (d *stubDriver) Start(_ context.Context, _ string) error         { return nil }
func (d *stubDriver) Stop(_ context.Context, _ string) error          { return nil }
func (d *stubDriver) Restore(_ context.Context, _ string) error       { return nil }
func (d *stubDriver) Pause(_ context.Context, _ string) error         { return nil }
func (d *stubDriver) Resume(_ context.Context, _ string) error        { return nil }
func (d *stubDriver) Fork(_ context.Context, _, _ string) error       { return nil }
func (d *stubDriver) Destroy(_ context.Context, _ string) error       { return nil }

func TestSelectDriverLinuxDoesNotFallbackToProcessWhenFirecrackerUnavailable(t *testing.T) {
	f := NewFactory([]Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: false},
		{Name: "runtime.process", Available: true},
	}, map[string]Driver{
		"process": &stubDriver{backend: "process"},
	})

	if _, err := f.SelectDriver([]string{"linux"}, nil); err == nil {
		t.Fatal("expected linux requirement to fail when firecracker is unavailable")
	}
}

func TestSelectDriverPrefersFirecrackerOnLinux(t *testing.T) {
	f := NewFactory([]Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: true},
		{Name: "runtime.process", Available: true},
	}, map[string]Driver{
		"firecracker": &stubDriver{backend: "firecracker"},
		"process":     &stubDriver{backend: "process"},
	})

	d, err := f.SelectDriver([]string{"firecracker"}, nil)
	if err != nil {
		t.Fatalf("select firecracker driver: %v", err)
	}
	if d.Backend() != "firecracker" {
		t.Fatalf("expected firecracker backend, got %q", d.Backend())
	}
}

func TestSelectDriverRejectsUnknownBackend(t *testing.T) {
	f := NewFactory([]Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: true},
		{Name: "runtime.process", Available: true},
	}, map[string]Driver{
		"firecracker": &stubDriver{backend: "firecracker"},
		"process":     &stubDriver{backend: "process"},
	})

	_, err := f.SelectDriver([]string{"lima"}, nil)
	if err == nil {
		t.Fatal("expected unknown backend to be rejected, got nil error")
	}
}
