//go:build integration

// Package selfhost — S-E2E-SOURCE-FIDELITY end-to-end proof.
//
// nexus3 now captures the host working tree via builder.WorktreeToDiskWithExtra
// (walking the live filesystem) rather than via `git archive HEAD` or an
// in-guest `git clone`.  This test boots a REAL VM with that captured disk and
// verifies six fidelity claims against live guest state:
//
//  1. An uncommitted edit to a tracked file is visible in the guest.
//  2. An untracked file is visible in the guest.
//  3. A .dockerignore'd path is NOT present in the guest.
//  4. node_modules is mounted from a SEPARATE block device (shadow disk), and
//     writes into it succeed.
//  5. The workspace disk is backed by the CORRECT device letter despite shadow
//     disks shifting the virtio-blk assignment.
//  6. The host .dockerignore is byte-identical after the run (host not mutated).
//
// # Failure discipline
//
// Every claim is asserted with hard Errorf/Fatalf — NO assertion is weakened
// to achieve green.  If a claim fails, the test reports the exact symptom and
// the guest serial log.  Infrastructure absence (KVM, CH binary, mke2fs,
// docker) causes t.Skip with an explanatory message.
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run TestWorkspaceSourceFidelity \
//	    ./internal/test/selfhost/ -v -timeout 30m
package selfhost

