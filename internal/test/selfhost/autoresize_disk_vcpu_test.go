//go:build integration

package selfhost

// autoresize_disk_vcpu_test.go — AR-LIVE-DISK-CPU acceptance tests (sub-part 3).
//
// TestAutoResizeDiskTelemetry proves the foundation: a supervisor-owned VM
// booted with a workspace disk and workspace-mount cmdline reports
// DiskSupported=true in vsock:3002 telemetry. Without this, any "disk grow
// passed" result is meaningless (the disk axis silently no-ops on DiskSupported=false).
//
// TestAutoResizeDiskGrowDevice proves the disk grow axis routes to the correct
// backing file on a 2-disk topology (shadow at ExtraDisks[0]/dev/vdb + workspace at
// ExtraDisks[1]/dev/vdc). Key findings documented inline:
//   - The workspace backing file grows, shadow unchanged → device routing correct.
//   - resize2fs IS triggered end-to-end (AR-GA closed): GrowDisk expands the backing file,
//     calls vm.resize-disk (CH v52.0 acknowledged with 204 in live proof), then sends
//     EncodeGrowRequest over vsock so the guest agent runs resize2fs. A resize2fs failure
//     on the guest side is logged but does not propagate as a GrowDisk error.
//   - The atomic-rollback code at driver_resize.go:214-221 is verified by inspection;
//     live testing requires a TOCTOU VM-kill during vm.resize-disk (not safe to contrive).
//
// TestAutoResizeVCPU proves vCPU hotplug via the CPU pressure axis: a VM
// booted with 1 vCPU and VCPUMax=2 grows to 2 online vCPUs when
// CPUPSISomeAvg10 >= 15% (cpuGrowPressure). Requires CONFIG_PSI=y in guest kernel.
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration \
//	    -run 'TestAutoResizeDiskTelemetry|TestAutoResizeDiskGrowDevice|TestAutoResizeVCPU' \
//	    ./internal/test/selfhost/ -v -timeout 60m
import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	"github.com/IniZio/nexus3/internal/core/resize"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

// arWsMountCmdline builds a kernel cmdline that routes workspace and shadow
// mount specs to the guest agent via --workspace-mount= PID-1 args.
//
// Uses the 5-field format: device:target:fstype:readonly:workspace.
// The "workspace" field must be "true" on exactly one mount for the agent to
// set DiskSupported=true in telemetry (selectWorkspaceMount uses this field).
//
// Mirrors workspaceMountCmdline in internal/cli/cmd_sandbox.go; redefined here
// because the selfhost package avoids importing internal/cli.
func arWsMountCmdline(mounts []agent.GuestMount) string {
	b := diskBootCmdlineBase + " --"
	for _, m := range mounts {
		ro, ws := "false", "false"
		if m.ReadOnly {
			ro = "true"
		}
		if m.IsWorkspace {
			ws = "true"
		}
		b += fmt.Sprintf(" --workspace-mount=%s:%s:%s:%s:%s",
			m.Device, m.Target, m.FSType, ro, ws)
	}
	return b
}

// arGuestDev returns the guest virtio-blk device path for ExtraDisks[index].
//
//	ExtraDisks[0] → /dev/vdb
//	ExtraDisks[1] → /dev/vdc
//	ExtraDisks[i] → /dev/vd{chr('b'+i)}
func arGuestDev(index int) string {
	return fmt.Sprintf("/dev/vd%c", rune('b'+index))
}

// arMakeExt4Disk creates a sparse ext4 disk image at path with sizeMiB capacity.
func arMakeExt4Disk(t *testing.T, path string, sizeMiB int) {
	t.Helper()
	// Create the file (O_CREATE) then expand to the target size as a sparse file.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("arMakeExt4Disk: create %s: %v", path, err)
	}
	f.Close()
	if err := os.Truncate(path, int64(sizeMiB)*1024*1024); err != nil {
		t.Fatalf("arMakeExt4Disk: truncate %s: %v", path, err)
	}
	out, err := exec.Command("mke2fs", "-t", "ext4", "-F", path).CombinedOutput()
	if err != nil {
		t.Fatalf("arMakeExt4Disk: mke2fs %s: %v\n%s", path, err, out)
	}
}

// ── Test 1: DiskSupported telemetry foundation ────────────────────────────────

