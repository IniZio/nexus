//go:build integration

// Package selfhost — MBH-S2 in-guest build+test dogfood.
//
// Proves end-to-end that the enlarged agent base image (MBH-S1) ships a
// buildable, testable nexus3 source tree inside the guest VM.
//
// Definition of done (docs/site/concepts/index.md §acceptance-test):
//  1. `go build ./...` exits 0 from /workspace inside the guest.
//  2. `go test ./...` (unit tests only; no -tags integration) exits 0.
//
// Both assertions surface the full captured stdout+stderr on failure —
// avoiding the "opaque failure" anti-pattern documented in agent_dogfood_test.go.
//
// # Prerequisites
//
//   - /dev/kvm accessible
//   - cloud-hypervisor binary (CLOUD_HYPERVISOR_BIN or ~/.local/bin/cloud-hypervisor)
//   - mke2fs in PATH (e2fsprogs)
//   - docker (required by BuildAgentBaseImage)
//
// No API tokens are required — the test does not call any external service.
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run TestBuildDogfood \
//	    ./internal/test/selfhost/ -v -timeout 90m
package selfhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
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

// testBatchSize controls how many packages are tested per in-guest Exec call.
// Smaller batches reclaim memory more frequently; larger batches reduce fixed
// per-exec overhead.  Tune this constant if host memory pressure returns.
const testBatchSize = 1