import (
	"bytes"
	"context"
	"crypto/sha256"
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
	cloudhypervisor "github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// diskBootCmdlineBaseSF is the kernel command line prefix for disk-boot
// sandboxes.  Mirrors the private constant in internal/cli/cmd_sandbox.go so
// this test file does not depend on the CLI package.
const diskBootCmdlineBaseSF = "root=/dev/vda rw init=/sbin/nexus3-agent console=ttyS0"

// sfSockNameLen is "sb-<26chars>.sock" — the length of the socket file that
// the cloud-hypervisor driver writes under SocketDir.  Used to guard against
// the 107-byte Linux AF_UNIX sun_path limit.
const sfSockNameLen = 35

// TestWorkspaceSourceFidelity is the S-E2E-SOURCE-FIDELITY end-to-end proof.
//
// It creates a fixture git worktree, captures it with WorktreeToDiskWithExtra,
// boots a VM with the captured workspace disk and a node_modules shadow disk,
// and asserts all six fidelity claims against live in-guest state.
func TestWorkspaceSourceFidelity(t *testing.T) {
	// ── 1. Infrastructure skip guards ────────────────────────────────────────
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── 2. Build / obtain the self-hosting base image ─────────────────────────
	//
	// We need a VM that runs nexus3-agent as PID 1.  It processes
	// --workspace-mount args from the kernel cmdline before opening the vsock
	// listener so the assertions can use agent.Exec on a workspace-ready guest.
	t.Log("obtaining self-hosting base image (first run ~10 min; subsequent: seconds from Docker cache) …")
	imgCtx, imgCancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer imgCancel()

	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

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

	// ── 3. Create the fixture worktree ────────────────────────────────────────
	//
	// A real git repo so that we can prove the capture includes dirty-tracked
	// and untracked files (both of which `git archive HEAD` would drop).
	fixtureDir := t.TempDir()
	setupFixtureWorktreeSF(t, fixtureDir)

	// Record the .dockerignore content before the test for Claim 6.
	dockerignorePath := filepath.Join(fixtureDir, ".dockerignore")
	dockerignoreContent, err := os.ReadFile(dockerignorePath)
	if err != nil {
		t.Fatalf("read .dockerignore before test: %v", err)
	}
	dockerignoreHashBefore := sha256.Sum256(dockerignoreContent)

	// ── 4. Create the shadow disk (node_modules) ──────────────────────────────
	//
	// Device-letter contract (D-DC-10):
	//   ExtraDisks[0] = node_modules shadow disk → /dev/vdb
	//   ExtraDisks[1] = workspace disk (appended last by CreateAndBoot) → /dev/vdc
	//
	// Using one shadow disk keeps the test self-contained while still catching
	// the device-order regression: if accounting is off by one the workspace
	// would land on /dev/vdb (the empty shadow disk) and Claims 1–3 would fail.
	socketDir, err := os.MkdirTemp("/tmp", "sfidelity-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if len(socketDir)+sfSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir) //nolint:errcheck
		t.Skipf("socket dir path too long for AF_UNIX: %s", socketDir)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) }) //nolint:errcheck

	shadowPath := filepath.Join(socketDir, "node_modules.shadow.ext4")
	if err := sfCreateShadowDisk(t, shadowPath); err != nil {
		t.Fatalf("create node_modules shadow disk: %v", err)
	}
	t.Logf("node_modules shadow disk: %s", shadowPath)

	// ── 5. Build the kernel cmdline with workspace-mount specs ───────────────
	//
	// The Linux kernel passes tokens after "--" directly to PID 1 as os.Args.
	// nexus3-agent (PID 1) parses --workspace-mount=<dev>:<target>:<fs>:<ro>
	// and calls agent.MountWorkspace before opening the vsock listener.
	const guestPath = "/workspace/fixture"
	guestMounts := []agent.GuestMount{
		// ExtraDisks[0] → /dev/vdb  (node_modules shadow)
		{Device: "/dev/vdb", Target: guestPath + "/node_modules", FSType: "ext4"},
		// ExtraDisks[1] → /dev/vdc  (workspace, appended last by CreateAndBoot)
		{Device: "/dev/vdc", Target: guestPath, FSType: "ext4"},
	}
	cmdline := sfWorkspaceMountCmdline(guestMounts)
	t.Logf("kernel cmdline: %s", cmdline)

	// ── 6. Wire the service layer ─────────────────────────────────────────────
	storeRoot := t.TempDir()
	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}

	serialPath := filepath.Join(socketDir, "sfidelity-serial.log")

	// svcDrv drives lifecycle operations (Stop, Remove).
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

	var sandboxID domain.SandboxID
	t.Cleanup(func() {
		// Always log the serial output on failure for diagnosis.
		if content, rerr := os.ReadFile(serialPath); rerr == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== guest serial log (tail 8 KiB) ===\n%s", tail(content, 8192))
		}
		if sandboxID != (domain.SandboxID{}) {
			rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if rerr := svc.Remove(rmCtx, sandboxID.String()); rerr != nil {
				t.Logf("cleanup: svc.Remove: %v", rerr)
			}
		}
	})

	// bootDrv is captured by the factory closure for use with agent.NewClient.
	var bootDrv *cloudhypervisor.CHDriver
	factory := service.DriverFactory(func(resolvedExt4 string, extraDisks []service.ExtraDisk) (driver.Driver, error) {
		chExtra := make([]cloudhypervisor.ExtraDisk, len(extraDisks))
		for i, ed := range extraDisks {
			chExtra[i] = cloudhypervisor.ExtraDisk{Path: ed.Path}
		}
		var newErr error
		bootDrv, newErr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:       chBin,
			SocketDir:        socketDir,
			KernelPath:       kernelPath,
			DiskImagePath:    resolvedExt4,
			SerialOutputPath: serialPath,
			MemoryMiB:        2048,
			VCPUs:            2,
			StartTimeout:     60 * time.Second,
			Cmdline:          cmdline, // wires --workspace-mount specs into PID 1 argv
			ExtraDisks:       chExtra,
		})
		return bootDrv, newErr
	})

	probe := service.ProbeFunc(func(ctx context.Context, _ driver.Driver, id domain.SandboxID) error {
		return realProbeSH(bootDrv)(ctx, nil, id)
	})

	// ── 7. Boot the sandbox with workspace ───────────────────────────────────
	t.Log("creating and booting workspace sandbox …")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer bootCancel()

	sb, err := service.CreateAndBoot(
		bootCtx, svc, cache, factory, probe,
		"sfidelity", fmt.Sprintf("sfidelity-%d", time.Now().UnixNano()),
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(img.Digest)},
			CacheRoot:           cacheRoot,
			ReachabilityTimeout: 90 * time.Second,
			DiskDir:             socketDir,
			// ExtraDisks[0] = shadow disk → /dev/vdb
			ExtraDisks: []service.ExtraDisk{{Path: shadowPath}},
			// Workspace appended at ExtraDisks[1] → /dev/vdc by CreateAndBoot.
			Workspace: &service.WorkspaceSpec{
				SourcePath: fixtureDir,
				GuestPath:  guestPath,
			},
			// Exclude node_modules from the workspace capture image: the shadow
			// disk provides a writable overlay at that path in-guest.
			WorkspaceCapturer: func(ctx context.Context, srcDir, outExt4 string, maxBytes int64) error {
				return builder.WorktreeToDiskWithExtra(ctx, srcDir, outExt4, maxBytes, []string{"node_modules"})
			},
		},
	)
	if err != nil {
		if content, rerr := os.ReadFile(serialPath); rerr == nil {
			t.Logf("=== boot-failure serial log ===\n%s", tail(content, 8192))
		}
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxID = sb.ID
	t.Logf("workspace sandbox booted: id=%s", sb.ID)

	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)
	ac := agent.NewClient(bootDrv, sb.ID)

	// ── 8. In-guest assertions ────────────────────────────────────────────────
	//
	// Each claim is run as an independent Exec so failures are targeted.
	execSF := func(label, script string) (stdout string, exitCode int) {
		t.Helper()
		var out bytes.Buffer
		var errBuf bytes.Buffer
		execCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		code, execErr := ac.Exec(execCtx, agent.ExecOptions{
			Argv:   []string{"/bin/sh", "-c", script},
			Env:    map[string]string{"PATH": "/usr/local/bin:/usr/bin:/bin:/sbin:/usr/sbin"},
			Stdout: &out,
			Stderr: &errBuf,
		})
		if execErr != nil {
			t.Errorf("[%s] transport error: %v", label, execErr)
		}
		if errBuf.Len() > 0 {
			t.Logf("[%s] stderr: %s", label, strings.TrimSpace(errBuf.String()))
		}
		return strings.TrimSpace(out.String()), int(code)
	}

	// ── Claim 1: uncommitted edit to tracked file is visible ─────────────────
	t.Log("Claim 1: uncommitted edit to tracked file visible in guest …")
	out1, code1 := execSF("claim1", fmt.Sprintf("cat %s/tracked.go", guestPath))
	if code1 != 0 {
		t.Errorf("CLAIM 1 FAIL: cat tracked.go exited %d", code1)
	} else if !strings.Contains(out1, "DIRTY_EDIT") {
		t.Errorf("CLAIM 1 FAIL: tracked.go does not contain DIRTY_EDIT\nactual content:\n%s", out1)
	} else {
		t.Logf("CLAIM 1 PROVEN: tracked.go contains DIRTY_EDIT — uncommitted edit captured")
	}

	// ── Claim 2: untracked file is visible ────────────────────────────────────
	t.Log("Claim 2: untracked file visible in guest …")
	out2, code2 := execSF("claim2", fmt.Sprintf("cat %s/untracked.txt", guestPath))
	if code2 != 0 {
		t.Errorf("CLAIM 2 FAIL: cat untracked.txt exited %d", code2)
	} else if !strings.Contains(out2, "UNTRACKED_FILE_SENTINEL") {
		t.Errorf("CLAIM 2 FAIL: untracked.txt missing UNTRACKED_FILE_SENTINEL\nactual:\n%s", out2)
	} else {
		t.Logf("CLAIM 2 PROVEN: untracked.txt visible in guest — untracked file captured")
	}

	// ── Claim 3: dockerignored path is absent ─────────────────────────────────
	t.Log("Claim 3: dockerignored path absent in guest …")
	out3, _ := execSF("claim3", fmt.Sprintf("[ -f %s/secret.txt ] && echo PRESENT || echo ABSENT", guestPath))
	if out3 != "ABSENT" {
		t.Errorf("CLAIM 3 FAIL: secret.txt is %q in guest — .dockerignore exclusion not honoured", out3)
	} else {
		t.Logf("CLAIM 3 PROVEN: secret.txt absent in guest — .dockerignore exclusion honoured")
	}

	// ── Claim 4: node_modules is a separate mounted filesystem ───────────────
	t.Log("Claim 4: node_modules on a separate block device + writes succeed …")

	// 4a. Device numbers must differ between workspace root and node_modules.
	out4dev, code4dev := execSF("claim4-devnums",
		fmt.Sprintf("stat -c '%%d' %s; stat -c '%%d' %s/node_modules", guestPath, guestPath))
	if code4dev != 0 {
		t.Errorf("CLAIM 4 FAIL: stat device numbers exited %d", code4dev)
	} else {
		devLines := strings.Split(out4dev, "\n")
		if len(devLines) < 2 {
			t.Errorf("CLAIM 4 FAIL: stat output has fewer than 2 lines: %q", out4dev)
		} else if devLines[0] == devLines[1] {
			t.Errorf("CLAIM 4 FAIL: workspace and node_modules share device number %s — NOT on a separate disk", devLines[0])
		} else {
			t.Logf("CLAIM 4a PROVEN: workspace dev=%s, node_modules dev=%s — separate filesystems", devLines[0], devLines[1])
		}
	}

	// 4b. node_modules appears as a separate mount in /proc/mounts.
	out4mnt, _ := execSF("claim4-mounts",
		fmt.Sprintf("grep -c '%s/node_modules' /proc/mounts", guestPath))
	if out4mnt == "" || out4mnt == "0" {
		t.Errorf("CLAIM 4 FAIL: %s/node_modules not found as a mount in /proc/mounts", guestPath)
	} else {
		t.Logf("CLAIM 4b PROVEN: node_modules has its own mount entry (%s match(es) in /proc/mounts)", out4mnt)
	}

	// 4c. Write into node_modules succeeds and is readable back.
	_, code4w := execSF("claim4-write",
		fmt.Sprintf("echo 'SHADOW_WRITE_SENTINEL' > %s/node_modules/shadow-test.txt", guestPath))
	if code4w != 0 {
		t.Errorf("CLAIM 4 FAIL: write into node_modules exited %d", code4w)
	} else {
		out4r, _ := execSF("claim4-read",
			fmt.Sprintf("cat %s/node_modules/shadow-test.txt", guestPath))
		if !strings.Contains(out4r, "SHADOW_WRITE_SENTINEL") {
			t.Errorf("CLAIM 4 FAIL: written sentinel not readable from node_modules; got: %q", out4r)
		} else {
			t.Logf("CLAIM 4c PROVEN: write+read into node_modules shadow disk succeeded")
		}
	}

	// ── Claim 5: workspace is backed by the correct device ───────────────────
	//
	// With 1 shadow disk (node_modules at ExtraDisks[0] → /dev/vdb), the
	// workspace is appended at ExtraDisks[1] → /dev/vdc.
	// A device-order regression would make the workspace land on /dev/vdb (the
	// empty shadow disk), and Claims 1–3 would already have failed — but this
	// assertion makes the regression mode explicit and directly observable.
	t.Log("Claim 5: workspace backed by /dev/vdc (correct device despite shadow shifting) …")
	out5, code5 := execSF("claim5",
		fmt.Sprintf("grep ' %s ' /proc/mounts | awk '{print $1}'", guestPath))
	const wantWorkspaceDev = "/dev/vdc"
	if code5 != 0 {
		t.Errorf("CLAIM 5 FAIL: grep /proc/mounts for workspace exited %d", code5)
	} else if out5 != wantWorkspaceDev {
		t.Errorf("CLAIM 5 FAIL: workspace %s is on %q, want %q — device-order regression",
			guestPath, out5, wantWorkspaceDev)
	} else {
		t.Logf("CLAIM 5 PROVEN: workspace at %s is on %s (1 shadow+workspace → vdb,vdc)", guestPath, out5)
	}

	// ── Claim 6: host .dockerignore byte-identical after run ─────────────────
	//
	// WorktreeToDiskWithExtra copies by filesystem walk (not bind mount).
	// No byte of the host .dockerignore should change.
	t.Log("Claim 6: host .dockerignore byte-identical after run …")
	dockerignoreAfter, readErr := os.ReadFile(dockerignorePath)
	if readErr != nil {
		t.Errorf("CLAIM 6 FAIL: cannot read .dockerignore after run: %v", readErr)
	} else {
		hashAfter := sha256.Sum256(dockerignoreAfter)
		if hashAfter != dockerignoreHashBefore {
			t.Errorf("CLAIM 6 FAIL: .dockerignore sha256 changed\nbefore: %x\nafter:  %x",
				dockerignoreHashBefore, hashAfter)
		} else {
			t.Logf("CLAIM 6 PROVEN: host .dockerignore sha256=%x unchanged", hashAfter)
		}
	}
}

