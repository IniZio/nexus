package cloudhypervisor

// ch_vsock.go implements the driver.GuestDialer capability for CHDriver.
//
// Cloud Hypervisor exposes guest vsock to the host as an AF_UNIX socket.
// The host multiplexer protocol is the standard virtio-vsock-proxy shape:
//
//  1. Connect to the per-sandbox AF_UNIX socket.
//  2. Write "CONNECT <port>\n".
//  3. Read the reply line: "OK <n>\n" on success, anything else is an error.
//  4. The socket is now a raw bidirectional stream to the guest at that port.
//
// This is the same protocol used by firecracker-containerd's vsock proxy and
// systemd-ssh-proxy. CH's implementation is in
// vmm/src/api_server/api_server.rs (virtio-vsock channel multiplexing).

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
)

// guestCID is the vsock CID assigned to every sandbox VM. Because each
// sandbox runs in its own isolated cloud-hypervisor process there are no
// CID collisions across sandboxes.
const guestCID uint64 = 3

// vsockHandshakeTimeout is the maximum time to wait for the multiplexer to
// respond to the CONNECT line. It is intentionally short — the VMM either
// accepts or rejects immediately.
const vsockHandshakeTimeout = 5 * time.Second

// vmVsockConfig maps to CH's VsockConfig for the vm.create payload.
// Verified against cloud-hypervisor.yaml @ v52.0: VsockConfig requires
// cid and socket; id is optional.
type vmVsockConfig struct {
	CID    uint64 `json:"cid"`
	Socket string `json:"socket"`
	ID     string `json:"id,omitempty"`
}

// vmConfigWithVsock extends [vmConfig] with an optional vsock device.
// It is used as the vm.create payload when the vsock transport is enabled,
// avoiding any modification of the base vmConfig type in client.go.
type vmConfigWithVsock struct {
	vmConfig
	Vsock *vmVsockConfig `json:"vsock,omitempty"`
}

// VMCreateWithVsock is like [client.VMCreate] but includes a vsock device in
// the vm.create payload. Defined here (not in client.go) to keep the vsock
// surface self-contained.
func (c *client) VMCreateWithVsock(ctx context.Context, cfg vmConfig, vsock *vmVsockConfig) error {
	full := vmConfigWithVsock{vmConfig: cfg, Vsock: vsock}
	resp, err := c.do(ctx, http.MethodPut, "/vm.create", full)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: vm.create (vsock): %w", err)
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cloudhypervisor: vm.create (vsock): unexpected status %d: %s",
			resp.StatusCode, body)
	}
	return nil
}

// vsockPath returns the per-sandbox AF_UNIX socket path that CH's vsock
// multiplexer binds. It follows the same naming convention as socketPath and
// satisfies the ≤107-byte sun_path limit whenever socketPath does.
func (d *CHDriver) vsockPath(id domain.SandboxID) string {
	return filepath.Join(d.cfg.SocketDir, id.String()+".vsock")
}

// vsockConn wraps a net.Conn together with the buffered reader used to
// consume the multiplexer handshake reply. Any bytes the peer sent
// immediately after the "OK" line are preserved in the reader's buffer and
// returned on the first Read call after the handshake.
type vsockConn struct {
	net.Conn
	r io.Reader
}

func (c *vsockConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// DialGuest connects to the given port inside the VM identified by id via
// CH's vsock AF_UNIX multiplexer and returns a raw [net.Conn].
//
// The handshake is:
//
//	→ "CONNECT <port>\n"
//	← "OK <n>\n"   (success — socket is now a bidirectional stream)
//	← anything else (error — connection is closed before returning)
//
// Implements [driver.GuestDialer].
func (d *CHDriver) DialGuest(ctx context.Context, id domain.SandboxID, port uint32) (net.Conn, error) {
	vsockSock := d.vsockPath(id)

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", vsockSock)
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: dial guest %s: connect vsock socket: %w", id, err)
	}

	// Apply handshake deadline — the shorter of the caller's deadline and
	// vsockHandshakeTimeout.
	deadline := time.Now().Add(vsockHandshakeTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return nil, fmt.Errorf("cloudhypervisor: dial guest %s: set deadline: %w", id, err)
	}

	// Send CONNECT handshake.
	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("cloudhypervisor: dial guest %s: send CONNECT: %w", id, err)
	}

	// Read reply line. Use a bufio.Reader so we don't mis-read stream bytes
	// that arrive immediately after "OK\n". The reader is preserved in the
	// returned vsockConn so subsequent reads drain the buffer first.
	br := bufio.NewReader(conn)
	reply, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		if err == io.EOF {
			// EOF here means the guest closed the connection before sending any
			// reply — the vsock multiplexer is up (the AF_UNIX connect succeeded)
			// but nothing is listening on port %d inside the VM yet.  This is
			// the signature of a race between the host dialer and in-guest agent
			// startup, not a transport fault.
			return nil, fmt.Errorf("cloudhypervisor: dial guest %s: read handshake reply:"+
				" EOF (guest agent not yet listening on vsock port %d — VM may still be starting up)", id, port)
		}
		return nil, fmt.Errorf("cloudhypervisor: dial guest %s: read handshake reply: %w", id, err)
	}
	reply = strings.TrimRight(reply, "\r\n")

	if !strings.HasPrefix(reply, "OK") {
		conn.Close()
		return nil, fmt.Errorf("cloudhypervisor: dial guest %s: multiplexer rejected connection: %q", id, reply)
	}

	// Clear the deadline — the caller controls connection lifetime from here.
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("cloudhypervisor: dial guest %s: clear deadline: %w", id, err)
	}

	return &vsockConn{Conn: conn, r: io.MultiReader(br, conn)}, nil
}

// Compile-time interface assertion — GuestDialer is implemented by CHDriver.
var _ driver.GuestDialer = (*CHDriver)(nil)
