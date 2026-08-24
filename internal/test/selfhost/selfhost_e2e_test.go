//go:build integration

package selfhost

// selfhost_e2e_test.go is the acceptance bar for the whole nexus3 project:
// "develop nexus3 inside a nexus3 workspace".
//
// It creates a real nexus3 workspace from the self-hosting base image (S1),
// seeds nexus3's own source into the guest, and proves that:
//
//   - go build ./... succeeds INSIDE the workspace, offline (seeded mod cache)
//   - a representative go test subset passes inside the workspace
//   - the HOST filesystem is not mutated (seed-by-copy, not bind mount)
//
// # Seed mechanism note
//
// agent.Copy with COPY_DIRECTION_PUSH is fully implemented: the guest agent's
// handleCopyTransfer calls pushTransfer, which receives data frames from the
// host and writes or extracts them to the guest path (raw bytes for a single
// file; tar extraction for a directory). However, this test seeds via
// agent.Exec + "mkdir -p && tar -xf -" with the tar archive streamed over
// Stdin — a simpler path that avoids the Copy RPC handshake overhead and is
// sufficient for the seed-by-copy, not-bind-mount contract.
//
// # Skip conditions
//
// The test skips (never fails) when any of the following is absent:
//   - /dev/kvm accessible
//   - cloud-hypervisor binary (CLOUD_HYPERVISOR_BIN or default path)
//   - mke2fs in PATH (e2fsprogs)
//   - images/kernel/vmlinux-x86_64 or testdata fallback
//   - docker (required by BuildSelfHostBaseImage)
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run 'SelfHost' \
//	  ./internal/test/selfhost/ -v -timeout 30m

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── constants ─────────────────────────────────────────────────────────────────

const (
	// selfhostDefaultCHBin is the default cloud-hypervisor binary path.
	selfhostDefaultCHBin = "$HOME/.local/bin/cloud-hypervisor"

	// selfhostSunPathMax is the usable sun_path limit for AF_UNIX sockets.
	selfhostSunPathMax = 107

	// selfhostSockNameLen is "sb-<26chars>.sock".
	selfhostSockNameLen = 35

	// selfhostGuestSrcDir is the directory in the guest where nexus3 source
	// is seeded.
	selfhostGuestSrcDir = "/root/nexus3"
)

// ── skip guards ───────────────────────────────────────────────────────────────

// skipUnlessKVMSH skips t if /dev/kvm is absent or inaccessible.
func skipUnlessKVMSH(t *testing.T) {
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

// skipUnlessCHBinSH returns the cloud-hypervisor binary path, skipping if absent.
func skipUnlessCHBinSH(t *testing.T) string {
	t.Helper()
	chBin := os.Getenv("CLOUD_HYPERVISOR_BIN")
	if chBin == "" {
		chBin = selfhostDefaultCHBin
	}
	if _, err := os.Stat(chBin); err != nil {
		t.Skipf("skipping: cloud-hypervisor binary not found at %s (set CLOUD_HYPERVISOR_BIN)", chBin)
	}
	return chBin
}

// skipUnlessMke2fsSH skips t if mke2fs is not in PATH.
func skipUnlessMke2fsSH(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("skipping: mke2fs not found in PATH (install e2fsprogs)")
	}
}

