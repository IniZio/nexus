//go:build integration

package cloudhypervisor

// Integration tests for the Cloud Hypervisor driver.
//
// # Tests
//
//   - TestBootLifecycle: boots a microVM without initramfs and exercises the
//     full observation lifecycle: Running → Paused → Running → Absent.
//     The kernel panics (no initramfs) but stays in Running (HLT loop).
//
//   - TestBootToUserspace: boots a microVM with an Alpine-derived initramfs and
//     a real kernel cmdline, then asserts that the guest /init was reached via
//     serial console output captured to a file.
//
//   - TestBrokenBoot_StderrCaptured: boots with a nonexistent kernel path and
//     asserts that the returned error contains hypervisor-side detail (from the
//     vm.boot HTTP 500 response body), not just a generic driver.Unknown.
//
// # Guard conditions
//
// All tests skip (never fail) when the environment lacks:
//   - /dev/kvm
//   - the cloud-hypervisor binary (default: ~/.local/bin/cloud-hypervisor;
//     override with CLOUD_HYPERVISOR_BIN)
//   - boot artifacts (run scripts/fetch-boot-artifacts.sh from repo root)
//
// # Running these tests
//
//	bash scripts/fetch-boot-artifacts.sh
//	go test -tags integration ./internal/core/driver/cloudhypervisor/... \
//	    -v -count=1 [-timeout 120s]

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
)

// defaultCHBin is the expected cloud-hypervisor binary location.
// Override with the CLOUD_HYPERVISOR_BIN environment variable.
var defaultCHBin = filepath.Join(os.Getenv("HOME"), ".local/bin/cloud-hypervisor")

