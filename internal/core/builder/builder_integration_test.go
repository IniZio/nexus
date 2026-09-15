//go:build integration

package builder_test

// builder_integration_test.go proves, end-to-end, that:
//
//  1. (TestImageBootsAndAgentReachable) An ext4 image produced by the builder's
//     ext4 path boots under Cloud Hypervisor with nexus3-agent as PID-1 init,
//     and the guest agent is reachable over vsock.
//
//  2. (TestBuildkitBaseBuild) Builder.Build against images/base/Containerfile
//     produces a digest-keyed ext4 in the image cache — skipped when no
//     buildkitd endpoint is available.
//
// # Driver gap note (Run-4 input)
//
// TestImageBootsAndAgentReachable bypasses the nexus3 CHDriver because the
// driver's vmConfig struct has no disk/blk surface (Payload/CPUs/Memory/Serial
// only; no Disks field). The pinned kernel DOES have virtio-blk built in
// (confirmed via strings(1) on vmlinux-x86_64: virtio_blk.c, virtio-blk,
// virtio_blk.queue_depth strings present). The gap is driver-layer only: Run 4
// must add DiskImagePath to Config and vmDiskConfig to vmConfig in the
// cloudhypervisor package. This test calls cloud-hypervisor directly with
// --disk CLI flags to prove the ext4 boot path independently.
//
// # Guard conditions
//
// TestImageBootsAndAgentReachable skips when:
//   - /dev/kvm is absent
//   - cloud-hypervisor binary is absent (default ~/.local/bin/cloud-hypervisor;
//     override with CLOUD_HYPERVISOR_BIN)
//   - mke2fs is absent (install e2fsprogs)
//   - images/kernel/vmlinux-x86_64 is absent
//   - socket path would exceed the Linux sun_path limit
//
// TestBuildkitBaseBuild skips when no buildkitd endpoint is reachable.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/image"
)

// ── constants ─────────────────────────────────────────────────────────────────

// defaultCHBin is the expected cloud-hypervisor binary path.
var defaultCHBin = filepath.Join(os.Getenv("HOME"), ".local/bin/cloud-hypervisor")

// kernelRelPath is the kernel artifact path relative to the repo root.
const kernelRelPath = "images/kernel/vmlinux-x86_64"

// maxSunPathLen is the maximum usable length of a Unix socket path on Linux.
const maxSunPathLen = 107

// ── guard helpers ─────────────────────────────────────────────────────────────

func skipUnlessKVM(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("skipping: /dev/kvm not available")
	}
}

func skipUnlessCH(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("CLOUD_HYPERVISOR_BIN")
	if bin == "" {
		bin = defaultCHBin
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("skipping: cloud-hypervisor not found at %s (set CLOUD_HYPERVISOR_BIN to override)", bin)
	}
	return bin
}

// repoRoot returns the directory containing go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Skipf("skipping: go env GOMOD: %v", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		t.Skip("skipping: not in a Go module")
	}
	return filepath.Dir(mod)
}

func skipUnlessKernel(t *testing.T) string {
	t.Helper()
	p := filepath.Join(repoRoot(t), kernelRelPath)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("skipping: kernel artifact not found at %s (run scripts/fetch-boot-artifacts.sh)", p)
	}
	return p
}

// ── binary builders ───────────────────────────────────────────────────────────

// buildNexus3Agent compiles cmd/nexus3-agent as a static Linux/amd64 binary
// and returns its path in a temp dir cleaned up when t ends.
func buildNexus3Agent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "nexus3-agent")
	cmd := exec.Command("go", "build", "-o", bin,
		"github.com/IniZio/nexus3/cmd/nexus3-agent")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build nexus3-agent: %s\n%v", out, err)
	}
	return bin
}

// buildHelloGuestBin compiles a tiny static Linux/amd64 binary that prints
// "hello-from-guest" to stdout and exits 0. This is the command executed via
// agent.Exec to prove the guest agent answers.
func buildHelloGuestBin(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "hello.go")
	binFile := filepath.Join(dir, "hello")

	src := `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stdout, "hello-from-guest")
}
`
	if err := os.WriteFile(srcFile, []byte(src), 0o600); err != nil {
		t.Fatalf("write hello.go: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", binFile, srcFile)
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build hello: %s\n%v", out, err)
	}
	return binFile
}

