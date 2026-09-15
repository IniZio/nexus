//go:build integration

package cloudhypervisor

// disk_integration_test.go verifies that the Cloud Hypervisor driver can boot
// a guest from a raw ext4 root disk (virtio-blk) via Config.DiskImagePath.
//
// # Test
//
//   - TestDiskBoot: builds a minimal ext4 image containing a static
//     nexus3-agent binary at /sbin/nexus3-agent, boots it THROUGH the driver
//     (not by calling cloud-hypervisor directly), and asserts that the guest
//     agent is reachable over vsock via drv.DialGuest.
//
// # Guard conditions
//
// TestDiskBoot skips (never fails) when the environment lacks:
//   - /dev/kvm
//   - cloud-hypervisor binary (default: ~/.local/bin/cloud-hypervisor;
//     override with CLOUD_HYPERVISOR_BIN)
//   - mke2fs (install e2fsprogs)
//   - images/kernel/vmlinux-x86_64 (run scripts/fetch-boot-artifacts.sh)
//   - socket path fits within the 107-byte Linux sun_path limit
//
// # Running this test
//
//	bash scripts/fetch-boot-artifacts.sh
//	go test -tags integration -run 'Disk' ./internal/core/driver/cloudhypervisor/ -v

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

// diskTestSunPathMax is the usable sun_path limit for AF_UNIX sockets on Linux.
// Defined locally to avoid re-declaring the symbol in boot_integration_test.go
// (which uses the same value as maxSocketPathLen in the package).
const diskTestSunPathMax = 107

// skipUnlessMke2fs skips t if mke2fs is not found in PATH.
func skipUnlessMke2fs(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("skipping: mke2fs not found in PATH (install e2fsprogs)")
	}
}

