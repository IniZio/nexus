// Package-level network attachment for Cloud Hypervisor sandboxes.
//
// Each sandbox NIC is a vhost-user device served by nexus inside the sandbox's
// netns child; framePump copies raw Ethernet frames between the vhost slot and
// one end of an AF_UNIX SOCK_DGRAM socketpair. The other end is returned to
// the perimeter layer via GuestNetworkFD.
package cloudhypervisor

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"syscall"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
)

const (
	// frameBufSize is the read buffer for the pump goroutines.
	// Must be ≥ the maximum Ethernet frame size (including jumbo frames).
	// AF_UNIX SOCK_DGRAM silently truncates datagrams that exceed the read
	// buffer (MSG_TRUNC is set; excess bytes are discarded, no error returned).
	// 65536 comfortably covers any MTU in use.
	frameBufSize = 65536
)

// sandboxMac derives a stable locally-administered unicast MAC from the sandbox ID.
// Uses SHA-256 of the raw ID bytes to fill the vendor-specific octets.
func sandboxMac(id domain.SandboxID) string {
	h := sha256.Sum256(id[:])
	// 52:54:00 is the QEMU OUI; bit 1 of first byte set = locally administered,
	// bit 0 clear = unicast.
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", h[0], h[1], h[2])
}

// vmNetConfig is the JSON representation of a CH net device in vm.create.
//
// CH connects to Socket as a vhost-user client; the nexus process serves the
// backend on that socket.
//
// CH v53 NetConfig fields: vhost_user (bool), vhost_socket
// (string), mac (string), num_queues (int). Source: CH OpenAPI schema at
// github.com/cloud-hypervisor/cloud-hypervisor v53.0 vmm/src/api/openapi/cloud-hypervisor.yaml.
type vmNetConfig struct {
	VhostUser bool   `json:"vhost_user,omitempty"`
	Socket    string `json:"vhost_socket,omitempty"`
	Mac       string `json:"mac"`
	NumQueues int    `json:"num_queues"`
}

// vmConfigWithNet extends vmConfig with vsock, net, and virtiofs-fs devices.
// All three device classes are collapsed into this one type (not split across
// vmConfigWithNet + vmConfigWithFsAndNet) so that a single PUT /vm.create
// payload covers every device nexus attaches at boot.
type vmConfigWithNet struct {
	vmConfig
	Vsock *vmVsockConfig `json:"vsock,omitempty"`
	Net   []vmNetConfig  `json:"net,omitempty"`
	Fs    []vmFsConfig   `json:"fs,omitempty"`
}

// VMCreateWithNet sends PUT /vm.create with vsock, vhost-user network, and optional
// virtiofs-fs devices.
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
// The NIC pump lives inside the isolated user+network namespace managed by
// rt; teardown calls rt.Stop() which kills the whole process group.
type netState struct {
	rt        *NetnsRuntime // always non-nil on the netns path; nil only in unit tests
	perimConn net.Conn      // returned to caller via GuestNetworkFD (= rt.PerimConn)
	pumpDone  chan struct{} // unused on netns path; retained for ch_net_test.go TestGuestNetworkFD_OneCallGuard
	claimed   bool          // true after GuestNetworkFD has been called once
}

// errPumpClosed is returned by swappableConn.swap when the pump has already
// been permanently closed (closePermanently called) — swapping in a fresh
// conn after real teardown has begun would leak it forever, since nothing
// will ever read or write it again.
var errPumpClosed = fmt.Errorf("cloudhypervisor: netns pump: permanently closed")

// swappableConn lets a live framePump replace its underlying conn without
// either pump goroutine ever returning — see the framePump doc comment for why
// that invariant is load-bearing (D-HSH-17: a returned framePump falls through
// to os.Exit(0) in RunNetnsChild, which takes the guest VM down via
// Pdeathsig).
//
// A generation channel (gen) is closed exactly once per swap (or on
// permanent close) to wake a goroutine that is blocked because its current
// conn errored: the swap already closed the OLD conn (that is what unblocks
// a pending Read on it), and closing gen tells the blocked goroutine why —
// "a new conn is ready, retry" versus "shut down for real, exit".
//
// All methods are safe for concurrent use.
type swappableConn struct {
	mu     sync.Mutex
	conn   io.ReadWriteCloser
	gen    chan struct{}
	closed bool
}

