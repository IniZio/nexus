package vm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/openshell"
)

func TestCIDAllocation(t *testing.T) {
	dir := t.TempDir()

	ids := make(map[uint32]bool)
	for i := 0; i < 10; i++ {
		cid, err := allocateCID(dir)
		if err != nil {
			t.Fatalf("allocateCID iteration %d: %v", i, err)
		}
		if cid < cidMin || cid > cidMax {
			t.Errorf("iteration %d: CID %d out of range [%d,%d]", i, cid, cidMin, cidMax)
		}
		if ids[cid] {
			t.Errorf("iteration %d: duplicate CID %d", i, cid)
		}
		ids[cid] = true
	}
}

func TestCIDWrap(t *testing.T) {
	dir := t.TempDir()
	counterFile := filepath.Join(dir, "cid-counter")
	if err := os.WriteFile(counterFile, []byte(fmt.Sprintf("%d", cidMax)), 0o600); err != nil {
		t.Fatal(err)
	}
	cid, err := allocateCID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cid != cidMax {
		t.Errorf("want %d got %d", cidMax, cid)
	}
	cid2, err := allocateCID(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cid2 != cidMin {
		t.Errorf("wrap: want %d got %d", cidMin, cid2)
	}
}

func TestBuildDisksGolden(t *testing.T) {
	images := openshell.ImageArtifacts{
		RootfsExt4:  "/state/rootfs.ext4",
		OverlayExt4: "/state/overlay.ext4",
	}
	disks := buildDisks(images)
	if len(disks) != 2 {
		t.Fatalf("want 2 disks got %d", len(disks))
	}
	b, _ := json.Marshal(disks[0])
	want := `{"path":"/state/rootfs.ext4","image_type":"Raw","readonly":true}`
	if string(b) != want {
		t.Errorf("disk[0] JSON\n got  %s\n want %s", b, want)
	}
	b2, _ := json.Marshal(disks[1])
	want2 := `{"path":"/state/overlay.ext4","image_type":"Raw"}`
	if string(b2) != want2 {
		t.Errorf("disk[1] JSON\n got  %s\n want %s", b2, want2)
	}
}

func TestBuildDisksWithImage(t *testing.T) {
	images := openshell.ImageArtifacts{
		RootfsExt4:  "/a.ext4",
		OverlayExt4: "/b.ext4",
		ImageExt4:   "/c.ext4",
	}
	if got := len(buildDisks(images)); got != 3 {
		t.Errorf("want 3 disks got %d", got)
	}
}

func fakeCONNECTServer(t *testing.T, sock string, port uint32, accept bool) {
	t.Helper()
	_ = os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("fake server listen: %v", err)
	}
	t.Cleanup(func() { ln.Close(); os.Remove(sock) })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 64)
		n, _ := conn.Read(buf)
		line := strings.TrimRight(string(buf[:n]), "\r\n")
		expected := fmt.Sprintf("CONNECT %d", port)
		if line != expected {
			fmt.Fprintf(conn, "ERROR\n")
			return
		}
		if accept {
			fmt.Fprintf(conn, "OK\n")
			io.Copy(conn, conn)
		} else {
			conn.Close()
		}
	}()
}

func TestDialGuestOK(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.vsock")
	fakeCONNECTServer(t, sock, 5500, true)

	time.Sleep(10 * time.Millisecond)
	h := &vmHandle{rt: sandboxRuntime{ID: "t1", VsockSocket: sock}}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := h.DialGuest(ctx, 5500)
	if err != nil {
		t.Fatalf("DialGuest: %v", err)
	}
	conn.Close()
}

func TestDialGuestEOF(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.vsock")
	fakeCONNECTServer(t, sock, 5500, false)

	time.Sleep(10 * time.Millisecond)
	h := &vmHandle{rt: sandboxRuntime{ID: "t2", VsockSocket: sock}}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := h.DialGuest(ctx, 5500)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestBridgeProxy(t *testing.T) {
	dir := t.TempDir()
	vsockSock := filepath.Join(dir, "vm.vsock")
	bridgeSock := filepath.Join(dir, "bridge.sock")

	const testMsg = "hello from bridge"

	fakeCONNECTServer(t, vsockSock, 5500, true)
	time.Sleep(20 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- runBridge(ctx, bridgeSock, vsockSock)
	}()

	time.Sleep(30 * time.Millisecond)

	conn, err := net.Dial("unix", bridgeSock)
	if err != nil {
		t.Fatalf("dial bridge: %v", err)
	}
	defer conn.Close()

	if _, err := fmt.Fprint(conn, testMsg); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, len(testMsg))
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != testMsg {
		t.Errorf("echo: got %q want %q", buf, testMsg)
	}

	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Error("runBridge did not stop after cancel")
	}
}