// TestAutoResizeDiskTelemetry proves that DiskSupported=true in vsock:3002
// telemetry when the supervisor boots with a workspace disk attached and a
// --workspace-mount=...:<target>:ext4:false:true cmdline arg.
//
// This is the mandatory foundation check. A DiskSupported=false result means
// the workspace mount or cmdline is broken — any subsequent "disk grow passed"
// result would be a silent false positive.
func TestAutoResizeDiskTelemetry(t *testing.T) {
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	hostMiB := hostMemAvailableMiB()
	if hostMiB >= 0 && hostMiB < 2048 {
		t.Skipf("skipping: host MemAvailable=%d MiB < 2048 MiB required", hostMiB)
	}
	t.Logf("host MemAvailable: %d MiB", hostMiB)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── Step 1: infrastructure ────────────────────────────────────────────────
	socketDir, err := os.MkdirTemp("/tmp", "ar-dt-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp socketDir: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("socket dir path too long for AF_UNIX: %s", socketDir)
	}
	stateDir, err := os.MkdirTemp("/tmp", "ar-dt-state-")
	if err != nil {
		os.RemoveAll(socketDir)
		t.Fatalf("MkdirTemp stateDir: %v", err)
	}
	diskDir := t.TempDir()
	storeRoot := t.TempDir()
	cacheRoot := filepath.Join(storeRoot, "images")

	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}
	svcDrv, err := cloudhypervisor.New(cloudhypervisor.Config{BinaryPath: chBin, SocketDir: socketDir})
	if err != nil {
		t.Fatalf("cloudhypervisor.New (svcDrv): %v", err)
	}
	svc := service.New(st, svcDrv, lifecycle.New())

	var supervisorPID int
	var sandboxID domain.SandboxID

	// Pre-create workspace disk (200 MiB ext4, ExtraDisks[0] → /dev/vdb).
	wsDiskPath := filepath.Join(diskDir, "workspace.raw")
	arMakeExt4Disk(t, wsDiskPath, 200)
	t.Logf("workspace disk: %s (200 MiB ext4, /dev/vdb)", wsDiskPath)

	t.Cleanup(func() {
		if supervisorPID > 0 {
			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := supervisor.StopSupervisor(stopCtx, supervisor.SockPath(stateDir)); err != nil {
				t.Logf("cleanup StopSupervisor: %v", err)
			}
		}
		if sandboxID != (domain.SandboxID{}) {
			rmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := svc.Remove(rmCtx, sandboxID.String()); err != nil {
				t.Logf("cleanup svc.Remove: %v", err)
			}
		}
		if svLog, err := os.ReadFile(filepath.Join(stateDir, "supervisor.log")); err == nil && t.Failed() {
			t.Logf("=== supervisor log (tail 100) ===\n%s", lastNLines(string(svLog), 100))
		}
		os.RemoveAll(socketDir)
		os.RemoveAll(stateDir)
	})

	// ── Step 2: build base image + nexus3 binary ──────────────────────────────
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	t.Log("building agent base image …")
	imgCtx, imgCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer imgCancel()
	img, buildErr := BuildAgentBaseImage(imgCtx, cache)
	if buildErr != nil {
		switch {
		case errors.Is(buildErr, ErrDockerUnavailable):
			t.Skip("docker unavailable:", buildErr)
		case errors.Is(buildErr, builder.ErrMke2fsUnavailable):
			t.Skip("mke2fs unavailable:", buildErr)
		}
		t.Fatalf("BuildAgentBaseImage: %v", buildErr)
	}
	t.Logf("base image ready: digest=%s", img.Digest)
	nexus3Bin := buildNexus3Bin(t)
	t.Logf("nexus3 binary: %s", nexus3Bin)

	// ── Step 3: CreateAndBoot with workspace disk ─────────────────────────────
	const memCeiling int64 = 1024 * 1024 * 1024 // 1 GiB

	var rootfsDiskPath string
	var bootDrv *cloudhypervisor.CHDriver

	// Workspace mount: ExtraDisks[0] → /dev/vdb → /workspace (IsWorkspace=true).
	wsMount := agent.GuestMount{
		Device:      arGuestDev(0), // /dev/vdb
		Target:      "/workspace",
		FSType:      "ext4",
		IsWorkspace: true,
	}
	// PID-1 args follow the -- separator already written by arWsMountCmdline.
	svCmdline := arWsMountCmdline([]agent.GuestMount{wsMount}) +
		" --mem-ceiling=" + strconv.FormatInt(memCeiling, 10)
	t.Logf("supervisor cmdline: %s", svCmdline)

	factory := service.DriverFactory(func(resolvedExt4 string, extraDisks []service.ExtraDisk) (driver.Driver, error) {
		rootfsDiskPath = resolvedExt4
		chExtra := make([]cloudhypervisor.ExtraDisk, len(extraDisks))
		for i, ed := range extraDisks {
			chExtra[i] = cloudhypervisor.ExtraDisk{Path: ed.Path}
		}
		var newErr error
		bootDrv, newErr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:    chBin,
			SocketDir:     socketDir,
			KernelPath:    kernelPath,
			DiskImagePath: resolvedExt4,
			ExtraDisks:    chExtra,
			StartTimeout:  30 * time.Second,
			MemoryMaxMiB:  1024,
		})
		return bootDrv, newErr
	})
	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		return realProbeSH(bootDrv)(ctx, drv, id)
	})

	t.Log("creating and booting sandbox (1 workspace disk) …")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer bootCancel()
	sb, err := service.CreateAndBoot(bootCtx, svc, cache, factory, probe,
		"ar-dt", "telemetry",
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(img.Digest)},
			CacheRoot:           cacheRoot,
			ReachabilityTimeout: 60 * time.Second,
			ExtraDisks:          []service.ExtraDisk{{Path: wsDiskPath}},
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxID = sb.ID
	t.Logf("sandbox booted: id=%s rootfs=%s", sb.ID, rootfsDiskPath)

	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer stopCancel()
	if _, err := svc.Stop(stopCtx, sb.ID.String()); err != nil {
		t.Fatalf("svc.Stop: %v", err)
	}
	t.Log("sandbox stopped; rootfs and workspace disk retained")

	// ── Step 4: SpawnDetached — supervisor with workspace disk ────────────────
	// HasWorkspaceDisk=false keeps the disk governor passive (no grow).
	// The agent still serves DiskSupported=true from statfs(/workspace).
	pid, _, err := supervisor.SpawnDetached(supervisor.SpawnConfig{
		Config: supervisor.Config{
			SandboxRef:         sb.ID.String(),
			StoreRoot:          storeRoot,
			StateDir:           stateDir,
			CHBin:              chBin,
			SocketDir:          socketDir,
			KernelPath:         kernelPath,
			DiskPath:           rootfsDiskPath,
			ExtraDisks:         []string{wsDiskPath},
			MemoryMiB:          512,
			GovBounds:          resize.Bounds{MemMinBytes: 600 << 20, MemMaxBytes: 1024 << 20},
			HasWorkspaceDisk:   false, // governor passive; telemetry path independent
			WorkspaceDiskIndex: 0,
			Cmdline:            svCmdline,
		},
		Exe:          nexus3Bin,
		LogPath:      filepath.Join(stateDir, "supervisor.log"),
		ReadyTimeout: 3 * time.Minute,
	})
	if err != nil {
		t.Fatalf("supervisor.SpawnDetached: %v", err)
	}
	supervisorPID = pid
	t.Logf("supervisor ready: pid=%d sock=%s", pid, supervisor.SockPath(stateDir))

	shadowDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath: chBin, SocketDir: socketDir, KernelPath: kernelPath, DiskImagePath: rootfsDiskPath,
	})
	if err != nil {
		t.Fatalf("shadowDrv: %v", err)
	}

	waitForAgentSH(t, shadowDrv, sb.ID, 60*time.Second)
	t.Log("guest agent reachable")

	// ── Step 5: Assertion — DiskSupported=true ────────────────────────────────
	// Poll for up to 30s: the agent's disk telemetry is initialized synchronously
	// but the first statfs call happens when the telemetry server receives a request.
	const diskPollTimeout = 30 * time.Second
	pollStart := time.Now()
	var diskSample resize.Sample
	for time.Since(pollStart) < diskPollTimeout {
		s, sErr := dialTelemetrySample(shadowDrv, sb.ID)
		if sErr != nil {
			t.Logf("telemetry poll: %v (will retry)", sErr)
			time.Sleep(3 * time.Second)
			continue
		}
		diskSample = s
		if s.DiskSupported {
			break
		}
		time.Sleep(3 * time.Second)
	}

	t.Logf("EVIDENCE telemetry: DiskSupported=%v DiskTotalBytes=%d MiB DiskUsedBytes=%d MiB",
		diskSample.DiskSupported, diskSample.DiskTotalBytes>>20, diskSample.DiskUsedBytes>>20)

	if !diskSample.DiskSupported {
		t.Errorf("FAIL assertion 1: DiskSupported=false — workspace mount cmdline or agent routing is broken")
	} else {
		t.Log("PASS assertion 1: DiskSupported=true — workspace disk mounted, statfs active")
	}
	if diskSample.DiskTotalBytes == 0 {
		t.Errorf("FAIL assertion 2: DiskTotalBytes=0 — statfs on /workspace returned zero total bytes")
	} else {
		t.Logf("PASS assertion 2: DiskTotalBytes=%d MiB > 0", diskSample.DiskTotalBytes>>20)
	}
}