// kernelPathSH returns the vmlinux kernel path, skipping if absent.
// Mirrors the logic from internal/test/acceptance/workspace_e2e_test.go.
func kernelPathSH(t *testing.T, repoRoot string) string {
	t.Helper()
	if p := os.Getenv("NEXUS3_KERNEL_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	primary := filepath.Join(repoRoot, "images", "kernel", "vmlinux-x86_64")
	if _, err := os.Stat(primary); err == nil {
		return primary
	}
	fallback := filepath.Join(repoRoot,
		"internal", "core", "driver", "cloudhypervisor", "testdata", "vmlinux-x86_64")
	if _, err := os.Stat(fallback); err == nil {
		return fallback
	}
	t.Skipf("skipping: vmlinux-x86_64 not found — tried:\n  %s\n  %s\n  Set NEXUS3_KERNEL_PATH",
		primary, fallback)
	panic("unreachable")
}

// ── probe helpers ─────────────────────────────────────────────────────────────

// waitForAgentSH polls the agent control port until reachable or timeout.
func waitForAgentSH(t *testing.T, drv *cloudhypervisor.CHDriver, id domain.SandboxID, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		conn, err := drv.DialGuest(ctx, id, driver.AgentControlPort)
		cancel()
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("guest agent port %d not reachable within %v", driver.AgentControlPort, timeout)
}

// realProbeSH returns a ProbeFunc that polls the agent vsock port until ready.
func realProbeSH(drv *cloudhypervisor.CHDriver) service.ProbeFunc {
	return func(ctx context.Context, _ driver.Driver, id domain.SandboxID) error {
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			conn, err := drv.DialGuest(dialCtx, id, driver.AgentControlPort)
			cancel()
			if err == nil {
				conn.Close()
				return nil
			}
			time.Sleep(300 * time.Millisecond)
		}
	}
}

// ── source tarball ────────────────────────────────────────────────────────────

// skipDirSH returns true for directories that should not be seeded into the
// guest. The denylist is expressed as an allowlist-by-exclusion: we include
// cmd/, internal/, proto/ explicitly and exclude everything else at the root,
// plus universal exclusions for any depth.
func skipDirSH(name string) bool {
	switch name {
	case ".git", ".scratch", "vendor", "testdata":
		return true
	}
	// Skip hidden dirs at any depth.
	return strings.HasPrefix(name, ".")
}

