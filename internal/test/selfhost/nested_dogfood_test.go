//go:build integration

// Package selfhost — S0N nested-dogfood integration proof.
//
// Proves end-to-end that an outer nexus3 microVM (booted with NestedVirt=true)
// can:
//  1. Run buildkitd in-guest (rootful, --oci-worker-snapshotter=native).
//  2. Use buildctl to build a minimal inner nexus3 image from a Containerfile.
//  3. Convert the rootfs to a raw ext4 image via mke2fs.
//  4. Boot a REAL inner nexus3 microVM from the ext4 using cloud-hypervisor
//     and assert the inner nexus3-agent reports via its serial console
//     (not just a bare kernel — the inner agent boots to "nexus3-agent:").
//
// This is S0N (self-contained nested dogfood) — it uses the idiomatic
// Containerfile/image → build → boot model, not a custom --rootfs approach
// (D-ORCH-07 rejected that path).
//
// # Failure discipline
//
// Transport errors hard-fail. Unexpected output hard-fails. Only absent
// infrastructure causes a t.Skip.
//
// # Prerequisites
//
//   - /dev/kvm accessible on the host
//   - Host nested KVM enabled (/sys/module/kvm_{intel,amd}/parameters/nested == 1|Y)
//   - cloud-hypervisor binary (CLOUD_HYPERVISOR_BIN or ~/.local/bin/cloud-hypervisor)
//   - mke2fs in PATH (e2fsprogs)
//   - docker in PATH
//   - images/kernel/vmlinux-x86_64 present in the repo root
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -run TestNestedDogfood \
//	    ./internal/test/selfhost/ -v -timeout 120m
package selfhost

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/perimeter"
	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
	"github.com/IniZio/nexus3/internal/core/perimeter/netfilter"
	"github.com/IniZio/nexus3/internal/core/perimeter/netstack"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// innerBuildAndBootScript is the shell program run INSIDE the outer guest.