// ── Test 2: Disk grow device routing ─────────────────────────────────────────

// TestAutoResizeDiskGrowDevice proves disk grow routes to the correct backing
// file on a 2-disk topology (shadow + workspace).
//
// Setup:
//   - ExtraDisks[0] = 10 MiB shadow disk → /dev/vdb (IsWorkspace=false)
//   - ExtraDisks[1] = 200 MiB workspace disk → /dev/vdc (IsWorkspace=true)
//   - DiskMaxBytes = 512 MiB (grow ceiling)
//   - WorkspaceDiskIndex = 1
//
// The workspace disk is filled to 85% (170/200 MiB). The disk governor triggers
// when DiskUsed/DiskTotal > 0.80 and grows the workspace to DiskMaxBytes.
//
// Assertions:
//  1. DiskSupported=true (foundation check, test aborts if false)
//  2. Workspace backing file apparent size grows from 200 MiB → 512 MiB
//  3. Shadow backing file size unchanged (proves index routing correct)
//
func TestAutoResizeDiskGrowDevice(t *testing.T) {
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	hostMiB := hostMemAvailableMiB()
	if hostMiB >= 0 && hostMiB < 2048 {
		t.Skipf("skipping: host MemAvailable=%d MiB < 2048 MiB", hostMiB)
	}
	t.Logf("host MemAvailable: %d MiB", hostMiB)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── Step 1: infrastructure ────────────────────────────────────────────────
	socketDir, err := os.MkdirTemp("/tmp", "ar-dg-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp socketDir: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("socket dir too long for AF_UNIX: %s", socketDir)
	}
	stateDir, err := os.MkdirTemp("/tmp", "ar-dg-state-")
	if err != nil {
		os.RemoveAll(socketDir)
		t.Fatalf("MkdirTemp stateDir: %v", err)
	}
	diskDir := t.TempDir()
	storeRoot := t.TempDir()
	cacheRoot := filepath.Join(storeRoot, "images")

	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}
	svcDrv, err := cloudhypervisor.New(cloudhypervisor.Config{BinaryPath: chBin, SocketDir: socketDir})
	if err != nil {
		t.Fatalf("cloudhypervisor.New (svcDrv): %v", err)
	}
	svc := service.New(st, svcDrv, lifecycle.New())

	var supervisorPID int
	var sandboxID domain.SandboxID

	// Pre-create disks.
	shadowPath := filepath.Join(diskDir, "shadow.raw")
	wsPath := filepath.Join(diskDir, "workspace.raw")
	arMakeExt4Disk(t, shadowPath, 10)
	arMakeExt4Disk(t, wsPath, 200)
	t.Logf("shadow disk: %s (10 MiB, ExtraDisks[0] → /dev/vdb)", shadowPath)
	t.Logf("workspace disk: %s (200 MiB, ExtraDisks[1] → /dev/vdc)", wsPath)

	t.Cleanup(func() {
		if supervisorPID > 0 {
			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := supervisor.StopSupervisor(stopCtx, supervisor.SockPath(stateDir)); err != nil {
				t.Logf("cleanup StopSupervisor: %v", err)
			}
		}
		if sandboxID != (domain.SandboxID{}) {
			rmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := svc.Remove(rmCtx, sandboxID.String()); err != nil {
				t.Logf("cleanup svc.Remove: %v", err)
			}
		}
		if svLog, err := os.ReadFile(filepath.Join(stateDir, "supervisor.log")); err == nil && t.Failed() {
			t.Logf("=== supervisor log (tail 100) ===\n%s", lastNLines(string(svLog), 100))
		}
		os.RemoveAll(socketDir)
		os.RemoveAll(stateDir)
	})

	// ── Step 2: build base image + nexus3 binary ──────────────────────────────
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	t.Log("building agent base image …")
	imgCtx, imgCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer imgCancel()
	img, buildErr := BuildAgentBaseImage(imgCtx, cache)
	if buildErr != nil {
		switch {
		case errors.Is(buildErr, ErrDockerUnavailable):
			t.Skip("docker unavailable:", buildErr)
		case errors.Is(buildErr, builder.ErrMke2fsUnavailable):
			t.Skip("mke2fs unavailable:", buildErr)
		}
		t.Fatalf("BuildAgentBaseImage: %v", buildErr)
	}
	t.Logf("base image ready: digest=%s", img.Digest)
	nexus3Bin := buildNexus3Bin(t)

	// ── Step 3: CreateAndBoot with 2 extra disks ──────────────────────────────
	const memCeiling int64 = 1024 * 1024 * 1024  // 1 GiB
	const diskCeiling int64 = 512 * 1024 * 1024   // 512 MiB grow ceiling (keeps test host-friendly)

	// Cmdline: shadow at /dev/vdb (not workspace), workspace at /dev/vdc.
	// PID-1 args follow the -- already in arWsMountCmdline.
	svCmdline := arWsMountCmdline([]agent.GuestMount{
		{Device: arGuestDev(0), Target: "/shadow", FSType: "ext4", IsWorkspace: false},
		{Device: arGuestDev(1), Target: "/workspace", FSType: "ext4", IsWorkspace: true},
	}) + " --mem-ceiling=" + strconv.FormatInt(memCeiling, 10)
	t.Logf("supervisor cmdline: %s", svCmdline)

	var rootfsDiskPath string
	var bootDrv *cloudhypervisor.CHDriver

	factory := service.DriverFactory(func(resolvedExt4 string, extraDisks []service.ExtraDisk) (driver.Driver, error) {
		rootfsDiskPath = resolvedExt4
		chExtra := make([]cloudhypervisor.ExtraDisk, len(extraDisks))
		for i, ed := range extraDisks {
			chExtra[i] = cloudhypervisor.ExtraDisk{Path: ed.Path}
		}
		var newErr error
		bootDrv, newErr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:    chBin,
			SocketDir:     socketDir,
			KernelPath:    kernelPath,
			DiskImagePath: resolvedExt4,
			ExtraDisks:    chExtra,
			StartTimeout:  30 * time.Second,
			MemoryMaxMiB:  1024,
		})
		return bootDrv, newErr
	})
	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		return realProbeSH(bootDrv)(ctx, drv, id)
	})

	t.Log("creating and booting sandbox (shadow + workspace disks) …")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer bootCancel()
	sb, err := service.CreateAndBoot(bootCtx, svc, cache, factory, probe,
		"ar-dg", "grow",
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(img.Digest)},
			CacheRoot:           cacheRoot,
			ReachabilityTimeout: 60 * time.Second,
			ExtraDisks:          []service.ExtraDisk{{Path: shadowPath}, {Path: wsPath}},
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxID = sb.ID
	t.Logf("sandbox booted: id=%s", sb.ID)

	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer stopCancel()
	if _, err := svc.Stop(stopCtx, sb.ID.String()); err != nil {
		t.Fatalf("svc.Stop: %v", err)
	}

	// ── Step 4: SpawnDetached with disk grow governor ─────────────────────────
	// WorkspaceDiskIndex=1 → GrowDisk targets ExtraDisks[1] (wsPath) → /dev/vdc.
	pid, _, err := supervisor.SpawnDetached(supervisor.SpawnConfig{
		Config: supervisor.Config{
			SandboxRef: sb.ID.String(),
			StoreRoot:  storeRoot,
			StateDir:   stateDir,
			CHBin:      chBin,
			SocketDir:  socketDir,
			KernelPath: kernelPath,
			DiskPath:   rootfsDiskPath,
			ExtraDisks: []string{shadowPath, wsPath}, // [0]=shadow→/dev/vdb, [1]=workspace→/dev/vdc
			MemoryMiB:  512,
			GovBounds: resize.Bounds{
				MemMinBytes:  600 << 20,
				MemMaxBytes:  1024 << 20,
				DiskMaxBytes: diskCeiling,
				// VCPUMin/VCPUMax=0: CPU axis passive (focus on disk grow).
			},
			HasWorkspaceDisk:   true,
			WorkspaceDiskIndex: 1, // ExtraDisks[1] = wsPath
			Cmdline:            svCmdline,
		},
		Exe:          nexus3Bin,
		LogPath:      filepath.Join(stateDir, "supervisor.log"),
		ReadyTimeout: 3 * time.Minute,
	})
	if err != nil {
		t.Fatalf("supervisor.SpawnDetached: %v", err)
	}
	supervisorPID = pid
	t.Logf("supervisor ready: pid=%d", pid)

	shadowDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath: chBin, SocketDir: socketDir, KernelPath: kernelPath, DiskImagePath: rootfsDiskPath,
	})
	if err != nil {
		t.Fatalf("shadowDrv: %v", err)
	}
	waitForAgentSH(t, shadowDrv, sb.ID, 60*time.Second)
	agentClient := agent.NewClient(shadowDrv, sb.ID)
	t.Log("guest agent reachable")

	// ── Step 5: Foundation check — DiskSupported=true ────────────────────────
	firstSample, firstErr := dialTelemetrySample(shadowDrv, sb.ID)
	if firstErr != nil {
		t.Fatalf("first telemetry: %v", firstErr)
	}
	t.Logf("EVIDENCE first sample: DiskSupported=%v DiskTotal=%d MiB DiskUsed=%d MiB",
		firstSample.DiskSupported, firstSample.DiskTotalBytes>>20, firstSample.DiskUsedBytes>>20)
	if !firstSample.DiskSupported {
		t.Fatalf("FAIL FOUNDATION: DiskSupported=false — workspace mount or cmdline broken; " +
			"aborting disk-grow device test (a passing grow result would be meaningless)")
	}
	t.Log("PASS FOUNDATION: DiskSupported=true — workspace disk at /dev/vdc mounted")

	// Capture baseline backing file sizes.
	shadowSizeBefore, err := arApparentSize(shadowPath)
	if err != nil {
		t.Fatalf("stat shadow before: %v", err)
	}
	wsSizeBefore, err := arApparentSize(wsPath)
	if err != nil {
		t.Fatalf("stat workspace before: %v", err)
	}
	t.Logf("EVIDENCE baseline backing files: shadow=%d MiB workspace=%d MiB",
		shadowSizeBefore>>20, wsSizeBefore>>20)

	// ── Step 6: Fill workspace disk to >80% to trigger disk governor ─────────
	// DiskTotal (from guest statfs) ≈ 171 MiB for a 200 MiB ext4 image.
	// Threshold: DiskUsed/DiskTotal > 0.80. Need DiskUsed > 137 MiB.
	// Writing 140 MiB: DiskUsed ≈ 141 MiB → ratio ≈ 82.5% > 80%.
	// Root can use the reserved-for-root 5%, so 140 MiB fits in 171 MiB capacity.
	// Boot delay: 15s. Eval interval: 5s. Expected trigger: ~25-30s from VM start.
	// Poll timeout: 90s (conservative).
	t.Log("filling workspace disk (140 MiB dd fill → >80% of 171 MiB) …")
	fillCtx, fillCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer fillCancel()
	var fillBuf bytes.Buffer
	fillExit, fillErr := agentClient.Exec(fillCtx, agent.ExecOptions{
		Argv: []string{"/bin/sh", "-c",
			"dd if=/dev/zero of=/workspace/hog bs=1M count=140 status=none && sync"},
		Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Stdout: &fillBuf,
		Stderr: &fillBuf,
	})
	if fillErr != nil {
		t.Fatalf("fill exec: %v (output: %s)", fillErr, fillBuf.String())
	}
	if fillExit != 0 {
		t.Fatalf("fill exit=%d: %s", fillExit, fillBuf.String())
	}
	t.Log("workspace fill done; waiting for governor to detect >80% and trigger GrowDisk …")

	// Confirm ratio is visible in telemetry.
	if s, sErr := dialTelemetrySample(shadowDrv, sb.ID); sErr == nil && s.DiskTotalBytes > 0 {
		t.Logf("EVIDENCE post-fill: DiskUsed=%d MiB DiskTotal=%d MiB ratio=%.1f%%",
			s.DiskUsedBytes>>20, s.DiskTotalBytes>>20,
			100*float64(s.DiskUsedBytes)/float64(s.DiskTotalBytes))
	}

	// ── Step 7: Poll backing file for governor-triggered grow ────────────────
	const growPoll = 10 * time.Second
	const growWait = 90 * time.Second
	growStart := time.Now()
	var growDetected bool

	for !time.Now().After(growStart.Add(growWait)) {
		time.Sleep(growPoll)
		wsSize, sErr := arApparentSize(wsPath)
		if sErr != nil {
			t.Logf("stat workspace: %v", sErr)
			continue
		}
		t.Logf("poll: workspace apparent size=%d MiB elapsed=%s",
			wsSize>>20, time.Since(growStart).Round(time.Second))
		if wsSize > wsSizeBefore {
			growDetected = true
			break
		}
	}

	// Capture final state.
	shadowSizeAfter, _ := arApparentSize(shadowPath)
	wsSizeAfter, _ := arApparentSize(wsPath)
	t.Logf("EVIDENCE final backing files: shadow=%d MiB workspace=%d MiB",
		shadowSizeAfter>>20, wsSizeAfter>>20)

	// ── Assertions ────────────────────────────────────────────────────────────

	// Read supervisor log to gather governor-activity evidence.
	// This happens after the 90s poll window, so grow attempts should be logged.
	svLog, svLogReadErr := os.ReadFile(filepath.Join(stateDir, "supervisor.log"))
	var svLogStr string
	if svLogReadErr == nil {
		svLogStr = string(svLog)
	}
	governorTargetedIdx1 := strings.Contains(svLogStr, "diskIndex=1")
	ch400Rejected := strings.Contains(svLogStr, "unexpected status 400")

	t.Logf("EVIDENCE supervisor log: governorTargetedIdx1=%v ch400Rejected=%v",
		governorTargetedIdx1, ch400Rejected)

	// Assertion 1: workspace backing file grew.
	//
	// An HTTP 400 from vm.resize-disk is not an acceptable outcome: the root cause
	// (nexus3 sending JSON field "size" instead of "desired_size") was fixed in
	// client.go:231-233 and confirmed via a live 204 response against CH v52.0.
	// If ch400Rejected is true here, that fix has regressed.
	//
	// A non-grow with no HTTP 400 indicates the governor threshold was not crossed,
	// checkFreeSpace declined for insufficient host pool space, or the 90s poll
	// window was exhausted before the grow landed — none of these are API rejections.
	if growDetected {
		t.Logf("PASS assertion 1 (full grow): workspace grew %d MiB → %d MiB",
			wsSizeBefore>>20, wsSizeAfter>>20)
	} else if ch400Rejected {
		t.Errorf("FAIL assertion 1: vm.resize-disk returned HTTP 400 — regression of the "+
			"desired_size fix (client.go:231-233); CH v52.0 requires JSON field \"desired_size\" "+
			"not \"size\"; a live proof against a real CH v52.0 socket returned 204 after the fix "+
			"(governorTargetedIdx1=%v wsBefore=%d MiB wsAfter=%d MiB) — "+
			"check supervisor.log for the full CH error body",
			governorTargetedIdx1, wsSizeBefore>>20, wsSizeAfter>>20)
	} else {
		t.Errorf("FAIL assertion 1: workspace backing file did not grow within %s "+
			"(before=%d MiB after=%d MiB) and governor activity not confirmed in supervisor log "+
			"(governorTargetedIdx1=%v ch400Rejected=%v) — check supervisor.log for govern.disk.*",
			growWait, wsSizeBefore>>20, wsSizeAfter>>20, governorTargetedIdx1, ch400Rejected)
	}

	// Assertion 2: shadow backing file unchanged (device routing correct).
	if shadowSizeAfter > shadowSizeBefore {
		t.Errorf("FAIL assertion 2: shadow backing file grew! before=%d MiB after=%d MiB "+
			"— this means GrowDisk targeted ExtraDisks[0] (shadow) instead of ExtraDisks[1] (workspace)",
			shadowSizeBefore>>20, shadowSizeAfter>>20)
	} else {
		t.Logf("PASS assertion 2: shadow unchanged at %d MiB (GrowDisk correctly targeted ExtraDisks[1]/dev/vdc)",
			shadowSizeAfter>>20)
	}

	// Assertion 3: if the backing file grew, the guest filesystem must also have grown.
	// AR-GA is closed: GrowDisk expands the backing file, calls vm.resize-disk, then sends
	// EncodeGrowRequest over vsock so the guest agent runs resize2fs. If the grow was not
	// confirmed (backing file not larger), the vsock path was never reached — skip this check.
	postGrowSample, _ := dialTelemetrySample(shadowDrv, sb.ID)
	t.Logf("EVIDENCE post-grow telemetry: DiskTotal=%d MiB (before=%d MiB)",
		postGrowSample.DiskTotalBytes>>20, firstSample.DiskTotalBytes>>20)
	if growDetected {
		if postGrowSample.DiskTotalBytes > firstSample.DiskTotalBytes {
			t.Logf("PASS assertion 3: guest DiskTotal grew %d→%d MiB — resize2fs triggered via GrowRequest vsock",
				firstSample.DiskTotalBytes>>20, postGrowSample.DiskTotalBytes>>20)
		} else {
			t.Errorf("FAIL assertion 3: backing file grew but guest DiskTotal unchanged (%d MiB) — "+
				"GrowRequest vsock round-trip or in-guest resize2fs may have failed; check supervisor.log",
				postGrowSample.DiskTotalBytes>>20)
		}
	} else {
		// Grow was not confirmed: backing file did not grow within the poll window, so
		// GrowDisk either never ran (governor threshold not crossed), was rejected by
		// checkFreeSpace (host pool insufficient), hit a filesystem or CH API error, or
		// the 90s poll window was exhausted. resize2fs is not reached in any of these cases.
		t.Logf("INFO assertion 3 skipped: backing-file grow not confirmed; "+
			"DiskTotal=%d MiB (unchanged) — check supervisor.log for govern.disk or GrowDisk errors",
			postGrowSample.DiskTotalBytes>>20)
	}
}

