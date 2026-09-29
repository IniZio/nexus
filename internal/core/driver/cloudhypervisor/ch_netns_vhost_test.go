//go:build linux

package cloudhypervisor

import (
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestVhostSlot_LifecycleNoLeak(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ctl")
	path := VhostSocketPath(dir, "sb-test")
	before := runtime.NumGoroutine()

	slot, err := startVhostSlot(path)
	if err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode: %v %v", st, err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode: %v %v", st, err)
	}
	if n, err := slot.Write([]byte("frame")); n != 5 || err != nil {
		t.Fatalf("write without master = %d, %v; want silent drop", n, err)
	}

	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()

	readDone := make(chan error, 1)
	go func() { _, err := slot.Read(make([]byte, 2048)); readDone <- err }()
	time.Sleep(50 * time.Millisecond)
	slot.Close()
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Read did not return after Close")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("socket not removed: %v", err)
	}
	for i := 0; i < 50 && runtime.NumGoroutine() > before; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Errorf("goroutines: before=%d after=%d", before, after)
	}
}
