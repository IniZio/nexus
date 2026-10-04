package supervisor

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/portfwd"
)

func newPersistSup(t *testing.T, stateDir, mapFile string, guest uint16) *portForwardSupervisor {
	t.Helper()
	backend := &fakeBackend{
		refs:  []portfwd.SandboxRef{{ID: "sb1", Status: portfwd.SandboxStatusRunning}},
		binds: []portfwd.PortBind{{Port: guest, BindAddr: "0.0.0.0"}},
	}
	return &portForwardSupervisor{
		sandboxRef: "test/sb1",
		backend:    backend,
		disc:       &portfwd.Discoverer{Backend: backend},
		dialer:     fakeDialer{},
		stateDir:   stateDir,
		interval:   time.Second,
		listeners:  make(map[uint16]net.Listener),
		mapFile:    mapFile,
		remembered: loadPortMap(mapFile),
	}
}

func TestPortMap_PersistedAndRebound(t *testing.T) {
	const guest = uint16(8080)
	stateDir, sbDir := t.TempDir(), t.TempDir()
	mapFile := filepath.Join(sbDir, portMapFileName)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s1 := newPersistSup(t, stateDir, mapFile, guest)
	if err := s1.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	host := s1.hostPorts[guest]
	if host == 0 {
		t.Fatal("no host port bound")
	}
	st, err := os.Stat(mapFile)
	if err != nil {
		t.Fatalf("map file not written: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
	if got := loadPortMap(mapFile); got[guest] != host {
		t.Fatalf("persisted = %v, want %d→%d", got, guest, host)
	}

	// Supervisor respawn: old listeners die, new supervisor rebinds same port.
	s1.teardownAll()
	s2 := newPersistSup(t, stateDir, mapFile, guest)
	if err := s2.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	defer s2.teardownAll()
	if got := s2.hostPorts[guest]; got != host {
		t.Fatalf("restart host port = %d, want %d", got, host)
	}
}

func TestPortMap_TakenPortFallsBackAndUpdatesFile(t *testing.T) {
	const guest = uint16(8080)
	stateDir, sbDir := t.TempDir(), t.TempDir()
	mapFile := filepath.Join(sbDir, portMapFileName)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	squat, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer squat.Close()
	taken := uint16(squat.Addr().(*net.TCPAddr).Port)
	if err := savePortMap(mapFile, map[uint16]uint16{guest: taken}); err != nil {
		t.Fatal(err)
	}

	s := newPersistSup(t, stateDir, mapFile, guest)
	if err := s.reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.teardownAll()
	got := s.hostPorts[guest]
	if got == 0 || got == taken {
		t.Fatalf("host port = %d, want ephemeral != %d", got, taken)
	}
	if m := loadPortMap(mapFile); m[guest] != got {
		t.Fatalf("file not updated: %v, want %d", m, got)
	}
}