// arApparentSize returns the logical file size (apparent, not actual allocated bytes).
// For a sparse file created by os.Truncate this equals the truncation target.
func arApparentSize(path string) (int64, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// ── Test 3: vCPU hotplug ──────────────────────────────────────────────────────

// TestAutoResizeVCPU proves vCPU hotplug via the CPU pressure governor.
//
// A VM is booted with 1 vCPU and VCPUMax=2. A CPU-intensive workload pushes
// CPUPSISomeAvg10 >= 15% (cpuGrowPressure). The governor, which is eager
// (cpuGrowConsecutive=1), fires ResizeCPU on the first pressured sample.
// Cloud Hypervisor hot-plugs a second vCPU; the in-guest CPUOnliner goroutine
// writes "1" to /sys/devices/system/cpu/cpu1/online within 3s.
//
// Requires CONFIG_PSI=y in the guest kernel. If CPUPSISupported=false,
// the test is skipped (the governor hard-gates on the PSI flag).
func TestAutoResizeVCPU(t *testing.T) {
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	hostMiB := hostMemAvailableMiB()
	if hostMiB >= 0 && hostMiB < 2048 {
		t.Skipf("skipping: host MemAvailable=%d MiB < 2048 MiB", hostMiB)
	}
	t.Logf("host MemAvailable: %d MiB", hostMiB)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── Step 1: infrastructure ────────────────────────────────────────────────
	socketDir, err := os.MkdirTemp("/tmp", "ar-vcpu-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp socketDir: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("socket dir too long for AF_UNIX: %s", socketDir)
	}
	stateDir, err := os.MkdirTemp("/tmp", "ar-vcpu-state-")
	if err != nil {
		os.RemoveAll(socketDir)
		t.Fatalf("MkdirTemp stateDir: %v", err)
	}
	storeRoot := t.TempDir()
	cacheRoot := filepath.Join(storeRoot, "images")

	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}
	svcDrv, err := cloudhypervisor.New(cloudhypervisor.Config{BinaryPath: chBin, SocketDir: socketDir})
	if err != nil {
		t.Fatalf("cloudhypervisor.New (svcDrv): %v", err)
	}
	svc := service.New(st, svcDrv, lifecycle.New())

	var supervisorPID int
	var sandboxID domain.SandboxID

	t.Cleanup(func() {
		if supervisorPID > 0 {
			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := supervisor.StopSupervisor(stopCtx, supervisor.SockPath(stateDir)); err != nil {
				t.Logf("cleanup StopSupervisor: %v", err)
			}
		}
		if sandboxID != (domain.SandboxID{}) {
			rmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := svc.Remove(rmCtx, sandboxID.String()); err != nil {
				t.Logf("cleanup svc.Remove: %v", err)
			}
		}
		if svLog, err := os.ReadFile(filepath.Join(stateDir, "supervisor.log")); err == nil && t.Failed() {
			t.Logf("=== supervisor log (tail 100) ===\n%s", lastNLines(string(svLog), 100))
		}
		os.RemoveAll(socketDir)
		os.RemoveAll(stateDir)
	})

	// ── Step 2: build base image + nexus3 binary ──────────────────────────────
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	t.Log("building agent base image …")
	imgCtx, imgCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer imgCancel()
	img, buildErr := BuildAgentBaseImage(imgCtx, cache)
	if buildErr != nil {
		switch {
		case errors.Is(buildErr, ErrDockerUnavailable):
			t.Skip("docker unavailable:", buildErr)
		case errors.Is(buildErr, builder.ErrMke2fsUnavailable):
			t.Skip("mke2fs unavailable:", buildErr)
		}
		t.Fatalf("BuildAgentBaseImage: %v", buildErr)
	}
	nexus3Bin := buildNexus3Bin(t)

	// ── Step 3: CreateAndBoot with VCPUMax=2 ─────────────────────────────────
	// VCPUMax=2 reserves a hotplug slot in CH at vm.create time.
	// Without this, vm.resize with vcpus=2 will fail (max_vcpus immutable after create).
	const memCeiling int64 = 1024 * 1024 * 1024 // 1 GiB

	var rootfsDiskPath string
	var bootDrv *cloudhypervisor.CHDriver

	svCmdline := diskBootCmdlineBase +
		" -- --mem-ceiling=" + strconv.FormatInt(memCeiling, 10)

	factory := service.DriverFactory(func(resolvedExt4 string, _ []service.ExtraDisk) (driver.Driver, error) {
		rootfsDiskPath = resolvedExt4
		var newErr error
		bootDrv, newErr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:    chBin,
			SocketDir:     socketDir,
			KernelPath:    kernelPath,
			DiskImagePath: resolvedExt4,
			StartTimeout:  30 * time.Second,
			MemoryMaxMiB:  1024,
			VCPUs:         1,
			VCPUMax:       2,
		})
		return bootDrv, newErr
	})
	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		return realProbeSH(bootDrv)(ctx, drv, id)
	})

	t.Log("creating and booting sandbox (1 vCPU, VCPUMax=2) …")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer bootCancel()
	sb, err := service.CreateAndBoot(bootCtx, svc, cache, factory, probe,
		"ar-vcpu", "hotplug",
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
	t.Logf("sandbox booted: id=%s", sb.ID)

	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer stopCancel()
	if _, err := svc.Stop(stopCtx, sb.ID.String()); err != nil {
		t.Fatalf("svc.Stop: %v", err)
	}

	// ── Step 4: SpawnDetached with VCPUMin=1, VCPUMax=2 ──────────────────────
	// BootVCPUs=1 seeds the resizer so CurrentVCPUs() returns 1 before any resize.
	// The governor's CPU axis is eager (cpuGrowWindow=0s): one sample at >= 15%
	// fires ResizeCPU immediately.
	pid, _, err := supervisor.SpawnDetached(supervisor.SpawnConfig{
		Config: supervisor.Config{
			SandboxRef: sb.ID.String(),
			StoreRoot:  storeRoot,
			StateDir:   stateDir,
			CHBin:      chBin,
			SocketDir:  socketDir,
			KernelPath: kernelPath,
			DiskPath:   rootfsDiskPath,
			MemoryMiB:  512,
			BootVCPUs:  1,
			GovBounds: resize.Bounds{
				MemMinBytes: 600 << 20,
				MemMaxBytes: 1024 << 20,
				VCPUMin:     1,
				VCPUMax:     2,
			},
			Cmdline: svCmdline,
		},
		Exe:          nexus3Bin,
		LogPath:      filepath.Join(stateDir, "supervisor.log"),
		ReadyTimeout: 3 * time.Minute,
	})
	if err != nil {
		t.Fatalf("supervisor.SpawnDetached: %v", err)
	}
	supervisorPID = pid
	t.Logf("supervisor ready: pid=%d", pid)

	shadowDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath: chBin, SocketDir: socketDir, KernelPath: kernelPath, DiskImagePath: rootfsDiskPath,
	})
	if err != nil {
		t.Fatalf("shadowDrv: %v", err)
	}
	waitForAgentSH(t, shadowDrv, sb.ID, 60*time.Second)
	agentClient := agent.NewClient(shadowDrv, sb.ID)
	t.Log("guest agent reachable")

	// ── Step 5: Assert baseline VCPUOnline and check PSI support ─────────────
	baseSample, baseErr := dialTelemetrySample(shadowDrv, sb.ID)
	if baseErr != nil {
		t.Fatalf("baseline telemetry: %v", baseErr)
	}
	t.Logf("EVIDENCE baseline: VCPUCount=%d VCPUOnline=%d CPUPSISomeAvg10=%.2f CPUPSISupported=%v",
		baseSample.VCPUCount, baseSample.VCPUOnline,
		baseSample.CPUPSISomeAvg10, baseSample.CPUPSISupported)

	if !baseSample.CPUPSISupported {
		t.Logf("FINDING: CPUPSISupported=false — /proc/pressure/cpu absent in guest kernel; " +
			"CPU governor hard-gated (govern/cpu.go:138), vCPU grow cannot fire")
		t.Skip("skipping vCPU grow assertion: guest kernel PSI unavailable")
	}

	// ── Step 6: CPU stress — push CPUPSISomeAvg10 >= 15% ────────────────────
	// PSI some_avg10 measures the fraction of time at least one task was STALLED
	// waiting for CPU. A single process that is always running never stalls →
	// PSI stays at 0. We need multiple concurrent processes competing for the
	// single vCPU so some are always waiting (PSI > 0).
	//
	// Strategy: 4 parallel dd spins. With 4 processes contending for 1 vCPU,
	// at any instant 3 are runnable-but-waiting → PSI some_avg10 approaches 75%.
	// The 10-second exponential average climbs to ≥ 15% within a few seconds.
	t.Log("starting CPU stress (4 parallel dd spins) to push PSI >= 15% …")
	stressCtx, stressCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer stressCancel()

	go func() {
		var buf bytes.Buffer
		//nolint:errcheck // stress is background; stressCancel stops it
		agentClient.Exec(stressCtx, agent.ExecOptions{
			Argv: []string{"/bin/sh", "-c",
				// 4 background dd processes compete for 1 vCPU → PSI some_avg10 ~75%.
				// `wait` keeps sh alive so context cancellation reaches the group.
				"dd if=/dev/zero of=/dev/null bs=1M 2>/dev/null &" +
					"dd if=/dev/zero of=/dev/null bs=1M 2>/dev/null &" +
					"dd if=/dev/zero of=/dev/null bs=1M 2>/dev/null &" +
					"dd if=/dev/zero of=/dev/null bs=1M 2>/dev/null &" +
					"wait"},
			Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
			Stdout: &buf,
			Stderr: &buf,
		})
	}()

	// ── Step 7: Poll for vCPU grow ───────────────────────────────────────────
	// Governor: cpuGrowPressure=15, cpuGrowWindow=0s (eager), cpuEvalInterval=5s.
	// Expected latency: 5-10s after stress starts.
	// Budget: 90s (includes PSI ramp-up time).
	const psiThreshold = 15.0
	const vcpuPoll = 5 * time.Second
	const vcpuWait = 90 * time.Second
	growStart := time.Now()
	var psiReached, growDetected bool
	var maxPSI float64
	var finalVCPUOnline int32

	for !time.Now().After(growStart.Add(vcpuWait)) {
		time.Sleep(vcpuPoll)
		s, sErr := dialTelemetrySample(shadowDrv, sb.ID)
		if sErr != nil {
			t.Logf("telemetry poll: %v", sErr)
			continue
		}
		elapsed := time.Since(growStart).Round(time.Second)
		t.Logf("poll: VCPUOnline=%d CPUPSISomeAvg10=%.2f elapsed=%s",
			s.VCPUOnline, s.CPUPSISomeAvg10, elapsed)
		if s.CPUPSISomeAvg10 >= psiThreshold {
			psiReached = true
		}
		if s.CPUPSISomeAvg10 > maxPSI {
			maxPSI = s.CPUPSISomeAvg10
		}
		if s.VCPUOnline > baseSample.VCPUOnline {
			growDetected = true
			finalVCPUOnline = s.VCPUOnline
			break
		}
	}
	stressCancel() // stop stress workload

	// Read the online CPU mask from sysfs as a second witness.
	var cpuMaskBuf bytes.Buffer
	cpuMaskCtx, cpuMaskCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cpuMaskCancel()
	cpuMaskExit, cpuMaskErr := agentClient.Exec(cpuMaskCtx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "cat /sys/devices/system/cpu/online"},
		Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Stdout: &cpuMaskBuf,
		Stderr: &cpuMaskBuf,
	})
	cpuOnlineMask := strings.TrimSpace(cpuMaskBuf.String())
	t.Logf("EVIDENCE /sys/devices/system/cpu/online (exit=%d): %q err=%v",
		cpuMaskExit, cpuOnlineMask, cpuMaskErr)
	t.Logf("EVIDENCE CPU summary: maxPSI=%.2f%% psiReached=%v growDetected=%v finalVCPUOnline=%d",
		maxPSI, psiReached, growDetected, finalVCPUOnline)

	// ── Assertions ────────────────────────────────────────────────────────────
	if !psiReached {
		t.Errorf("FAIL PSI: CPUPSISomeAvg10 never reached %.1f%% (max=%.2f%%) within %s — "+
			"stress workload may not have saturated the vCPU", psiThreshold, maxPSI, vcpuWait)
	} else {
		t.Logf("PASS PSI: CPUPSISomeAvg10=%.2f%% >= %.1f%% threshold observed", maxPSI, psiThreshold)
	}

	if !growDetected {
		t.Errorf("FAIL vCPU grow: VCPUOnline did not increase from %d within %s "+
			"(maxPSI=%.2f%%) — check supervisor log for govern.cpu.grow",
			baseSample.VCPUOnline, vcpuWait, maxPSI)
	} else {
		t.Logf("PASS vCPU grow: VCPUOnline %d → %d; online mask=%s",
			baseSample.VCPUOnline, finalVCPUOnline, cpuOnlineMask)
	}
}
