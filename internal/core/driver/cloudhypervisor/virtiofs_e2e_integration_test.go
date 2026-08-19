//go:build integration

package cloudhypervisor

// virtiofs_e2e_integration_test.go — live proof of D-PD-53 virtiofs mounts
// in a real KVM microVM.
//
// # What this tests
//
// Boots a microVM with nexus3-agent as init and two live virtiofs mounts:
//
//   - /mnt/rw  read-write, contains a .git directory (D-PD-99)
//   - /mnt/ro  read-only
//
// Acceptance items (one sub-test per item; sub-test order matters — AC-5 is
// last because it tears down the VM):
//
//  1. Boot succeeds; guest sees host directory contents.
//  2. Bidirectional I/O: host→guest and guest→host writes both propagate.
//  3. :ro is genuinely read-only — guest write is rejected with non-zero exit.
//  4. Fork and Snapshot refuse at RUNTIME through the real service code path.
//  5. After sandbox removal, no virtiofsd process remains and socket files are
//     unlinked.
//  6. A host directory that CONTAINS a .git directory mounts and is readable
//     in-guest (D-PD-99 — deliberate divergence from --mount-named behaviour).
//
// # Guard conditions
//
//   /dev/kvm, cloud-hypervisor binary, vmlinux-x86_64 artifact,
//   alpine-initramfs.cpio.gz artifact, gzip, cpio,
//   $HOME/.local/bin/virtiofsd (version 1.13.3)
//
// # Running
//
//	bash scripts/fetch-boot-artifacts.sh
//	TMPDIR=/tmp go test -tags integration -run TestLiveVirtiofsE2E \
//	    ./internal/core/driver/cloudhypervisor/ -v -timeout 600s

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	driverfake "github.com/IniZio/nexus3/internal/core/driver/fake"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

const e2eVirtiofsdBin = "$HOME/.local/bin/virtiofsd"

