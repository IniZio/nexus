package runtime

import (
	"context"
	"testing"
)

type mockDriver struct {
	backend string
}

func (m *mockDriver) Backend() string { return m.backend }
func (m *mockDriver) Create(ctx context.Context, req CreateRequest) error {
	return nil
}
func (m *mockDriver) Start(ctx context.Context, workspaceID string) error   { return nil }
func (m *mockDriver) Stop(ctx context.Context, workspaceID string) error    { return nil }
func (m *mockDriver) Restore(ctx context.Context, workspaceID string) error { return nil }
func (m *mockDriver) Pause(ctx context.Context, workspaceID string) error   { return nil }
func (m *mockDriver) Resume(ctx context.Context, workspaceID string) error  { return nil }
func (m *mockDriver) Fork(ctx context.Context, workspaceID, childWorkspaceID string) error {
	return nil
}
func (m *mockDriver) Destroy(ctx context.Context, workspaceID string) error { return nil }

func TestSelectDriver_PreferFirst(t *testing.T) {
	f := NewFactory(
		[]Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]Driver{"firecracker": &mockDriver{backend: "firecracker"}},
	)
	driver, err := f.SelectDriver([]string{"firecracker"}, "prefer-first", nil)
	if err != nil {
		t.Fatalf("expected firecracker selection, got %v", err)
	}
	if driver.Backend() != "firecracker" {
		t.Fatalf("expected backend firecracker, got %s", driver.Backend())
	}
}

func TestSelectDriver_PreferFirst_FallsToSecond(t *testing.T) {
	f := NewFactory(
		[]Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]Driver{"firecracker": &mockDriver{backend: "firecracker"}},
	)
	driver, err := f.SelectDriver([]string{"dind", "firecracker"}, "prefer-first", nil)
	if err != nil {
		t.Fatalf("expected firecracker selection (dind not registered), got %v", err)
	}
	if driver.Backend() != "firecracker" {
		t.Fatalf("expected backend firecracker, got %s", driver.Backend())
	}
}

func TestSelectDriver_NoRequiredBackendAvailable(t *testing.T) {
	f := NewFactory(
		[]Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]Driver{"firecracker": &mockDriver{backend: "firecracker"}},
	)
	_, err := f.SelectDriver([]string{"dind"}, "prefer-first", nil)
	if err == nil {
		t.Fatal("expected error when no required backend available")
	}
}

func TestSelectDriver_RequiredCapabilityMissing(t *testing.T) {
	f := NewFactory(
		[]Capability{{Name: "runtime.firecracker", Available: false}},
		map[string]Driver{"firecracker": &mockDriver{backend: "firecracker"}},
	)
	_, err := f.SelectDriver([]string{"firecracker"}, "prefer-first", []string{"runtime.firecracker"})
	if err == nil {
		t.Fatal("expected error when required capability missing")
	}
	if err.Error() != `required capability "runtime.firecracker" is not available` {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSelectDriver_SelectsLocalWhenFirecrackerUnavailable(t *testing.T) {
	f := NewFactory(
		[]Capability{
			{Name: "runtime.firecracker", Available: false},
			{Name: "runtime.local", Available: true},
		},
		map[string]Driver{
			"firecracker": &mockDriver{backend: "firecracker"},
			"local":       &mockDriver{backend: "local"},
		},
	)
	driver, err := f.SelectDriver([]string{"firecracker", "local"}, "prefer-first", nil)
	if err != nil {
		t.Fatalf("expected local selection when firecracker unavailable, got %v", err)
	}
	if driver.Backend() != "local" {
		t.Fatalf("expected backend local, got %s", driver.Backend())
	}
}

func TestSelectDriver_LocalOnly(t *testing.T) {
	f := NewFactory(
		[]Capability{{Name: "runtime.local", Available: true}},
		map[string]Driver{"local": &mockDriver{backend: "local"}},
	)
	driver, err := f.SelectDriver([]string{"local"}, "prefer-first", nil)
	if err != nil {
		t.Fatalf("expected local selection, got %v", err)
	}
	if driver.Backend() != "local" {
		t.Fatalf("expected backend local, got %s", driver.Backend())
	}
}

func TestSelectDriver_LocalCapabilityUnavailable(t *testing.T) {
	f := NewFactory(
		[]Capability{{Name: "runtime.local", Available: false}},
		map[string]Driver{"local": &mockDriver{backend: "local"}},
	)
	_, err := f.SelectDriver([]string{"local"}, "prefer-first", nil)
	if err == nil {
		t.Fatal("expected error when local capability unavailable")
	}
}

func TestSelectDriver_RejectsLegacyBackends(t *testing.T) {
	f := NewFactory(
		[]Capability{{Name: "runtime.firecracker", Available: true}},
		map[string]Driver{"firecracker": &mockDriver{backend: "firecracker"}},
	)
	_, err := f.SelectDriver([]string{"dind"}, "prefer-first", nil)
	if err == nil {
		t.Fatal("expected error for legacy dind backend")
	}

	_, err = f.SelectDriver([]string{"lxc"}, "prefer-first", nil)
	if err == nil {
		t.Fatal("expected error for legacy lxc backend")
	}
}

func TestSelectDriver_LinuxPrefersFirecrackerThenLXC(t *testing.T) {
	f := NewFactory(
		[]Capability{{Name: "runtime.linux", Available: true}, {Name: "runtime.firecracker", Available: true}, {Name: "runtime.lxc", Available: true}},
		map[string]Driver{"firecracker": &mockDriver{backend: "firecracker"}, "lxc": &mockDriver{backend: "lxc"}},
	)

	driver, err := f.SelectDriver([]string{"linux"}, "prefer-first", nil)
	if err != nil {
		t.Fatalf("expected linux selection to succeed, got %v", err)
	}
	if driver.Backend() != "firecracker" {
		t.Fatalf("expected firecracker backend preference, got %s", driver.Backend())
	}
}

func TestSelectDriver_LinuxFallsBackToLXC(t *testing.T) {
	f := NewFactory(
		[]Capability{{Name: "runtime.linux", Available: true}, {Name: "runtime.firecracker", Available: false}, {Name: "runtime.lxc", Available: true}},
		map[string]Driver{"firecracker": &mockDriver{backend: "firecracker"}, "lxc": &mockDriver{backend: "lxc"}},
	)

	driver, err := f.SelectDriver([]string{"linux"}, "prefer-first", nil)
	if err != nil {
		t.Fatalf("expected linux fallback selection to succeed, got %v", err)
	}
	if driver.Backend() != "lxc" {
		t.Fatalf("expected lxc fallback backend, got %s", driver.Backend())
	}
}

func TestSelectDriver_LinuxRequiresRuntimeLinuxCapability(t *testing.T) {
	f := NewFactory(
		[]Capability{{Name: "runtime.linux", Available: false}, {Name: "runtime.firecracker", Available: true}, {Name: "runtime.lxc", Available: true}},
		map[string]Driver{"firecracker": &mockDriver{backend: "firecracker"}, "lxc": &mockDriver{backend: "lxc"}},
	)

	_, err := f.SelectDriver([]string{"linux"}, "prefer-first", nil)
	if err == nil {
		t.Fatal("expected linux requirement to fail when runtime.linux capability unavailable")
	}
}
