//go:build integration

package selfhost

// disk_grow_http_evidence_test.go — R-CHLIVE live proof (Slice R-CHLIVE).
//
// TestDiskGrowHTTPEvidence provides raw, unambiguous evidence for two legs that
// have NEVER hit real Cloud Hypervisor hardware before this test:
//
//  1. Wire-format fix (desired_size): captures the verbatim HTTP status line from
//     curl --unix-socket against the live CH API socket for
//     PUT /api/v1/vm.resize-disk with {"id":"_disk1","desired_size":NN}.
//
//  2. In-guest grow leg (resize2fs): `df -k /workspace` from inside the guest,
//     before and after a vsock GrowRequest triggers resize2fs.
//
// Evidence standard (per R-CHLIVE task specification):
//   - Raw HTTP status line captured by curl --include, not inferred from logs.
//   - Verbatim df -k /workspace output before and after.
//   - grow_guest_unreachable / grow_guest_failed: either means FAIL.
//
// Running:
//
//	TMPDIR=/tmp go test -tags integration \
//	    -run TestDiskGrowHTTPEvidence \
//	    ./internal/test/selfhost/ -v -timeout 60m

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
)

// TestDiskGrowHTTPEvidence is the R-CHLIVE live proof for:
//
//  1. The desired_size wire-format fix (raw HTTP status from live CH socket).
//  2. The resize2fs guest leg (df before/after a GrowRequest over vsock).
//
// The test is DELIBERATELY NOT using the governor loop; it manually triggers the
// two legs to maximise evidence clarity and minimise timing uncertainty.
func TestDiskGrowHTTPEvidence(t *testing.T) {
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

	// ── Step 1: Infrastructure ────────────────────────────────────────────────
	// Use /tmp directly (not t.TempDir()) for socket dir so sun_path stays short.
	socketDir, err := os.MkdirTemp("/tmp", "r-chlive-sock-")
	if err != nil {
		t.Fatalf("MkdirTemp socketDir: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("socket dir too long for AF_UNIX (107-byte limit): %s", socketDir)
	}

	storeRoot := t.TempDir()
	diskDir := t.TempDir()
	cacheRoot := filepath.Join(storeRoot, "images")

	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}
	svcDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath: chBin,
		SocketDir:  socketDir,
	})
	if err != nil {
		t.Fatalf("cloudhypervisor.New (svcDrv): %v", err)
	}
	svc := service.New(st, svcDrv, lifecycle.New())

	var sandboxID domain.SandboxID
	t.Cleanup(func() {
		if sandboxID != (domain.SandboxID{}) {
			rmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := svc.Remove(rmCtx, sandboxID.String()); err != nil {
				t.Logf("cleanup svc.Remove %s: %v", sandboxID, err)
			}
		}
		os.RemoveAll(socketDir)
	})

	// ── Step 2: Workspace disk — 200 MiB ext4, target 600 MiB after grow ─────
	// 200 → 600 MiB grow is well below the governor step (16 GiB) but large
	// enough to produce an unambiguous df delta (≈590 vs ≈170 MiB usable).
	const initialMiB = 200
	const targetBytes = int64(600 * 1024 * 1024) // 600 MiB

	wsPath := filepath.Join(diskDir, "workspace.raw")
	arMakeExt4Disk(t, wsPath, initialMiB)
	t.Logf("workspace disk created: %s (%d MiB ext4, ExtraDisks[0] → /dev/vdb)", wsPath, initialMiB)

	// ── Step 3: Build agent base image ───────────────────────────────────────
	// BuildAgentBaseImage compiles a fresh nexus3-agent (CGO_ENABLED=0) and
	// bakes it into an ext4 rootfs via Docker. Cache hits are common; a cold
	// build takes ~10–20 min.
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}
	t.Log("building agent base image (may use cached layer) …")
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

	// ── Step 4: Boot sandbox with workspace disk mounted at /workspace ────────
	// Cmdline: --workspace-mount mounts /dev/vdb at /workspace (not a telemetry
	// workspace — IsWorkspace=false keeps the disk governor passive). The mount
	// happens synchronously in the agent init phase, before vsock control opens.
	const wsMountCmdline = diskBootCmdlineBase +
		" -- --workspace-mount=/dev/vdb:/workspace:ext4:false:false"

	var bootDrv *cloudhypervisor.CHDriver
	factory := service.DriverFactory(func(resolvedExt4 string, extraDisks []service.ExtraDisk) (driver.Driver, error) {
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
			Cmdline:       wsMountCmdline,
			StartTimeout:  30 * time.Second,
			MemoryMaxMiB:  1024,
		})
		return bootDrv, newErr
	})
	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		return realProbeSH(bootDrv)(ctx, drv, id)
	})

	t.Log("creating and booting sandbox …")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer bootCancel()
	sb, err := service.CreateAndBoot(bootCtx, svc, cache, factory, probe,
		"r-chlive", "diskgrow",
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(img.Digest)},
			CacheRoot:           cacheRoot,
			ReachabilityTimeout: 60 * time.Second,
			ExtraDisks:          []service.ExtraDisk{{Path: wsPath}},
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxID = sb.ID
	t.Logf("sandbox booted: id=%s", sb.ID)

	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)
	t.Log("guest agent reachable — workspace disk should be mounted at /workspace")

	agentClient := agent.NewClient(bootDrv, sb.ID)

	// Give the mount a moment to settle (agent mounts synchronously but kernel
	// may need a brief moment for the ext4 journal to commit).
	time.Sleep(2 * time.Second)

	// ── Step 5: df BEFORE — baseline filesystem size ──────────────────────────
	dfBefore := rchliveDFWorkspace(t, agentClient, "BEFORE")
	t.Logf("EVIDENCE df BEFORE grow:\n%s", dfBefore)

	// ── Step 6: Expand backing file on the host ───────────────────────────────
	// Safety rule 4 (atomic-on-failure) requires the file to be expanded FIRST,
	// then CH notified. We replicate that order here.
	if err := os.Truncate(wsPath, targetBytes); err != nil {
		t.Fatalf("expand backing file: %v", err)
	}
	fi, _ := os.Stat(wsPath)
	t.Logf("backing file expanded: %s → %d MiB (apparent size)", wsPath, fi.Size()>>20)

	// ── Step 7: Raw HTTP proof — PUT /api/v1/vm.resize-disk ──────────────────
	// Call the live CH socket directly with curl --include to capture the raw
	// HTTP status line. This is the primary evidence for the desired_size fix.
	//
	//   CH disk ID derivation: ExtraDisks[0] → _disk1 (rootfs occupies _disk0).
	//   desired_size: 600 MiB in bytes (matches the truncated backing file size).
	socketPath := filepath.Join(socketDir, sb.ID.String()+".sock")
	diskID := "_disk1" // ExtraDisks[0]; see diskIndexToCHID() in driver_resize.go
	curlBody := fmt.Sprintf(`{"id":%q,"desired_size":%d}`, diskID, targetBytes)
	t.Logf("curl body: %s", curlBody)

	curlCmd := exec.Command("curl",
		"--unix-socket", socketPath,
		"--include",  // include HTTP response headers (status line + headers)
		"--silent",   // suppress progress meter
		"--show-error", // but still show errors
		"-X", "PUT",
		"-H", "Content-Type: application/json",
		"-d", curlBody,
		"http://ch/api/v1/vm.resize-disk",
	)
	var curlOut bytes.Buffer
	curlCmd.Stdout = &curlOut
	curlCmd.Stderr = &curlOut
	curlRunErr := curlCmd.Run()

	rawCurlOutput := curlOut.String()
	t.Logf("EVIDENCE raw HTTP response from PUT /api/v1/vm.resize-disk:\n---\n%s\n---", rawCurlOutput)

	if curlRunErr != nil {
		t.Logf("curl process error: %v", curlRunErr)
		// curl itself may fail even if CH responded; check what we got
	}

	// Extract and assert HTTP status line.
	statusLine := strings.SplitN(rawCurlOutput, "\n", 2)[0]
	statusLine = strings.TrimSpace(statusLine)
	t.Logf("EVIDENCE raw HTTP status line: %q", statusLine)

	if strings.Contains(statusLine, "204") {
		t.Logf("PASS assertion 1 (wire format): CH returned HTTP 204 — desired_size field accepted by live CH v52.0 socket")
	} else if strings.Contains(statusLine, "400") {
		t.Errorf("FAIL assertion 1 (wire format): CH returned HTTP 400 — desired_size fix did NOT reach the live socket (check that binary was rebuilt after client.go 09:23 today)")
	} else {
		t.Errorf("FAIL assertion 1 (wire format): unexpected HTTP status: %s", statusLine)
	}

	// ── Step 8: In-guest resize2fs via vsock GrowRequest ─────────────────────
	// This is the second unproven leg: GrowDisk calls sendGrowToGuest, which dials
	// vsock port 3002 and sends a GrowRequest. The guest agent runs resize2fs.
	// We replicate that call here to prove it works.
	//
	// grow_guest_unreachable / grow_guest_failed warn events: if either appears
	// in this log output, the test marks FAIL.
	t.Log("sending GrowRequest over vsock (resize.TelemetryVsockPort=3002) …")
	growCtx, growCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer growCancel()

	conn, dialErr := bootDrv.DialGuest(growCtx, sb.ID, resize.TelemetryVsockPort)
	if dialErr != nil {
		// This is the grow_guest_unreachable failure mode.
		t.Fatalf("FAIL grow_guest_unreachable: DialGuest vsock:%d: %v",
			resize.TelemetryVsockPort, dialErr)
	}
	growReq := resize.GrowRequest{DiskIndex: 0, TargetBytes: targetBytes}
	if encErr := resize.EncodeGrowRequest(conn, growReq); encErr != nil {
		conn.Close()
		t.Fatalf("FAIL grow_guest_failed (encode): EncodeGrowRequest: %v", encErr)
	}
	growResp, decErr := resize.DecodeGrowResponse(conn)
	conn.Close()
	if decErr != nil {
		t.Fatalf("FAIL grow_guest_failed (decode): DecodeGrowResponse: %v", decErr)
	}
	if growResp.Error != "" {
		t.Errorf("FAIL grow_guest_failed (resize2fs): guest reported error: %s", growResp.Error)
	} else {
		t.Logf("PASS grow vsock: resize2fs succeeded — ResultBytes=%d (%d MiB)",
			growResp.ResultBytes, growResp.ResultBytes>>20)
	}

	// Brief pause for the filesystem to fully commit the resize before df.
	time.Sleep(500 * time.Millisecond)

	// ── Step 9: df AFTER — verify filesystem grew ─────────────────────────────
	dfAfter := rchliveDFWorkspace(t, agentClient, "AFTER")
	t.Logf("EVIDENCE df AFTER grow:\n%s", dfAfter)

	// ── Step 10: Assert filesystem grew ──────────────────────────────────────
	beforeBlocks := rchliveExtract1KBlocks(dfBefore)
	afterBlocks := rchliveExtract1KBlocks(dfAfter)
	t.Logf("EVIDENCE 1K-blocks comparison: before=%d after=%d (delta=%d)",
		beforeBlocks, afterBlocks, afterBlocks-beforeBlocks)

	if afterBlocks > beforeBlocks {
		t.Logf("PASS assertion 2 (df): guest filesystem grew %d → %d 1K-blocks — resize2fs expanded ext4 successfully",
			beforeBlocks, afterBlocks)
	} else if afterBlocks == 0 {
		t.Errorf("FAIL assertion 2 (df): could not parse 1K-blocks from df output (df-before=%q df-after=%q)",
			dfBefore, dfAfter)
	} else {
		t.Errorf("FAIL assertion 2 (df): filesystem did NOT grow (before=%d after=%d 1K-blocks)",
			beforeBlocks, afterBlocks)
	}

	// ── Step 11: Stop sandbox ─────────────────────────────────────────────────
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer stopCancel()
	if _, err := svc.Stop(stopCtx, sb.ID.String()); err != nil {
		t.Logf("svc.Stop: %v (non-fatal, cleanup will remove)", err)
	} else {
		t.Log("sandbox stopped cleanly")
	}
}