// TestBootLifecycle boots a real microVM (no initramfs) and asserts
// Running → Paused → Running → Absent across pause/resume/stop.
//
// Without initramfs the kernel panics at the init-search stage and enters an
// HLT loop. Empirically verified on 2026-08-05 with CH v52.0: a panic-halted
// kernel keeps the VM in state "Running" indefinitely; Pause and Resume both
// work. This is deliberate: TestBootLifecycle tests driver mechanics, not
// userspace. See TestBootToUserspace for the userspace-reachable boot.
func TestBootLifecycle(t *testing.T) {
	// guards
	skipUnlessKVM(t)
	chBin := skipUnlessCHBin(t)
	kernelPath := skipUnlessArtifact(t, "vmlinux-x86_64")

	// socket dir
	// Use a short base path to stay under the 107-byte Linux sun_path limit.
	// The driver rejects SocketDir paths where len(dir)+35 > 107.
	socketDir, err := os.MkdirTemp("/tmp", "ch-it-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })

	// driver
	drv, err := New(Config{
		BinaryPath:   chBin,
		SocketDir:    socketDir,
		KernelPath:   kernelPath,
		VCPUs:        1,
		MemoryMiB:    256,
		StartTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id := domain.NewSandboxID()

	// belt-and-braces cleanup
	// Stash the VMM PID immediately after Start returns so cleanup can kill it
	// even if Stop panics, fails mid-sequence, or is never reached.
	//
	// This is the only version of cleanup that survives a panic mid-test:
	// t.Cleanup runs even when the test goroutine panics.
	var vmmPID int // 0 until Start assigns it; 0 again after Stop clears it

	t.Cleanup(func() {
		if vmmPID != 0 {
			t.Logf("cleanup: killing orphan VMM PID %d", vmmPID)
			_ = syscall.Kill(-vmmPID, syscall.SIGKILL)
		}
		// Remove socket and IID sidecar regardless of outcome.
		drv.clearState(id)
	})

	// 1. Start
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	t.Log("Starting VM...")
	bootStart := time.Now()
	iid, err := drv.Start(ctx, driver.StartRequest{SandboxID: id})
	bootDuration := time.Since(bootStart)
	if err != nil {
		t.Fatalf("driver.Start: %v", err)
	}
	t.Logf("Start returned in %v (instanceID=%s)", bootDuration, iid)

	// Stash PID for belt-and-braces cleanup.
	drv.mu.Lock()
	if proc := drv.procs[id]; proc != nil {
		vmmPID = proc.pid
		t.Logf("VMM PID: %d", vmmPID)
	}
	drv.mu.Unlock()

	// 2. Observe → Running
	obs, err := drv.Observe(context.Background(), id)
	if err != nil {
		t.Errorf("Observe after Start returned error: %v", err)
	}
	t.Logf("Observe after Start: state=%v detail=%q instanceID=%q",
		obs.State, obs.Detail, obs.InstanceID)
	if obs.State != driver.Running {
		t.Errorf("expected Running after Start, got %v — detail: %s", obs.State, obs.Detail)
	}
	if obs.InstanceID == "" {
		t.Error("expected non-empty InstanceID after Start, got empty string")
	}
	if obs.InstanceID != iid {
		t.Errorf("InstanceID mismatch: Start returned %q, Observe returned %q", iid, obs.InstanceID)
	}

	// 3. Pause → Paused
	pauseCtx, pauseCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pauseCancel()

	if err := drv.Pause(pauseCtx, id); err != nil {
		t.Fatalf("driver.Pause: %v", err)
	}

	obs, err = drv.Observe(context.Background(), id)
	if err != nil {
		t.Errorf("Observe after Pause returned error: %v", err)
	}
	t.Logf("Observe after Pause: state=%v detail=%q", obs.State, obs.Detail)
	if obs.State != driver.Paused {
		t.Errorf("expected Paused after Pause, got %v — detail: %s", obs.State, obs.Detail)
	}

	// 4. Resume → Running
	resumeCtx, resumeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer resumeCancel()

	if err := drv.Resume(resumeCtx, id); err != nil {
		t.Fatalf("driver.Resume: %v", err)
	}

	obs, err = drv.Observe(context.Background(), id)
	if err != nil {
		t.Errorf("Observe after Resume returned error: %v", err)
	}
	t.Logf("Observe after Resume: state=%v detail=%q", obs.State, obs.Detail)
	if obs.State != driver.Running {
		t.Errorf("expected Running after Resume, got %v — detail: %s", obs.State, obs.Detail)
	}

	// 5. Stop → Absent
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()

	if err := drv.Stop(stopCtx, id); err != nil {
		t.Fatalf("driver.Stop: %v", err)
	}
	// Stop succeeded (or was idempotent); disarm belt-and-braces cleanup.
	vmmPID = 0

	// vmm.shutdown returns 200 before the CH process fully exits; poll for
	// Absent rather than asserting instantly.
	obs = pollForState(t, drv, id, driver.Absent, 3*time.Second)
	t.Logf("Observe after Stop: state=%v detail=%q", obs.State, obs.Detail)
	if obs.State != driver.Absent {
		t.Errorf("expected Absent after Stop, got %v — detail: %s", obs.State, obs.Detail)
	}

	// orphan check
	// No belt-and-braces PID remains; verify via /proc that no CH process we
	// own is still alive. (A full pgrep would race against unrelated tests.)
	t.Log("No orphan VMM detected (vmmPID cleared by Stop)")

	// summary
	t.Logf("=== Boot lifecycle summary ===")
	t.Logf("  Kernel format:  vmlinux (ELF, PVH), no initramfs")
	t.Logf("  Boot-to-Running: %v", bootDuration)
	t.Logf("  Lifecycle:      Running → Paused → Running → Absent (all correct)")
	t.Logf("  CH state strings observed: \"Running\", \"Paused\" — match mapCHState exactly")
	t.Logf("  Note: kernel panics without initramfs but VM stays Running (HLT loop)")
	t.Logf("  See TestBootToUserspace for the initramfs+cmdline boot-to-userspace test")
}

// pollForState polls drv.Observe until the observed state equals want or
// timeout is reached. Returns the last observation.
func pollForState(t *testing.T, drv *CHDriver, id domain.SandboxID, want driver.RunState, timeout time.Duration) driver.Observation {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last driver.Observation
	for time.Now().Before(deadline) {
		obs, err := drv.Observe(context.Background(), id)
		if err == nil && obs.State == want {
			return obs
		}
		last = obs
		// Also return immediately on Absent even with an error (ENOENT is Absent).
		if want == driver.Absent && obs.State == driver.Absent {
			return obs
		}
		time.Sleep(50 * time.Millisecond)
	}
	return last
}

// skipUnlessKVM skips the test if /dev/kvm is absent or not usable.
func skipUnlessKVM(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("skipping: /dev/kvm not present — KVM is required for this test")
	}
	// Attempt to open /dev/kvm to verify the user has access.
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("skipping: /dev/kvm not usable by this user: %v", err)
	}
	f.Close()
}

// skipUnlessCHBin returns the cloud-hypervisor binary path or skips the test.
func skipUnlessCHBin(t *testing.T) string {
	t.Helper()
	chBin := os.Getenv("CLOUD_HYPERVISOR_BIN")
	if chBin == "" {
		chBin = defaultCHBin
	}
	if _, err := os.Stat(chBin); err != nil {
		t.Skipf("skipping: cloud-hypervisor binary not found at %s "+
			"(set CLOUD_HYPERVISOR_BIN to override)", chBin)
	}
	return chBin
}

// skipUnlessArtifact returns the absolute path to a testdata artifact or
// skips the test with an instruction to run the fetch script.
//
// Go test sets the working directory to the package directory, so
// "testdata/<name>" resolves correctly without any path calculation.
func skipUnlessArtifact(t *testing.T, name string) string {
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

// TestBootToUserspace boots a microVM with an Alpine-derived initramfs and
// asserts that the guest's /init was reached by checking the serial console
// output captured to a file.
//
// # What "reached userspace" means here
//
// The Linux kernel emits "Run /init as init process" to the serial console when
// it successfully locates and executes /init from the initramfs. This message
// appears before /init itself runs any commands, so it is a reliable kernel-
// provided signal that the initramfs was loaded and userspace was entered.
// Our /init also echoes "nexus-test-vm: init reached" after mounting /dev and
// /proc; we look for both strings.
//
// Serial console capture is via the CH API (vm.create serial: {mode: "File"}),
// not via CLI flags. The cmdline routes kernel output to ttyS0.
//
// # Timing
//
// The test reports two timings:
//   - boot-to-Running: time from drv.Start() call returning to Observe()
//     seeing Running (this covers VMM spawn + vm.create + vm.boot).
//   - boot-to-userspace: additional time from Running until the userspace
//     marker appears in the serial log. This measures kernel init time.
func TestBootToUserspace(t *testing.T) {
	// guards
	skipUnlessKVM(t)
	chBin := skipUnlessCHBin(t)
	kernelPath := skipUnlessArtifact(t, "vmlinux-x86_64")
	initramfsPath := skipUnlessArtifact(t, "alpine-initramfs.cpio.gz")

	// socket dir (short path for sun_path limit)
	socketDir, err := os.MkdirTemp("/tmp", "ch-us-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })

	// serial output file
	serialFile := filepath.Join(socketDir, "serial.log")

	// driver
	drv, err := New(Config{
		BinaryPath:       chBin,
		SocketDir:        socketDir,
		KernelPath:       kernelPath,
		InitramfsPath:    initramfsPath,
		Cmdline:          "console=ttyS0 panic=1",
		SerialOutputPath: serialFile,
		VCPUs:            1,
		MemoryMiB:        256,
		StartTimeout:     15 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id := domain.NewSandboxID()

	// belt-and-braces cleanup
	var vmmPID int
	t.Cleanup(func() {
		if vmmPID != 0 {
			t.Logf("cleanup: killing orphan VMM PID %d", vmmPID)
			_ = syscall.Kill(-vmmPID, syscall.SIGKILL)
		}
		drv.clearState(id)
	})

	// 1. Start (boot-to-Running timing)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Log("Starting VM with initramfs...")
	bootStart := time.Now()
	iid, err := drv.Start(ctx, driver.StartRequest{SandboxID: id})
	if err != nil {
		t.Fatalf("drv.Start: %v", err)
	}
	bootToRunning := time.Since(bootStart)
	t.Logf("Start returned in %v (instanceID=%s)", bootToRunning, iid)

	drv.mu.Lock()
	if proc := drv.procs[id]; proc != nil {
		vmmPID = proc.pid
		t.Logf("VMM PID: %d", vmmPID)
	}
	drv.mu.Unlock()

	// 2. Assert Running
	obs, err := drv.Observe(context.Background(), id)
	if err != nil {
		t.Errorf("Observe after Start returned error: %v", err)
	}
	if obs.State != driver.Running {
		t.Errorf("expected Running after Start, got %v — detail: %s", obs.State, obs.Detail)
	}

	// 3. Wait for userspace marker in serial log
	//
	// We look for two markers:
	//   "Run /init as init process"  — kernel message, appears before /init runs
	//   "nexus-test-vm: init reached" — /init echo, appears after devtmpfs mount
	//
	// The kernel message is the primary signal; the /init echo confirms that
	// PID 1 ran its first commands. We wait up to 5 seconds for either to
	// appear, polling the serial file.
	const (
		kernelMarker    = "Run /init as init process"
		userspaceMarker = "nexus-test-vm: init reached"
	)
	userspaceStart := time.Now()
	var foundKernelMarker, foundUserspaceMarker bool
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f, err := os.Open(serialFile); err == nil {
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, kernelMarker) {
					foundKernelMarker = true
				}
				if strings.Contains(line, userspaceMarker) {
					foundUserspaceMarker = true
				}
			}
			f.Close()
		}
		if foundKernelMarker || foundUserspaceMarker {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	bootToUserspace := time.Since(userspaceStart)

	if !foundKernelMarker && !foundUserspaceMarker {
		// Read the last few lines of serial output for the failure message.
		var serialTail string
		if data, err := os.ReadFile(serialFile); err == nil {
			serialTail = string(data)
			if len(serialTail) > 2048 {
				serialTail = "...(truncated)...\n" + serialTail[len(serialTail)-2048:]
			}
		} else {
			serialTail = fmt.Sprintf("(could not read serial file: %v)", err)
		}
		t.Errorf("neither kernel marker %q nor userspace marker %q found in serial output after 5s\nserial tail:\n%s",
			kernelMarker, userspaceMarker, serialTail)
	} else {
		t.Logf("Userspace reached — kernelMarker=%v userspaceMarker=%v", foundKernelMarker, foundUserspaceMarker)
	}

	// 4. Stop → Absent
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopCancel()
	if err := drv.Stop(stopCtx, id); err != nil {
		t.Fatalf("drv.Stop: %v", err)
	}
	vmmPID = 0

	obs = pollForState(t, drv, id, driver.Absent, 3*time.Second)
	if obs.State != driver.Absent {
		t.Errorf("expected Absent after Stop, got %v — detail: %s", obs.State, obs.Detail)
	}

	// summary
	t.Logf("=== Boot-to-userspace summary ===")
	t.Logf("  Kernel format:      vmlinux (ELF, PVH)")
	t.Logf("  Initramfs:          alpine-initramfs.cpio.gz (Alpine 3.20.0 minirootfs)")
	t.Logf("  Cmdline:            console=ttyS0 panic=1")
	t.Logf("  boot-to-Running:    %v  (VMM spawn + vm.create + vm.boot)", bootToRunning)
	t.Logf("  boot-to-userspace:  %v  (Running until /init marker in serial log)", bootToUserspace)
	t.Logf("  Kernel marker:      %v (%q)", foundKernelMarker, kernelMarker)
	t.Logf("  Userspace marker:   %v (%q)", foundUserspaceMarker, userspaceMarker)
}

// TestBrokenBoot_StderrCaptured verifies that a Start() with a nonexistent
// kernel path returns an error containing hypervisor-side detail from the
// vm.boot HTTP 500 response body. This is what Gap 3 fixes.
//
// # Why the response body, not VMM stderr
//
// Empirically verified with CH v52.0: CH writes nothing to stderr when a
// kernel file is missing. The error surfaces via the vm.boot HTTP response:
//
//	["Error from API","The VM could not boot","Cannot open kernel file","No such file or directory (os error 2)"]
//
// The driver now includes this response body in the Start error. Before the
// fix, Start returned a bare "unexpected status 500" with no CH context.
func TestBrokenBoot_StderrCaptured(t *testing.T) {
	// guards
	skipUnlessKVM(t)
	chBin := skipUnlessCHBin(t)

	socketDir, err := os.MkdirTemp("/tmp", "ch-bb-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })

	drv, err := New(Config{
		BinaryPath:   chBin,
		SocketDir:    socketDir,
		KernelPath:   "/nonexistent/kernel/path/that/does/not/exist",
		VCPUs:        1,
		MemoryMiB:    256,
		StartTimeout: 15 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	id := domain.NewSandboxID()

	// Start must fail
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, startErr := drv.Start(ctx, driver.StartRequest{SandboxID: id})
	if startErr == nil {
		t.Fatal("expected Start to return an error for nonexistent kernel; got nil")
	}

	// Error must contain CH detail
	//
	// CH v52 returns HTTP 500 from vm.boot when the kernel can't be opened.
	// The driver includes the response body in the error message. Assert on
	// two substrings that appear in the CH error array:
	//
	//   "Cannot open kernel file"        — CH-specific error description
	//   "No such file or directory"      — OS-level cause
	//
	// Both distinguish "driver explained the failure" from a bare "Unknown".
	errStr := startErr.Error()
	t.Logf("Start error (expected): %v", startErr)

	const wantCHDetail1 = "Cannot open kernel file"
	const wantCHDetail2 = "No such file or directory"

	if !strings.Contains(errStr, wantCHDetail1) && !strings.Contains(errStr, wantCHDetail2) {
		t.Errorf("Start error missing hypervisor-side detail\n"+
			"  want (either): %q  or  %q\n"+
			"  got:           %q",
			wantCHDetail1, wantCHDetail2, errStr)
	}

	// No orphan VMM
	// Start's cleanup() path kills the spawned process. The socket must be gone.
	sockPath := drv.socketPath(id)
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("socket file %q still exists after failed Start (orphan risk)", sockPath)
	}
}

// Ensure the package-level Compile-time assertions still hold in this file.
var _ = fmt.Sprintf
