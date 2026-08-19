// Package-level network attachment for Cloud Hypervisor sandboxes.
//
// # Topology
//
// For each sandbox, three kernel interfaces are created:
//
//	GuestTAP (nx3g-<id>)  — owned by CH after vm.boot (TUNSETIFF)
//	HostTAP  (nx3h-<id>)  — owned by nexus3; pump goroutines bridge it to gvproxy
//	Bridge   (nx3b-<id>)  — unrouted L2 bridge connecting GuestTAP ↔ HostTAP
//
// The pump goroutines copy raw Ethernet frames (one read = one frame) between
// the HostTAP fd and one end of an AF_UNIX SOCK_DGRAM socketpair. The other
// end is returned to the perimeter layer via GuestNetworkFD.
//
// # LEAK-TIGHT invariant
//
// No interface ever receives an IPv4 or IPv6 address:
//   - disable_ipv6=1 is written BEFORE each interface is brought up
//   - per-interface forwarding=0 (never the global /proc/sys/net/ipv4/ip_forward)
//   - No "ip addr add" is ever called (enforced by omission)
//
// These sysctl writes happen in applySandboxNetSysctls, called from createTapBridge.
package cloudhypervisor

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

const (
	// tapBufSize is the read buffer for the pump goroutines.
	// Must be ≥ the maximum Ethernet frame size (including jumbo frames).
	// AF_UNIX SOCK_DGRAM silently truncates datagrams that exceed the read
	// buffer (MSG_TRUNC is set; excess bytes are discarded, no error returned).
	// 65536 comfortably covers any MTU in use.
	tapBufSize = 65536
)

// tapIfNames returns deterministic Linux interface names (≤15 chars, IFNAMSIZ-1)
// for a sandbox. All three names are distinct (different prefixes, same suffix).
//
//   - guestTap: "nx3g-" + 10 hex chars  — CH will TUNSETIFF this at vm.boot
//   - hostTap:  "nx3h-" + 10 hex chars  — nexus3 holds the fd; bridged by pump
//   - bridge:   "nx3b-" + 10 hex chars  — unrouted L2 bridge; no IP assigned
func tapIfNames(id domain.SandboxID) (guestTap, hostTap, bridge string) {
	suffix := fmt.Sprintf("%x", id[:5]) // 10 lowercase hex chars
	return "nx3g-" + suffix, "nx3h-" + suffix, "nx3b-" + suffix
}

// sandboxMac derives a stable locally-administered unicast MAC from the sandbox ID.
// Uses SHA-256 of the raw ID bytes to fill the vendor-specific octets.
func sandboxMac(id domain.SandboxID) string {
	h := sha256.Sum256(id[:])
	// 52:54:00 is the QEMU OUI; bit 1 of first byte set = locally administered,
	// bit 0 clear = unicast.
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", h[0], h[1], h[2])
}

// vmNetConfig is the JSON representation of a CH net device in vm.create.
type vmNetConfig struct {
	Tap       string `json:"tap"`
	Mac       string `json:"mac"`
	NumQueues int    `json:"num_queues"`
}

// vmConfigWithNet extends vmConfig with vsock, net, and virtiofs-fs devices.
// All three device classes are collapsed into this one type (not split across
// vmConfigWithNet + vmConfigWithFsAndNet) so that a single PUT /vm.create
// payload covers every device nexus3 attaches at boot.
type vmConfigWithNet struct {
	vmConfig
	Vsock *vmVsockConfig `json:"vsock,omitempty"`
	Net   []vmNetConfig  `json:"net,omitempty"`
	Fs    []vmFsConfig   `json:"fs,omitempty"`
}

// VMCreateWithNet sends PUT /vm.create with vsock, TAP network, and optional
// virtiofs-fs devices. CH stores the config but does NOT call TUNSETIFF until
// vm.boot; the named TAP interface must already exist at create time.
// Pass nil (or an empty slice) for fs when no virtiofs mounts are needed.
func (c *client) VMCreateWithNet(ctx context.Context, cfg vmConfig, vsock *vmVsockConfig, nets []vmNetConfig, fs []vmFsConfig) error {
	full := vmConfigWithNet{vmConfig: cfg, Vsock: vsock, Net: nets, Fs: fs}
	resp, err := c.do(ctx, http.MethodPut, "/vm.create", full)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: vm.create (net): %w", err)
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cloudhypervisor: vm.create (net): unexpected status %d: %s",
			resp.StatusCode, body)
	}
	return nil
}

// netState holds the per-sandbox network resources for the S1 netns path.
// TAP/bridge/pump live inside the isolated user+network namespace managed by
// rt; teardown calls rt.Stop() which kills the whole process group.
type netState struct {
	rt        *NetnsRuntime // always non-nil on the netns path; nil only in unit tests
	perimConn net.Conn      // returned to caller via GuestNetworkFD (= rt.PerimConn)
	pumpDone  chan struct{} // unused on netns path; retained for ch_net_test.go TestGuestNetworkFD_OneCallGuard
	claimed   bool          // true after GuestNetworkFD has been called once
}