// makeSourceTar builds an in-memory tar archive of nexus3's Go source.
//
// Included tree (relative to repoRoot):
//
//	cmd/
//	internal/   (testdata/ subdirs excluded — avoids 50 MB kernel binaries)
//	proto/
//	third_party/gvisor-tap-vsock/   (required: go.mod replace directive)
//	go.mod
//	go.sum
//
// Returns the archive bytes and the archive size.
func makeSourceTar(repoRoot string) (io.Reader, int64, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Helper: add a single file to the tar.
	addFile := func(absPath, relPath string, info os.FileInfo) error {
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = relPath
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		f, err := os.Open(absPath)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	}

	// Walk a source subtree, recording paths relative to repoRoot.
	walkSub := func(subdir string) error {
		absSubdir := filepath.Join(repoRoot, subdir)
		return filepath.WalkDir(absSubdir, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && skipDirSH(d.Name()) {
				return filepath.SkipDir
			}
			// Only include Go source, proto, and module files.
			if !d.IsDir() {
				ext := filepath.Ext(d.Name())
				switch ext {
				case ".go", ".proto", ".mod", ".sum":
					// include
				default:
					return nil
				}
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			return addFile(path, rel, info)
		})
	}

	// Seed subtrees.
	// third_party/gvisor-tap-vsock is required because go.mod has a local
	// replace directive pointing to it (D-P1-12). Without it, "go build ./..."
	// fails inside the guest with "replacement directory does not exist".
	for _, sub := range []string{"cmd", "internal", "proto", "third_party/gvisor-tap-vsock"} {
		if _, err := os.Stat(filepath.Join(repoRoot, sub)); os.IsNotExist(err) {
			continue // sub may not exist (proto is optional)
		}
		if err := walkSub(sub); err != nil {
			return nil, 0, err
		}
	}

	// Seed root-level module files explicitly.
	for _, name := range []string{"go.mod", "go.sum"} {
		absPath := filepath.Join(repoRoot, name)
		info, err := os.Stat(absPath)
		if err != nil {
			return nil, 0, err
		}
		if err := addFile(absPath, name, info); err != nil {
			return nil, 0, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, 0, err
	}
	sz := int64(buf.Len())
	return &buf, sz, nil
}

// ── the test ──────────────────────────────────────────────────────────────────

// TestSelfHostE2E is the acceptance bar for the nexus3 project:
// "develop nexus3 inside a nexus3 workspace".
//
// It proves the self-hosting dev loop on Linux end-to-end:
//   - workspace boots from the self-hosting base image (Debian+Go+seeded cache)
//   - nexus3's source is seeded in via agent.Exec + tar stdin
//   - go build ./... succeeds offline (GOPROXY=off)
//   - a representative go test subset passes offline
//   - the host repo is not mutated
func TestSelfHostE2E(t *testing.T) {
	// ── skip guards ───────────────────────────────────────────────────────────
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	// Resolve repo root once — used for kernel path, source seeding, and the
	// host-not-mutated check.
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}

	kernelPath := kernelPathSH(t, repoRoot)

	// ── host-not-mutated baseline ─────────────────────────────────────────────
	// Record go.mod mtime before the test. The value must be identical after
	// the run, proving no host filesystem mutation occurred (seed-by-copy, not
	// bind mount: the guest lives in its own ext4 block device).
	goModPath := filepath.Join(repoRoot, "go.mod")
	goModStatBefore, err := os.Stat(goModPath)
	if err != nil {
		t.Fatalf("stat go.mod before test: %v", err)
	}

	// ── Step 1: build/obtain the self-hosting base image ─────────────────────
	t.Log("setting up image cache …")
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	t.Log("building self-hosting base image (first run: 5–10 min; subsequent: seconds from cache) …")
	imgCtx, imgCancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer imgCancel()

	img, err := BuildSelfHostBaseImage(imgCtx, cache)
	if err != nil {
		switch {
		case errors.Is(err, ErrDockerUnavailable):
			t.Skip("skipping: docker unavailable:", err)
		case errors.Is(err, builder.ErrMke2fsUnavailable):
			t.Skip("skipping: mke2fs unavailable:", err)
		}
		t.Fatalf("BuildSelfHostBaseImage: %v", err)
	}
	t.Logf("base image ready: digest=%s size=%.0f MiB", img.Digest, float64(img.Size)/(1024*1024))

	// ── Step 2: boot a workspace from the image ───────────────────────────────

	// Socket dir in /tmp to stay within the 107-byte Linux sun_path limit.
	socketDir, err := os.MkdirTemp("/tmp", "selfhost-e2e-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("skipping: socket dir too long for unix socket: %s", socketDir)
	}

	storeRoot := t.TempDir()
	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}

	serialPath := filepath.Join(socketDir, "serial.log")

	// svcDrv is used by the Service for lifecycle operations (Stop, Remove).
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

	// sandboxID is set after CreateAndBoot so the cleanup func can remove it.
	var sandboxID domain.SandboxID

	t.Cleanup(func() {
		// Print serial log on failure for diagnosis.
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== guest serial output ===\n%s", content)
		}
		// Stop and remove the workspace. Remove internally stops the VM.
		if sandboxID != (domain.SandboxID{}) {
			rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := svc.Remove(rmCtx, sandboxID.String()); err != nil {
				t.Logf("cleanup: svc.Remove(%s): %v", sandboxID, err)
			} else {
				t.Logf("cleanup: workspace %s removed", sandboxID)
			}
		}
		os.RemoveAll(socketDir)
	})

	// bootDrv is captured by the factory closure for the probe and agentClient.
	var bootDrv *cloudhypervisor.CHDriver
	factory := service.DriverFactory(func(resolvedExt4 string, _ []service.ExtraDisk) (driver.Driver, error) {
		var newErr error
		bootDrv, newErr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:       chBin,
			SocketDir:        socketDir,
			KernelPath:       kernelPath,
			DiskImagePath:    resolvedExt4,
			SerialOutputPath: serialPath,
			VCPUs:            2,    // 2 vCPUs for reasonable build speed
			MemoryMiB:        2048, // 2 GiB — Go builds are RAM-hungry
			StartTimeout:     30 * time.Second,
		})
		return bootDrv, newErr
	})

	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		return realProbeSH(bootDrv)(ctx, drv, id)
	})

	t.Log("creating and booting workspace …")
	createCtx, createCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer createCancel()

	sb, err := service.CreateAndBoot(
		createCtx,
		svc,
		cache,
		factory,
		probe,
		"selfhost", "selfhost-e2e",
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
	t.Logf("workspace booted: sandbox=%s state=%s", sb.ID, sb.State)

	if sb.State != domain.Running {
		t.Fatalf("expected state=Running after CreateAndBoot, got %s", sb.State)
	}

	// Wait for agent to be fully reachable before issuing RPCs.
	t.Log("waiting for guest agent …")
	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)
	t.Log("guest agent reachable")

	agentClient := agent.NewClient(bootDrv, sb.ID)

	// ── Step 3: seed nexus3 source into the guest ─────────────────────────────
	//
	// Mechanism: agent.Exec + Stdin, using "head -c N > /tmp/src.tar" to read
	// EXACTLY N bytes then exit — avoiding the stdin-EOF deadlock.
	//
	// Background on the deadlock:
	//   The guest agent's handleDataConn inbound goroutine forwards Data(Stdin)
	//   frames to the process's stdinW pipe but never closes stdinW when the
	//   data connection ends. A command like "tar -xf -" that blocks on stdin
	//   EOF hangs forever, causing a deadlock:
	//     - guest: tar waits for stdin close (stdinW never closed)
	//     - host: runDataPump waits for Exit frame (never arrives)
	//   Fix (without modifying the agent): use "head -c N" which exits after
	//   reading exactly N bytes without needing EOF, then extract from file.
	//
	// This approach satisfies "seed-by-copy, not bind mount": the source is
	// read-only on the host (only local file reads during tar creation), and
	// the guest runs in its own ext4 block device (no bind mount involved).
	t.Log("building source tar archive (cmd/, internal/, proto/, go.mod, go.sum) …")
	tarStart := time.Now()
	srcTar, tarSize, err := makeSourceTar(repoRoot)
	if err != nil {
		t.Fatalf("makeSourceTar: %v", err)
	}
	t.Logf("source tar ready: %.1f MiB in %v", float64(tarSize)/(1024*1024), time.Since(tarStart))

	// head -c N reads exactly N bytes from stdin then exits (no EOF needed).
	// tar then extracts from the file (not stdin). rm cleans up.
	const guestTarTmp = "/tmp/nexus3-src.tar"
	seedCmd := "mkdir -p " + selfhostGuestSrcDir +
		" && head -c " + fmt.Sprintf("%d", tarSize) + " > " + guestTarTmp +
		" && tar -xf " + guestTarTmp + " -C " + selfhostGuestSrcDir +
		" && rm " + guestTarTmp

	t.Log("seeding source into guest …")
	seedStart := time.Now()
	seedCtx, seedCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer seedCancel()

	var seedOut, seedErrBuf bytes.Buffer
	exitCode, err := agentClient.Exec(seedCtx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", seedCmd},
		Env:    map[string]string{"HOME": "/root"},
		Stdin:  srcTar,
		Stdout: &seedOut,
		Stderr: &seedErrBuf,
	})
	if err != nil {
		t.Fatalf("seed exec: %v\nstdout: %s\nstderr: %s",
			err, seedOut.String(), seedErrBuf.String())
	}
	if exitCode != 0 {
		t.Fatalf("seed exit code %d\nstdout: %s\nstderr: %s",
			exitCode, seedOut.String(), seedErrBuf.String())
	}
	t.Logf("source seeded in %v", time.Since(seedStart))

	// ── in-workspace environment ───────────────────────────────────────────────
	// PATH is a literal value — agent.Exec passes env as-is (no shell expansion).
	inWorkspaceEnv := map[string]string{
		"PATH":        "/usr/local/go/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME":        "/root",
		"GOPATH":      "/usr/local/gopath",
		"GOMODCACHE":  "/usr/local/gopath/pkg/mod",
		"GOPROXY":     "off",      // offline — proves seeded cache covers all deps
		"GOTOOLCHAIN": "local",    // use the in-image Go; don't re-download
		"CGO_ENABLED": "0",        // static builds throughout
		"GOFLAGS":     "-mod=mod", // allow go.sum update inside the VM
		"GOCACHE":     "/root/.cache/go-build",
	}

	// ── Step 4: go build ./... inside the workspace ───────────────────────────
	t.Log("running go build ./... in workspace (offline, GOPROXY=off) …")
	buildStart := time.Now()
	buildCtx, buildCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer buildCancel()

	var buildOut, buildErrBuf bytes.Buffer
	exitCode, err = agentClient.Exec(buildCtx, agent.ExecOptions{
		Argv:   []string{"/usr/local/go/bin/go", "build", "./..."},
		Env:    inWorkspaceEnv,
		Cwd:    selfhostGuestSrcDir,
		Stdout: &buildOut,
		Stderr: &buildErrBuf,
	})
	buildDur := time.Since(buildStart)
	if err != nil {
		t.Fatalf("go build exec error: %v\nstdout: %s\nstderr: %s",
			err, buildOut.String(), buildErrBuf.String())
	}
	if exitCode != 0 {
		t.Fatalf("go build ./... FAILED (exit %d, elapsed %v)\nstdout: %s\nstderr: %s",
			exitCode, buildDur, buildOut.String(), buildErrBuf.String())
	}
	t.Logf("go build ./... PASSED in %v", buildDur)
	if buildErrBuf.Len() > 0 {
		t.Logf("go build stderr: %s", buildErrBuf.String())
	}

	// ── Step 5: representative go test subset ─────────────────────────────────
	//
	// Fast, pure packages with no external deps and no integration build tags.
	// Prototype-28 measured per-package tests at ~2 s; this subset is bounded.
	// The full suite (-race, integration) is NOT run inside the VM.
	testPkgs := []string{
		"./internal/core/domain/...",
		"./internal/core/agent/wire/...",
		"./internal/core/image/...",
	}
	testArgv := append(
		[]string{"/usr/local/go/bin/go", "test", "-v"},
		testPkgs...,
	)

	t.Logf("running go test subset: %v", testPkgs)
	testStart := time.Now()
	testCtx, testCancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer testCancel()

	var testOut, testErrBuf bytes.Buffer
	exitCode, err = agentClient.Exec(testCtx, agent.ExecOptions{
		Argv:   testArgv,
		Env:    inWorkspaceEnv,
		Cwd:    selfhostGuestSrcDir,
		Stdout: &testOut,
		Stderr: &testErrBuf,
	})
	testDur := time.Since(testStart)
	if err != nil {
		t.Fatalf("go test exec error: %v\nstdout: %s\nstderr: %s",
			err, testOut.String(), testErrBuf.String())
	}
	if exitCode != 0 {
		t.Fatalf("go test subset FAILED (exit %d, elapsed %v)\nstdout: %s\nstderr: %s",
			exitCode, testDur, testOut.String(), testErrBuf.String())
	}
	t.Logf("go test subset PASSED in %v", testDur)
	if testOut.Len() > 0 {
		t.Logf("go test output:\n%s", testOut.String())
	}

	// ── Step 6: assert host repo not mutated ──────────────────────────────────
	//
	// The seed used archive/tar + agent.Exec (read-only access to the host
	// filesystem). The guest runs in a separate ext4 block device. No bind
	// mounts are used. We verify the host go.mod mtime is unchanged as a
	// concrete, observable invariant.
	goModStatAfter, err := os.Stat(goModPath)
	if err != nil {
		t.Fatalf("stat go.mod after test: %v", err)
	}
	if !goModStatAfter.ModTime().Equal(goModStatBefore.ModTime()) {
		t.Errorf("HOST MUTATION DETECTED: go.mod mtime changed %v → %v (seed-by-copy violated)",
			goModStatBefore.ModTime(), goModStatAfter.ModTime())
	}
	t.Log("host-not-mutated PASSED: go.mod mtime unchanged")

	t.Logf("=== SelfHostE2E summary ===")
	t.Logf("  base image:   %s (%.0f MiB)", img.Digest, float64(img.Size)/(1024*1024))
	t.Logf("  source seed:  %.1f MiB tar, elapsed %v", float64(tarSize)/(1024*1024), time.Since(seedStart))
	t.Logf("  go build:     PASSED in %v", buildDur)
	t.Logf("  go test:      PASSED in %v", testDur)
	t.Logf("  host mutated: NO")
}
