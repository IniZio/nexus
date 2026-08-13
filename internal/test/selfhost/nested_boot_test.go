//go:build integration

// Package selfhost — T5 nested-boot integration proof.
//
// Proves end-to-end that the cloud-hypervisor driver's NestedVirt opt-in (T4)
// works correctly at every layer:
//
//   - (AC T5-AC1) Outer sandbox with NestedVirt=true exposes /dev/kvm inside
//     the guest and it is accessible to user processes.
//   - (AC T5-AC2) The outer guest can launch an INNER cloud-hypervisor VM using
//     the staged KVM device. The inner kernel prints its serial boot banner
//     ("Linux version"), proving KVM-accelerated nested boot is functioning.
//   - (AC T5-AC3) Outer sandbox WITHOUT NestedVirt (default false) does NOT
//     expose /dev/kvm — the perimeter change is strictly opt-in.
//
// Both TestNestedBootInner (AC1 + AC2) and TestNestedBootNegativeControl (AC3)
// share the same outer image (BuildNestedBootImage) so a cached image can be
// reused across runs.
//
// # Failure discipline
//
// Failures surface full captured stdout+stderr, mirroring build_dogfood_test.go
// discipline. A transport error (vsock EOF) hard-fails; an unexpected exit code
// or missing banner string hard-fails. Only unavailable infrastructure causes a
// t.Skip.
//
// # Prerequisites
//
//   - /dev/kvm accessible on the host
//   - Host nested KVM enabled (/sys/module/kvm_{intel,amd}/parameters/nested == 1|Y)
//     required for TestNestedBootInner only; NOT required for TestNestedBootNegativeControl
//   - cloud-hypervisor binary (CLOUD_HYPERVISOR_BIN or ~/.local/bin/cloud-hypervisor)
//   - mke2fs in PATH (e2fsprogs)
//   - docker in PATH
//   - images/kernel/vmlinux-x86_64 present in the repo root
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run TestNestedBoot \
//	    ./internal/test/selfhost/ -v -timeout 90m
package selfhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// innerBootScript is the shell command run inside the outer guest to launch an
// inner cloud-hypervisor VM and capture its serial console output.
//
// Design decisions:
//   - No rootfs/initramfs: the inner kernel will panic on init failure. That is
//     expected and desired — we only need the serial banner, not a live inner guest.
//   - panic=0: on panic, do NOT reboot — the kernel halts after printing its banner.
//     (panic=-1 would reboot immediately, causing a tight loop of early-boot output.)
//   - No `quiet`: omitting quiet keeps console loglevel at default (7), so the
//     "Linux version ..." banner (KERN_NOTICE, level 5) reaches ttyS0.
//   - --serial file=...: capture serial to a file so we can read it after CH exits.
//   - timeout 30: cloud-hypervisor exits after 30s even if the VM is still running.
//   - The final `cat` prints the serial log as stdout so the host test captures it.
const innerBootScript = `set -e
mkdir -p /tmp/inner-vm
timeout 30 cloud-hypervisor \
    --kernel /boot/vmlinux \
    --cmdline 'console=ttyS0 panic=0' \
    --cpus boot=1 \
    --memory size=128M \
    --serial file=/tmp/inner-vm/serial.log \
    --console off \
    --api-socket /tmp/inner-vm/ch.sock \
|| true
if [ -f /tmp/inner-vm/serial.log ]; then
    cat /tmp/inner-vm/serial.log
else
    echo 'NO_SERIAL_LOG: inner cloud-hypervisor did not produce serial output'
fi
`

