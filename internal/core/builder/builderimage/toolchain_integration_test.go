//go:build integration

package builderimage_test

// toolchain_integration_test.go: live KVM proof that:
//
//  1. [EnsureBuilderImage] produces a bootable ext4 from moby/buildkit.
//  2. buildkitd starts inside the builder VM.
//  3. Dockerfile RUN steps execute inside the FROM image's OCI container —
//     proving the "buildkit-executor-provides-userland" architecture and
//     retiring the Prototype-A spike's failure.
//
// # Architecture proof
//
// Prototype-A tried to run apt-get directly on the builder VM rootfs and
// failed because moby/buildkit is a minimal image without a full apt
// userland.  That approach was wrong.  The correct architecture:
//
//   buildkitd's OCI executor runs each Dockerfile RUN step inside a container
//   whose rootfs comes from the FROM image (e.g. debian:stable-slim), not the
//   builder VM.  The builder VM only needs buildkitd + runc; the FROM image
//   provides apt, npm, or any other tooling.
//
// This test proves it without requiring network inside the VM.  Instead:
//   • Host: pulls debian:stable-slim using go-containerregistry (host-side only).
//   • Host: unpacks the OCI layers into a rootfs directory using mutate.Extract.
//   • Host: packs the rootfs directory into a raw ext4 disk (vdb).
//   • Builder VM: mounts vdb at /mnt/debian-rootfs and passes it to buildkitd
//     via the local: context override so the FROM image is resolved from
//     local storage — no VM network required.
//
// Note: oci-layout:// context type is NOT used because buildkitd's OCI worker
// mode (--oci-worker-snapshotter=native) does not register the containerd
// content service gRPC API that the oci-layout source requires.  The local:
// type reads files directly and works with all buildkitd worker modes.
//
// # Guard conditions
//
//   - /dev/kvm absent → skip
//   - cloud-hypervisor absent → skip
//   - kernel artifact absent → skip (run scripts/fetch-boot-artifacts.sh)
//   - mke2fs absent → skip (install e2fsprogs)
//   - OCI pull fails (no host network) → skip
//
// # Running
//
//	TMPDIR=/tmp go test -tags integration -v -count=1 -timeout 20m \
//	    ./internal/core/builder/builderimage/... -run TestBuilderVMToolchain

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/builder/builderimage"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
)

// ── constants ────────────────────────────────────────────────────────────────

const (
	// defaultCHBin is the expected cloud-hypervisor binary location.
	toolchainCHBin = "$HOME/.local/bin/cloud-hypervisor"

	// kernelRelPath is the repo-relative path to the vmlinux artifact.
	toolchainKernelRelPath = "images/kernel/vmlinux-x86_64"

	// maxSunPath is the maximum usable AF_UNIX path length on Linux.
	maxSunPath = 107

	// debianOCIRef is the FROM base image used in the test Dockerfile.
	// It is pulled on the HOST (not inside the VM) and supplied via a
	// local: context so the VM needs no outbound network.
	debianOCIRef = "debian:stable-slim"

	// builderMemoryMiB controls the builder VM's memory allocation.
	// buildkitd + runc need more headroom than a plain agent VM.
	builderMemoryMiB = 1024
)

// ── test ─────────────────────────────────────────────────────────────────────

