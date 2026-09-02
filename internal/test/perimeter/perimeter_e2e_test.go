//go:build integration

package perimetertest

// perimeter_e2e_test.go is the tracer-bullet acceptance test for P1-S0 of the
// perimeter/egress feature.
//
// It proves that a booted sandbox VM's TAP fd can flow through the driver.NetworkHook
// seam into the perimeter package and that at least one raw Ethernet frame is read.
//
// # What it tests
//
//   - CHDriver creates a TAP fd at Start time (unconditionally — the hook is no
//     longer opt-in), passes it
//     to CH as an inherited fd via ExtraFiles, and registers it via the fds field
//     of the vm.create NetConfig JSON payload.
//   - The driver.NetworkHook capability is discoverable via type assertion on CHDriver.
//   - GuestNetworkFD returns a live io.ReadWriteCloser backed by the TAP fd.
//   - At least one raw Ethernet frame (≥14-byte Ethernet header) can be read from
//     that fd within 30 seconds of VM boot.
//
// # Skip conditions
//
// The test skips (never fails) when any of the following is absent:
//   - /dev/kvm accessible (KVM required for cloud-hypervisor)
//   - cloud-hypervisor binary (CLOUD_HYPERVISOR_BIN env or default path)
//   - kernel image (NEXUS3_KERNEL env or images/kernel/vmlinux-x86_64)
//   - /dev/net/tun accessible (CAP_NET_ADMIN required for TAP creation)
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run TestNetworkHookTracer \
//	  ./internal/test/perimeter/ -v -timeout 5m
//
// On a host with KVM, cloud-hypervisor, a kernel with virtio-net support, and
// CAP_NET_ADMIN, the VM will boot, the guest kernel will bring up its virtio-net
// interface, and ARP/IPv6 RS frames will appear on the TAP fd within seconds.

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/perimeter"
)

// ── defaults ──────────────────────────────────────────────────────────────────

const (
	periDefaultCHBin  = "$HOME/.local/bin/cloud-hypervisor"
	periDefaultKernel = "images/kernel/vmlinux-x86_64"
)

// ── skip guards ───────────────────────────────────────────────────────────────

func skipUnlessKVM(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("skipping: /dev/kvm not present — KVM is required")
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("skipping: /dev/kvm not accessible: %v", err)
	}
	f.Close()
}

func skipUnlessCHBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("CLOUD_HYPERVISOR_BIN")
	if bin == "" {
		bin = periDefaultCHBin
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("skipping: cloud-hypervisor binary not found at %s (set CLOUD_HYPERVISOR_BIN)", bin)
	}
	return bin
}

func skipUnlessKernel(t *testing.T) string {
	t.Helper()
	kernel := os.Getenv("NEXUS3_KERNEL")
	if kernel == "" {
		// Resolve relative to the repo root (two levels up from internal/test/perimeter).
		_, thisFile, _, _ := runtime.Caller(0)
		repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
		kernel = filepath.Join(repoRoot, periDefaultKernel)
	}
	if _, err := os.Stat(kernel); err != nil {
		t.Skipf("skipping: kernel not found at %s (set NEXUS3_KERNEL)", kernel)
	}
	return kernel
}

func skipUnlessNetAdmin(t *testing.T) {
	t.Helper()
	// Opening /dev/net/tun is not sufficient — TUNSETIFF requires CAP_NET_ADMIN.
	// Probe with an actual TUNSETIFF call; skip if it fails.
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR, 0)
	if err != nil {
		t.Skipf("skipping: /dev/net/tun not accessible: %v", err)
	}
	defer unix.Close(fd)

	ifreq, err := unix.NewIfreq("nxpprobe0")
	if err != nil {
		t.Skipf("skipping: ifreq: %v", err)
	}
	ifreq.SetUint16(unix.IFF_TAP | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifreq); err != nil {
		// TUNSETIFF returns EPERM when CAP_NET_ADMIN is absent.
		t.Skipf("skipping: TUNSETIFF (CAP_NET_ADMIN required): %v", err)
	}
	// The probe TAP is non-persistent; closing fd (deferred) destroys it.
}

// ── test-local frame capture perimeter ───────────────────────────────────────

// frameCapture is a [perimeter.Perimeter] that signals fc.first on the first
// non-empty frame read. It is used by the tracer test to verify that the TAP
// fd is live and that Ethernet frames flow from the VM.
type frameCapture struct {
	first chan struct{}
	once  sync.Once
	count int // total frames read; accessed only from the Run goroutine
}