// tapPump copies raw Ethernet frames in both directions between tapFd and conn.
//
// Both sides are packet-mode (one Read = one complete frame):
//   - tapFd is a TAP device fd opened with IFF_NO_PI — one Read = one Ethernet frame
//   - conn is an AF_UNIX SOCK_DGRAM connection — one Read = one datagram = one frame
//
// tapPump blocks until both goroutines exit. A goroutine exits when its source
// returns a read error (typically io.EOF or a closed-fd error from teardown).
// tapPump does NOT close tapFd or conn; callers are responsible for cleanup.
func tapPump(tapFd io.ReadWriteCloser, conn io.ReadWriteCloser) {
	done := make(chan struct{}, 2)

	// tapFd → conn (guest → host / gvproxy direction)
	go func() {
		buf := make([]byte, tapBufSize)
		for {
			n, err := tapFd.Read(buf)
			if n > 0 {
				_, _ = conn.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}()

	// conn → tapFd (host / gvproxy → guest direction)
	go func() {
		buf := make([]byte, tapBufSize)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				_, _ = tapFd.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}()

	<-done
	<-done
}

// unixgramPair creates two connected AF_UNIX SOCK_DGRAM conns via socketpair(2).
// No privileges are required. Each Read on either conn returns exactly one
// datagram, preserving message boundaries (unlike SOCK_STREAM).
func unixgramPair() (net.Conn, net.Conn, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("socketpair(AF_UNIX, SOCK_DGRAM): %w", err)
	}

	// net.FileConn dups the fd internally; we close our originals after wrapping.
	fileA := os.NewFile(uintptr(fds[0]), "unixgram-a")
	fileB := os.NewFile(uintptr(fds[1]), "unixgram-b")

	a, err := net.FileConn(fileA)
	fileA.Close() // FileConn dup'd; close the os.File wrapper (closes the underlying fd)
	if err != nil {
		fileB.Close()
		return nil, nil, fmt.Errorf("socketpair FileConn[0]: %w", err)
	}

	b, err := net.FileConn(fileB)
	fileB.Close()
	if err != nil {
		a.Close()
		return nil, nil, fmt.Errorf("socketpair FileConn[1]: %w", err)
	}

	return a, b, nil
}

// applySandboxNetSysctls enforces the LEAK-TIGHT invariant on all three
// sandbox interfaces. Must be called BEFORE ip link set <iface> up.
//
//   - disable_ipv6=1: prevents link-local RA autoconfiguration
//   - per-interface forwarding=0: no routing through this interface
//
// The global /proc/sys/net/ipv4/ip_forward is NEVER written — that is
// host-wide state owned by the host network stack.
func applySandboxNetSysctls(guestTap, hostTap, bridge string) error {
	for _, iface := range []string{guestTap, hostTap, bridge} {
		// Disable IPv6 before the interface is brought up.
		v6path := fmt.Sprintf("/proc/sys/net/ipv6/conf/%s/disable_ipv6", iface)
		if err := os.WriteFile(v6path, []byte("1\n"), 0o644); err != nil {
			return fmt.Errorf("disable_ipv6 for %s: %w", iface, err)
		}
		// Per-interface forwarding=0 (NOT the global ip_forward knob).
		fwdpath := fmt.Sprintf("/proc/sys/net/ipv4/conf/%s/forwarding", iface)
		if err := os.WriteFile(fwdpath, []byte("0\n"), 0o644); err != nil {
			return fmt.Errorf("per-iface forwarding=0 for %s: %w", iface, err)
		}
	}
	return nil
}

// createTapBridge creates the two-TAP/L2-bridge topology for a sandbox.
// Requires CAP_NET_ADMIN.
//
// Order of operations:
//  1. Create bridge interface
//  2. Create guestTap (CH will TUNSETIFF this at vm.boot)
//  3. Create hostTap (nexus3 opens this via openHostTap)
//  4. Apply LEAK-TIGHT sysctls BEFORE bringing interfaces up
//  5. Enslave both taps to the bridge
//  6. Bring all three interfaces up
//
// No IP address is ever assigned to any interface (LEAK-TIGHT invariant).
func createTapBridge(guestTap, hostTap, bridge string) error {
	run := func(args ...string) error {
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w: %s", args[0], err, out)
		}
		return nil
	}

	if err := run("ip", "link", "add", bridge, "type", "bridge"); err != nil {
		return err
	}
	if err := run("ip", "tuntap", "add", guestTap, "mode", "tap"); err != nil {
		_ = run("ip", "link", "del", bridge)
		return err
	}
	if err := run("ip", "tuntap", "add", hostTap, "mode", "tap"); err != nil {
		_ = run("ip", "tuntap", "del", guestTap, "mode", "tap")
		_ = run("ip", "link", "del", bridge)
		return err
	}

	// LEAK-TIGHT: apply sysctls BEFORE bringing interfaces up.
	if err := applySandboxNetSysctls(guestTap, hostTap, bridge); err != nil {
		_ = run("ip", "tuntap", "del", hostTap, "mode", "tap")
		_ = run("ip", "tuntap", "del", guestTap, "mode", "tap")
		_ = run("ip", "link", "del", bridge)
		return err
	}

	if err := run("ip", "link", "set", guestTap, "master", bridge); err != nil {
		_ = run("ip", "tuntap", "del", hostTap, "mode", "tap")
		_ = run("ip", "tuntap", "del", guestTap, "mode", "tap")
		_ = run("ip", "link", "del", bridge)
		return err
	}
	if err := run("ip", "link", "set", hostTap, "master", bridge); err != nil {
		_ = run("ip", "tuntap", "del", hostTap, "mode", "tap")
		_ = run("ip", "tuntap", "del", guestTap, "mode", "tap")
		_ = run("ip", "link", "del", bridge)
		return err
	}
	if err := run("ip", "link", "set", bridge, "up"); err != nil {
		_ = run("ip", "tuntap", "del", hostTap, "mode", "tap")
		_ = run("ip", "tuntap", "del", guestTap, "mode", "tap")
		_ = run("ip", "link", "del", bridge)
		return err
	}
	if err := run("ip", "link", "set", guestTap, "up"); err != nil {
		_ = run("ip", "link", "set", bridge, "down")
		_ = run("ip", "tuntap", "del", hostTap, "mode", "tap")
		_ = run("ip", "tuntap", "del", guestTap, "mode", "tap")
		_ = run("ip", "link", "del", bridge)
		return err
	}
	if err := run("ip", "link", "set", hostTap, "up"); err != nil {
		_ = run("ip", "link", "set", bridge, "down")
		_ = run("ip", "link", "set", guestTap, "down")
		_ = run("ip", "tuntap", "del", hostTap, "mode", "tap")
		_ = run("ip", "tuntap", "del", guestTap, "mode", "tap")
		_ = run("ip", "link", "del", bridge)
		return err
	}
	return nil
}