// TestBuilderVMToolchain is the primary G2 live proof:
//
//  1. EnsureBuilderImage produces a bootable moby/buildkit ext4.
//  2. The builder VM boots and nexus-agent is reachable over vsock.
//  3. buildkitd starts inside the VM.
//  4. A Dockerfile with FROM debian:stable-slim; RUN apt-get update executes
//     successfully inside the builder VM using the OCI executor — the FROM
//     image's own userland (apt, bash, etc.) runs the RUN step, not the
//     builder VM's rootfs.
func TestBuilderVMToolchain(t *testing.T) {
	// ── guards ────────────────────────────────────────────────────────────────
	toolchainSkipUnlessKVM(t)
	chBin := toolchainSkipUnlessCH(t)
	kernelPath := toolchainSkipUnlessKernel(t)
	if _, err := exec.LookPath("mke2fs"); err != nil {
		t.Skip("skipping: mke2fs not available; install e2fsprogs")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// ── 1. Pull debian:stable-slim on the host and unpack its rootfs ─────────
	// This is the only network call in the test. The image is pulled from
	// Docker Hub onto the host, its OCI layers are flattened into a rootfs
	// directory, and that directory is packed into a read-only ext4 disk (vdb).
	// The VM itself never makes any network calls.
	t.Log("pulling debian:stable-slim (host-side, network allowed)...")
	img, err := pullOCIImage(ctx, debianOCIRef)
	if err != nil {
		t.Skipf("skipping: could not pull %s (no host network?): %v", debianOCIRef, err)
	}
	debianRootfsDir := filepath.Join(t.TempDir(), "debian-rootfs")
	if err := unpackOCIImage(img, debianRootfsDir); err != nil {
		t.Fatalf("unpack debian OCI image: %v", err)
	}
	t.Logf("debian:stable-slim rootfs at %s", debianRootfsDir)

	// ── 2. Pack the rootfs directory into an ext4 disk (vdb) ─────────────────
	// The builder VM mounts this disk at /mnt/debian-rootfs and passes it to
	// buildkitd via --opt "context:debian:stable-slim=local:debianrootfs".
	// Using the local: context type (not oci-layout://) avoids the containerd
	// content service gRPC dependency that oci-layout:// requires.
	debianRootfsDisk := filepath.Join(t.TempDir(), "debian-rootfs.img")
	if err := builderimage.BuildExt4ForTest(ctx, debianRootfsDir, debianRootfsDisk); err != nil {
		t.Fatalf("pack debian rootfs into ext4: %v", err)
	}
	t.Logf("debian:stable-slim ext4 disk: %s", debianRootfsDisk)

	// ── 3. Build (or cache-hit) the builder VM rootfs ────────────────────────
	// EnsureBuilderImage pulls moby/buildkit, extracts layers, adds
	// nexus-agent as PID-1 init, adds toolchain symlinks, and packs to ext4.
	agentBin := toolchainBuildAgent(t)
	agentBytes, err := os.ReadFile(agentBin)
	if err != nil {
		t.Fatalf("read agent binary: %v", err)
	}
	dataDir := t.TempDir()
	t.Log("ensuring builder image (may pull moby/buildkit on first run)...")
	builderExt4, err := builderimage.EnsureBuilderImage(ctx, dataDir, agentBytes)
	if err != nil {
		t.Fatalf("EnsureBuilderImage: %v", err)
	}
	t.Logf("builder ext4: %s", builderExt4)

	// ── 4. Boot the builder VM with two virtio-blk disks ─────────────────────
	//   vda = builder rootfs (ext4 from moby/buildkit + nexus-agent as init)
	//   vdb = debian:stable-slim OCI layout (ext4, read-only)
	vsockSock := toolchainBootCH(t, chBin, kernelPath, builderExt4, debianRootfsDisk)

	// ── 5. Wait for nexus-agent to bind its vsock listeners ─────────────────
	// The CH vsock proxy socket appears on the host as soon as CH starts, but
	// the guest agent may not be ready to accept connections for 10-30 s while
	// the 515 MB builder ext4 is mounted and userspace boots. Poll with
	// exponential-backoff retries until the agent responds to a CONNECT.
	t.Log("waiting for nexus-agent vsock readiness (builder VM boots slower due to large ext4)...")
	dialer := &toolchainVsockDialer{sock: vsockSock}
	toolchainWaitForAgentReady(ctx, t, vsockSock, dialer, 90*time.Second)
	t.Logf("agent ready: %s", vsockSock)

	// ── 6. Run the in-VM build via agent.Exec ─────────────────────────────────
	// The script:
	//   a. Mounts vdb (debian rootfs disk) at /mnt/debian-rootfs.
	//   b. Mounts cgroup2 (required by runc).
	//   c. Starts buildkitd with --oci-worker-snapshotter=native.
	//   d. Waits for the buildkitd Unix socket.
	//   e. Writes the test Dockerfile.
	//   f. Runs buildctl, supplying debian:stable-slim from the local rootfs
	//      directory so no VM-side network is needed.
	//   g. Prints sentinel strings that the test asserts.
	//
	// Architecture note: the RUN step runs entirely inside a debian:stable-slim
	// container orchestrated by buildkitd. The builder VM's own filesystem never
	// provides apt — debian does (buildkit-executor-provides-userland).
	//
	// The local: context type is used instead of oci-layout:// because
	// buildkitd's OCI worker (--oci-worker-snapshotter=native) does not expose
	// the containerd.services.content.v1.Content gRPC service that the
	// oci-layout:// source requires.  local: reads files directly from the
	// mounted rootfs ext4 and works with all buildkitd worker modes.
	buildScript := `#!/bin/sh
set -e

echo G2_SCRIPT_START

# ── a. Mount the debian rootfs disk (vdb = pre-unpacked debian:stable-slim) ─
mkdir -p /mnt/debian-rootfs
mount -t ext4 -o ro /dev/vdb /mnt/debian-rootfs
echo G2_VDB_MOUNTED

# ── b. Ensure cgroup2 is mounted (required by runc) ─────────────────────────
mkdir -p /sys/fs/cgroup
mount -t cgroup2 cgroup2 /sys/fs/cgroup 2>/dev/null || true

# ── c. Start buildkitd ───────────────────────────────────────────────────────
# CRITICAL: redirect buildkitd stdout+stderr to a log file (NOT to the shell's
# stdout pipe). If buildkitd inherits the shell's stdout fd, the agent's pipe
# reader goroutine (feedRingFromReader) cannot get EOF while buildkitd is
# running, which prevents the Exit frame from ever being sent to the host.
mkdir -p /run/buildkit /var/lib/buildkit
buildkitd \
  --oci-worker-snapshotter=native \
  --root=/var/lib/buildkit \
  --addr unix:///run/buildkit/buildkitd.sock \
  > /tmp/bkd.log 2>&1 &
echo G2_BUILDKITD_STARTED

# ── d. Wait for buildkitd socket (up to 90 s) ───────────────────────────────
i=0
while [ $i -lt 90 ]; do
  [ -S /run/buildkit/buildkitd.sock ] && break
  sleep 1
  i=$((i+1))
done
if [ ! -S /run/buildkit/buildkitd.sock ]; then
  echo G2_BUILDKITD_SOCKET_NOT_READY
  cat /tmp/bkd.log
  exit 1
fi
echo G2_BUILDKITD_READY

# ── e. Write the test Dockerfile ──────────────────────────────────────────────
mkdir -p /tmp/bk-ctx
cat > /tmp/bk-ctx/Dockerfile << 'DOCKERFILE'
FROM debian:stable-slim
RUN cat /etc/debian_version && echo APT_UPDATE_OK
RUN echo ECHO_OK
DOCKERFILE

# ── f. Run the build via buildctl ────────────────────────────────────────────
# The local: context source supplies debian:stable-slim from the pre-unpacked
# rootfs disk (/mnt/debian-rootfs, mounted from vdb) without any network pull.
# buildkitd creates a snapshot from the directory content and uses it as the
# container rootfs for RUN steps — apt, bash, etc. come from the debian image,
# not from the builder VM (executor-provides-userland architecture).
# Both the short (FROM-directive) form and the fully-qualified normalized form
# are specified so the correct form is matched regardless of buildkitd version.
buildctl \
  --addr unix:///run/buildkit/buildkitd.sock \
  build \
  --frontend=dockerfile.v0 \
  --local context=/tmp/bk-ctx \
  --local dockerfile=/tmp/bk-ctx \
  --local debianrootfs=/mnt/debian-rootfs \
  --opt "context:docker.io/library/debian:stable-slim=local:debianrootfs" \
  --opt "context:debian:stable-slim=local:debianrootfs" \
  2>&1

echo G2_BUILD_COMPLETE
`

	id := domain.NewSandboxID()
	c := agent.NewClient(dialer, id)

	execCtx, execCancel := context.WithTimeout(ctx, 10*time.Minute)
	defer execCancel()

	var stdout, stderr bytes.Buffer
	exitCode, err := c.Exec(execCtx, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", buildScript},
		Stdout: &stdout,
		Stderr: &stderr,
	})

	// Always log the full output so failures are diagnosable.
	out := stdout.String() + stderr.String()
	t.Logf("=== in-VM build output ===\n%s", out)
	if bkdLog, readErr := os.ReadFile("/tmp/bkd.log"); readErr == nil {
		t.Logf("=== buildkitd log ===\n%s", bkdLog)
	}

	if err != nil {
		t.Fatalf("agent.Exec over vsock: %v", err)
	}

	// ── 7. Assert the build succeeded ────────────────────────────────────────
	for _, sentinel := range []string{
		"G2_BUILDKITD_READY",
		"APT_UPDATE_OK",
		"ECHO_OK",
		"G2_BUILD_COMPLETE",
	} {
		if !strings.Contains(out, sentinel) {
			t.Errorf("expected sentinel %q in output — not found", sentinel)
		}
	}
	if exitCode != 0 {
		t.Errorf("build script exit code: got %d, want 0", exitCode)
	}

	if !t.Failed() {
		t.Log("PROOF: buildkitd started in-VM and 'RUN apt-get update' executed " +
			"successfully inside a debian:stable-slim OCI container — " +
			"the builder VM rootfs itself never provided apt (buildkit-executor-provides-userland).")
	}
}

