//go:build integration

package acceptance

// workspace_e2e_test.go proves the Run-4 stack end-to-end on real KVM:
//   - nexus create boots a real workspace from a pipeline-built image
//   - the agent is reachable and exec works
//   - pause/resume lifecycle transitions are observable
//   - a simulated VMM crash (SIGKILL) recovers to stopped(memory_lost)
//   - rm deletes the record
//
// # Guard conditions (test SKIPS, never fails, when any prerequisite is absent)
//   - /dev/kvm accessible by this user
//   - cloud-hypervisor binary (default ~/.local/bin/cloud-hypervisor;
//     override with CLOUD_HYPERVISOR_BIN)
//   - mke2fs in PATH (e2fsprogs)
//   - images/kernel/vmlinux-x86_64 under the repository root
//   - socket path fits within the 107-byte Linux sun_path limit
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run 'Workspace' \
//	  ./internal/test/acceptance/ -v -timeout 300s

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	cloudhypervisor "github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/recovery"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
)

// e2eSunPathMax is the usable sun_path limit for AF_UNIX sockets on Linux.
const e2eSunPathMax = 107

// e2eDefaultCHBin is the default cloud-hypervisor binary path.
var e2eDefaultCHBin = filepath.Join(os.Getenv("HOME"), ".local/bin/cloud-hypervisor")

// ── skip guards ───────────────────────────────────────────────────────────────

// skipUnlessKVME2E skips t if /dev/kvm is absent or inaccessible.
func skipUnlessKVME2E(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("skipping: /dev/kvm not present — KVM is required for this test")
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("skipping: /dev/kvm not usable by this user: %v", err)
	}
	f.Close()
}

// skipUnlessCHBinE2E returns the cloud-hypervisor binary path, skipping if absent.
func skipUnlessCHBinE2E(t *testing.T) string {
	t.Helper()
	chBin := os.Getenv("CLOUD_HYPERVISOR_BIN")
	if chBin == "" {
		chBin = e2eDefaultCHBin
	}
	if _, err := os.Stat(chBin); err != nil {
		t.Skipf("skipping: cloud-hypervisor binary not found at %s "+
			"(set CLOUD_HYPERVISOR_BIN to override)", chBin)
	}
	return chBin
}

// skipUnlessMke2fsE2E skips t if mke2fs is not found in PATH.
func skipUnlessMke2fsE2E(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("skipping: mke2fs not found in PATH (install e2fsprogs)")
	}
}

// e2eKernelPath returns the absolute path to the vmlinux kernel, skipping if absent.
// It searches:
//  1. $NEXUS_KERNEL_PATH env var
//  2. images/kernel/vmlinux-x86_64 under the repository root
//  3. testdata/vmlinux-x86_64 in the driver's testdata (symlink fallback)
func e2eKernelPath(t *testing.T) string {
	t.Helper()

	if p := os.Getenv("NEXUS_KERNEL_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// Locate repo root via go env GOMOD.
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Skipf("skipping: go env GOMOD: %v", err)
	}
	goMod := strings.TrimSpace(string(out))
	if goMod == "" || goMod == os.DevNull {
		t.Skip("skipping: not in a Go module")
	}
	repoRoot := filepath.Dir(goMod)

	// Primary: images/kernel/vmlinux-x86_64
	primary := filepath.Join(repoRoot, "images", "kernel", "vmlinux-x86_64")
	if _, err := os.Stat(primary); err == nil {
		return primary
	}

	// Fallback: driver testdata.
	fallback := filepath.Join(repoRoot,
		"internal", "core", "driver", "cloudhypervisor", "testdata", "vmlinux-x86_64")
	if _, err := os.Stat(fallback); err == nil {
		return fallback
	}

	t.Skipf("skipping: vmlinux-x86_64 kernel not found — tried:\n  %s\n  %s\n"+
		"  Set NEXUS_KERNEL_PATH or run scripts/fetch-boot-artifacts.sh",
		primary, fallback)
	panic("unreachable")
}

// ── binary builders ───────────────────────────────────────────────────────────