// newSwappableConn wraps c as the initial (generation 0) conn.
func newSwappableConn(c io.ReadWriteCloser) *swappableConn {
	return &swappableConn{conn: c, gen: make(chan struct{})}
}

// current returns the live conn, its generation-change channel (closed when
// this conn is replaced or the pump is permanently closed), and whether the
// pump has been permanently closed.
func (s *swappableConn) current() (conn io.ReadWriteCloser, gen <-chan struct{}, closed bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn, s.gen, s.closed
}

// write sends p on whatever conn is current at the moment of the call.
// Errors are discarded — this preserves the pre-existing asymmetry
// (ch_net.go framePump doc): the guest→host direction never blocks or exits on
// a write failure, so a dead or not-yet-installed conn silently drops frames
// instead of taking the pump down.
func (s *swappableConn) write(p []byte) {
	s.mu.Lock()
	c := s.conn
	s.mu.Unlock()
	_, _ = c.Write(p)
}

// swap installs newConn as the current conn, wakes any goroutine blocked on
// the previous generation (by closing the old gen channel), and closes the
// previous conn. Returns errPumpClosed without installing newConn if the
// pump has already been permanently closed — the caller must close newConn
// itself in that case.
func (s *swappableConn) swap(newConn io.ReadWriteCloser) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errPumpClosed
	}
	old := s.conn
	oldGen := s.gen
	s.conn = newConn
	s.gen = make(chan struct{})
	s.mu.Unlock()

	close(oldGen)
	_ = old.Close()
	return nil
}

// closePermanently marks the pump for real shutdown: the conn→nicFd
// goroutine, if currently blocked waiting for a swap, wakes and exits
// (sending to framePump's done channel) instead of waiting forever. Idempotent.
func (s *swappableConn) closePermanently() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	old := s.conn
	oldGen := s.gen
	s.mu.Unlock()

	close(oldGen)
	_ = old.Close()
}