// TestLiveVirtiofsE2E is the live end-to-end proof for D-PD-53 virtiofs mounts.
func TestLiveVirtiofsE2E(t *testing.T) {
	// ── guard conditions ────────────────────────────────────────────────────────
	skipUnlessKVM(t)
	chBin := skipUnlessCHBin(t)
	kernelPath := skipUnlessArtifact(t, "vmlinux-x86_64")
	baseInitramfs := skipUnlessArtifact(t, "alpine-initramfs.cpio.gz")
	skipUnlessTool(t, "cpio")
	skipUnlessTool(t, "gzip")
	if _, err := os.Stat(e2eVirtiofsdBin); err != nil {
		t.Skipf("virtiofsd not found at %s — install virtiofsd 1.x first", e2eVirtiofsdBin)
	}

	// ── build nexus3-agent initramfs ────────────────────────────────────────────
	agentBin := buildNexus3Agent(t)
	initramfsPath := buildAgentInitramfs(t, agentBin, baseInitramfs)

	// ── host share directories ──────────────────────────────────────────────────
	// rwShare: read-write; also contains a .git directory (AC-6 / D-PD-99).
	rwShare, err := os.MkdirTemp("/tmp", "nx3fs-rw-")
	if err != nil {
		t.Fatalf("MkdirTemp rwShare: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(rwShare) })

	if err := os.WriteFile(filepath.Join(rwShare, "host-seed.txt"), []byte("from-host"), 0o644); err != nil {
		t.Fatalf("write host-seed.txt: %v", err)
	}
	// .git directory inside the rw share (D-PD-99): live mounts must NOT reject
	// host paths that contain .git (contrast with --mount-named behaviour).
	if err := os.Mkdir(filepath.Join(rwShare, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rwShare, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatalf("write .git/HEAD: %v", err)
	}

	// roShare: read-only share.
	roShare, err := os.MkdirTemp("/tmp", "nx3fs-ro-")
	if err != nil {
		t.Fatalf("MkdirTemp roShare: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(roShare) })
	if err := os.WriteFile(filepath.Join(roShare, "ro-seed.txt"), []byte("ro-content"), 0o644); err != nil {
		t.Fatalf("write ro-seed.txt: %v", err)
	}

	// ── driver setup ────────────────────────────────────────────────────────────
	socketDir, err := os.MkdirTemp("/tmp", "ch-virtiofs-e2e-")
	if err != nil {
		t.Fatalf("MkdirTemp socketDir: %v", err)
	}
	if len(socketDir)+35 > maxSocketPathLen {
		os.RemoveAll(socketDir)
		t.Skipf("socket dir too long for AF_UNIX sun_path: %s", socketDir)
	}

	serialPath := filepath.Join(socketDir, "serial.log")

	liveMounts := []domain.LiveMount{
		{HostPath: rwShare, GuestPath: "/mnt/rw", ReadOnly: false}, // virtiofs tag: nx3fs0
		{HostPath: roShare, GuestPath: "/mnt/ro", ReadOnly: true},  // virtiofs tag: nx3fs1
	}

	// Kernel cmdline: kernel params, " --" PID-1 boundary, then
	// --workspace-mount args consumed by nexus3-agent from os.Args.
	// Format matches workspaceMountCmdline (cmd_sandbox.go:603):
	//   --workspace-mount=<tag>:<guestPath>:<fstype>:<ro>:<isWorkspace>
	cmdline := "console=ttyS0 panic=1 init=/init" +
		" -- --workspace-mount=nx3fs0:/mnt/rw:virtiofs:false:false" +
		" --workspace-mount=nx3fs1:/mnt/ro:virtiofs:true:false"

	drv, err := New(Config{
		BinaryPath:       chBin,
		SocketDir:        socketDir,
		KernelPath:       kernelPath,
		InitramfsPath:    initramfsPath,
		Cmdline:          cmdline,
		SerialOutputPath: serialPath,
		VCPUs:            1,
		MemoryMiB:        512,
		StartTimeout:     40 * time.Second,
		VirtiofsdPath:    e2eVirtiofsdBin,
		LiveMounts:       liveMounts,
	})
	if err != nil {
		os.RemoveAll(socketDir)
		t.Fatalf("New CHDriver: %v", err)
	}

	id := domain.NewSandboxID()
	t.Logf("sandbox ID: %s", id)

	var vmmPID int
	t.Cleanup(func() {
		// Always print serial log so boot failures are diagnosable.
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 {
			t.Logf("guest serial log (%s):\n%s", serialPath, string(content))
		}
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = drv.Stop(stopCtx, id)
		if vmmPID != 0 {
			_ = syscall.Kill(-vmmPID, syscall.SIGKILL)
		}
		drv.clearState(id) // idempotent: no-op if AC-5 already cleared
		os.RemoveAll(socketDir)
	})

	// ── boot ────────────────────────────────────────────────────────────────────
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer bootCancel()

	if _, err := drv.Start(bootCtx, driver.StartRequest{SandboxID: id}); err != nil {
		t.Fatalf("drv.Start: %v", err)
	}

	drv.mu.Lock()
	if proc := drv.procs[id]; proc != nil {
		vmmPID = proc.pid
	}
	drv.mu.Unlock()

	// Give nexus3-agent time to mount virtiofs shares and bind vsock listeners.
	// Virtiofs mounts happen before vsock listeners bind (fatal if they fail),
	// so a successful agent.Exec confirms mounts succeeded.
	time.Sleep(3 * time.Second)

	c := agent.NewClient(drv, id)

	// guestExec runs argv in-guest and returns (combined stdout+stderr, exitCode).
	// Calls t.Fatal on transport or exec protocol errors; non-zero exit is normal.
	guestExec := func(t *testing.T, timeout time.Duration, argv ...string) (string, int32) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		var stdout, stderr bytes.Buffer
		code, err := c.Exec(ctx, agent.ExecOptions{
			Argv:   argv,
			Stdout: &stdout,
			Stderr: &stderr,
		})
		if err != nil {
			t.Fatalf("guest exec %v: %v\nstderr: %s", argv, err, stderr.String())
		}
		return stdout.String() + stderr.String(), code
	}

	// ── AC-1: boot + guest reads host-seeded file ──────────────────────────────
	t.Run("AC1_guest_reads_host_file", func(t *testing.T) {
		out, code := guestExec(t, 20*time.Second, "/bin/cat", "/mnt/rw/host-seed.txt")
		if code != 0 {
			t.Fatalf("cat host-seed.txt: exit %d\noutput: %q", code, out)
		}
		if !strings.Contains(out, "from-host") {
			t.Fatalf("expected 'from-host' in output; got %q", out)
		}
		t.Logf("AC-1 PASS: guest read host-seed.txt → %q", strings.TrimSpace(out))
	})

	// ── AC-2a: host writes file → guest reads it immediately ───────────────────
	t.Run("AC2a_host_to_guest", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(rwShare, "h2g.txt"), []byte("host-to-guest-content"), 0o644); err != nil {
			t.Fatalf("host write h2g.txt: %v", err)
		}
		out, code := guestExec(t, 15*time.Second, "/bin/cat", "/mnt/rw/h2g.txt")
		if code != 0 {
			t.Fatalf("cat h2g.txt in guest: exit %d\noutput: %q", code, out)
		}
		if !strings.Contains(out, "host-to-guest-content") {
			t.Fatalf("expected 'host-to-guest-content'; got %q", out)
		}
		t.Logf("AC-2a PASS: host→guest: %q", strings.TrimSpace(out))
	})

	// ── AC-2b: guest writes file → host reads it ───────────────────────────────
	t.Run("AC2b_guest_to_host", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var stdout, stderr bytes.Buffer
		code, err := c.Exec(ctx, agent.ExecOptions{
			Argv:   []string{"/bin/sh", "-c", "printf 'guest-to-host-content' > /mnt/rw/g2h.txt"},
			Stdout: &stdout,
			Stderr: &stderr,
		})
		if err != nil {
			t.Fatalf("guest write g2h.txt: %v\nstderr: %s", err, stderr.String())
		}
		if code != 0 {
			t.Fatalf("guest write g2h.txt: exit %d\nstderr: %s", code, stderr.String())
		}
		data, err := os.ReadFile(filepath.Join(rwShare, "g2h.txt"))
		if err != nil {
			t.Fatalf("host read g2h.txt after guest write: %v", err)
		}
		if !strings.Contains(string(data), "guest-to-host-content") {
			t.Fatalf("expected 'guest-to-host-content' on host; got %q", string(data))
		}
		t.Logf("AC-2b PASS: guest→host: %q", strings.TrimSpace(string(data)))
	})

	// ── AC-3: :ro mount genuinely rejects guest writes ─────────────────────────
	t.Run("AC3_readonly_rejects_write", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var stdout, stderr bytes.Buffer
		// The shell redirect ">" must fail with EROFS. Redirect stderr to stdout
		// (2>&1) so the "Read-only file system" message from the shell is captured
		// in stdout — the guest exec surface may not wire a separate stderr pipe.
		code, err := c.Exec(ctx, agent.ExecOptions{
			Argv:   []string{"/bin/sh", "-c", "printf 'nope' > /mnt/ro/denied.txt 2>&1"},
			Stdout: &stdout,
			Stderr: &stderr,
		})
		if err != nil {
			t.Fatalf("guest write-to-ro exec error: %v", err)
		}
		combined := stdout.String() + stderr.String()
		if code == 0 {
			t.Fatalf("expected non-zero exit for write to :ro mount; exit was 0 — write was NOT rejected\noutput: %s",
				combined)
		}
		// Prove rejection was EROFS, not an unrelated failure.
		if !strings.Contains(strings.ToLower(combined), "read-only") {
			t.Errorf("AC-3: expected 'read-only file system' in guest output; got %q — exit %d is not EROFS", combined, code)
		}
		// Belt-and-suspenders: verify the file was NOT created on the host.
		if _, statErr := os.Stat(filepath.Join(roShare, "denied.txt")); !os.IsNotExist(statErr) {
			t.Errorf("denied.txt appeared in ro share on host — kernel did not enforce MS_RDONLY")
		}
		t.Logf("AC-3 PASS: exit %d, output: %q", code, strings.TrimSpace(combined))
	})

	// ── AC-6: .git directory inside a live mount is accessible (D-PD-99) ───────
	// D-PD-99: --mount deliberately does NOT reject host paths containing .git.
	// Prove it here with a real VM read.
	t.Run("AC6_git_dir_accessible", func(t *testing.T) {
		out, code := guestExec(t, 15*time.Second, "/bin/cat", "/mnt/rw/.git/HEAD")
		if code != 0 {
			t.Fatalf("cat .git/HEAD: exit %d\noutput: %q", code, out)
		}
		if !strings.Contains(out, "refs/heads/main") {
			t.Fatalf("expected 'refs/heads/main' in .git/HEAD; got %q", out)
		}
		t.Logf("AC-6 PASS: .git/HEAD readable in guest: %q", strings.TrimSpace(out))
	})

	// ── AC-4: Fork and Snapshot refuse at runtime via real service path ─────────
	// Set up a minimal service backed by a real filestore. Insert a sandbox
	// record with LiveMounts set and Running state, then call Fork and Snapshot.
	// Both must return an error mentioning "live" before touching the driver.
	t.Run("AC4_fork_snapshot_refuse", func(t *testing.T) {
		storeDir := filepath.Join(socketDir, "svc-store")
		if err := os.MkdirAll(storeDir, 0o700); err != nil {
			t.Fatalf("mkdir storeDir: %v", err)
		}
		st, err := store.NewFileStore(storeDir)
		if err != nil {
			t.Fatalf("store.NewFileStore: %v", err)
		}
		// FakeDriver implements driver.Snapshotter (needed for the Snapshot path
		// to reach the LiveMounts guard inside store.Update).
		fakeDrv := driverfake.New()
		svc := service.New(st, fakeDrv, lifecycle.New())

		svcID := domain.NewSandboxID()
		sb := domain.Sandbox{
			ID:         svcID,
			Name:       "virtiofs-refusal-test",
			Project:    "e2e",
			State:      domain.Running, // TriggerSnapshot is a self-edge from Running
			LiveMounts: liveMounts,
		}
		ctx := context.Background()
		if err := st.Create(ctx, sb); err != nil {
			t.Fatalf("store.Create: %v", err)
		}

		// Fork must refuse citing live mounts.
		_, forkErr := svc.Fork(ctx, svcID.String(), 1)
		switch {
		case forkErr == nil:
			t.Errorf("service.Fork: expected live-mount refusal error; got nil")
		case !strings.Contains(forkErr.Error(), "live"):
			t.Errorf("service.Fork: expected 'live' in error message; got: %v", forkErr)
		default:
			t.Logf("AC-4 Fork PASS: %v", forkErr)
		}

		// Snapshot must refuse citing live mounts.
		_, snapErr := svc.Snapshot(ctx, svcID.String())
		switch {
		case snapErr == nil:
			t.Errorf("service.Snapshot: expected live-mount refusal error; got nil")
		case !strings.Contains(snapErr.Error(), "live"):
			t.Errorf("service.Snapshot: expected 'live' in error message; got: %v", snapErr)
		default:
			t.Logf("AC-4 Snapshot PASS: %v", snapErr)
		}
	})

	// ── AC-5: no virtiofsd orphans after sandbox removal ──────────────────────
	// MUST RUN LAST — tears down the VM and clears driver state.
	t.Run("AC5_no_virtiofsd_orphans", func(t *testing.T) {
		// Capture virtiofsd PIDs and socket paths BEFORE clearState removes them
		// from the driver's internal map.
		drv.mu.Lock()
		vprocs := drv.virtiofsdProcs[id]
		pids := make([]int, 0, len(vprocs))
		for _, vp := range vprocs {
			if vp != nil {
				pids = append(pids, vp.pid)
			}
		}
		drv.mu.Unlock()

		sockPaths := make([]string, len(liveMounts))
		for i := range liveMounts {
			sockPaths[i] = virtiofsdSockPath(socketDir, id, i)
		}

		t.Logf("virtiofsd PIDs before teardown: %v", pids)
		t.Logf("virtiofsd socket paths: %v", sockPaths)

		if len(pids) == 0 {
			t.Errorf("AC-5 PRE-CHECK: no virtiofsd PIDs recorded — driver may not have spawned virtiofsd")
		}

		// Stop the VM then clear driver state.  clearState kills virtiofsd procs
		// (SIGKILL to process group) and unlinks their sockets.
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if stopErr := drv.Stop(stopCtx, id); stopErr != nil {
			t.Logf("drv.Stop: %v (may already be gone)", stopErr)
		}
		drv.clearState(id)

		// Verify all virtiofsd processes are dead.
		for _, pid := range pids {
			if err := syscall.Kill(pid, 0); err == nil {
				// Kill with signal 0 succeeds → process is still alive.
				t.Errorf("AC-5 FAIL: virtiofsd PID %d still alive after clearState", pid)
			} else {
				t.Logf("AC-5 PASS: virtiofsd PID %d dead (Kill(0) → %v)", pid, err)
			}
		}

		// Verify socket files have been unlinked.
		for _, sp := range sockPaths {
			if _, statErr := os.Stat(sp); !os.IsNotExist(statErr) {
				t.Errorf("AC-5 FAIL: virtiofsd socket still present: %s", sp)
			} else {
				t.Logf("AC-5 PASS: socket unlinked: %s", sp)
			}
		}
	})
}
