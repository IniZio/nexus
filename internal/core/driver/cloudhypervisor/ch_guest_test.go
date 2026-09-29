package cloudhypervisor

import (
	"testing"

	"github.com/IniZio/nexus/internal/core/driver"
)

func TestCHCapabilitiesReflectOptionalInterfaces(t *testing.T) {
	d := &CHDriver{}
	var drv driver.Driver = d
	got := d.Capabilities()

	check := func(name string, flag bool, ok bool) {
		t.Helper()
		if flag != ok {
			t.Errorf("%s = %v, want %v", name, flag, ok)
		}
	}
	_, ok := drv.(driver.PauseResumer)
	check("Pause", got.Pause, ok)
	_, ok = drv.(driver.GuestDialer)
	check("GuestDial", got.GuestDial, ok)
	_, ok = drv.(driver.Snapshotter)
	check("Snapshot", got.Snapshot, ok)
	_, ok = drv.(driver.Forker)
	check("Fork", got.Fork, ok)
	_, ok = drv.(driver.SnapshotRemover)
	check("SnapshotRemove", got.SnapshotRemove, ok)
	_, ok = drv.(driver.NetworkHook)
	check("NetworkHook", got.NetworkHook, ok)
	_, ok = drv.(driver.NetnsStateProvider)
	check("NetnsState", got.NetnsState, ok)
	_, ok = drv.(driver.SessionAttacher)
	check("SessionAttach", got.SessionAttach, ok)

	if got.GuestOS != driver.GuestOSLinux {
		t.Errorf("GuestOS = %q", got.GuestOS)
	}
	if got.Egress != driver.EgressEnforced {
		t.Errorf("Egress = %q", got.Egress)
	}
	if !got.GuestDial || !got.SessionAttach {
		t.Errorf("expected GuestDial and SessionAttach true: %+v", got)
	}
}