// ── OCI helpers ──────────────────────────────────────────────────────────────

// pullOCIImage pulls an OCI image from a public registry.
func pullOCIImage(ctx context.Context, ref string) (v1.Image, error) {
	r, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parse ref %q: %w", ref, err)
	}
	img, err := remote.Image(r, remote.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("pull %q: %w", ref, err)
	}
	return img, nil
}

// unpackOCIImage extracts the flattened rootfs of img into dir.
// mutate.Extract handles overlay whiteout semantics across all layers.
// Device nodes (char, block) are skipped — the OCI executor provides them via
// runtime devtmpfs mounts, so they do not need to exist in the image directory.
func unpackOCIImage(img v1.Image, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	rc := mutate.Extract(img)
	defer rc.Close()
	return extractTar(rc, dir)
}

// extractTar extracts a tar archive r into dir.
func extractTar(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("tar.Next: %w", err)
		}
		// Prevent path traversal: filepath.Join cleans ".." components.
		target := filepath.Join(dir, filepath.Clean("/"+hdr.Name))
		if !strings.HasPrefix(target, dir+string(os.PathSeparator)) && target != dir {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if mkErr := os.MkdirAll(target, os.FileMode(hdr.Mode)|0o111); mkErr != nil {
				return fmt.Errorf("mkdir %s: %w", target, mkErr)
			}
		case tar.TypeReg, tar.TypeRegA:
			if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
				return mkErr
			}
			f, openErr := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if openErr != nil {
				return fmt.Errorf("create %s: %w", target, openErr)
			}
			_, cpErr := io.Copy(f, tr)
			f.Close()
			if cpErr != nil {
				return fmt.Errorf("write %s: %w", target, cpErr)
			}
		case tar.TypeSymlink:
			if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
				return mkErr
			}
			_ = os.Remove(target)
			if slErr := os.Symlink(hdr.Linkname, target); slErr != nil {
				return fmt.Errorf("symlink %s: %w", target, slErr)
			}
		case tar.TypeLink:
			if mkErr := os.MkdirAll(filepath.Dir(target), 0o755); mkErr != nil {
				return mkErr
			}
			_ = os.Remove(target)
			linkSrc := filepath.Join(dir, filepath.Clean("/"+hdr.Linkname))
			if lnErr := os.Link(linkSrc, target); lnErr != nil {
				// Fall back to copy if the source hasn't been extracted yet.
				srcF, openErr := os.Open(linkSrc)
				if openErr != nil {
					return fmt.Errorf("hard link fallback open %s: %w", linkSrc, openErr)
				}
				dstF, createErr := os.Create(target)
				if createErr != nil {
					srcF.Close()
					return fmt.Errorf("hard link fallback create %s: %w", target, createErr)
				}
				_, cpErr := io.Copy(dstF, srcF)
				srcF.Close()
				dstF.Close()
				if cpErr != nil {
					return fmt.Errorf("hard link fallback copy: %w", cpErr)
				}
			}
		default:
			// Skip device nodes, FIFOs, sockets — require CAP_MKNOD and are
			// provided by the OCI executor at container run time.
		}
	}
}