// deleteTapBridge tears down the bridge and both TAP interfaces.
// Best-effort: individual errors are silently ignored (interfaces may already
// be gone if the VMM crashed). Called from teardownSandboxNet.
func deleteTapBridge(guestTap, hostTap, bridge string) {
	run := func(args ...string) {
		_ = exec.Command(args[0], args[1:]...).Run()
	}
	run("ip", "link", "set", bridge, "down")
	run("ip", "link", "set", guestTap, "down")
	run("ip", "link", "set", hostTap, "down")
	run("ip", "link", "del", guestTap)
	run("ip", "link", "del", hostTap)
	run("ip", "link", "del", bridge)
}

// teardownSandboxNet kills the netns child process group (child + CH
// grandchild), waits for exit, and closes PerimConn. Idempotent. Must NOT be
// called while d.mu is held (teardown acquires d.mu internally to remove the
// entry). Kernel auto-reclaims the user+network namespace and all interfaces
// (nx3g-*, nx3h-*, nx3b-*) when the last process in the netns exits.
func (d *CHDriver) teardownSandboxNet(id domain.SandboxID) {
	d.mu.Lock()
	ns, ok := d.nets[id]
	if ok {
		delete(d.nets, id)
	}
	d.mu.Unlock()

	if !ok {
		return
	}

	if ns.rt != nil {
		ns.rt.Stop()
	}
}

// GuestNetworkFD implements driver.NetworkHook. Returns the perimeter-facing
// end of the AF_UNIX SOCK_DGRAM socketpair created at Start time.
//
// The dynamic type of the returned io.ReadWriteCloser is net.Conn. The
// perimeter layer can type-assert it back to net.Conn for AcceptVfkit:
//
//	rw, _ := hook.GuestNetworkFD(ctx, id)
//	conn := rw.(net.Conn)
//	network.AcceptVfkit(ctx, conn)
//
// Ownership is transferred on the first call. A second call for the same
// sandbox returns an error without closing or invalidating the first result.
func (d *CHDriver) GuestNetworkFD(ctx context.Context, id domain.SandboxID) (io.ReadWriteCloser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	ns, ok := d.nets[id]
	if !ok {
		return nil, fmt.Errorf("cloudhypervisor: GuestNetworkFD %s: no network state (sandbox not started or already stopped)", id)
	}
	if ns.claimed {
		return nil, fmt.Errorf("cloudhypervisor: GuestNetworkFD %s: fd already transferred (GuestNetworkFD called twice)", id)
	}
	ns.claimed = true
	return ns.perimConn, nil // net.Conn implements io.ReadWriteCloser
}

// Compile-time assertion that CHDriver implements driver.NetworkHook.
// (Driver/PauseResumer/Snapshotter/Forker assertions are in driver.go;
// GuestDialer assertion is in ch_vsock.go.)
var _ driver.NetworkHook = (*CHDriver)(nil)