//
// It drives the complete in-guest build + inner VM boot sequence:
//  1. Mount kernel pseudo-FSes (proc, sys, dev, cgroup) — idempotent; nexus3-
//     agent may already have mounted some of these, non-fatal either way.
//  2. Mount a tmpfs for buildkitd state to avoid virtiofs xattr failures.
//  3. Write the runc --no-new-keyring wrapper.
//  4. Start buildkitd rootful with --oci-worker-snapshotter=native.
//  5. Wait for the buildkitd socket (90 s timeout).
//  6. Write the inner Containerfile to /tmp.
//  7. Run buildctl solve → local rootfs dir export.
//  8. Append nexus3-agent as the final layer (copy it into the rootfs).
//  9. mke2fs -d → raw ext4 inner disk image.
// 10. Launch inner cloud-hypervisor VM using the ext4, capture serial log.
// 11. Print serial log; assert "nexus3-agent" appears (inner agent booted).
//
// Gotchas mirrored from the old nexus buildkit_task.go:
//   - buildkitd state MUST be on tmpfs (not virtiofs) — xattr issues.
//   - --oci-worker-snapshotter=native required (overlay can't nest on virtiofs).
//   - runc --no-new-keyring prevents session-keyring quota exhaustion.
//   - SSL_CERT_FILE must be set so buildkitd can pull from docker.io.
const innerBuildAndBootScript = `#!/bin/sh
set -eu

BUILDKITD=/usr/local/bin/buildkitd
BUILDCTL=/usr/local/bin/buildctl
RUNC=/usr/local/bin/buildkit-runc
CLOUD_HYPERVISOR=/usr/local/bin/cloud-hypervisor
KERNEL=/boot/vmlinux

echo "==> [S0N] step 1: mounting kernel pseudo-FSes"
mount -t proc proc /proc               2>/dev/null || true
mount -t sysfs sysfs /sys              2>/dev/null || true
mount -t devtmpfs devtmpfs /dev        2>/dev/null || true
mkdir -p /dev/pts
mount -t devpts devpts /dev/pts        2>/dev/null || true
mkdir -p /sys/fs/cgroup
mount -t cgroup2 cgroup2 /sys/fs/cgroup 2>/dev/null || true

echo "==> [S0N] step 2: tmpfs for buildkitd state"
mkdir -p /var/lib/buildkit
mount -t tmpfs tmpfs /var/lib/buildkit -o size=4g 2>/dev/null || true

echo "==> [S0N] step 3: runc wrapper (--no-new-keyring)"
mkdir -p /run/buildkit
cat > /run/buildkit/nexus-runc << 'WRAPPER'
#!/bin/sh
set -eu
REAL_RUNC=/usr/local/bin/buildkit-runc
args=""
injected=false
for arg in "$@"; do
  args="$args '$arg'"
  if [ "$injected" = "false" ] && [ "${arg#-}" = "$arg" ]; then
    case "$arg" in
      run|create) args="$args --no-new-keyring"; injected=true ;;
    esac
  fi
done
eval exec "$REAL_RUNC" $args
WRAPPER
chmod 755 /run/buildkit/nexus-runc

echo "==> [S0N] step 4: starting buildkitd"
mkdir -p /run/buildkit
SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt \
SSL_CERT_DIR=/etc/ssl/certs \
BUILDKITD_SNAPSHOTTER=native \
  "$BUILDKITD" \
    --root /var/lib/buildkit/root \
    --addr unix:///run/buildkit/buildkitd.sock \
    --oci-worker-snapshotter=native \
    --oci-worker-binary=/run/buildkit/nexus-runc \
    >> /tmp/buildkitd.log 2>&1 &
BKPID=$!

echo "==> [S0N] step 5: waiting for buildkitd socket (up to 90s)"
i=0
while [ $i -lt 450 ]; do
  [ -S /run/buildkit/buildkitd.sock ] && break
  sleep 0.2
  i=$((i+1))
done
if [ ! -S /run/buildkit/buildkitd.sock ]; then
  echo "ERROR: buildkitd socket never appeared"
  cat /tmp/buildkitd.log || true
  exit 1
fi
echo "buildkitd ready (pid=$BKPID)"

echo "==> [S0N] step 6: writing inner Containerfile"
mkdir -p /tmp/inner-ctx
cat > /tmp/inner-ctx/Containerfile << 'CF'
FROM ubuntu:24.04
CF
# Stage nexus3-agent binary into build context (appended as final layer below).
cp /sbin/nexus3-agent /tmp/inner-ctx/nexus3-agent

echo "==> [S0N] step 7: buildctl solve → inner rootfs"
mkdir -p /tmp/inner-rootfs
"$BUILDCTL" \
  --addr unix:///run/buildkit/buildkitd.sock \
  build \
    --frontend=dockerfile.v0 \
    --opt filename=Containerfile \
    --local context=/tmp/inner-ctx \
    --local dockerfile=/tmp/inner-ctx \
    --progress plain \
    --output type=local,dest=/tmp/inner-rootfs

echo "==> [S0N] step 8: baking nexus3-agent into rootfs"
install -m 755 /sbin/nexus3-agent /tmp/inner-rootfs/sbin/nexus3-agent

echo "==> [S0N] step 9: mke2fs → inner ext4"
INNER_EXT4=/tmp/inner.ext4
truncate -s 2G "$INNER_EXT4"
mke2fs -t ext4 -d /tmp/inner-rootfs \
  -L nexus3-inner \
  -U 00000000-0000-0000-0000-000000000001 \
  "$INNER_EXT4"

echo "==> [S0N] step 10: pre-boot diagnostics"
ls -la "$INNER_EXT4"
df -h /tmp
stat -f -c '%T' /tmp
free -m
echo "==> [S0N] cloud-hypervisor version:"
"$CLOUD_HYPERVISOR" --version || true
echo "==> [S0N] loopback write test:"
if command -v mount >/dev/null 2>&1; then
  mkdir -p /tmp/inner-mnt
  if mount -o loop "$INNER_EXT4" /tmp/inner-mnt; then
    touch /tmp/inner-mnt/.__writetest && sync && umount /tmp/inner-mnt && echo "LOOP-WRITE-OK" || { umount /tmp/inner-mnt 2>/dev/null; echo "LOOP-WRITE-FAIL"; }
  else
    echo "LOOP-MOUNT-FAIL"
  fi
else
  echo "LOOP-WRITE-SKIP: mount not available"
fi

echo "==> [S0N] step 10: booting inner nexus3 microVM"
mkdir -p /tmp/inner-vm
# inner rootfs mounted ro — CH virtio-blk writes fail under nested KVM (outer kernel async-I/O);
# the agent boot smoke-test needs no rootfs writes (writable dirs are tmpfs/devtmpfs).
timeout 60 "$CLOUD_HYPERVISOR" \
  --kernel "$KERNEL" \
  --cmdline 'root=/dev/vda ro init=/sbin/nexus3-agent console=ttyS0 panic=0' \
  --disk path="$INNER_EXT4",readonly=off,direct=off \
  --cpus boot=1 \
  --memory size=256M \
  --serial file=/tmp/inner-vm/serial.log \
  --console off \
  --api-socket /tmp/inner-vm/ch.sock \
  || true

echo "==> [S0N] step 11: inner VM serial output:"
if [ -f /tmp/inner-vm/serial.log ]; then
  cat /tmp/inner-vm/serial.log
else
  echo "ERROR: no serial log produced"
  exit 1
fi
`