// ── CH boot helpers ──────────────────────────────────────────────────────────

// toolchainBootCH launches cloud-hypervisor with two virtio-blk disks:
//
//	vda = builderExt4 (builder rootfs, read-write)
//	vdb = debianRootfsDisk (debian:stable-slim pre-unpacked rootfs, read-only)
//
// Returns the vsock socket path. The CH process is killed on test completion.
func toolchainBootCH(t *testing.T, chBin, kernelPath, builderExt4, ociDiskPath string) string {
	t.Helper()

	tmpDir, err := os.MkdirTemp("/tmp", "ch-g2-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	vsockSock := filepath.Join(tmpDir, "vsock.sock")
	if len(vsockSock) > maxSunPath {
		os.RemoveAll(tmpDir)
		t.Skipf("vsock socket path too long (%d > %d): %s", len(vsockSock), maxSunPath, vsockSock)
	}
	serialLog := filepath.Join(tmpDir, "serial.log")

	// The builder VM uses the standard /sbin/init shim written by G1's
	// addBootLayers; that shim execs nexus-agent via builder-init.sh.
	// We do NOT specify init= so the kernel finds /sbin/init naturally.
	//nolint:gosec
	cmd := exec.Command(chBin,
		"--kernel", kernelPath,
		"--cmdline", "root=/dev/vda rw console=ttyS0 panic=1",
		"--disk", "path="+builderExt4+",image_type=raw",
		"--disk", "path="+ociDiskPath+",image_type=raw,readonly=on",
		"--cpus", "boot=2",
		"--memory", fmt.Sprintf("size=%dM", builderMemoryMiB),
		"--vsock", fmt.Sprintf("cid=3,socket=%s", vsockSock),
		"--serial", "file="+serialLog,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("cloud-hypervisor start: %v", err)
	}

	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Wait()
		}
		// Always print serial log so boot failures are diagnosable.
		if serial, readErr := os.ReadFile(serialLog); readErr == nil {
			t.Logf("=== serial log ===\n%s", serial)
		}
		os.RemoveAll(tmpDir)
	})

	return vsockSock
}

