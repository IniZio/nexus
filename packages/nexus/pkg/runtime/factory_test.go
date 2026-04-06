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
		[]Capability{{Name: "runtime.linux", Available: true}},
		map[string]Driver{"linux": &mockDriver{backend: "linux"}},
	)
	driver, err := f.SelectDriver([]string{"linux"}, "prefer-first", nil)
	if err != nil {
		t.Fatalf("expected linux selection, got %v", err)
	}
	if driver.Backend() != "linux" {
		t.Fatalf("expected backend linux, got %s", driver.Backend())
	}
}

func TestSelectDriver_PreferFirst_FallsToSecond(t *testing.T) {
	f := NewFactory(
		[]Capability{{Name: "runtime.linux", Available: true}},
		map[string]Driver{"linux": &mockDriver{backend: "linux"}},
	)
	driver, err := f.SelectDriver([]string{"dind", "linux"}, "prefer-first", nil)
	if err != nil {
		t.Fatalf("expected linux selection (dind not registered), got %v", err)
	}
	if driver.Backend() != "linux" {
		t.Fatalf("expected backend linux, got %s", driver.Backend())
	}
}

func TestSelectDriver_NoRequiredBackendAvailable(t *testing.T) {
	f := NewFactory(
		[]Capability{{Name: "runtime.linux", Available: true}},
		map[string]Driver{"linux": &mockDriver{backend: "linux"}},
	)
	_, err := f.SelectDriver([]string{"dind"}, "prefer-first", nil)
	if err == nil {
		t.Fatal("expected error when no required backend available")
	}
}

func TestSelectDriver_RequiredCapabilityMissing(t *testing.T) {
	f := NewFactory(
		[]Capability{{Name: "runtime.linux", Available: false}},
		map[string]Driver{"linux": &mockDriver{backend: "linux"}},
	)
	_, err := f.SelectDriver([]string{"linux"}, "prefer-first", []string{"runtime.linux"})
	if err == nil {
		t.Fatal("expected error when required capability missing")
	}
	if err.Error() != `required capability "runtime.linux" is not available` {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSelectDriver_SelectsLocalWhenLinuxUnavailable(t *testing.T) {
	f := NewFactory(
		[]Capability{
			{Name: "runtime.linux", Available: false},
			{Name: "runtime.local", Available: true},
		},
		map[string]Driver{
			"linux": &mockDriver{backend: "linux"},
			"local": &mockDriver{backend: "local"},
		},
	)
	driver, err := f.SelectDriver([]string{"linux", "local"}, "prefer-first", nil)
	if err != nil {
		t.Fatalf("expected local selection when linux unavailable, got %v", err)
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
		[]Capability{{Name: "runtime.linux", Available: true}},
		map[string]Driver{"linux": &mockDriver{backend: "linux"}},
	)
	_, err := f.SelectDriver([]string{"dind"}, "prefer-first", nil)
	if err == nil {
		t.Fatal("expected error for legacy dind backend")
	}

	_, err = f.SelectDriver([]string{"kubernetes"}, "prefer-first", nil)
	if err == nil {
		t.Fatal("expected error for unsupported kubernetes backend")
	}
}

func TestSelectDriver_AcceptsLegacyAliasesForLinux(t *testing.T) {
	f := NewFactory(
		[]Capability{{Name: "runtime.linux", Available: true}},
		map[string]Driver{"linux": &mockDriver{backend: "linux"}},
	)

	for _, alias := range []string{"firecracker", "vm", "lxc", "sandbox"} {
		driver, err := f.SelectDriver([]string{alias}, "prefer-first", nil)
		if err != nil {
			t.Fatalf("expected alias %q to resolve to linux, got error: %v", alias, err)
		}
		if driver.Backend() != "linux" {
			t.Fatalf("expected alias %q to resolve backend linux, got %s", alias, driver.Backend())
		}
	}
}