// ── rootfs builder ────────────────────────────────────────────────────────────

// buildRootfsDir creates a minimal rootfs tree suitable for nexus3-agent as
// PID-1 init. The tree contains:
//
//	/sbin/nexus3-agent  — the agent binary (static)
//	/bin/hello          — a tiny static hello binary for Exec probing
//	/dev/               — empty; agent mounts devtmpfs here
//	/proc/              — empty; agent mounts proc here
//	/sys/               — empty; agent mounts sysfs here
//	/tmp/               — empty; scratch space
func buildRootfsDir(t *testing.T, agentBin, helloBin string) string {
	t.Helper()
	rootfs := t.TempDir()

	for _, d := range []string{"sbin", "bin", "dev", "proc", "sys", "tmp"} {
		if err := os.MkdirAll(filepath.Join(rootfs, d), 0o755); err != nil {
			t.Fatalf("mkdir rootfs/%s: %v", d, err)
		}
	}
	copyExec(t, agentBin, filepath.Join(rootfs, "sbin", "nexus3-agent"))
	copyExec(t, helloBin, filepath.Join(rootfs, "bin", "hello"))
	return rootfs
}

// copyExec copies src to dst with executable permissions.
func copyExec(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("copyExec read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatalf("copyExec write %s: %v", dst, err)
	}
}

// ── ext4 producer ─────────────────────────────────────────────────────────────

// buildExt4 creates a raw ext4 image from srcDir using the builder's
// runMke2fs path. Returns the path to the image file.
func buildExt4(t *testing.T, srcDir string) string {
	t.Helper()
	ext4 := filepath.Join(t.TempDir(), "disk.ext4")
	// imageMinSizeBytes from ext4.go: 64 MiB.
	const minSize = 64 * 1024 * 1024
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := builder.RunMke2fs(ctx, srcDir, ext4, minSize); err != nil {
		t.Fatalf("RunMke2fs: %v", err)
	}
	fi, err := os.Stat(ext4)
	if err != nil {
		t.Fatalf("stat ext4: %v", err)
	}
	t.Logf("ext4 image: %s (%d bytes)", ext4, fi.Size())
	return ext4
}

// ── Cloud Hypervisor direct boot ──────────────────────────────────────────────

// bootCHWithDisk launches cloud-hypervisor directly with CLI flags, using the
// given ext4 as a virtio-blk root disk. Returns the vsock socket path and
// serial log path. The CH process is killed on test completion; serial log is
// printed on failure.
//
// NOTE: This bypasses the nexus3 CHDriver (Run-4 gap: driver lacks disk/blk
// surface). CH CLI mode auto-boots the VM from the supplied flags.
func bootCHWithDisk(t *testing.T, chBin, kernelPath, ext4Path string) (vsockSock, serialLog string) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("/tmp", "ch-blk-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	vsockSock = filepath.Join(tmpDir, "vsock.sock")
	if len(vsockSock) > maxSunPathLen {
		os.RemoveAll(tmpDir)
		t.Skipf("skipping: vsock socket path too long (%d > %d): %s",
			len(vsockSock), maxSunPathLen, vsockSock)
	}
	serialLog = filepath.Join(tmpDir, "serial.log")

	//nolint:gosec // subprocess with controlled arguments; test-only code.
	cmd := exec.Command(chBin,
		"--kernel", kernelPath,
		"--cmdline", "root=/dev/vda rw init=/sbin/nexus3-agent console=ttyS0 panic=1",
		// image_type=raw is required on CH v52+. Without it CH auto-detects
		// the image type and disables sector-0 writes as a protection against
		// overwriting MBR/GPT partition tables. Ext4 writes its superblock at
		// sector 0 (byte offset 1024) on rw mount, so this protection causes
		// EIO. Explicit image_type=raw bypasses the auto-detection path and
		// permits sector-0 writes.
		// See: CH vmm/src/device_manager.rs:2745 "Disabling sector 0 writes".
		"--disk", "path="+ext4Path+",image_type=raw",
		"--cpus", "boot=1",
		"--memory", "size=256M",
		"--vsock", fmt.Sprintf("cid=3,socket=%s", vsockSock),
		"--serial", "file="+serialLog,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("cloud-hypervisor start: %v", err)
	}

	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
		if t.Failed() {
			serial, _ := os.ReadFile(serialLog)
			t.Logf("serial log (%s):\n%s", serialLog, serial)
		}
		os.RemoveAll(tmpDir)
	})

	return vsockSock, serialLog
}