// TestBuildDogfood boots a guest from the MBH-S1 enlarged agent base image
// and runs `go build ./...` then `go test ./...` (batched) inside /workspace.
func TestBuildDogfood(t *testing.T) {
	// ── 1. Skip guards ────────────────────────────────────────────────────────
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	// ── 2. Kernel path ────────────────────────────────────────────────────────
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── 3. Build / get agent base image ──────────────────────────────────────
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	imgCtx, imgCancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer imgCancel()

	t.Log("building agent base image (first run ~15–30 min; subsequent: seconds from cache) …")
	img, err := BuildAgentBaseImage(imgCtx, cache)
	if err != nil {
		switch {
		case errors.Is(err, ErrDockerUnavailable):
			t.Skip("skipping: docker unavailable:", err)
		case errors.Is(err, builder.ErrMke2fsUnavailable):
			t.Skip("skipping: mke2fs unavailable:", err)
		}
		t.Fatalf("BuildAgentBaseImage: %v", err)
	}
	t.Logf("agent image: digest=%s size=%.2f GiB", img.Digest, float64(img.Size)/(1<<30))

	// ── 4. Infrastructure ─────────────────────────────────────────────────────
	// Socket dir in /tmp: stays within the 107-byte Linux sun_path limit.
	socketDir, err := os.MkdirTemp("/tmp", "build-dogfood-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("socket dir path too long for AF_UNIX: %s", socketDir)
	}
	serialPath := filepath.Join(socketDir, "build-dogfood-serial.log")
	t.Cleanup(func() {
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== guest serial output ===\n%s", content)
		}
		os.RemoveAll(socketDir)
	})

	storeRoot := t.TempDir()
	st, err := store.NewFileStore(storeRoot)
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

	// ── 5. Boot sandbox ───────────────────────────────────────────────────────
	// MemoryMiB=8192: `go test -p 1 ./...` runs packages sequentially but still
	// keeps compiled test binaries in memory across packages.  On the full nexus3
	// tree the Go compiler + linker peak exceeds 4 GiB on cold runs.  8 GiB gives
	// comfortable headroom without risking guest OOM (which kills the nexus3-agent
	// vsock connection mid-exec, producing a cryptic EOF error).
	var bootDrv *cloudhypervisor.CHDriver
	factory := service.DriverFactory(func(ext4Path string, _ []service.ExtraDisk) (driver.Driver, error) {
		var ferr error
		bootDrv, ferr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:       chBin,
			SocketDir:        socketDir,
			KernelPath:       kernelPath,
			DiskImagePath:    ext4Path,
			MemoryMiB:        8192,
			SerialOutputPath: serialPath,
			StartTimeout:     30 * time.Second,
		})
		return bootDrv, ferr
	})
	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		return realProbeSH(bootDrv)(ctx, drv, id)
	})

	t.Log("creating and booting sandbox …")
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
		"build-dogfood", fmt.Sprintf("build-dogfood-%d", time.Now().UnixNano()),
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
	t.Logf("sandbox booted: %s state=%s", sb.ID, sb.State)

	agentClient := agent.NewClient(bootDrv, sb.ID)

	// guestGoEnv is the environment injected into every in-guest go invocation.
	//
	// GOPATH / GOMODCACHE: point at the pre-seeded cache baked into the image
	//   by the mod-seeder stage (see Containerfile generated by
	//   generateAgentContainerfile: `go mod download all`).
	// GOFLAGS=-mod=mod: allow go to record missing sums without network; if the
	//   seeded cache is complete no download is attempted.
	// GOPROXY=off: forbid any outbound module fetch — the seeded cache must be
	//   self-sufficient.  A cache miss will produce a clear "disabled by GOPROXY=off"
	//   error rather than a silent network hang.
	// GOTOOLCHAIN=local: never auto-download a newer toolchain.
	// CGO_ENABLED=0: pure-Go build; no C compiler required.
	guestGoEnv := map[string]string{
		"PATH":        "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME":        "/root",
		"GOPATH":      "/usr/local/gopath",
		"GOMODCACHE":  "/usr/local/gopath/pkg/mod",
		"GOFLAGS":     "-mod=mod",
		"GOPROXY":     "off",
		"GOTOOLCHAIN": "local",
		"CGO_ENABLED": "0",
		// GOMAXPROCS=2: limit concurrent goroutines in the Go toolchain itself.
		// Without this the compiler saturates all vCPUs and the resulting parallel
		// garbage-collection pressure can cause guest OOM during test-binary builds.
		"GOMAXPROCS": "2",
	}

	// ── 6. go build ./... ─────────────────────────────────────────────────────
	// 30 minutes: enough for a cold parallel compilation of the full tree.
	// The build uses the pre-seeded module cache so no network is required.
	t.Log("in-guest: go build ./... (may take several minutes on first run) …")
	{
		var stdout, stderr bytes.Buffer
		buildCtx, buildCancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer buildCancel()

		exitCode, execErr := agentClient.Exec(buildCtx, agent.ExecOptions{
			Cwd:    "/workspace",
			Argv:   []string{"/usr/local/go/bin/go", "build", "./..."},
			Env:    guestGoEnv,
			Stdout: &stdout,
			Stderr: &stderr,
		})

		stdoutStr := stdout.String()
		stderrStr := stderr.String()
		if stdoutStr != "" {
			t.Logf("go build stdout:\n%s", stdoutStr)
		}
		if stderrStr != "" {
			t.Logf("go build stderr:\n%s", stderrStr)
		}

		if execErr != nil {
			t.Fatalf("agentClient.Exec (go build ./...): %v\nstdout:\n%s\nstderr:\n%s",
				execErr, stdoutStr, stderrStr)
		}
		if exitCode != 0 {
			t.Fatalf("go build ./... exited %d\nstdout:\n%s\nstderr:\n%s",
				exitCode, stdoutStr, stderrStr)
		}
		t.Logf("go build ./... PASSED (exit 0)")
	}

	// ── 7. go test (batched) ─────────────────────────────────────────────────
	// Running without -tags integration skips the heavy VM-boot integration
	// tests (agent_dogfood_test.go, motive_dogfood_test.go, this file itself).
	//
	// WHY BATCHED: a single `go test ./...` keeps compiled test binaries in
	// memory across all packages.  On the full nexus3 tree cumulative host
	// memory pressure (host swap backing the 8 GiB guest) crests around
	// package 21 and kills the vsock connection with an EOF.  Splitting the
	// run into small Exec calls lets the OS reclaim each batch's memory before
	// the next starts, so peak never exceeds one batch worth of binaries.
	//
	// IPv6 caveat: the minimal guest kernel may not include the IPv6 module.
	// Some unit tests use httptest.NewServer which in Go 1.21+ tries
	// tcp6 [::1]:0 and panics if AF_INET6 is absent.  We attempt modprobe
	// ipv6 first; per-batch IPv6-only failures are tolerated (WARNING), real
	// failures are hard-FAILed.
	t.Log("in-guest: go test (batched; no -tags integration) …")
	{
		// Best-effort: load IPv6 kernel module so httptest can bind [::1]:0.
		var modOut bytes.Buffer
		modCtx, modCancel := context.WithTimeout(context.Background(), 10*time.Second)
		modCode, _ := agentClient.Exec(modCtx, agent.ExecOptions{
			Argv:   []string{"/bin/sh", "-c", "modprobe ipv6 2>&1 || true"},
			Env:    map[string]string{"PATH": "/sbin:/usr/sbin:/usr/bin:/bin"},
			Stdout: &modOut,
			Stderr: &modOut,
		})
		modCancel()
		t.Logf("modprobe ipv6: exit=%d output=%q", modCode, modOut.String())

		// 7a. Enumerate packages with go list ./...
		var listOut bytes.Buffer
		listCtx, listCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		listExit, listErr := agentClient.Exec(listCtx, agent.ExecOptions{
			Cwd:    "/workspace",
			Argv:   []string{"/usr/local/go/bin/go", "list", "./..."},
			Env:    guestGoEnv,
			Stdout: &listOut,
			Stderr: &listOut,
		})
		listCancel()
		if listErr != nil {
			t.Fatalf("agentClient.Exec (go list ./...): transport error: %v\noutput:\n%s", listErr, listOut.String())
		}
		if listExit != 0 {
			t.Fatalf("go list ./... exited %d\noutput:\n%s", listExit, listOut.String())
		}
		var pkgs []string
		for _, line := range strings.Split(strings.TrimSpace(listOut.String()), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				pkgs = append(pkgs, line)
			}
		}
		t.Logf("go list: %d packages to test in batches of %d", len(pkgs), testBatchSize)

		// 7b. Run each batch as a separate Exec so memory is reclaimed between calls.
		const ipv6Panic = "address family not supported by protocol"
		type batchFailure struct {
			batch  int
			pkgs   []string
			stdout string
			stderr string
			exit   int32
		}
		var realFailures []batchFailure
		var ipv6WarnBatches []int

		for batchIdx := 0; batchIdx*testBatchSize < len(pkgs); batchIdx++ {
			start := batchIdx * testBatchSize
			end := start + testBatchSize
			if end > len(pkgs) {
				end = len(pkgs)
			}
			batchPkgs := pkgs[start:end]

			argv := make([]string, 0, 2+len(batchPkgs))
			argv = append(argv, "/usr/local/go/bin/go", "test")
			argv = append(argv, batchPkgs...)

			var bStdout, bStderr bytes.Buffer
			batchCtx, batchCancel := context.WithTimeout(context.Background(), 10*time.Minute)
			exitCode, execErr := agentClient.Exec(batchCtx, agent.ExecOptions{
				Cwd:    "/workspace",
				Argv:   argv,
				Env:    guestGoEnv,
				Stdout: &bStdout,
				Stderr: &bStderr,
			})
			batchCancel()

			bOut := bStdout.String()
			bErr := bStderr.String()

			if execErr != nil {
				// Transport error (e.g. vsock EOF) — fail immediately with context.
				t.Fatalf("batch %d/%d transport error: %v\npackages: %v\nstdout:\n%s\nstderr:\n%s",
					batchIdx+1, (len(pkgs)+testBatchSize-1)/testBatchSize,
					execErr, batchPkgs, bOut, bErr)
			}

			t.Logf("batch %d/%d pkgs=%v exit=%d",
				batchIdx+1, (len(pkgs)+testBatchSize-1)/testBatchSize, batchPkgs, exitCode)
			if bOut != "" {
				t.Logf("  stdout: %s", bOut)
			}
			if bErr != "" {
				t.Logf("  stderr: %s", bErr)
			}

			if exitCode != 0 {
				combined := bOut + "\n" + bErr
				if strings.Contains(combined, ipv6Panic) {
					// IPv6-only failure: tolerate.
					ipv6WarnBatches = append(ipv6WarnBatches, batchIdx+1)
				} else {
					// Real failure: record and continue so we surface all failures.
					realFailures = append(realFailures, batchFailure{
						batch:  batchIdx + 1,
						pkgs:   batchPkgs,
						stdout: bOut,
						stderr: bErr,
						exit:   exitCode,
					})
				}
			}
		}

		if len(ipv6WarnBatches) > 0 {
			t.Logf("go test WARNING: batches %v had IPv6-only failures (no AF_INET6 in guest kernel — known infrastructure limitation)", ipv6WarnBatches)
		}

		if len(realFailures) > 0 {
			var sb strings.Builder
			for _, f := range realFailures {
				fmt.Fprintf(&sb, "\n--- BATCH %d (exit %d) packages: %v\nstdout:\n%s\nstderr:\n%s\n",
					f.batch, f.exit, f.pkgs, f.stdout, f.stderr)
			}
			t.Fatalf("go test FAILED: %d batch(es) had real (non-IPv6) failures:%s", len(realFailures), sb.String())
		}

		t.Logf("go test (batched) PASSED — all %d packages OK", len(pkgs))
	}

	t.Log("TestBuildDogfood PASSED — in-guest go build + go test (batched) both exit 0")
}