// TestNestedBootInner proves that:
//  1. An outer sandbox booted with NestedVirt=true exposes /dev/kvm inside
//     the guest (AC T5-AC1).
//  2. The outer guest can launch an inner cloud-hypervisor VM that boots its
//     Linux kernel to the serial banner stage (AC T5-AC2).
func TestNestedBootInner(t *testing.T) {
	// ── 1. Skip guards ────────────────────────────────────────────────────────
	skipUnlessKVMSH(t)
	skipUnlessNestedKVM(t) // inner boot requires nested KVM on the host
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	// ── 2. Kernel path (validates asset is present on host) ───────────────────
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── 3. Build outer nested-boot image ─────────────────────────────────────
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	imgCtx, imgCancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer imgCancel()

	t.Log("building nested-boot outer image (first run: ~10–20 min; cached: seconds) ...")
	img, err := BuildNestedBootImage(imgCtx, cache)
	if err != nil {
		switch {
		case errors.Is(err, ErrDockerUnavailable):
			t.Skip("skipping: docker unavailable:", err)
		case errors.Is(err, builder.ErrMke2fsUnavailable):
			t.Skip("skipping: mke2fs unavailable:", err)
		}
		t.Fatalf("BuildNestedBootImage: %v", err)
	}
	t.Logf("nested-boot image: digest=%s size=%.2f GiB", img.Digest, float64(img.Size)/(1<<30))

	// ── 4. Infrastructure ─────────────────────────────────────────────────────
	// Socket dir in /tmp: stays within the 107-byte Linux sun_path limit.
	socketDir, err := os.MkdirTemp("/tmp", "nested-boot-inner-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })

	serialPath := socketDir + "/outer-serial.log"

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}

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

	// ── 5. Boot outer sandbox with NestedVirt=true ────────────────────────────
	// MemoryMiB=2048: the outer guest runs cloud-hypervisor to launch an inner
	// VM; 2 GiB gives the inner VM (128 MiB) plus outer OS overhead comfortable
	// headroom.
	var bootDrv *cloudhypervisor.CHDriver
	factory := service.DriverFactory(func(ext4Path string, _ []service.ExtraDisk) (driver.Driver, error) {
		var ferr error
		bootDrv, ferr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:       chBin,
			SocketDir:        socketDir,
			KernelPath:       kernelPath,
			DiskImagePath:    ext4Path,
			MemoryMiB:        2048,
			SerialOutputPath: serialPath,
			StartTimeout:     60 * time.Second,
			NestedVirt:       true, // T4 opt-in: expose /dev/kvm + set CpusConfig.nested=true
		})
		return bootDrv, ferr
	})
	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		return realProbeSH(bootDrv)(ctx, drv, id)
	})

	t.Log("creating and booting OUTER sandbox (NestedVirt=true) ...")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer bootCancel()

	var sandboxID domain.SandboxID
	t.Cleanup(func() {
		if sandboxID == (domain.SandboxID{}) {
			return
		}
		rmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if rerr := svc.Remove(rmCtx, sandboxID.String()); rerr != nil {
			t.Logf("cleanup: svc.Remove(%s): %v", sandboxID, rerr)
		}
	})

	sb, err := service.CreateAndBoot(
		bootCtx, svc, cache, factory, probe,
		"nested-boot-inner", fmt.Sprintf("nested-boot-inner-%d", time.Now().UnixNano()),
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(img.Digest)},
			CacheRoot:           cacheRoot,
			ReachabilityTimeout: 60 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxID = sb.ID
	t.Logf("outer sandbox booted: %s state=%s", sb.ID, sb.State)

	agentClient := agent.NewClient(bootDrv, sb.ID)

	// ── 6. AC T5-AC1: assert /dev/kvm is present in outer guest ───────────────
	t.Log("AC T5-AC1: asserting /dev/kvm is present in outer guest (NestedVirt=true) ...")

	var devKvmOut bytes.Buffer
	devKvmExit, devKvmErr := agentClient.Exec(context.Background(), agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "[ -c /dev/kvm ] && echo KVM_OK || echo KVM_ABSENT"},
		Env:    map[string]string{"PATH": "/usr/local/bin:/sbin:/usr/sbin:/usr/bin:/bin"},
		Stdout: &devKvmOut,
		Stderr: &devKvmOut,
	})
	if devKvmErr != nil {
		t.Fatalf("agentClient.Exec (/dev/kvm check): transport error: %v\noutput:\n%s",
			devKvmErr, devKvmOut.String())
	}
	kvmCheckOutput := devKvmOut.String()
	if devKvmExit != 0 || !strings.Contains(kvmCheckOutput, "KVM_OK") {
		t.Fatalf("AC T5-AC1 FAIL: /dev/kvm not accessible in outer guest (NestedVirt=true)\n"+
			"exit=%d\noutput:\n%s", devKvmExit, kvmCheckOutput)
	}
	t.Logf("AC T5-AC1 PASS: /dev/kvm present and accessible in outer guest")

	// ── 7. AC T5-AC2: launch inner VM, verify Linux boot banner ───────────────
	// The inner VM has no disk/initramfs; it will boot the kernel, print the
	// Linux serial banner, then panic (no init). The panic is expected: we only
	// need to detect "Linux version" in the serial output to prove the inner
	// KVM-accelerated boot worked.
	t.Log("AC T5-AC2: launching inner cloud-hypervisor VM inside outer guest ...")

	var innerOut bytes.Buffer
	innerCtx, innerCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer innerCancel()

	innerExit, innerErr := agentClient.Exec(innerCtx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", innerBootScript},
		Env:    map[string]string{"PATH": "/usr/local/bin:/sbin:/usr/sbin:/usr/bin:/bin"},
		Stdout: &innerOut,
		Stderr: &innerOut,
	})
	innerOutput := innerOut.String()

	if innerErr != nil {
		t.Fatalf("agentClient.Exec (inner boot): transport error: %v\nfull output:\n%s",
			innerErr, innerOutput)
	}
	// exit 0 (normal) and 124 (timeout) are both acceptable outcomes — the inner
	// VM kernels panics once its timer fires or after init failure.
	t.Logf("inner cloud-hypervisor exited %d; captured %d bytes of serial output",
		innerExit, len(innerOutput))

	if !strings.Contains(innerOutput, "Linux version") {
		t.Fatalf("AC T5-AC2 FAIL: 'Linux version' not found in inner VM serial output —"+
			" nested KVM boot unproven\nexit=%d\nfull output:\n%s", innerExit, innerOutput)
	}
	t.Logf("AC T5-AC2 PASS: 'Linux version' found in inner VM serial output — nested KVM boot proven")
}