// ── fixture helpers ───────────────────────────────────────────────────────────

// setupFixtureWorktreeSF initialises a git repository in dir and populates
// the four hazard types that the old ingestion paths lost:
//
//	(i)  tracked file with uncommitted edit  → tracked.go (dirty after first commit)
//	(ii) untracked file                      → untracked.txt (never git-added)
//	     .dockerignore exclusion             → secret.txt excluded
//	     node_modules on shadow disk         → node_modules/ present on host but
//	                                           excluded from workspace capture
func setupFixtureWorktreeSF(t *testing.T, dir string) {
	t.Helper()
	gitEnv := append(os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"HOME=/tmp",
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = gitEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("fixture setup %v: %v\n%s", args, err, string(out))
		}
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("fixture write %s: %v", name, err)
		}
	}
	mkdir := func(name string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatalf("fixture mkdir %s: %v", name, err)
		}
	}

	// Initialise git repo and set identity.
	run("git", "init", "-q")
	run("git", "config", "user.email", "test@test.com")
	run("git", "config", "user.name", "Test")

	// Create the initial commit: tracked.go at ORIGINAL content + .dockerignore.
	write("tracked.go", "package main\n\n// ORIGINAL_CONTENT\nfunc main() {}\n")
	write(".dockerignore", "secret.txt\n")
	run("git", "add", "tracked.go", ".dockerignore")
	run("git", "commit", "-m", "initial commit")

	// (i) Dirty the tracked file — git archive HEAD would give ORIGINAL_CONTENT.
	//     WorktreeToDiskWithExtra must give DIRTY_EDIT.
	write("tracked.go", "package main\n\n// DIRTY_EDIT — uncommitted modification\nfunc main() {}\n")

	// (ii) Untracked file — never git-added; git clone from remote omits it.
	write("untracked.txt", "UNTRACKED_FILE_SENTINEL\n")

	// dockerignore'd file — WorktreeToDiskWithExtra must exclude it.
	write("secret.txt", "this must not reach the guest\n")

	// node_modules present on the host — the shadow-disk capturer excludes it
	// from the workspace ext4; the guest gets the shadow disk at that path instead.
	mkdir("node_modules/lodash")
	write("node_modules/lodash/index.js", "// lodash stub\nmodule.exports = {};\n")
}

// sfCreateShadowDisk creates a sparse 1 GiB ext4 image at path.
func sfCreateShadowDisk(t *testing.T, path string) error {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	if err := f.Truncate(1 * 1024 * 1024 * 1024); err != nil {
		_ = f.Close()
		return fmt.Errorf("truncate: %w", err)
	}
	_ = f.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "mke2fs",
		"-t", "ext4",
		"-F",                                    // force (no interactive confirmation)
		"-E", "lazy_itable_init=0,lazy_journal_init=0", // fully initialise inode table
		path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mke2fs: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// sfWorkspaceMountCmdline formats the kernel command line that wires workspace
// and shadow mounts into the guest agent's argv.
//
// Format: <base> -- --workspace-mount=<dev>:<target>:<fs>:<ro> …
//
// Mirrors the private workspaceMountCmdline in internal/cli/cmd_sandbox.go.
func sfWorkspaceMountCmdline(mounts []agent.GuestMount) string {
	b := diskBootCmdlineBaseSF + " --"
	for _, m := range mounts {
		ro := "false"
		if m.ReadOnly {
			ro = "true"
		}
		b += fmt.Sprintf(" --workspace-mount=%s:%s:%s:%s", m.Device, m.Target, m.FSType, ro)
	}
	return b
}
