//go:build integration

package selfhost

// orca_ssh_test.go — ORCA-S1 live SSH proof.
//
// Boots a sandbox with an ephemeral ed25519 keypair provisioned via
// SeedSSHAuthorizedKeys, then SSH-es into the guest directly over vsock:22
// using the matching private key via golang.org/x/crypto/ssh and runs
// /bin/echo to assert key authentication works end-to-end.
//
// # Skip conditions (same as TestHerdrHello)
//
//   - /dev/kvm absent or inaccessible
//   - cloud-hypervisor binary not found (CLOUD_HYPERVISOR_BIN or PATH)
//   - mke2fs not in PATH
//   - images/kernel/vmlinux-x86_64 absent and NEXUS3_KERNEL_PATH not set
//   - docker unavailable (needed by BuildSelfHostBaseImage)
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run TestOrcaSSH \
//	    ./internal/test/selfhost/ -v -timeout 30m

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

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

// TestOrcaSSH boots a sandbox with a provisioned ephemeral ed25519 keypair
// and proves that an SSH client can log in using key authentication by dialing
// vsock:22 directly, running /bin/echo ok in-guest.
func TestOrcaSSH(t *testing.T) {
	// ── skip guards ────────────────────────────────────────────────────────────
	skipUnlessKVMSH(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── Step 1: build/obtain the self-host base image ─────────────────────────
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	t.Log("building self-host base image (may take several minutes) …")
	img, buildErr := BuildSelfHostBaseImage(context.Background(), cache)
	if buildErr != nil {
		switch {
		case errors.Is(buildErr, ErrDockerUnavailable):
			t.Skip("skipping: docker unavailable:", buildErr)
		case errors.Is(buildErr, builder.ErrMke2fsUnavailable):
			t.Skip("skipping: mke2fs unavailable:", buildErr)
		}
		t.Fatalf("BuildSelfHostBaseImage: %v", buildErr)
	}
	t.Logf("base image ready: digest=%s size=%.0f MiB", img.Digest, float64(img.Size)/(1024*1024))

	// ── Step 2: create service + driver infrastructure ────────────────────────
	socketDir, err := os.MkdirTemp("/tmp", "orca-ssh-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if len(socketDir)+selfhostSockNameLen > selfhostSunPathMax {
		os.RemoveAll(socketDir)
		t.Skipf("skipping: socket dir path too long for AF_UNIX: %s", socketDir)
	}
	serialPath := filepath.Join(socketDir, "orca-ssh-serial.log")
	t.Cleanup(func() {
		// Dump serial output on failure so we can see guest kernel/sshd messages.
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== guest serial output ===\n%s", content)
		}
	})

	storeRoot := t.TempDir()
	st, err := store.NewFileStore(storeRoot)
	if err != nil {
		t.Fatalf("store.NewFileStore: %v", err)
	}

	svcDrv, err := cloudhypervisor.New(cloudhypervisor.Config{
		BinaryPath: chBin,
		SocketDir:  socketDir,
	})
	if err != nil {
		os.RemoveAll(socketDir)
		t.Fatalf("cloudhypervisor.New (svcDrv): %v", err)
	}
	svc := service.New(st, svcDrv, lifecycle.New())

	var sandboxID domain.SandboxID
	t.Cleanup(func() {
		if sandboxID != (domain.SandboxID{}) {
			rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := svc.Remove(rmCtx, sandboxID.String()); err != nil {
				t.Logf("cleanup: svc.Remove(%s): %v", sandboxID, err)
			} else {
				t.Logf("cleanup: sandbox %s removed", sandboxID)
			}
		}
		os.RemoveAll(socketDir)
	})

	// ── Step 3: generate ephemeral ed25519 keypair ────────────────────────────
	t.Log("generating ephemeral SSH keypair …")
	pubKey, privKeyPEM, err := service.GenerateEphemeralSSHKeypair()
	if err != nil {
		t.Fatalf("GenerateEphemeralSSHKeypair: %v", err)
	}
	t.Logf("public key: %s", pubKey)

	// ── Step 4: wire SSH seeder onto the service ──────────────────────────────
	// bootDrv is set inside the factory closure and implements driver.GuestDialer.
	var bootDrv *cloudhypervisor.CHDriver

	factory := service.DriverFactory(func(resolvedExt4 string, _ []service.ExtraDisk) (driver.Driver, error) {
		var newErr error
		bootDrv, newErr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:       chBin,
			SocketDir:        socketDir,
			KernelPath:       kernelPath,
			DiskImagePath:    resolvedExt4,
			SerialOutputPath: serialPath,
		})
		return bootDrv, newErr
	})

	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		return realProbeSH(bootDrv)(ctx, drv, id)
	})

	// ── Step 5: boot with SSH provisioning ────────────────────────────────────
	t.Log("creating and booting sandbox …")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer bootCancel()

	// Two-phase approach:
	//   a) Boot without SSH seeder so CreateAndBoot step 10 is a no-op.
	//   b) Wait for guest agent, build agent.Client from bootDrv (GuestDialer),
	//      call SeedSSHAuthorizedKeys directly.
	opts := service.CreateAndBootOptions{
		Image:               service.ImageSpec{Digest: string(img.Digest)},
		CacheRoot:           cacheRoot,
		ReachabilityTimeout: 60 * time.Second,
	}

	sb, err := service.CreateAndBoot(
		bootCtx,
		svc,
		cache,
		factory,
		probe,
		"orca", "ssh-proof",
		opts,
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxID = sb.ID
	t.Logf("sandbox booted: id=%s state=%s", sb.ID, sb.State)

	if sb.State != domain.Running {
		t.Fatalf("expected state=Running after CreateAndBoot, got %s", sb.State)
	}

	// ── Step 6: wait for guest agent and inject authorized_keys ───────────────
	t.Log("waiting for guest agent …")
	waitForAgentSH(t, bootDrv, sb.ID, 30*time.Second)
	t.Log("guest agent reachable; injecting SSH authorized_keys …")

	// bootDrv implements driver.GuestDialer; construct the agent client from it.
	agentClient := agent.NewClient(bootDrv, sb.ID)

	sshSeeder := service.NewAgentSSHKeyCopySeeder(agentClient)
	injectCtx, injectCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer injectCancel()
	if err := service.SeedSSHAuthorizedKeys(injectCtx, pubKey, sb.ID, sshSeeder); err != nil {
		t.Fatalf("SeedSSHAuthorizedKeys: %v", err)
	}
	t.Log("authorized_keys injected")

	// ── Diagnostic: dump in-guest SSH state before dialing ───────────────────
	// Exec a single shell command that shows /root/.ssh, authorized_keys,
	// sshd config, and the listening :22 socket so we can see the real in-guest
	// state without guessing.
	diagCtx, diagCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer diagCancel()
	var diagBuf bytes.Buffer
	diagCmd := `/bin/sh -c 'echo "== ls /root =="; ls -la /root; echo "== ls /root/.ssh =="; ls -la /root/.ssh; echo "== authorized_keys =="; cat /root/.ssh/authorized_keys; echo "== sshd binary =="; ls -la /usr/sbin/sshd; echo "== sshd -T pubkey/root/strict =="; /usr/sbin/sshd -T 2>&1 | grep -iE "pubkey|permitroot|authorizedkeysfile|strictmodes"; echo "== drop-in =="; cat /etc/ssh/sshd_config.d/99-nexus3-orca.conf; echo "== listening =="; (ss -ltnp 2>/dev/null || netstat -ltnp 2>/dev/null) | grep -i :22'`
	_, diagErr := agentClient.Exec(diagCtx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", diagCmd},
		Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Stdout: &diagBuf,
		Stderr: &diagBuf,
	})
	t.Logf("=== in-guest SSH diagnostics (diagErr=%v) ===\n%s", diagErr, diagBuf.String())

	// ── Step 7: SSH into the guest directly via vsock:22 ─────────────────────
	// Dial vsock port 22 (sshd) directly through the cloud-hypervisor vsock
	// multiplexer — no nexus3 binary or ProxyCommand needed.
	t.Log("dialing guest sshd via vsock port 22 …")
	sshCtx, sshCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer sshCancel()

	conn, err := bootDrv.DialGuest(sshCtx, sb.ID, 22)
	if err != nil {
		t.Fatalf("DialGuest vsock:22: %v", err)
	}
	defer conn.Close()

	signer, err := ssh.ParsePrivateKey([]byte(privKeyPEM))
	if err != nil {
		t.Fatalf("ssh.ParsePrivateKey: %v", err)
	}

	//nolint:gosec // InsecureIgnoreHostKey is intentional in a test sandbox.
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, "sandbox:22", &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         20 * time.Second,
	})
	if err != nil {
		t.Fatalf("ssh.NewClientConn: %v", err)
	}
	defer sshConn.Close()

	sshClient := ssh.NewClient(sshConn, chans, reqs)
	defer sshClient.Close()

	sess, err := sshClient.NewSession()
	if err != nil {
		t.Fatalf("sshClient.NewSession: %v", err)
	}
	defer sess.Close()

	out, err := sess.Output("/bin/echo orca-ssh-ok")
	if err != nil {
		t.Fatalf("session.Output(/bin/echo orca-ssh-ok): %v", err)
	}

	got := strings.TrimSpace(string(out))
	if got != "orca-ssh-ok" {
		t.Fatalf("ssh echo output = %q, want %q", got, "orca-ssh-ok")
	}
	t.Logf("TestOrcaSSH PASS: ssh output = %q", got)
}
