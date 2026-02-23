package docker

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestIsPortInUse(t *testing.T) {
	tests := []struct {
		name     string
		port     int32
		expected bool
	}{
		{"free high port", 59999, false},
		{"used port (ssh)", 22, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isPortInUse(tt.port)
			if result != tt.expected {
				t.Errorf("isPortInUse(%d) = %v, want %v", tt.port, result, tt.expected)
			}
		})
	}
}

func TestNewPortManager(t *testing.T) {
	pm := NewPortManager(32800, 34999)

	if pm.minPort != 32800 {
		t.Errorf("expected minPort 32800, got %d", pm.minPort)
	}
	if pm.maxPort != 34999 {
		t.Errorf("expected maxPort 34999, got %d", pm.maxPort)
	}
	if pm.nextPort != 32800 {
		t.Errorf("expected nextPort 32800, got %d", pm.nextPort)
	}
	if pm.allocated == nil {
		t.Error("expected allocated map to be initialized")
	}
}

func TestAllocate(t *testing.T) {
	t.Run("allocates first available port", func(t *testing.T) {
		pm := NewPortManager(32800, 34999)

		port, err := pm.Allocate()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if port < 32800 || port > 34999 {
			t.Errorf("port %d out of range", port)
		}
	})

	t.Run("allocates different ports sequentially", func(t *testing.T) {
		pm := NewPortManager(32800, 32802)

		port1, _ := pm.Allocate()
		port2, _ := pm.Allocate()
		port3, _ := pm.Allocate()

		if port1 == port2 || port2 == port3 || port1 == port3 {
			t.Errorf("expected different ports, got %d, %d, %d", port1, port2, port3)
		}
	})

	t.Run("skips already allocated ports", func(t *testing.T) {
		pm := NewPortManager(32800, 32802)

		pm.allocated[32800] = true
		pm.nextPort = 32800

		port, err := pm.Allocate()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if port != 32801 {
			t.Errorf("expected port 32801, got %d", port)
		}
	})

	t.Run("returns error when no ports available", func(t *testing.T) {
		pm := NewPortManager(32800, 32800)
		pm.allocated[32800] = true

		_, err := pm.Allocate()
		if err == nil {
			t.Error("expected error when no ports available")
		}
	})
}

func TestAllocateSpecific(t *testing.T) {
	t.Run("allocates specific port in range", func(t *testing.T) {
		pm := NewPortManager(32800, 34999)

		err := pm.AllocateSpecific(33000)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !pm.IsAllocated(33000) {
			t.Error("expected port to be allocated")
		}
	})

	t.Run("returns error for port out of range", func(t *testing.T) {
		pm := NewPortManager(32800, 34999)

		err := pm.AllocateSpecific(30000)
		if err == nil {
			t.Error("expected error for port out of range")
		}
	})

	t.Run("returns error for already allocated port", func(t *testing.T) {
		pm := NewPortManager(32800, 34999)
		pm.allocated[33000] = true

		err := pm.AllocateSpecific(33000)
		if err == nil {
			t.Error("expected error for already allocated port")
		}
	})
}

func TestRelease(t *testing.T) {
	t.Run("releases allocated port", func(t *testing.T) {
		pm := NewPortManager(32800, 34999)

		port, _ := pm.Allocate()
		err := pm.Release(port)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if pm.IsAllocated(port) {
			t.Error("expected port to be released")
		}
	})

	t.Run("returns error for unallocated port", func(t *testing.T) {
		pm := NewPortManager(32800, 34999)

		err := pm.Release(33000)
		if err == nil {
			t.Error("expected error for unallocated port")
		}
	})
}

func TestIsAllocated(t *testing.T) {
	pm := NewPortManager(32800, 34999)

	if pm.IsAllocated(33000) {
		t.Error("expected port to not be allocated")
	}

	pm.allocated[33000] = true

	if !pm.IsAllocated(33000) {
		t.Error("expected port to be allocated")
	}
}

func TestGetAllocatedPorts(t *testing.T) {
	pm := NewPortManager(32800, 34999)
	pm.allocated[32800] = true
	pm.allocated[32801] = true
	pm.allocated[32802] = true

	ports := pm.GetAllocatedPorts()
	if len(ports) != 3 {
		t.Errorf("expected 3 ports, got %d", len(ports))
	}
}

func TestGetState(t *testing.T) {
	pm := NewPortManager(32800, 34999)
	pm.allocated[32800] = true
	pm.nextPort = 32850

	state := pm.GetState()

	if state.NextPort != 32850 {
		t.Errorf("expected NextPort 32850, got %d", state.NextPort)
	}
	if len(state.Allocated) != 1 {
		t.Errorf("expected 1 allocated port, got %d", len(state.Allocated))
	}
}

func TestRestore(t *testing.T) {
	t.Run("restores multiple ports", func(t *testing.T) {
		pm := NewPortManager(32800, 34999)

		err := pm.Restore([]int32{32800, 32801, 32802})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !pm.IsAllocated(32800) || !pm.IsAllocated(32801) || !pm.IsAllocated(32802) {
			t.Error("expected all ports to be allocated")
		}
	})

	t.Run("returns error for port out of range", func(t *testing.T) {
		pm := NewPortManager(32800, 34999)

		err := pm.Restore([]int32{30000})
		if err == nil {
			t.Error("expected error for port out of range")
		}
	})
}

func TestPortRangeValidation(t *testing.T) {
	tests := []struct {
		name     string
		minPort  int32
		maxPort  int32
		expectOk bool
	}{
		{"valid range", 32800, 34999, true},
		{"min equals max", 32800, 32800, true},
		{"invalid range (min > max)", 34999, 32800, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pm := NewPortManager(tt.minPort, tt.maxPort)

			if tt.expectOk {
				if pm.minPort != tt.minPort || pm.maxPort != tt.maxPort {
					t.Errorf("expected min=%d max=%d, got min=%d max=%d",
						tt.minPort, tt.maxPort, pm.minPort, pm.maxPort)
				}
			} else {
				if pm.minPort <= pm.maxPort {
					t.Error("expected invalid range to be handled")
				}
			}
		})
	}
}

func TestPortManagerWithState(t *testing.T) {
	tmpDir := t.TempDir()
	stateFile := filepath.Join(tmpDir, "port-state.json")

	pm := NewPortManagerWithState(32800, 34999, stateFile)

	pm.AllocateSpecific(32800)
	pm.AllocateSpecific(32801)

	pm2 := NewPortManagerWithState(32800, 34999, stateFile)

	if !pm2.IsAllocated(32800) {
		t.Error("expected port 32800 to be allocated after state load")
	}
	if !pm2.IsAllocated(32801) {
		t.Error("expected port 32801 to be allocated after state load")
	}

	os.Remove(stateFile)

	pm3 := NewPortManagerWithState(32800, 34999, stateFile)
	if len(pm3.GetAllocatedPorts()) != 0 {
		t.Error("expected no ports for non-existent state file")
	}
}

func TestAllocateSkipsInUsePorts(t *testing.T) {
	pm := NewPortManager(32800, 34999)

	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Skip("cannot bind to random port for test")
	}
	defer ln.Close()

	usedPort := int32(ln.Addr().(*net.TCPAddr).Port)
	pm.allocated[usedPort] = true

	port, err := pm.Allocate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if port == usedPort {
		t.Errorf("expected Allocate to skip in-use port %d", usedPort)
	}
}