// e2eBuildAgent compiles cmd/nexus-agent as a static Linux/amd64 binary.
func e2eBuildAgent(t *testing.T) string {
	t.Helper()

	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatalf("go env GOMOD: %v", err)
	}
	goMod := strings.TrimSpace(string(out))
	if goMod == "" || goMod == os.DevNull {
		t.Skip("skipping: go env GOMOD returned empty")
	}
	repoRoot := filepath.Dir(goMod)

	dir := t.TempDir()
	agentBin := filepath.Join(dir, "nexus-agent")

	cmd := exec.Command("go", "build", "-o", agentBin,
		"github.com/IniZio/nexus/cmd/nexus-agent")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
	)
	if buildOut, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build nexus-agent:\n%s\n%v", buildOut, err)
	}
	return agentBin
}

// e2eBuildHello compiles a tiny static Linux/amd64 binary that prints
// "hello-from-disk\n" to stdout and exits 0.
func e2eBuildHello(t *testing.T) string {
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
		t.Fatalf("go build hello (e2e): %s\n%v", out, err)
	}
	return binFile
}

// e2eBuildRootfs creates a minimal rootfs directory:
//
//	/sbin/nexus-agent  — the agent binary (PID-1 init)
//	/bin/hello          — tiny "hello-from-disk" exec target
//	/dev/ /proc/ /sys/ /tmp/ — empty mount points
func e2eBuildRootfs(t *testing.T, agentBin, helloBin string) string {
	t.Helper()
	rootfs := t.TempDir()

	for _, d := range []string{"sbin", "bin", "dev", "proc", "sys", "tmp"} {
		if err := os.MkdirAll(filepath.Join(rootfs, d), 0o755); err != nil {
			t.Fatalf("mkdir rootfs/%s: %v", d, err)
		}
	}
	for _, pair := range [][2]string{
		{agentBin, filepath.Join(rootfs, "sbin", "nexus-agent")},
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

// e2eBuildExt4 creates a raw ext4 image from srcDir using mke2fs -d.
// The image is pre-allocated as a 64 MiB sparse file.
func e2eBuildExt4(t *testing.T, srcDir string) string {
	t.Helper()

	mke2fsPath, err := exec.LookPath("mke2fs")
	if err != nil {
		t.Skipf("skipping: mke2fs not available: %v", err)
	}

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "rootfs.ext4")

	const imgSize = 64 * 1024 * 1024
	f, err := os.Create(imgPath)
	if err != nil {
		t.Fatalf("create ext4 image file: %v", err)
	}
	f.Close()
	if err := os.Truncate(imgPath, imgSize); err != nil {
		t.Fatalf("truncate ext4 image: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, mke2fsPath,
		"-t", "ext4",
		"-d", srcDir,
		"-U", "00000000-0000-0000-0000-000000000000",
		"-E", "hash_seed=00000000-0000-0000-0000-000000000000",
		imgPath,
	)
	cmd.Env = append(os.Environ(), "SOURCE_DATE_EPOCH=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mke2fs: %v\n%s", err, out)
	}
	return imgPath
}

// ── image cache helpers ───────────────────────────────────────────────────────

// e2ePutImageCache hashes ext4Path, stores it in cache, and returns the digest.
func e2ePutImageCache(t *testing.T, cache *image.Cache, ext4Path string) domain.Digest {
	t.Helper()

	// Compute SHA-256 of the ext4 file.
	h := sha256.New()
	f, err := os.Open(ext4Path)
	if err != nil {
		t.Fatalf("open ext4 for hashing: %v", err)
	}
	if _, err := io.Copy(h, f); err != nil {
		f.Close()
		t.Fatalf("hash ext4: %v", err)
	}
	f.Close()

	digestStr := "sha256:" + hex.EncodeToString(h.Sum(nil))
	d, err := domain.ParseDigest(digestStr)
	if err != nil {
		t.Fatalf("ParseDigest: %v", err)
	}

	// Re-open for Put.
	f, err = os.Open(ext4Path)
	if err != nil {
		t.Fatalf("open ext4 for Put: %v", err)
	}
	defer f.Close()

	img := domain.Image{
		Digest: d,
		Ref:    "test-e2e:latest",
		Kind:   domain.KindBase,
	}
	if err := cache.Put(context.Background(), img, f); err != nil {
		t.Fatalf("cache.Put: %v", err)
	}
	return d
}

// ── VMM PID discovery ─────────────────────────────────────────────────────────

// findVMMPID scans /proc to find a cloud-hypervisor process whose cmdline
// contains "--api-socket <socketPath>". Returns the PID or an error if not found.
func findVMMPID(socketPath string) (int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0, fmt.Errorf("readdir /proc: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		cmdlineBytes, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			continue // process may have exited
		}
		// cmdline is NUL-separated; args[i] == "--api-socket" and args[i+1] == socketPath.
		args := strings.Split(string(cmdlineBytes), "\x00")
		for i, arg := range args {
			if arg == "--api-socket" && i+1 < len(args) && args[i+1] == socketPath {
				return pid, nil
			}
		}
	}
	return 0, fmt.Errorf("no cloud-hypervisor process found with --api-socket %s", socketPath)
}