// rchliveDFWorkspace execs `df -k /workspace` inside the guest and returns the
// verbatim output. Fatals the test if the exec fails.
func rchliveDFWorkspace(t *testing.T, c *agent.Client, label string) string {
	t.Helper()
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	exitCode, execErr := c.Exec(ctx, agent.ExecOptions{
		Argv: []string{"df", "-k", "/workspace"},
		Env: map[string]string{
			"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		},
		Stdout: &out,
		Stderr: &out,
	})
	if execErr != nil {
		t.Fatalf("df -k /workspace (%s): exec error: %v (output: %s)", label, execErr, out.String())
	}
	if exitCode != 0 {
		t.Fatalf("df -k /workspace (%s): exit=%d (output: %s)", label, exitCode, out.String())
	}
	return out.String()
}

// rchliveExtract1KBlocks parses the "1K-blocks" (total blocks) field from
// the second data line of `df -k` output. Returns 0 if parsing fails.
//
// df -k output format (two lines):
//
//	Filesystem     1K-blocks  Used Available Use% Mounted on
//	/dev/vdb          172015  2364    157631   2% /workspace
func rchliveExtract1KBlocks(dfOutput string) int64 {
	for _, line := range strings.Split(dfOutput, "\n") {
		fields := strings.Fields(line)
		// Skip header line and blank lines; data line starts with a device path.
		if len(fields) < 5 || strings.HasPrefix(fields[0], "Filesystem") {
			continue
		}
		var v int64
		if _, err := fmt.Sscan(fields[1], &v); err == nil {
			return v
		}
	}
	return 0
}