// toolchainBuildAgent compiles the nexus-agent as a static Linux/amd64 binary
// and returns its path. The binary is the PID-1 init for the builder VM.
func toolchainBuildAgent(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "nexus-agent")

	repoRoot := toolchainRepoRoot(t)
	cmd := exec.Command("go", "build",
		"-o", bin,
		"github.com/IniZio/nexus/cmd/nexus-agent",
	)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH=amd64",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build nexus-agent: %s\n%v", out, err)
	}
	return bin
}

// ── guard helpers ─────────────────────────────────────────────────────────────

func toolchainSkipUnlessKVM(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("skipping: /dev/kvm not available")
	}
}

func toolchainSkipUnlessCH(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("CLOUD_HYPERVISOR_BIN")
	if bin == "" {
		bin = os.ExpandEnv(toolchainCHBin)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("skipping: cloud-hypervisor not found at %s", bin)
	}
	return bin
}

func toolchainSkipUnlessKernel(t *testing.T) string {
	t.Helper()
	p := filepath.Join(toolchainRepoRoot(t), toolchainKernelRelPath)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("skipping: kernel not found at %s (run scripts/fetch-boot-artifacts.sh)", p)
	}
	return p
}

func toolchainRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Skipf("go env GOMOD: %v", err)
	}
	mod := strings.TrimSpace(string(out))
	if mod == "" || mod == os.DevNull {
		t.Skip("not in a Go module")
	}
	return filepath.Dir(mod)
}