// e2eWaitForAgentReady polls drv.DialGuest on the agent control port (1024)
// until the guest agent accepts a connection or the timeout elapses.
func e2eWaitForAgentReady(t *testing.T, drv *cloudhypervisor.CHDriver, id domain.SandboxID, timeout time.Duration) {
	t.Helper()
	const agentControlPort = 1024
	deadline := time.Now().Add(timeout)
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

// e2eRealProbe returns a ProbeFunc that polls the agent vsock port until ready.
func e2eRealProbe(drv *cloudhypervisor.CHDriver) service.ProbeFunc {
	const agentControlPort = 1024
	return func(ctx context.Context, _ driver.Driver, id domain.SandboxID) error {
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			conn, err := drv.DialGuest(dialCtx, id, agentControlPort)
			cancel()
			if err == nil {
				conn.Close()
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(300 * time.Millisecond):
			}
		}
	}
}

// ── main test ─────────────────────────────────────────────────────────────────

// TestWorkspaceE2E is the full end-to-end acceptance test for the Run-4 stack.
func TestWorkspaceE2E(t *testing.T) {
	// ── guards ────────────────────────────────────────────────────────────────
	skipUnlessKVME2E(t)
	chBin := skipUnlessCHBinE2E(t)
	skipUnlessMke2fsE2E(t)
	kernelPath := e2eKernelPath(t)

	// ── build binaries ────────────────────────────────────────────────────────
	t.Log("building nexus-agent …")
	agentBin := e2eBuildAgent(t)
	t.Log("building hello binary …")
	helloBin := e2eBuildHello(t)

	// ── assemble rootfs + ext4 ────────────────────────────────────────────────
	t.Log("assembling rootfs …")
	rootfsDir := e2eBuildRootfs(t, agentBin, helloBin)
	t.Log("building ext4 image …")
	ext4Path := e2eBuildExt4(t, rootfsDir)

	// ── image cache ───────────────────────────────────────────────────────────
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	imgDigest := e2ePutImageCache(t, cache, ext4Path)
	t.Logf("image cached with digest %s", imgDigest)

	// ── socket dir ────────────────────────────────────────────────────────────
	// Use /tmp to stay under the 107-byte Linux sun_path limit.
	socketDir, err := os.MkdirTemp("/tmp", "e2e-ch-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	// The socket name is "sb-<26-char-crockford>.sock" = 35 chars.
	if len(socketDir)+35 > e2eSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("skipping: socket dir path too long for unix socket: %s", socketDir)
	}

	// ── store ─────────────────────────────────────────────────────────────────
	storeRoot := t.TempDir()
	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("NewFileStore: %v", err)
	}

	// ── service driver (for lifecycle ops after boot) ─────────────────────────
	// Shares the socketDir with the boot driver so it can reach running VMs.
	svcDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath:   chBin,
		SocketDir:    socketDir,
		KernelPath:   kernelPath,
		StartTimeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("cloudhypervisor.New (svcDrv): %v", err)
	}

	svc := service.New(st, svcDrv, lifecycle.New())

	// ── serial log path (for failure diagnosis) ───────────────────────────────
	serialPath := filepath.Join(socketDir, "serial.log")

	// ── cleanup ───────────────────────────────────────────────────────────────
	t.Cleanup(func() {
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("guest serial output:\n%s", content)
		}
		os.RemoveAll(socketDir)
	})

	// ── DriverFactory for CreateAndBoot ───────────────────────────────────────
	// Returns a fresh CHDriver configured with the ext4 disk image for boot.
	// Capture bootDrv so we can use it for the agent probe and to derive the
	// socket path for SIGKILL.
	var bootDrv *cloudhypervisor.CHDriver
	factory := service.DriverFactory(func(resolvedExt4 string, extraDisks []service.ExtraDisk) (driver.Driver, error) {
		// service.DriverFactory gained an extraDisks parameter: CreateAndBoot
		// passes opts.ExtraDisks plus the workspace disk it appends itself, and
		// documents the factory as "responsible for mapping them to
		// cloudhypervisor.ExtraDisk and wiring them into the driver Config".
		// Dropping them here would silently boot a VM missing /dev/vdb…, so
		// forward every entry in order — attachment order IS the guest device
		// order.
		chDisks := make([]cloudhypervisor.ExtraDisk, len(extraDisks))
		for i, ed := range extraDisks {
			chDisks[i] = cloudhypervisor.ExtraDisk{Path: ed.Path}
		}
		var newErr error
		bootDrv, newErr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:       chBin,
			SocketDir:        socketDir,
			KernelPath:       kernelPath,
			DiskImagePath:    resolvedExt4,
			ExtraDisks:       chDisks,
			SerialOutputPath: serialPath,
			VCPUs:            1,
			MemoryMiB:        256,
			StartTimeout:     30 * time.Second,
		})
		return bootDrv, newErr
	})

	// The probe is wired after the factory so it captures bootDrv.
	// CreateAndBoot calls probe(rCtx, bootDrv, id) after Start.
	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		// bootDrv is set by the factory before probe is called.
		return e2eRealProbe(bootDrv)(ctx, drv, id)
	})

	// ── Step 1: CreateAndBoot ─────────────────────────────────────────────────
	t.Log("creating and booting workspace …")
	createCtx, createCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer createCancel()

	sb, err := service.CreateAndBoot(
		createCtx,
		svc,
		cache,
		factory,
		probe,
		"acceptance", "workspace-e2e",
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(imgDigest)},
			CacheRoot:           cacheRoot,
			ReachabilityTimeout: 60 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	t.Logf("CreateAndBoot succeeded: sandbox %s state=%s", sb.ID, sb.State)

	// Verify the record is Running.
	if sb.State != domain.Running {
		t.Fatalf("expected state=Running after CreateAndBoot, got %s", sb.State)
	}

	// ── Step 2: wait for agent to be ready, then Exec ─────────────────────────
	t.Log("waiting for guest agent to be reachable …")
	e2eWaitForAgentReady(t, bootDrv, sb.ID, 30*time.Second)
	t.Log("guest agent reachable — running exec …")

	agentClient := agent.NewClient(bootDrv, sb.ID)
	var execOut bytes.Buffer

	execCtx, execCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer execCancel()

	exitCode, err := agentClient.Exec(execCtx, agent.ExecOptions{
		Argv:   []string{"/bin/hello"},
		Stdout: &execOut,
		Stderr: os.Stderr,
	})
	if err != nil {
		t.Fatalf("agent.Exec /bin/hello: %v", err)
	}
	if exitCode != 0 {
		t.Errorf("exec exit code: got %d, want 0", exitCode)
	}
	if !strings.Contains(execOut.String(), "hello-from-disk") {
		t.Errorf("exec stdout: got %q, want to contain 'hello-from-disk'", execOut.String())
	}
	t.Logf("exec succeeded: output=%q exit=%d", strings.TrimSpace(execOut.String()), exitCode)

	const ref = "acceptance/workspace-e2e"

	// ── Step 3: Pause / Resume ────────────────────────────────────────────────
	t.Log("pausing workspace …")
	pauseCtx, pauseCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer pauseCancel()

	paused, err := svc.Pause(pauseCtx, ref)
	if err != nil {
		t.Fatalf("svc.Pause: %v", err)
	}
	if paused.State != domain.Paused {
		t.Fatalf("expected state=Paused after Pause, got %s", paused.State)
	}
	t.Logf("paused: state=%s", paused.State)

	// Allow state to settle.
	time.Sleep(200 * time.Millisecond)

	t.Log("resuming workspace …")
	resumeCtx, resumeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer resumeCancel()

	resumed, err := svc.Resume(resumeCtx, ref)
	if err != nil {
		t.Fatalf("svc.Resume: %v", err)
	}
	if resumed.State != domain.Running {
		t.Fatalf("expected state=Running after Resume, got %s", resumed.State)
	}
	t.Logf("resumed: state=%s", resumed.State)

	// Let the agent re-bind vsock listeners after resume.
	time.Sleep(500 * time.Millisecond)

	// ── Step 4: Crash recovery ────────────────────────────────────────────────
	// Derive the API socket path: <socketDir>/sb-<26-char>.sock
	sandboxSocketPath := filepath.Join(socketDir, sb.ID.String()+".sock")

	t.Logf("looking for VMM process with api-socket %s …", sandboxSocketPath)
	vmmPID, err := findVMMPID(sandboxSocketPath)
	if err != nil {
		t.Fatalf("findVMMPID: %v", err)
	}
	t.Logf("found VMM PID %d — sending SIGKILL …", vmmPID)

	// SIGKILL the entire process group (same approach as disk_integration_test).
	if killErr := syscall.Kill(-vmmPID, syscall.SIGKILL); killErr != nil {
		// If the process already exited, that's fine for the test.
		t.Logf("SIGKILL(-pgid=%d): %v (may be already gone)", vmmPID, killErr)
	}

	// Wait until the VMM process is gone (the socket file disappears)
	// to ensure Recoverer observes Absent rather than a hung socket.
	t.Log("waiting for VMM process to exit …")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, err := os.Stat(sandboxSocketPath)
		if os.IsNotExist(err) {
			break
		}
		// Also check if the process is gone from /proc.
		if _, procErr := os.Stat(fmt.Sprintf("/proc/%d", vmmPID)); os.IsNotExist(procErr) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Log("VMM process gone — running recovery …")

	// Run recovery using svcDrv (same socketDir, so it can Observe → Absent).
	rec := recovery.New(st, svcDrv)
	recovCtx, recovCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer recovCancel()

	report, err := rec.Recover(recovCtx)
	if err != nil {
		t.Fatalf("Recoverer.Recover: %v", err)
	}

	// Find the outcome for our sandbox.
	var outcome *recovery.SandboxOutcome
	for i, o := range report.Outcomes {
		if o.ID == sb.ID {
			outcome = &report.Outcomes[i]
			break
		}
	}
	if outcome == nil {
		t.Fatalf("recovery report has no outcome for sandbox %s", sb.ID)
	}
	t.Logf("recovery outcome: kind=%s reason=%q", outcome.Kind, outcome.Reason)

	if outcome.Kind != recovery.OutcomeResolvedStopped {
		t.Errorf("expected OutcomeResolvedStopped, got %s", outcome.Kind)
	}

	// Verify the store record was updated to stopped(memory_lost).
	getCtx, getCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer getCancel()

	recovered, err := st.Get(getCtx, sb.ID)
	if err != nil {
		t.Fatalf("store.Get after recovery: %v", err)
	}
	if recovered.State != domain.Stopped {
		t.Errorf("recovered state: got %s, want Stopped", recovered.State)
	}
	if recovered.StopReason != domain.StopReasonMemoryLost {
		t.Errorf("recovered stop_reason: got %q, want %q", recovered.StopReason, domain.StopReasonMemoryLost)
	}
	t.Logf("recovery verified: state=%s stop_reason=%s", recovered.State, recovered.StopReason)

	// ── Step 5: rm ────────────────────────────────────────────────────────────
	t.Log("removing sandbox record …")
	rmCtx, rmCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer rmCancel()

	if err := svc.Remove(rmCtx, ref); err != nil {
		t.Fatalf("svc.Remove: %v", err)
	}

	// Verify the record is gone from the store.
	_, err = st.Get(context.Background(), sb.ID)
	if err == nil {
		t.Error("store.Get after Remove: expected error (ErrNotFound), got nil")
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("store.Get after Remove: got %v, want ErrNotFound", err)
	}
	t.Log("sandbox record removed — test complete")
}