// waitForSocket polls until path appears as a socket file or timeout elapses.
func waitForSocket(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("socket %s did not appear within %v", path, timeout)
}

// ── vsock dialer ──────────────────────────────────────────────────────────────

// vsockDialer implements driver.GuestDialer using a CH vsock AF_UNIX socket.
// The id parameter is ignored; the socket path is fixed at construction time.
//
// Protocol: connect to d.sock, write "CONNECT <port>\n", read "OK <n>\n".
// This is the standard virtio-vsock-proxy handshake used by Cloud Hypervisor.
type vsockDialer struct {
	sock string
}

var _ driver.GuestDialer = (*vsockDialer)(nil)

func (d *vsockDialer) DialGuest(ctx context.Context, _ domain.SandboxID, port uint32) (net.Conn, error) {
	var nd net.Dialer
	conn, err := nd.DialContext(ctx, "unix", d.sock)
	if err != nil {
		return nil, fmt.Errorf("vsock dial %s: %w", d.sock, err)
	}

	// Apply handshake deadline: min(ctx deadline, 5s).
	deadline := time.Now().Add(5 * time.Second)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vsock set deadline: %w", err)
	}

	// Send CONNECT handshake.
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vsock CONNECT: %w", err)
	}

	// Read reply line.
	br := bufio.NewReader(conn)
	reply, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("vsock read reply: %w", err)
	}
	reply = strings.TrimRight(reply, "\r\n")
	if !strings.HasPrefix(reply, "OK") {
		conn.Close()
		return nil, fmt.Errorf("vsock multiplexer rejected: %q", reply)
	}

	// Clear deadline — caller controls lifetime from here.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vsock clear deadline: %w", err)
	}
	// Wrap conn to drain the bufio buffer before reading from the raw conn.
	return &vsockConn{Conn: conn, r: io.MultiReader(br, conn)}, nil
}

// vsockConn wraps a net.Conn to drain any data buffered in the bufio.Reader
// before falling through to the underlying connection.
type vsockConn struct {
	net.Conn
	r io.Reader
}

