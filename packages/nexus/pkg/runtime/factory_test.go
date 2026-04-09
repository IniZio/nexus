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

func TestSelectDriverLinuxDoesNotFallbackToSeatbelt(t *testing.T) {
	f := NewFactory([]Capability{
		{Name: "runtime.linux", Available: true},
		{Name: "runtime.firecracker", Available: false},
		{Name: "runtime.seatbelt", Available: true},
	}, map[string]Driver{
		"seatbelt": &stubDriver{backend: "seatbelt"},
	})

	if _, err := f.SelectDriver([]string{"linux"}, nil); err == nil {
		t.Fatal("expected linux requirement to fail when firecracker is unavailable")
	}
}

func TestSelectDriverDarwinPrefersFirecrackerWhenPreflightPasses(t *testing.T) {
	f := NewFactory([]Capability{
		{Name: "runtime.darwin", Available: true},
		{Name: "runtime.firecracker", Available: true},
		{Name: "runtime.seatbelt", Available: true},
	}, map[string]Driver{
		"firecracker": &stubDriver{backend: "firecracker"},
		"seatbelt":    &stubDriver{backend: "seatbelt"},
	})

	d, err := f.SelectDriver([]string{"darwin"}, nil)
	if err != nil {
		t.Fatalf("select darwin driver: %v", err)
	}
	if d.Backend() != "firecracker" {
		t.Fatalf("expected firecracker backend, got %q", d.Backend())
	}
}