// TestNestedDogfood proves end-to-end that an outer nexus3 microVM can build
// and boot an inner nexus3 microVM using the Containerfile/image→build→boot
// model (D-ORCH-09 Option B).
//
// Acceptance criteria:
//   - (S0N-AC1) Outer guest has /dev/kvm (NestedVirt=true).
//   - (S0N-AC2) In-guest buildkitd starts and the buildctl solve succeeds.
//   - (S0N-AC3) The inner ext4 is produced by mke2fs.
//   - (S0N-AC4) The inner cloud-hypervisor boots the inner nexus3-agent;
//     the serial log contains "nexus3-agent" (the agent has booted).
func TestNestedDogfood(t *testing.T) {
	// ── 1. Skip guards ────────────────────────────────────────────────────────
	skipUnlessNestedKVM(t)
	chBin := skipUnlessCHBinSH(t)
	skipUnlessMke2fsSH(t)

	// ── 2. Kernel path ────────────────────────────────────────────────────────
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	kernelPath := kernelPathSH(t, repoRoot)

	// ── 3. Build outer dogfood image ──────────────────────────────────────────
	cacheRoot := t.TempDir()
	cache, err := image.NewCache(cacheRoot)
	if err != nil {
		t.Fatalf("image.NewCache: %v", err)
	}

	imgCtx, imgCancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer imgCancel()

	t.Log("building nested-dogfood outer image (first run: ~15–25 min; cached: seconds) ...")
	img, err := BuildNestedDogfoodImage(imgCtx, cache)
	if err != nil {
		switch {
		case errors.Is(err, ErrDockerUnavailable):
			t.Skip("skipping: docker unavailable:", err)
		case errors.Is(err, builder.ErrMke2fsUnavailable):
			t.Skip("skipping: mke2fs unavailable:", err)
		}
		t.Fatalf("BuildNestedDogfoodImage: %v", err)
	}
	t.Logf("outer dogfood image: digest=%s size=%.2f GiB",
		img.Digest, float64(img.Size)/(1<<30))

	// ── 4. Infrastructure ─────────────────────────────────────────────────────
	socketDir, err := os.MkdirTemp("/tmp", "nested-dogfood-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	serialPath := filepath.Join(socketDir, "nested-dogfood-serial.log")
	t.Cleanup(func() {
		// Dump outer serial on failure so perimeter/gvproxy/DNS startup lines are visible.
		if content, err := os.ReadFile(serialPath); err == nil && len(content) > 0 && t.Failed() {
			t.Logf("=== outer guest serial output ===\n%s", content)
		}
		os.RemoveAll(socketDir) //nolint:errcheck
	})

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
		t.Fatalf("cloudhypervisor.New (svc): %v", err)
	}
	broker := cred.NewBroker()
	svc := service.New(st, svcDrv, lifecycle.New()).WithBroker(broker)

	// ── 5. Boot outer sandbox with NestedVirt=true ────────────────────────────
	var bootDrv *cloudhypervisor.CHDriver
	factory := service.DriverFactory(func(ext4Path string, _ []service.ExtraDisk) (driver.Driver, error) {
		var ferr error
		bootDrv, ferr = cloudhypervisor.New(cloudhypervisor.Config{
			BinaryPath:       chBin,
			SocketDir:        socketDir,
			KernelPath:       kernelPath,
			DiskImagePath:    ext4Path,
			MemoryMiB:        2048, // generous: outer guest runs buildkitd + inner VM
			SerialOutputPath: serialPath,
			StartTimeout:     90 * time.Second,
			NestedVirt:       true, // expose /dev/kvm for inner cloud-hypervisor
		})
		return bootDrv, ferr
	})
	probe := service.ProbeFunc(func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		return realProbeSH(bootDrv)(ctx, drv, id)
	})

	t.Log("creating and booting outer sandbox (NestedVirt=true, 2 GiB RAM) ...")
	bootCtx, bootCancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer bootCancel()

	var sandboxID domain.SandboxID
	t.Cleanup(func() {
		if sandboxID == (domain.SandboxID{}) {
			return
		}
		rmCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if rerr := svc.Remove(rmCtx, sandboxID.String()); rerr != nil {
			t.Logf("cleanup: svc.Remove(%s): %v", sandboxID, rerr)
		}
	})

	sb, err := service.CreateAndBoot(
		bootCtx, svc, cache, factory, probe,
		"nested-dogfood", fmt.Sprintf("nested-dogfood-%d", time.Now().UnixNano()),
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Digest: string(img.Digest)},
			CacheRoot:           cacheRoot,
			ReachabilityTimeout: 90 * time.Second,
			NestedVirt:          true,
			// D-ORCH-11: outer sandbox needs egress so in-guest buildkitd can pull
			// ubuntu:24.04 from docker.io. AllowedHosts opens the egress perimeter
			// to the Docker Hub registry and auth service.
			AllowedHosts: []string{
				"registry-1.docker.io",
				"auth.docker.io",
				"index.docker.io",
				"production.cloudflare.docker.com",
				"production.cloudfront.docker.com",
			},
			Broker: broker,
		},
	)
	if err != nil {
		t.Fatalf("CreateAndBoot: %v", err)
	}
	sandboxID = sb.ID
	t.Logf("outer sandbox booted: %s state=%s", sb.ID, sb.State)

	// ── 5b. Start perimeter supervisor ───────────────────────────────────────
	// CreateAndBoot calls bootDrv.Start directly (not svc.Start), so the
	// perimeter supervisor is never auto-started. Wire it manually here,
	// mirroring agent_dogfood_test.go lines 247–306, so in-guest buildkitd
	// can reach Docker Hub through the MITM egress perimeter.
	nh := interface{}(bootDrv).(driver.NetworkHook)
	netFD, err := nh.GuestNetworkFD(context.Background(), sb.ID)
	if err != nil {
		t.Fatalf("GuestNetworkFD: %v", err)
	}

	nestedAllowedHosts := []string{
		"registry-1.docker.io",
		"auth.docker.io",
		"index.docker.io",
		"production.cloudflare.docker.com",
		"production.cloudfront.docker.com",
	}

	al, err := netfilter.NewAllowList(nil, nil, nestedAllowedHosts)
	if err != nil {
		t.Fatalf("netfilter.NewAllowList: %v", err)
	}
	stack := netstack.New(al, func(ev perimeter.AuditEvent) {
		t.Logf("perimeter audit: %s %s — %s", ev.Decision, ev.DestHost, ev.Reason)
	})

	mitmProxy, err := mitm.New(mitm.Config{
		SandboxID:    sb.ID,
		AllowedHosts: nestedAllowedHosts,
		Broker:       broker,
		Logger:       slog.Default(),
	})
	if err != nil {
		t.Fatalf("mitm.New: %v", err)
	}

	supCtx, supCancel := context.WithCancel(context.Background())
	defer supCancel()

	sup, err := perimeter.Start(supCtx, sb.ID, netFD, stack, mitmProxy, al)
	if err != nil {
		t.Fatalf("perimeter.Start: %v", err)
	}
	defer sup.Close()
	t.Logf("perimeter listening at %s", sup.MitmAddr())

	agentClient := agent.NewClient(bootDrv, sb.ID)

	// ── 6. S0N-AC1: assert /dev/kvm is present ────────────────────────────────
	t.Log("S0N-AC1: asserting /dev/kvm is present in outer guest ...")
	var kvmOut bytes.Buffer
	kvmExit, kvmErr := agentClient.Exec(context.Background(), agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", "[ -c /dev/kvm ] && echo KVM_OK || echo KVM_ABSENT"},
		Env:    map[string]string{"PATH": "/usr/local/bin:/sbin:/usr/sbin:/usr/bin:/bin"},
		Stdout: &kvmOut,
		Stderr: &kvmOut,
	})
	if kvmErr != nil {
		t.Fatalf("S0N-AC1: exec transport error: %v\noutput:\n%s", kvmErr, kvmOut.String())
	}
	if kvmExit != 0 || !strings.Contains(kvmOut.String(), "KVM_OK") {
		t.Fatalf("S0N-AC1 FAIL: /dev/kvm absent in outer guest (exit=%d)\n%s", kvmExit, kvmOut.String())
	}
	t.Log("S0N-AC1 PASS: /dev/kvm present in outer guest")

	// ── 6b. Seed MITM CA into outer guest system trust store ─────────────────
	// buildkitd uses the system cert pool to pull docker.io images; it will
	// reject the perimeter's MITM-signed TLS cert unless the CA is trusted.
	// Encode the per-sandbox CA as PEM, write it to the Debian/Ubuntu extra-CA
	// drop-in directory, then run update-ca-certificates to rebuild the bundle.
	{
		caPEM := pem.EncodeToMemory(&pem.Block{
			Type:  "CERTIFICATE",
			Bytes: mitmProxy.CACert().Raw,
		})
		var caOut bytes.Buffer
		caExit, caErr := agentClient.Exec(context.Background(), agent.ExecOptions{
			Argv: []string{
				"/bin/sh", "-c",
				"mkdir -p /usr/local/share/ca-certificates && " +
					"cat > /usr/local/share/ca-certificates/nexus3-mitm.crt && " +
					"update-ca-certificates",
			},
			Env:    map[string]string{"PATH": "/usr/local/sbin:/usr/local/bin:/sbin:/usr/sbin:/usr/bin:/bin"},
			Stdin:  bytes.NewReader(caPEM),
			Stdout: &caOut,
			Stderr: &caOut,
		})
		t.Logf("seed MITM CA output:\n%s", caOut.String())
		if caErr != nil {
			t.Fatalf("seed MITM CA: transport error: %v", caErr)
		}
		if caExit != 0 {
			t.Fatalf("seed MITM CA: update-ca-certificates exited %d\n%s", caExit, caOut.String())
		}
		t.Log("MITM CA seeded and trust store updated in outer guest")
	}

	// ── 7. S0N-AC2+AC3+AC4: run full nested dogfood script ───────────────────
	t.Log("S0N-AC2/AC3/AC4: running nested build-and-boot script (up to 60 min) ...")
	var scriptOut bytes.Buffer
	scriptCtx, scriptCancel := context.WithTimeout(context.Background(), 60*time.Minute)
	defer scriptCancel()

	scriptExit, scriptErr := agentClient.Exec(scriptCtx, agent.ExecOptions{
		Argv: []string{"/bin/sh", "-c", innerBuildAndBootScript},
		Env: map[string]string{
			"PATH":           "/usr/local/bin:/sbin:/usr/sbin:/usr/bin:/bin",
			"HOME":           "/root",
			"SSL_CERT_FILE":  "/etc/ssl/certs/ca-certificates.crt",
		},
		Stdout: &scriptOut,
		Stderr: &scriptOut,
	})

	output := scriptOut.String()
	t.Logf("nested dogfood script output (%d bytes):\n%s", len(output), output)

	if scriptErr != nil {
		t.Fatalf("S0N nested build+boot: transport error: %v", scriptErr)
	}
	if scriptExit != 0 {
		t.Fatalf("S0N nested build+boot: script exited %d\nfull output:\n%s", scriptExit, output)
	}

	// S0N-AC2: buildkitd started
	if !strings.Contains(output, "buildkitd ready") {
		t.Fatalf("S0N-AC2 FAIL: buildkitd did not reach ready state\noutput:\n%s", output)
	}
	t.Log("S0N-AC2 PASS: in-guest buildkitd started and reached ready state")

	// S0N-AC3: mke2fs produced the inner ext4
	if !strings.Contains(output, "inner ext4") {
		t.Fatalf("S0N-AC3 FAIL: mke2fs inner ext4 step not completed\noutput:\n%s", output)
	}
	t.Log("S0N-AC3 PASS: inner ext4 image produced by mke2fs")

	// S0N-AC4: inner nexus3-agent booted — require the agent's OWN startup banner
	// ("nexus3-agent: starting") which only appears when the process actually runs,
	// NOT the kernel cmdline echo of "nexus3-agent" which is a tautology.
	// Also guard against a kernel panic masking a false pass.
	for _, line := range strings.Split(output, "\n") {
		if strings.Contains(line, "Kernel panic") || strings.Contains(line, "Unable to mount root fs") {
			t.Fatalf("S0N-AC4 FAIL: inner VM kernel panic detected — agent never ran\noffending line: %s\nfull output:\n%s", line, output)
		}
	}
	if !strings.Contains(output, "nexus3-agent: starting") {
		t.Fatalf("S0N-AC4 FAIL: inner nexus3-agent startup banner not found in serial log (agent did not run)\noutput:\n%s", output)
	}
	t.Log("S0N-AC4 PASS: inner nexus3-agent booted — startup banner confirmed, no kernel panic")
}