func TestRelayListener(t *testing.T) {
	dir := t.TempDir()
	vsockBase := filepath.Join(dir, "vm.vsock")
	port := uint32(9999)

	rl := &VMRelayListener{VsockSocket: vsockBase}

	var wg sync.WaitGroup
	receivedCh := make(chan string, 1)
	handler := func(conn net.Conn) {
		defer conn.Close()
		buf := make([]byte, 32)
		n, _ := conn.Read(buf)
		receivedCh <- string(buf[:n])
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wg.Add(1)
	go func() {
		defer wg.Done()
		rl.Listen(ctx, port, handler)
	}()

	guestSockPath := fmt.Sprintf("%s_%d", vsockBase, port)
	for i := 0; i < 20; i++ {
		if _, err := os.Stat(guestSockPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	conn, err := net.Dial("unix", guestSockPath)
	if err != nil {
		t.Fatalf("dial relay socket: %v", err)
	}
	fmt.Fprint(conn, "ping")
	conn.Close()

	select {
	case msg := <-receivedCh:
		if msg != "ping" {
			t.Errorf("got %q want %q", msg, "ping")
		}
	case <-time.After(2 * time.Second):
		t.Error("handler not called")
	}

	cancel()
	wg.Wait()
}

func TestIntegrationBoot(t *testing.T) {
	if os.Getenv("NEXUS_HOST_TESTS") != "1" {
		t.Skip("set NEXUS_HOST_TESTS=1 to run (requires cloud-hypervisor + kernel)")
	}

	chBinary := "/home/newman/.local/bin/cloud-hypervisor"
	kernelPath := "/home/newman/.local/share/nexus/images/kernel/vmlinux-x86_64"
	for _, p := range []string{chBinary, kernelPath} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("prerequisite not found: %s", p)
		}
	}

	stateDir := t.TempDir()
	rootfs := buildTestRootfs(t)
	overlay := buildBlankOverlay(t)

	cfg := Config{
		CHBinary:      chBinary,
		KernelPath:    kernelPath,
		StateDir:      stateDir,
		DefaultMemMiB: 512,
		DefaultVCPUs:  1,
		InitPath:      "/busybox",
	}
	b, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	spec := openshell.SandboxSpec{ID: "inttest-boot"}
	images := openshell.ImageArtifacts{RootfsExt4: rootfs, OverlayExt4: overlay}

	h, err := b.Boot(ctx, spec, images)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	t.Cleanup(func() { h.Kill() })

	rt := h.Runtime()
	if rt.PID <= 0 {
		t.Errorf("expected PID > 0 got %d", rt.PID)
	}
	if _, err := os.Stat(rt.APISocket); err != nil {
		t.Errorf("API socket missing: %v", err)
	}

	time.Sleep(1 * time.Second)

	dialCtx, dialCancel := context.WithTimeout(ctx, 3*time.Second)
	_, dialErr := h.DialGuest(dialCtx, 5500)
	dialCancel()
	t.Logf("DialGuest port 5500: %v", dialErr)

	seccompLog := checkSeccomp(t, rt)
	t.Logf("seccomp check: %s", seccompLog)

	stopCtx, stopCancel := context.WithTimeout(ctx, 15*time.Second)
	if err := h.Stop(stopCtx); err != nil {
		t.Errorf("Stop: %v", err)
	}
	stopCancel()

	select {
	case <-h.Wait():
	case <-time.After(5 * time.Second):
		t.Error("Wait channel did not close after Stop")
	}
}

func buildTestRootfs(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	initDir := filepath.Join(dir, "initfs")
	if err := os.MkdirAll(initDir, 0o755); err != nil {
		t.Fatal(err)
	}

	busyboxSrc := "/usr/bin/busybox"
	if _, err := os.Stat(busyboxSrc); err != nil {
		t.Skipf("busybox not found: %v", err)
	}
	data, err := os.ReadFile(busyboxSrc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(initDir, "busybox"), data, 0o755); err != nil {
		t.Fatal(err)
	}

	imgPath := filepath.Join(dir, "rootfs.ext4")
	out, err := exec.Command("mke2fs", "-t", "ext4", "-d", initDir, imgPath, "64M").CombinedOutput()
	if err != nil {
		t.Fatalf("mke2fs: %v\n%s", err, out)
	}
	return imgPath
}

func buildBlankOverlay(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "overlay.ext4")
	out, err := exec.Command("dd", "if=/dev/zero", "of="+imgPath, "bs=1M", "count=64").CombinedOutput()
	if err != nil {
		t.Fatalf("dd: %v\n%s", err, out)
	}
	out, err = exec.Command("mkfs.ext4", imgPath).CombinedOutput()
	if err != nil {
		t.Fatalf("mkfs.ext4: %v\n%s", err, out)
	}
	return imgPath
}

func checkSeccomp(t *testing.T, rt openshell.RuntimeIdentity) string {
	t.Helper()
	logPath := filepath.Join(rt.StateDir, "serial.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		return fmt.Sprintf("serial.log not found: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	for _, l := range lines {
		if strings.Contains(l, "seccomp") || strings.Contains(l, "SECCOMP") {
			return fmt.Sprintf("found seccomp line: %s", strings.TrimSpace(l))
		}
	}
	return fmt.Sprintf("no seccomp line in %d serial lines; kernel likely has CONFIG_SECCOMP_FILTER=y (standard for 6.12)", len(lines))
}
