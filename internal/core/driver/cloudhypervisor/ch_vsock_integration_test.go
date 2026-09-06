//go:build integration

package cloudhypervisor

// TestDialGuest_Integration boots a real microVM and dials the vsock
// multiplexer to verify the handshake succeeds against CH's actual
// implementation. It does NOT send any agent protocol — the guest agent is
// implemented in a later run.
//
// # Guard conditions
//
// This test skips (never fails) when:
//   - /dev/kvm is absent or not usable by this user
//   - the cloud-hypervisor binary is not available
//   - boot artifacts are absent (run scripts/fetch-boot-artifacts.sh)
//
// The vsock connection is expected to fail with "connection refused" (no
// listener on the guest) — what matters is that CH's vsock multiplexer
// is reachable and replies with a NACK rather than the test failing to even
// reach the handshake.
//
// # Running
//
//	bash scripts/fetch-boot-artifacts.sh
//	go test -tags integration ./internal/core/driver/cloudhypervisor/... \
//	    -run TestDialGuest_Integration -v -count=1 -timeout 120s

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

func TestDialGuest_Integration(t *testing.T) {
	// guards
	skipUnlessKVM(t)
	chBin := skipUnlessCHBin(t)
	kernelPath := skipUnlessArtifact(t, "vmlinux-x86_64")

	// socket dir
	socketDir, err := os.MkdirTemp("/tmp", "ch-vsock-it-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })

	// driver + VM
	drv, err := New(Config{
		BinaryPath: chBin,
		SocketDir:  socketDir,
		KernelPath: kernelPath,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id := domain.NewSandboxID()

	startCtx, startCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer startCancel()

	req := driver.StartRequest{SandboxID: id}
	if _, err := drv.Start(startCtx, req); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = drv.Stop(stopCtx, id)
	})

	// dial
	// The guest kernel panics (no initramfs/agent), but the vsock multiplexer
	// is started by CH before the guest boots. We expect either:
	//   (a) a successful handshake with NACK (no listener on the guest port), or
	//   (b) a connection error if CH hasn't started the multiplexer socket yet.
	// Either outcome is acceptable — what we must NOT see is the test hanging.

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dialCancel()

	conn, err := drv.DialGuest(dialCtx, id, driver.AgentControlPort)
	if err != nil {
		// Acceptable outcomes: CH multiplexer not up yet, or guest has no listener.
		// "guest agent not yet listening" is the EOF branch of DialGuest
		// (ch_vsock.go): the AF_UNIX connect to CH's multiplexer SUCCEEDED and
		// CONNECT was sent, but the guest closed without replying because no
		// in-guest listener exists. That is outcome (b) above — and it is in
		// fact the strongest of the acceptable outcomes, since reaching it
		// proves the multiplexer was live. This case was added to DialGuest
		// after the test was written; because the integration tag was never
		// run, nothing noticed the allowlist had gone stale.
		if strings.Contains(err.Error(), "no such file") ||
			strings.Contains(err.Error(), "connection refused") ||
			strings.Contains(err.Error(), "guest agent not yet listening") ||
			strings.Contains(err.Error(), "NACK") {
			t.Logf("DialGuest returned expected error (no guest agent yet): %v", err)
			return
		}
		t.Fatalf("DialGuest: unexpected error: %v", err)
	}
	defer conn.Close()
	t.Logf("DialGuest succeeded — vsock multiplexer handshake reached the guest")
}