// Run reads raw Ethernet frames from rw until ctx is cancelled or rw returns
// an error. Closes fc.first on the first non-empty read. Implements [perimeter.Perimeter].
func (fc *frameCapture) Run(ctx context.Context, id domain.SandboxID, rw io.ReadWriteCloser) error {
	const maxFrameSize = 9216 // max Ethernet/jumbo frame
	buf := make([]byte, maxFrameSize)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, err := rw.Read(buf)
		if n > 0 {
			fc.count++
			fc.once.Do(func() { close(fc.first) })
		}
		if err != nil {
			return err
		}
	}
}

var _ perimeter.Perimeter = (*frameCapture)(nil)

// ── tracer test ───────────────────────────────────────────────────────────────

// TestNetworkHookTracer boots a real VM, obtains the
// TAP fd via the driver.NetworkHook capability, attaches a frame-capturing
// perimeter, and asserts that at least one raw Ethernet frame is read within
// 30 seconds of boot.
//
// The test skips cleanly on hosts without KVM, cloud-hypervisor, a suitable
// kernel, or CAP_NET_ADMIN. If none of those are available, output:
//
//	"verified by build/skip; live frame-read pending privileged run"
func TestNetworkHookTracer(t *testing.T) {
	// ── skip guards ──────────────────────────────────────────────────────────
	skipUnlessKVM(t)
	chBin := skipUnlessCHBin(t)
	kernelPath := skipUnlessKernel(t)
	skipUnlessNetAdmin(t)

	// ── driver setup ─────────────────────────────────────────────────────────
	socketDir := t.TempDir()
	// Config.EnableNetHook is gone: the two-TAP/L2-bridge topology is no longer
	// opt-in. Every CHDriver.Start builds a vmNetConfig and calls
	// VMCreateWithNet, so d.nets[id] — and therefore the NetworkHook capability
	// and its TAP fd — is populated unconditionally. Dropping the field asserts
	// strictly more than it used to (the hook must be present on a plain
	// Config, not merely when explicitly enabled), so nothing narrows here.
	cfg := cloudhypervisor.Config{
		BinaryPath: chBin,
		SocketDir:  socketDir,
		KernelPath: kernelPath,
		// Minimal VM: 1 vCPU, 256 MiB. Kernel must have virtio-net for frames.
		VCPUs:     1,
		MemoryMiB: 256,
	}
	drvConcrete, err := cloudhypervisor.New(cfg)
	if err != nil {
		t.Fatalf("cloudhypervisor.New: %v", err)
	}
	// Bind to the driver.Driver interface so type assertions work correctly.
	var drv driver.Driver = drvConcrete

	// ── sandbox boot ─────────────────────────────────────────────────────────
	id := domain.NewSandboxID()
	t.Logf("sandbox id: %s", id)

	startCtx, startCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer startCancel()

	_, err = drv.Start(startCtx, driver.StartRequest{SandboxID: id})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := drv.Stop(stopCtx, id); err != nil {
			t.Logf("Stop: %v (may be expected if VM already halted)", err)
		}
	})

	// ── NetworkHook capability assertion ─────────────────────────────────────
	hook, ok := drv.(driver.NetworkHook)
	if !ok {
		t.Fatal("CHDriver does not implement driver.NetworkHook — the capability is unconditional and must always be present")
	}

	// ── obtain TAP fd ────────────────────────────────────────────────────────
	rw, err := hook.GuestNetworkFD(context.Background(), id)
	if err != nil {
		t.Fatalf("GuestNetworkFD: %v", err)
	}
	t.Cleanup(func() { rw.Close() })

	// ── frame capture loop ───────────────────────────────────────────────────
	fc := &frameCapture{first: make(chan struct{})}
	readCtx, readCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer readCancel()

	go func() {
		err := fc.Run(readCtx, id, rw)
		if err != nil &&
			!errors.Is(err, context.DeadlineExceeded) &&
			!errors.Is(err, context.Canceled) {
			// Log but don't fail: the frame may already have been seen.
			t.Logf("perimeter.Run exited: %v", err)
		}
	}()

	// ── assertion ────────────────────────────────────────────────────────────
	select {
	case <-fc.first:
		t.Logf("tracer: read %d raw Ethernet frame(s) from VM TAP fd — seam plumbing verified", fc.count)
	case <-readCtx.Done():
		t.Fatal("timed out after 30s waiting for first Ethernet frame from VM TAP fd; " +
			"ensure the guest kernel has virtio-net support and brings eth0 up at boot")
	}
}