// buildHelloBinForDisk compiles a tiny static Linux/amd64 binary that prints
// "hello-from-disk\n" to stdout and exits 0. Used in TestDiskBoot to confirm
// the agent serves Exec RPCs over vsock from a disk-booted guest.
func buildHelloBinForDisk(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	srcFile := filepath.Join(dir, "hello.go")
	binFile := filepath.Join(dir, "hello")

	const src = `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stdout, "hello-from-disk")
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
		t.Fatalf("go build hello (disk test): %s\n%v", out, err)
	}
	return binFile
}

// buildRootfsForDisk creates a minimal rootfs directory tree for disk boot:
//
//	/sbin/nexus3-agent  — the agent binary (PID-1 init)
//	/bin/hello          — tiny "hello-from-disk" binary for Exec probing
//	/dev/               — empty; agent mounts devtmpfs here
//	/proc/              — empty; agent mounts procfs here
//	/sys/               — empty; agent mounts sysfs here
//	/tmp/               — empty; scratch space
//
// Returns the rootfs directory path.
func buildRootfsForDisk(t *testing.T, agentBin, helloBin string) string {
	t.Helper()
	rootfs := t.TempDir()

	for _, d := range []string{"sbin", "bin", "dev", "proc", "sys", "tmp"} {
		if err := os.MkdirAll(filepath.Join(rootfs, d), 0o755); err != nil {
			t.Fatalf("mkdir rootfs/%s: %v", d, err)
		}
	}

	for _, pair := range [][2]string{
		{agentBin, filepath.Join(rootfs, "sbin", "nexus3-agent")},
		{helloBin, filepath.Join(rootfs, "bin", "hello")},
	} {
		data, err := os.ReadFile(pair[0])
		if err != nil {
			t.Fatalf("read %s: %v", pair[0], err)
		}
		if err := os.WriteFile(pair[1], data, 0o755); err != nil {
			t.Fatalf("write %s: %v", pair[1], err)
		}
	}
	return rootfs
}

// buildExt4Image creates a raw ext4 image from srcDir using mke2fs -d.
// The image is pre-allocated as a 64 MiB sparse file. Returns the image path.
func buildExt4Image(t *testing.T, srcDir string) string {
	t.Helper()

	mke2fsPath, err := exec.LookPath("mke2fs")
	if err != nil {
		t.Skipf("skipping: mke2fs not available: %v", err)
	}

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "rootfs.ext4")

	// Pre-allocate a 64 MiB sparse file. mke2fs reads the file size and
	// formats it without a separate blocks-count argument.
	const imgSize = 64 * 1024 * 1024
	f, cerr := os.Create(imgPath)
	if cerr != nil {
		t.Fatalf("create ext4 image file: %v", cerr)
	}
	f.Close()
	if err := os.Truncate(imgPath, imgSize); err != nil {
		t.Fatalf("truncate ext4 image file: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, mke2fsPath,
		"-t", "ext4",
		"-d", srcDir,
		"-U", "00000000-0000-0000-0000-000000000000", // deterministic UUID
		"-E", "hash_seed=00000000-0000-0000-0000-000000000000",
		imgPath,
	)
	cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mke2fs: %v\n%s", err, out)
	}

	fi, err := os.Stat(imgPath)
	if err != nil {
		t.Fatalf("stat ext4 image: %v", err)
	}
	t.Logf("ext4 image: %s (%d bytes)", imgPath, fi.Size())
	return imgPath
}

// waitForAgentReady polls drv.DialGuest on port 1024 (AgentControlPort) until
// the guest agent accepts a connection or timeout elapses. The vsock socket
// file is created by CH at vm.create time, long before the guest agent starts
// listening; polling DialGuest is the correct signal.
func waitForAgentReady(t *testing.T, drv *CHDriver, id domain.SandboxID, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	const agentControlPort = 1024 // driver.AgentControlPort
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		conn, err := drv.DialGuest(ctx, id, agentControlPort)
		cancel()
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("guest agent vsock port %d not reachable within %v", agentControlPort, timeout)
}

// TestDiskBoot boots a microVM from a raw ext4 disk image through the driver
// and verifies the guest agent is reachable over vsock via DialGuest.
//
// Boot path:
//  1. Compile nexus3-agent (static Linux/amd64) as PID-1 init.
//  2. Compile a tiny "hello-from-disk" binary for Exec probing.
//  3. Assemble a minimal rootfs (sbin/nexus3-agent, bin/hello, empty mount points).
//  4. Pack it into a raw ext4 image with mke2fs -d.
//  5. Boot via driver.Start with DiskImagePath set — NOT by calling CH directly.
//     Driver uses cmdline "root=/dev/vda rw init=/sbin/nexus3-agent console=ttyS0".
//  6. Wait for the vsock socket to appear (agent has bound its listeners).
//  7. Dial the agent via drv (implements GuestDialer) and run Exec /bin/hello.
//  8. Assert "hello-from-disk" appears in stdout.
func TestDiskBoot(t *testing.T) {
	// guards
	skipUnlessKVM(t)
	chBin := skipUnlessCHBin(t)
	kernelPath := skipUnlessArtifact(t, "vmlinux-x86_64")
	skipUnlessMke2fs(t)

	// build binaries
	agentBin := buildNexus3Agent(t)
	helloBin := buildHelloBinForDisk(t)

	// assemble rootfs
	rootfsDir := buildRootfsForDisk(t, agentBin, helloBin)

	// build ext4
	ext4Path := buildExt4Image(t, rootfsDir)

	// socket dir
	// Use /tmp as base to stay under the 107-byte Linux sun_path limit.
	// The driver enforces: len(socketDir)+35 <= diskTestSunPathMax.
	socketDir, err := os.MkdirTemp("/tmp", "ch-disk-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	// serial
	// Capture serial to a file so the kernel cmdline + agent boot logs are
	// visible in t.Logf on failure.
	serialPath := filepath.Join(socketDir, "serial.log")

	// driver
	drv, err := New(Config{
		BinaryPath: chBin,
		SocketDir:  socketDir,
		KernelPath: kernelPath,
		// Setting DiskImagePath activates virtio-blk disk boot. The driver
		// adds a Disks entry with image_type=raw and substitutes the
		// disk-boot default cmdline when Cmdline is empty:
		//   root=/dev/vda rw init=/sbin/nexus3-agent console=ttyS0
		DiskImagePath:    ext4Path,
		SerialOutputPath: serialPath,
		VCPUs:            1,
		MemoryMiB:        256,
		StartTimeout:     30 * time.Second,
	})
	if err != nil {
		os.RemoveAll(socketDir)
		t.Fatalf("New CHDriver: %v", err)
	}

	id := domain.NewSandboxID()

	// Track VMM PID so cleanup can SIGKILL it if Stop hangs.
	var vmmPID int

	t.Cleanup(func() {
		// Print serial log on failure before stopping the VM.
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 {
			if t.Failed() {
				t.Logf("guest serial output:\n%s", content)
			}
		}
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = drv.Stop(stopCtx, id)
		if vmmPID != 0 {
			_ = syscall.Kill(-vmmPID, syscall.SIGKILL)
		}
		drv.clearState(id)
		os.RemoveAll(socketDir)
	})

	// start
	startCtx, startCancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer startCancel()

	if _, err := drv.Start(startCtx, driver.StartRequest{SandboxID: id}); err != nil {
		t.Fatalf("drv.Start (disk boot): %v\n"+
			"(check serial log at %s)", err, serialPath)
	}

	// Record VMM PID for cleanup.
	drv.mu.Lock()
	if proc := drv.procs[id]; proc != nil {
		vmmPID = proc.pid
	}
	drv.mu.Unlock()

	// wait for agent
	// The vsock socket file is created by CH at vm.create time, long before
	// the guest agent starts. Poll DialGuest until the agent's vsock listener
	// is reachable (kernel boot + ext4 mount + agent start ~2-5 s).
	t.Logf("polling for guest agent on vsock (disk boot, up to 30 s)...")
	waitForAgentReady(t, drv, id, 30*time.Second)
	t.Log("guest agent vsock reachable — proceeding with Exec")

	// assert agent reachable via DialGuest
	// agent.NewClient uses drv (which implements driver.GuestDialer via
	// drv.DialGuest → vsock AF_UNIX socket) to reach the control and data
	// planes inside the guest.
	c := agent.NewClient(drv, id)

	execCtx, execCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer execCancel()

	var stdout strings.Builder
	exitCode, err := c.Exec(execCtx, agent.ExecOptions{
		Argv:   []string{"/bin/hello"},
		Stdout: &stdout,
		Stderr: os.Stderr,
	})
	if err != nil {
		// Print serial log inline so it's visible in the failure output.
		if content, _ := os.ReadFile(serialPath); len(content) > 0 {
			t.Logf("serial log:\n%s", content)
		}
		t.Fatalf("agent.Exec over vsock (disk boot): %v", err)
	}
	if exitCode != 0 {
		t.Errorf("Exec exit code: got %d, want 0", exitCode)
	}
	out := stdout.String()
	if !strings.Contains(out, "hello-from-disk") {
		t.Errorf("expected %q in Exec output, got %q", "hello-from-disk", out)
	}
	t.Logf("TestDiskBoot passed: agent Exec output=%q (exit=%d)", strings.TrimSpace(out), exitCode)
}