// TestNestedBootNegativeControl proves that a sandbox booted WITHOUT NestedVirt
// does NOT expose /dev/kvm inside the guest (AC T5-AC3).
//
// This is the perimeter regression guard: if the default sandbox ever gains
// /dev/kvm unexpectedly, this test catches it.
func TestNestedBootNegativeControl(t *testing.T) {
	// ── 1. Skip guards ────────────────────────────────────────────────────────
	// Nested KVM on the host is NOT required for this test — we are only
	// asserting /dev/kvm is absent in the non-nested guest.
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	// ── 2. Kernel path ────────────────────────────────────────────────────────
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── 3. Build outer nested-boot image ─────────────────────────────────────
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	imgCtx, imgCancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer imgCancel()

	t.Log("building nested-boot outer image ...")
	img, err := BuildNestedBootImage(imgCtx, cache)
	if err != nil {
		switch {
		case errors.Is(err, ErrDockerUnavailable):
			t.Skip("skipping: docker unavailable:", err)
		case errors.Is(err, builder.ErrMke2fsUnavailable):
			t.Skip("skipping: mke2fs unavailable:", err)
		}
		t.Fatalf("BuildNestedBootImage: %v", err)
	}
	t.Logf("nested-boot image: digest=%s", img.Digest)

	// ── 4. Infrastructure ─────────────────────────────────────────────────────
	socketDir, err := os.MkdirTemp("/tmp", "nested-boot-neg-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })

	st, err := store.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}

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

	// ── 5. Boot WITHOUT NestedVirt (default = false) ──────────────────────────
	var bootDrv *cloudhypervisor.CHDriver
	factory := service.DriverFactory(func(ext4Path string, _ []service.ExtraDisk) (driver.Driver, error) {
		var ferr error
		bootDrv, ferr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:    chBin,
			SocketDir:     socketDir,
			KernelPath:    kernelPath,
			DiskImagePath: ext4Path,
			MemoryMiB:     1024,
			StartTimeout:  60 * time.Second,
			// NestedVirt intentionally omitted (defaults to false).
			// This is the invariant under test: no nested KVM in the default config.
		})
		return bootDrv, ferr
	})
	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		return realProbeSH(bootDrv)(ctx, drv, id)
	})

	t.Log("creating and booting OUTER sandbox (NestedVirt=false, default) ...")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer bootCancel()

	var sandboxID domain.SandboxID
	t.Cleanup(func() {
		if sandboxID == (domain.SandboxID{}) {
			return
		}
		rmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if rerr := svc.Remove(rmCtx, sandboxID.String()); rerr != nil {
			t.Logf("cleanup: svc.Remove(%s): %v", sandboxID, rerr)
		}
	})

	sb, err := service.CreateAndBoot(
		bootCtx, svc, cache, factory, probe,
		"nested-boot-neg", fmt.Sprintf("nested-boot-neg-%d", time.Now().UnixNano()),
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(img.Digest)},
			CacheRoot:           cacheRoot,
			ReachabilityTimeout: 60 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxID = sb.ID
	t.Logf("outer sandbox booted: %s state=%s", sb.ID, sb.State)

	agentClient := agent.NewClient(bootDrv, sb.ID)

	// ── 6. AC T5-AC3: assert /dev/kvm is ABSENT ───────────────────────────────
	t.Log("AC T5-AC3: asserting /dev/kvm is absent in outer guest (NestedVirt=false) ...")

	var kvmOut bytes.Buffer
	// Exit code is 0 regardless of whether /dev/kvm exists (the script does not
	// `exit 1`). We test the string content, not the exit code.
	_, kvmErr := agentClient.Exec(context.Background(), agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "[ -c /dev/kvm ] && echo KVM_PRESENT || echo KVM_ABSENT"},
		Env:    map[string]string{"PATH": "/usr/local/bin:/sbin:/usr/sbin:/usr/bin:/bin"},
		Stdout: &kvmOut,
		Stderr: &kvmOut,
	})
	if kvmErr != nil {
		t.Fatalf("agentClient.Exec (/dev/kvm check): transport error: %v\noutput:\n%s",
			kvmErr, kvmOut.String())
	}
	output := kvmOut.String()

	if strings.Contains(output, "KVM_PRESENT") {
		t.Fatalf("AC T5-AC3 FAIL: /dev/kvm is PRESENT in a non-nested sandbox —"+
			" NestedVirt perimeter breach! Check driver.go nested handling.\noutput:\n%s", output)
	}
	if !strings.Contains(output, "KVM_ABSENT") {
		t.Fatalf("AC T5-AC3 FAIL: unexpected /dev/kvm check output (expected KVM_ABSENT)\noutput:\n%s", output)
	}
	t.Logf("AC T5-AC3 PASS: /dev/kvm correctly absent in non-nested sandbox (NestedVirt=false)")
}

// skipUnlessNestedKVM skips t if the host kernel does not have nested KVM enabled.
//
// Nested KVM is required for TestNestedBootInner (the outer guest's cloud-hypervisor
// needs KVM to accelerate the inner VM). It is NOT needed for
// TestNestedBootNegativeControl which only checks /dev/kvm is absent.
//
// The check mirrors the production logic in internal/core/driver/cloudhypervisor/nested.go:
// it reads /sys/module/kvm_{intel,amd}/parameters/nested and looks for "1" or "Y".
func skipUnlessNestedKVM(t *testing.T) {
	t.Helper()
	for _, p := range []string{
		"/sys/module/kvm_intel/parameters/nested",
		"/sys/module/kvm_amd/parameters/nested",
	} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(b))
		if v == "1" || v == "Y" {
			return
		}
	}
	t.Skip("skipping: host does not support nested KVM " +
		"(/sys/module/kvm_{intel,amd}/parameters/nested must be '1' or 'Y')")
}