func (c *vsockConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// ── tests ─────────────────────────────────────────────────────────────────────

// TestImageBootsAndAgentReachable is the primary Run-3 proof:
// the builder's ext4 pipeline produces a rootfs that boots under Cloud
// Hypervisor with nexus3-agent as PID-1 init, and the guest agent answers an
// Exec RPC over vsock.
//
// DRIVER GAP (Run-4 input): The nexus3 CHDriver does not yet pass disk config
// to Cloud Hypervisor (vmConfig has no Disks field). This test invokes CH
// directly with --disk to provide the end-to-end proof independently of the
// driver gap. The gap is shallow: the kernel has virtio-blk built in, and CH
// supports --disk path=<raw>. Run 4 must wire DiskImagePath through the driver.
func TestImageBootsAndAgentReachable(t *testing.T) {
	skipUnlessKVM(t)
	chBin := skipUnlessCH(t)
	kernelPath := skipUnlessKernel(t)
	if !builder.Mke2fsAvailable() {
		t.Skip("skipping: mke2fs not available; install e2fsprogs")
	}

	agentBin := buildNexus3Agent(t)
	helloBin := buildHelloGuestBin(t)

	rootfsDir := buildRootfsDir(t, agentBin, helloBin)

	// Export to ext4 via the builder's ext4 path (RunMke2fs = runMke2fs).
	ext4Path := buildExt4(t, rootfsDir)

	// Boot CH with the ext4 as a virtio-blk root disk (root=/dev/vda).
	vsockSock, _ := bootCHWithDisk(t, chBin, kernelPath, ext4Path)

	// Wait for CH to create the vsock socket (agent must bind vsock first).
	//    Typically takes 2-4 s: kernel boot + devtmpfs + vsock.Listen.
	t.Log("waiting for vsock socket to appear (agent startup)...")
	waitForSocket(t, vsockSock, 30*time.Second)
	// Give the agent an extra moment to finish binding both vsock listeners.
	time.Sleep(500 * time.Millisecond)
	t.Logf("vsock socket appeared: %s", vsockSock)

	dialer := &vsockDialer{sock: vsockSock}
	id := domain.NewSandboxID()
	c := agent.NewClient(dialer, id)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var stdout bytes.Buffer
	exitCode, err := c.Exec(ctx, agent.ExecOptions{
		Argv:   []string{"/bin/hello"},
		Stdout: &stdout,
		Stderr: os.Stderr,
	})
	if err != nil {
		t.Fatalf("agent.Exec over vsock: %v\n(this proves agent is reachable; failure means vsock/gRPC broke)", err)
	}
	if exitCode != 0 {
		t.Errorf("Exec exit code: got %d, want 0", exitCode)
	}
	out := stdout.String()
	if !strings.Contains(out, "hello-from-guest") {
		t.Errorf("expected 'hello-from-guest' in stdout, got %q", out)
	}
	t.Logf("guest Exec succeeded: %q (exit %d)", out, exitCode)
}

// TestBuildkitBaseBuild skips unless a buildkitd endpoint is reachable, then
// runs Builder.Build on a minimal Containerfile and asserts a digest-keyed
// ext4 lands in the image cache.
func TestBuildkitBaseBuild(t *testing.T) {
	buildkitHost, ok := buildkitEndpoint()
	if !ok {
		t.Skip("no buildkitd available: neither BUILDKIT_HOST is set nor default socket exists")
	}
	if !builder.Mke2fsAvailable() {
		t.Skip("skipping: mke2fs not available; install e2fsprogs")
	}

	agentBin := buildNexus3Agent(t)
	cacheDir := t.TempDir()
	cache, err := image.NewCache(cacheDir)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	// Set up a minimal workspace with .nexus/Containerfile pointing to alpine.
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".nexus"), 0o755); err != nil {
		t.Fatalf("mkdir .nexus: %v", err)
	}
	cf := "FROM alpine:latest\n"
	if err := os.WriteFile(filepath.Join(workspace, ".nexus", "Containerfile"), []byte(cf), 0o644); err != nil {
		t.Fatalf("write Containerfile: %v", err)
	}

	b, err := builder.New(builder.Config{
		BuildkitdAddr:   buildkitHost,
		AgentBinaryPath: agentBin,
	}, cache)
	if err != nil {
		t.Skipf("no buildkitd available (connect failed): %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	img, err := b.Build(ctx, builder.BuildRequest{
		BaseRef:      "alpine:latest",
		WorkspaceDir: workspace,
		Ref:          "integration-test:alpine",
	})
	if err != nil {
		t.Fatalf("Builder.Build: %v", err)
	}
	if !img.Digest.Valid() {
		t.Errorf("Build returned invalid digest %q", img.Digest)
	}
	if img.Kind != domain.KindBase {
		t.Errorf("img.Kind = %v, want KindBase", img.Kind)
	}

	// Verify the ext4 artifact is retrievable from the cache.
	cached, err := cache.Get(ctx, img.Digest)
	if err != nil {
		t.Fatalf("cache.Get(%s): %v", img.Digest, err)
	}
	if cached.Digest != img.Digest {
		t.Errorf("cached.Digest = %v, want %v", cached.Digest, img.Digest)
	}
	t.Logf("BuildkitBaseBuild: digest=%s size=%d", img.Digest, img.Size)
}

// buildkitEndpoint returns the buildkitd address and true if available.
func buildkitEndpoint() (string, bool) {
	if h := os.Getenv("BUILDKIT_HOST"); h != "" {
		return h, true
	}
	const defaultSock = "/run/buildkit/buildkitd.sock"
	if _, err := os.Stat(defaultSock); err == nil {
		return "unix://" + defaultSock, true
	}
	return "", false
}