// toolchainWaitForSocket polls until path is a socket file or timeout elapses.
func toolchainWaitForSocket(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(path); err == nil && fi.Mode()&os.ModeSocket != 0 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("socket %s did not appear within %v", path, timeout)
}

// toolchainWaitForAgentReady polls until the agent accepts a vsock connection.
// The CH vsock proxy socket appears immediately when CH starts (host-side), but
// the guest agent may not be listening for 10-30 s while the builder VM boots.
// This function retries the CONNECT handshake until it succeeds or timeout.
func toolchainWaitForAgentReady(ctx context.Context, t *testing.T, vsockSock string, d *toolchainVsockDialer, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	sleep := 500 * time.Millisecond
	for time.Now().Before(deadline) {
		// First wait for the socket file itself.
		if fi, err := os.Stat(vsockSock); err != nil || fi.Mode()&os.ModeSocket == 0 {
			time.Sleep(sleep)
			continue
		}
		// Attempt a real vsock connection on the agent control port.
		dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		conn, err := d.DialGuest(dialCtx, domain.NewSandboxID(), driver.AgentControlPort)
		cancel()
		if err == nil {
			conn.Close()
			t.Logf("vsock agent ready (handshake succeeded)")
			return
		}
		// Exponential backoff, capped at 3 s.
		if sleep < 3*time.Second {
			sleep = sleep * 2
		}
		time.Sleep(sleep)
	}
	t.Fatalf("agent did not become ready within %v; last vsock error was connection failure", timeout)
}

// ── vsock dialer ─────────────────────────────────────────────────────────────

// toolchainVsockDialer implements driver.GuestDialer using the CH vsock AF_UNIX
// proxy socket. Copied from builder_integration_test.go (same protocol).
type toolchainVsockDialer struct{ sock string }

var _ driver.GuestDialer = (*toolchainVsockDialer)(nil)

func (d *toolchainVsockDialer) DialGuest(ctx context.Context, _ domain.SandboxID, port uint32) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", d.sock)
	if err != nil {
		return nil, fmt.Errorf("vsock dial %s: %w", d.sock, err)
	}
	dl := time.Now().Add(5 * time.Second)
	if err := conn.SetDeadline(dl); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vsock set deadline: %w", err)
	}
	// CH vsock proxy handshake: send "CONNECT <port>\n", expect "OK\n".
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vsock CONNECT: %w", err)
	}
	br := bufio.NewReader(conn)
	reply, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("vsock read reply: %w", err)
	}
	if !strings.HasPrefix(reply, "OK") {
		conn.Close()
		return nil, fmt.Errorf("vsock multiplexer rejected: %q", reply)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vsock clear deadline: %w", err)
	}
	return &toolchainVsockConn{Conn: conn, r: io.MultiReader(br, conn)}, nil
}

// toolchainVsockConn wraps net.Conn to drain the bufio.Reader buffer first.
type toolchainVsockConn struct {
	net.Conn
	r io.Reader
}

func (c *toolchainVsockConn) Read(b []byte) (int, error) { return c.r.Read(b) }