// framePump copies raw Ethernet frames in both directions between nicFd and
// pump's current conn.
//
// Both sides are packet-mode (one Read = one complete frame):
//   - nicFd is the NIC endpoint (vhost slot) — one Read = one Ethernet frame
//   - pump's conn is an AF_UNIX SOCK_DGRAM connection — one Read = one datagram = one frame
//
// framePump blocks until both goroutines exit. framePump does NOT close nicFd or
// pump's conn; callers are responsible for cleanup.
//
// # Never-terminate invariant (D-HSH-17)
//
// The guest→host goroutine (nicFd.Read) exits, as before, on a nicFd read
// error — that only happens when the process is really tearing down (the fd
// itself is being closed), so its exit behaviour is unchanged.
//
// The host→guest goroutine (pump.current().Read) used to exit on ANY read
// error, including the error produced when a dead supervisor's end of the
// socketpair is gone. That was correct when nothing could ever replace the
// conn — but a goroutine that already returned cannot be "swapped into"
// later; there is no stack left to resume. So this goroutine now BLOCKS on
// pump's generation channel instead of exiting when its current conn errors,
// and retries with whatever conn pump.swap installs next. It only truly
// exits when pump.closePermanently has been called (checked at the top of
// each loop iteration, including the one after waking from a swap wait).
func framePump(nicFd io.ReadWriteCloser, pump *swappableConn) {
	done := make(chan struct{}, 2)

	// nicFd → pump (guest → host / gvproxy direction). Unchanged: writes are
	// always attempted against whatever conn is current and errors are
	// discarded, so this goroutine automatically starts reaching a newly
	// swapped-in conn on its next iteration with no special-casing needed.
	go func() {
		buf := make([]byte, frameBufSize)
		for {
			n, err := nicFd.Read(buf)
			if n > 0 {
				pump.write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}()

	// pump → nicFd (host / gvproxy → guest direction).
	go func() {
		buf := make([]byte, frameBufSize)
		for {
			c, gen, closed := pump.current()
			if closed {
				break
			}
			n, err := c.Read(buf)
			if n > 0 {
				_, _ = nicFd.Write(buf[:n])
			}
			if err != nil {
				// Do NOT exit: block until pump.swap (retry with the new
				// conn) or pump.closePermanently (re-check closed, exit)
				// wakes us. This is what lets a crashed supervisor's dead
				// conn be replaced without ever letting framePump return.
				<-gen
				continue
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

// teardownSandboxNet kills the netns child process group (child + CH
// grandchild), waits for exit, and closes PerimConn. Idempotent. Must NOT be
// called while d.mu is held (teardown acquires d.mu internally to remove the
// entry). Kernel auto-reclaims the user+network namespace and all interfaces
// when the last process in the netns exits.
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
	// Teardown is the graceful path; a VM that died on its own never reaches
	// here before the supervisor has read RuntimeExit.
	ForgetRuntimeExit(id.String())
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
	if ns.perimConn == nil {
		c1, c2 := net.Pipe()
		c2.Close()
		return c1, nil
	}
	return ns.perimConn, nil // net.Conn implements io.ReadWriteCloser
}

// Compile-time assertion that CHDriver implements driver.NetworkHook.
// (Driver/PauseResumer/Snapshotter/Forker assertions are in driver.go;
// GuestDialer assertion is in ch_vsock.go.)
var _ driver.NetworkHook = (*CHDriver)(nil)

// NetnsState implements driver.NetnsStateProvider. It returns the five netns
// identity fields captured by the most recent successful StartNetnsRuntime call
// for id. Returns ok=false when no netns runtime is registered for id (e.g. the
// VM has not started yet, has been stopped, or was started on the in-process path).
//
// This method is read-only and acquires only d.mu (an in-memory lock), so it is
// safe to call from inside a store.Update callback.
func (d *CHDriver) NetnsState(id domain.SandboxID) (driver.NetnsIdentity, bool) {
	d.mu.Lock()
	ns := d.nets[id]
	d.mu.Unlock()
	if ns == nil || ns.rt == nil {
		return driver.NetnsIdentity{}, false
	}
	rt := ns.rt
	return driver.NetnsIdentity{
		ChildPID:       rt.ChildPID,
		ChildPGID:      rt.ChildPGID,
		ChildStartTime: rt.ChildStartTime,
		VhostSocket:    rt.VhostSocket,
		APISocket:      rt.APISocket,
		ControlSocket:  rt.ControlSocket,
		ControlToken:   rt.ControlToken,
	}, true
}

var _ driver.NetnsStateProvider = (*CHDriver)(nil)

// AdoptRuntime installs an already-adopted [NetnsRuntime] into the driver's
// in-memory state, so [CHDriver.Observe], [CHDriver.Stop], and
// [CHDriver.GuestNetworkFD] operate on id exactly as they would if this
// driver's own Start had produced rt. This is the seam a replacement
// supervisor uses in place of Start when a VM predates the process and must
// not be rebooted: the driver otherwise has no way to learn about a runtime
// it did not create.
//
// Callers must have already validated rt via [AdoptNetnsRuntime] — this
// method performs no pid-identity or fd validation of its own, only the
// bookkeeping to make id observable and stoppable.
//
// Refuses (returns a non-nil error, leaves d unmodified) when a runtime is
// already registered for id. On a freshly constructed driver (the only
// intended caller) this should never happen; if it does, it signals a caller
// bug rather than a race worth papering over.
func (d *CHDriver) AdoptRuntime(id domain.SandboxID, rt *NetnsRuntime) error {
	if rt == nil {
		return fmt.Errorf("cloudhypervisor: AdoptRuntime %s: rt is nil", id)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.nets[id]; exists {
		return fmt.Errorf("cloudhypervisor: AdoptRuntime %s: a runtime is already registered for this sandbox", id)
	}
	d.nets[id] = &netState{rt: rt, perimConn: rt.PerimConn}
	return nil
}
