package cloudhypervisor

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
)

func TestStart_noRootDisk_sentinel(t *testing.T) {
	socketDir := testSocketDir(t)
	d, err := New(Config{
		BinaryPath:   "/usr/bin/true",
		SocketDir:    socketDir,
		KernelPath:   "/dev/null",
		VCPUs:        1,
		MemoryMiB:    128,
		StartTimeout: 200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id := domain.NewSandboxID()
	_, startErr := d.Start(context.Background(), driver.StartRequest{SandboxID: id})
	if startErr == nil {
		t.Fatal("Start returned nil; want ErrNoRootDisk")
	}
	if !errors.Is(startErr, ErrNoRootDisk) {
		t.Fatalf("want errors.Is(err, ErrNoRootDisk), got: %v", startErr)
	}

	if _, serr := os.Stat(d.socketPath(id)); serr == nil {
		t.Error("socket exists after ErrNoRootDisk: netns child was spawned")
	}
}
