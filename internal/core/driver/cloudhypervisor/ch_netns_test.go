package cloudhypervisor

// ch_netns_test.go — tests for the netns-runtime mechanism (ch_netns.go).
//
// TestMain: re-exec dispatch — when NEXUS3_NETNS_RUN=1, runs RunNetnsChild()
// so the test binary itself acts as the re-exec image for StartNetnsRuntime.
//
// Unit tests (always compiled, no build-tag guards):
//   TestNetnsSocketpairFiles         — socketpair creation + fd split
//   TestNetnsChildAttr               — SysProcAttr Cloneflags/maps/setgroups
//   TestNetnsSocketpairCloseOrdering — parent closes pumpFile, child end ok
//
// Integration test (runtime-skipped when prerequisites absent; same re-exec
// image as the test binary works for both unit and integration runs):
//   TestNetnsRuntime_KVMProof — boots CH through netns-runtime, reads guest
//   frames on parent perimConn, asserts host CapEff bit 12 (CAP_NET_ADMIN)=0.

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
)

// TestMain checks the netns re-exec sentinel before running tests.
// When NEXUS3_NETNS_RUN=1 this process is the child inside the user+net ns;
// call RunNetnsChild() and exit — do not run any test functions.
//
// S1: wire this sentinel dispatch into cmd/nexus3/main.go
func TestMain(m *testing.M) {
	if os.Getenv(NetnsRunEnv) == "1" {
		RunNetnsChild()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// ── unit tests ────────────────────────────────────────────────────────────────

// TestNetnsSocketpairFiles verifies that netnsSocketpairFiles returns two
// distinct, valid *os.File descriptors (non-negative, non-identical fds).
func TestNetnsSocketpairFiles(t *testing.T) {
	perimFile, pumpFile, err := netnsSocketpairFiles()
	if err != nil {
		t.Fatalf("netnsSocketpairFiles: %v", err)
	}
	t.Cleanup(func() {
		_ = perimFile.Close()
		_ = pumpFile.Close()
	})

	if perimFile == nil || pumpFile == nil {
		t.Fatal("expected both files to be non-nil")
	}
	perimFd := int(perimFile.Fd())
	pumpFd := int(pumpFile.Fd())
	if perimFd < 0 {
		t.Errorf("perimFile fd = %d, want >= 0", perimFd)
	}
	if pumpFd < 0 {
		t.Errorf("pumpFile fd = %d, want >= 0", pumpFd)
	}
	if perimFd == pumpFd {
		t.Errorf("perimFile and pumpFile have the same fd %d; want distinct fds", perimFd)
	}

	// Verify the socketpair is connected: write on pumpFile, read on perimFile.
	msg := []byte("netns-test")
	if _, err := syscall.Write(int(pumpFile.Fd()), msg); err != nil {
		t.Fatalf("write pumpFile: %v", err)
	}
	buf := make([]byte, 64)
	n, err := syscall.Read(int(perimFile.Fd()), buf)
	if err != nil {
		t.Fatalf("read perimFile: %v", err)
	}
	if string(buf[:n]) != string(msg) {
		t.Errorf("round-trip: got %q want %q", buf[:n], msg)
	}
}

// TestNetnsChildAttr verifies the SysProcAttr produced by netnsChildAttr.
//
//   - Cloneflags must include CLONE_NEWUSER and CLONE_NEWNET.
//   - UidMappings must have exactly one entry mapping ContainerID=0 → host uid.
//   - GidMappings must have exactly one entry mapping ContainerID=0 → host gid.
//   - GidMappingsEnableSetgroups must be false (setgroups=deny preserves kvm gid).
func TestNetnsChildAttr(t *testing.T) {
	attr := netnsChildAttr()

	if attr.Cloneflags&syscall.CLONE_NEWUSER == 0 {
		t.Error("Cloneflags missing CLONE_NEWUSER")
	}
	if attr.Cloneflags&syscall.CLONE_NEWNET == 0 {
		t.Error("Cloneflags missing CLONE_NEWNET")
	}
	if attr.GidMappingsEnableSetgroups {
		t.Error("GidMappingsEnableSetgroups must be false (setgroups=deny to preserve kvm gid)")
	}

	if len(attr.UidMappings) != 1 {
		t.Fatalf("UidMappings len = %d, want 1", len(attr.UidMappings))
	}
	if attr.UidMappings[0].ContainerID != 0 {
		t.Errorf("UidMappings[0].ContainerID = %d, want 0", attr.UidMappings[0].ContainerID)
	}
	if attr.UidMappings[0].HostID != os.Getuid() {
		t.Errorf("UidMappings[0].HostID = %d, want %d (host uid)", attr.UidMappings[0].HostID, os.Getuid())
	}
	if attr.UidMappings[0].Size != 1 {
		t.Errorf("UidMappings[0].Size = %d, want 1", attr.UidMappings[0].Size)
	}

	if len(attr.GidMappings) != 1 {
		t.Fatalf("GidMappings len = %d, want 1", len(attr.GidMappings))
	}
	if attr.GidMappings[0].ContainerID != 0 {
		t.Errorf("GidMappings[0].ContainerID = %d, want 0", attr.GidMappings[0].ContainerID)
	}
	if attr.GidMappings[0].HostID != os.Getgid() {
		t.Errorf("GidMappings[0].HostID = %d, want %d (host gid)", attr.GidMappings[0].HostID, os.Getgid())
	}
	if attr.GidMappings[0].Size != 1 {
		t.Errorf("GidMappings[0].Size = %d, want 1", attr.GidMappings[0].Size)
	}
}

// TestNetnsSocketpairCloseOrdering verifies the fd-close contract:
// after the parent closes its copy of pumpFile, the parent end (perimFile)
// remains readable and the pumpFile fd is invalid.
func TestNetnsSocketpairCloseOrdering(t *testing.T) {
	perimFile, pumpFile, err := netnsSocketpairFiles()
	if err != nil {
		t.Fatalf("netnsSocketpairFiles: %v", err)
	}
	t.Cleanup(func() { _ = perimFile.Close() })

	// Write a frame on pumpFile before closing it.
	payload := []byte("close-ordering-test")
	if _, err := syscall.Write(int(pumpFile.Fd()), payload); err != nil {
		pumpFile.Close()
		t.Fatalf("write pumpFile: %v", err)
	}

	// Parent closes pumpFile — this is the post-spawn ordering step.
	pumpFile.Close()

	// perimFile (parent end) must still be readable; the datagram is buffered.
	buf := make([]byte, 64)
	n, err := syscall.Read(int(perimFile.Fd()), buf)
	if err != nil {
		t.Fatalf("read perimFile after pumpFile.Close: %v", err)
	}
	if string(buf[:n]) != string(payload) {
		t.Errorf("read back %q; want %q", buf[:n], payload)
	}
}

// ── integration test (runtime-skipped when KVM / CH binary / artifacts absent) ─

// TestNetnsRuntime_KVMProof is the end-to-end proof:
//   - boots CH through StartNetnsRuntime (inside rootless user+net ns)
//   - calls vm.create(guestTap)+vm.boot over the shared api-socket
//   - reads guest-NIC Ethernet frames off PerimConn
//   - asserts frames with the expected guest MAC arrive on the parent end
//   - asserts the HOST process holds ZERO CAP_NET_ADMIN (CapEff bit 12 clear)
//
// Runtime skip guards (no //go:build integration tag needed — the test binary
// IS the re-exec image for both unit and integration runs):
//   - /dev/kvm absent or not usable  → skip
//   - cloud-hypervisor binary absent → skip
//   - testdata artifacts absent      → skip
func TestNetnsRuntime_KVMProof(t *testing.T) {
	// guard: /dev/kvm
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("skipping TestNetnsRuntime_KVMProof: /dev/kvm not present")
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("skipping TestNetnsRuntime_KVMProof: /dev/kvm not usable: %v", err)
	}
	f.Close()

	// guard: unprivileged userns
	// Only check the sysctl knob (older kernels). On modern kernels the file
	// does not exist and unprivileged userns is enabled by default.
	if data, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if strings.TrimSpace(string(data)) == "0" {
			t.Skip("skipping TestNetnsRuntime_KVMProof: unprivileged_userns_clone=0")
		}
	}

	// guard: cloud-hypervisor binary
	const netnsDefaultCHBin = "$HOME/.local/bin/cloud-hypervisor"
	chBin := os.Getenv("CLOUD_HYPERVISOR_BIN")
	if chBin == "" {
		chBin = netnsDefaultCHBin
	}
	if _, err := os.Stat(chBin); err != nil {
		t.Skipf("skipping TestNetnsRuntime_KVMProof: cloud-hypervisor binary not found at %s "+
			"(set CLOUD_HYPERVISOR_BIN to override)", chBin)
	}

	// guard: testdata artifacts
	kernelPath := netnsSkipUnlessArtifact(t, "vmlinux-x86_64")
	initramfsPath := netnsSkipUnlessArtifact(t, "alpine-initramfs.cpio.gz")

	// assertion (a): host process has ZERO CAP_NET_ADMIN before the test
	assertHostCapNetAdminClear(t, "pre-test")

	// socket dir (short path for sun_path limit)
	socketDir, err := os.MkdirTemp("/tmp", "ch-netns-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })

	if len(socketDir)+35 > 107 {
		t.Skipf("skipping: MkdirTemp returned path too long for Unix socket: %s", socketDir)
	}

	socketPath := filepath.Join(socketDir, "ch-netns.sock")

	// sandbox identity
	id := domain.NewSandboxID()
	mac := sandboxMac(id)
	t.Logf("sandbox id=%x mac=%s", id[:8], mac)

	// start netns runtime
	cfg := Config{
		BinaryPath:   chBin,
		StartTimeout: 20 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	t.Log("Starting netns runtime (re-exec into user+net ns)...")
	rt, err := StartNetnsRuntime(ctx, cfg, id, socketPath, "") // "" = boot mode
	if err != nil {
		t.Fatalf("StartNetnsRuntime: %v", err)
	}
	t.Cleanup(func() { rt.Stop() })

	// poll CH API until ready
	t.Log("Polling CH API socket until ready...")
	c := newClient(socketPath)
	pollStart := time.Now()
	const pingTimeout = 25 * time.Second
	var pingErr error
	for time.Since(pollStart) < pingTimeout {
		pingCtx, pingCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		pingErr = c.Ping(pingCtx)
		pingCancel()
		if pingErr == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pingErr != nil {
		t.Logf("child stderr:\n%s", rt.ChildStderr())
		t.Fatalf("CH API not ready after %v: %v", pingTimeout, pingErr)
	}
	t.Logf("CH API ready in %v", time.Since(pollStart))

	// vm.create with guest TAP + MAC
	apiCtx, apiCancel := context.WithTimeout(ctx, 10*time.Second)
	defer apiCancel()

	vmcfg := vmConfig{
		Payload: vmPayloadConfig{
			Kernel:    kernelPath,
			Initramfs: initramfsPath,
			Cmdline:   "console=ttyS0 panic=5 ip=dhcp",
		},
		CPUs: &vmCPUsConfig{
			BootVCPUs: 1,
			MaxVCPUs:  1,
		},
		Memory: &vmMemoryConfig{
			SizeBytes: 256 * 1024 * 1024,
		},
	}
	nets := []vmNetConfig{
		{Tap: rt.GuestTap, Mac: mac, NumQueues: 2},
	}
	t.Logf("vm.create: guestTap=%s mac=%s", rt.GuestTap, mac)
	if err := c.VMCreateWithNet(apiCtx, vmcfg, nil, nets, nil); err != nil {
		t.Fatalf("vm.create: %v", err)
	}

	// vm.boot
	bootCtx, bootCancel := context.WithTimeout(ctx, 10*time.Second)
	defer bootCancel()
	t.Log("vm.boot...")
	if err := c.VMBoot(bootCtx); err != nil {
		t.Fatalf("vm.boot: %v", err)
	}
	t.Log("vm.boot: OK")

	// read guest Ethernet frames off PerimConn
	// We wait up to 30 s for at least one frame carrying the guest MAC.
	t.Log("Waiting for Ethernet frames from guest NIC on parent PerimConn...")
	rt.PerimConn.SetDeadline(time.Now().Add(30 * time.Second))

	buf := make([]byte, tapBufSize)
	var frameCount int
	var guestMACFrames int

	// Normalise the expected MAC (bytes, not string) for comparison.
	expectedMAC := parseMACBytes(mac)

	for frameCount < 20 && guestMACFrames == 0 {
		n, err := rt.PerimConn.Read(buf)
		if err != nil {
			if frameCount == 0 {
				t.Fatalf("read PerimConn: %v (no frames received)", err)
			}
			t.Logf("read PerimConn after %d frames: %v", frameCount, err)
			break
		}
		if n < 14 {
			continue // too short to be a valid Ethernet frame
		}
		frameCount++
		srcMAC := buf[6:12]
		ethertype := binary.BigEndian.Uint16(buf[12:14])
		t.Logf("FRAME #%d (%dB): dst=%s src=%s ethertype=0x%04x",
			frameCount, n,
			net.HardwareAddr(buf[0:6]).String(),
			net.HardwareAddr(srcMAC).String(),
			ethertype)
		if macEquals(srcMAC, expectedMAC) {
			guestMACFrames++
		}
	}

	// assertion (a): guest MAC frames arrived on parent end
	if guestMACFrames == 0 {
		t.Errorf("FAIL: no frames with guest MAC %s received on PerimConn after %d total frames",
			mac, frameCount)
	} else {
		t.Logf("PASS: %d frame(s) with guest MAC %s received on parent PerimConn", guestMACFrames, mac)
	}

	// assertion (b): host process holds ZERO CAP_NET_ADMIN
	assertHostCapNetAdminClear(t, "during-test")
}

// ── helpers ───────────────────────────────────────────────────────────────────

// assertHostCapNetAdminClear reads /proc/self/status CapEff and asserts
// that bit 12 (CAP_NET_ADMIN = 0x1000) is clear.
func assertHostCapNetAdminClear(t *testing.T, label string) {
	t.Helper()
	data, err := os.ReadFile("/proc/self/status")
	if err != nil {
		t.Fatalf("[%s] read /proc/self/status: %v", label, err)
	}
	var capEffHex string
	for _, line := range strings.Split(string(data), "\n") {
		if after, ok := strings.CutPrefix(line, "CapEff:"); ok {
			capEffHex = strings.TrimSpace(after)
			break
		}
	}
	if capEffHex == "" {
		t.Fatalf("[%s] CapEff not found in /proc/self/status", label)
	}
	capEff, err := strconv.ParseUint(capEffHex, 16, 64)
	if err != nil {
		t.Fatalf("[%s] parse CapEff %q: %v", label, capEffHex, err)
	}
	const capNetAdminBit = uint64(1) << 12 // CAP_NET_ADMIN = capability 12
	if capEff&capNetAdminBit != 0 {
		t.Errorf("[%s] FAIL: host process has CAP_NET_ADMIN set (CapEff=0x%x, bit 12 is SET)",
			label, capEff)
	} else {
		t.Logf("[%s] PASS: host CapEff=0x%x — bit 12 (CAP_NET_ADMIN) is CLEAR", label, capEff)
	}
}

// parseMACBytes parses a colon-separated MAC string into a 6-byte slice.
func parseMACBytes(mac string) []byte {
	parts := strings.Split(mac, ":")
	if len(parts) != 6 {
		return nil
	}
	b := make([]byte, 6)
	for i, p := range parts {
		v, err := strconv.ParseUint(p, 16, 8)
		if err != nil {
			return nil
		}
		b[i] = byte(v)
	}
	return b
}

// netnsSkipUnlessArtifact returns the absolute path to a testdata artifact or
// skips the test. Mirrors skipUnlessArtifact from boot_integration_test.go but
// is defined here (without a build tag) so it compiles in all test builds.
func netnsSkipUnlessArtifact(t *testing.T, name string) string {
	t.Helper()
	rel := filepath.Join("testdata", name)
	if _, err := os.Stat(rel); err != nil {
		t.Skipf("skipping: boot artifact %q not found\n"+
			"  Run:  bash scripts/fetch-boot-artifacts.sh\n"+
			"  from the repository root to fetch it.", rel)
	}
	abs, err := filepath.Abs(rel)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", rel, err)
	}
	return abs
}

// macEquals returns true if a and b represent the same 6-byte MAC address.
func macEquals(a, b []byte) bool {
	if len(a) != 6 || len(b) != 6 {
		return false
	}
	return hex.EncodeToString(a) == hex.EncodeToString(b)
}

// TestNetnsRuntime_CHOrphanKill verifies that rt.Stop() explicitly kills the
// CH grandchild (not just the netns child) via a whole-process-group kill, so
// no orphaned cloud-hypervisor process survives.
//
// Mechanism under test:
//   - netnsChildAttr sets Setpgid:true → child is a process group leader (pgid==pid)
//   - spawnVMMInGroup sets Setpgid:false → CH inherits the child's pgid
//   - rt.Stop() calls Kill(-childPgid, SIGKILL) → kills both child and CH
//
// The test locates CH's PID by scanning /proc/*/cmdline for the API socket
// path, then asserts the PID is gone (ESRCH) within a short deadline after
// rt.Stop() returns.
func TestNetnsRuntime_CHOrphanKill(t *testing.T) {
	// guards: same as KVMProof
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("skipping TestNetnsRuntime_CHOrphanKill: /dev/kvm not present")
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("skipping TestNetnsRuntime_CHOrphanKill: /dev/kvm not usable: %v", err)
	}
	f.Close()

	if data, err := os.ReadFile("/proc/sys/kernel/unprivileged_userns_clone"); err == nil {
		if strings.TrimSpace(string(data)) == "0" {
			t.Skip("skipping TestNetnsRuntime_CHOrphanKill: unprivileged_userns_clone=0")
		}
	}

	const netnsDefaultCHBin = "$HOME/.local/bin/cloud-hypervisor"
	chBin := os.Getenv("CLOUD_HYPERVISOR_BIN")
	if chBin == "" {
		chBin = netnsDefaultCHBin
	}
	if _, err := os.Stat(chBin); err != nil {
		t.Skipf("skipping TestNetnsRuntime_CHOrphanKill: CH binary not found at %s", chBin)
	}

	kernelPath := netnsSkipUnlessArtifact(t, "vmlinux-x86_64")

	// set up socket dir
	socketDir, err := os.MkdirTemp("/tmp", "ch-netns-kill-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })

	if len(socketDir)+35 > 107 {
		t.Skipf("path too long for Unix socket: %s", socketDir)
	}
	socketPath := filepath.Join(socketDir, "ch.sock")

	// start netns runtime
	id := domain.NewSandboxID()
	cfg := Config{
		BinaryPath:   chBin,
		StartTimeout: 20 * time.Second,
		KernelPath:   kernelPath, // for env-var completeness (only BinaryPath/StartTimeout matter here)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	rt, err := StartNetnsRuntime(ctx, cfg, id, socketPath, "") // "" = boot mode
	if err != nil {
		t.Fatalf("StartNetnsRuntime: %v", err)
	}
	t.Cleanup(func() { rt.Stop() }) // safety net; test calls Stop explicitly below

	t.Logf("netns child pid/pgid: %d", rt.childPgid)

	// poll CH API until ready
	c := newClient(socketPath)
	pollStart := time.Now()
	const pingTimeout = 25 * time.Second
	var pingErr error
	for time.Since(pollStart) < pingTimeout {
		pingCtx, pingCancel := context.WithTimeout(ctx, 500*time.Millisecond)
		pingErr = c.Ping(pingCtx)
		pingCancel()
		if pingErr == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if pingErr != nil {
		t.Logf("child stderr:\n%s", rt.ChildStderr())
		t.Fatalf("CH API not ready after %v: %v", pingTimeout, pingErr)
	}
	t.Logf("CH API ready in %v", time.Since(pollStart))

	// locate CH pid
	chPID, err := findCHPidForSocket(socketPath)
	if err != nil {
		t.Fatalf("could not locate CH pid: %v", err)
	}
	t.Logf("CH pid: %d (expected in pgid %d)", chPID, rt.childPgid)

	// Verify CH is alive before Stop.
	if killErr := syscall.Kill(chPID, 0); killErr != nil {
		t.Fatalf("CH pid %d not alive before rt.Stop(): %v", chPID, killErr)
	}

	// call rt.Stop() and verify CH is gone
	stopStart := time.Now()
	rt.Stop()
	t.Logf("rt.Stop() returned in %v", time.Since(stopStart))

	const reaped = "ESRCH"
	const reaperTimeout = 5 * time.Second
	deadline := time.Now().Add(reaperTimeout)
	for time.Now().Before(deadline) {
		killErr := syscall.Kill(chPID, 0)
		if killErr != nil && errors.Is(killErr, syscall.ESRCH) {
			t.Logf("PASS: CH pid %d gone (%s) within %v of rt.Stop()",
				chPID, reaped, time.Since(stopStart))
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Errorf("FAIL: CH pid %d still alive %v after rt.Stop() — orphan leak",
		chPID, reaperTimeout)
}

// findCHPidForSocket scans /proc/*/cmdline for a cloud-hypervisor process
// with --api-socket socketPath and returns its PID. Returns an error if no
// matching process is found.
func findCHPidForSocket(socketPath string) (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a numeric PID directory
		}
		cmdline, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		if err != nil {
			continue // process may have exited; skip
		}
		// /proc/pid/cmdline uses NUL bytes as field separators.
		// Convert to spaces for easy substring search.
		normalized := strings.ReplaceAll(string(cmdline), "\x00", " ")
		if strings.Contains(normalized, "cloud-hypervisor") && strings.Contains(normalized, socketPath) {
			return pid, nil
		}
	}
	return 0, &os.PathError{Op: "findCHPidForSocket", Path: socketPath, Err: os.ErrNotExist}
}
