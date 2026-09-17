package cloudhypervisor

// Driver.GuestDialer capability using virtio-vsock-proxy multiplexer.

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

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
)

const guestCID uint64 = 3 // vsock CID assigned to every sandbox VM

const vsockHandshakeTimeout = 5 * time.Second // max wait for multiplexer response to CONNECT

type vmVsockConfig struct { // CH's VsockConfig for vm.create payload
	CID    uint64 `json:"cid"`
	Socket string `json:"socket"`
	ID     string `json:"id,omitempty"`
}

type vmConfigWithVsock struct { // vmConfig with optional vsock device for vm.create payload
	vmConfig
	Vsock *vmVsockConfig `json:"vsock,omitempty"`
}

func (c *client) VMCreateWithVsock(ctx context.Context, cfg vmConfig, vsock *vmVsockConfig) error { // like VMCreate but with vsock device
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

func (d *CHDriver) vsockPath(id domain.SandboxID) string { // per-sandbox AF_UNIX socket path for vsock multiplexer
	return filepath.Join(d.cfg.SocketDir, id.String()+".vsock")
}

func (d *CHDriver) vsockGuestPortPath(id domain.SandboxID, port uint32) string { // AF_UNIX path for guest-initiated connection (T0b: underscore separator)
	return filepath.Join(d.cfg.SocketDir, fmt.Sprintf("%s.vsock_%d", id.String(), port))
}

type vsockConn struct { // net.Conn with buffered reader for multiplexer handshake reply
	net.Conn
	r io.Reader
}

func (c *vsockConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// DialGuest connects to port inside VM.
func (d *CHDriver) DialGuest(ctx context.Context, id domain.SandboxID, port uint32) (net.Conn, error) {
	vsockSock := d.vsockPath(id)

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", vsockSock)
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: dial guest %s: connect vsock socket: %w", id, err)
	}

	deadline := time.Now().Add(vsockHandshakeTimeout) // shorter of caller's deadline and vsockHandshakeTimeout
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return nil, fmt.Errorf("cloudhypervisor: dial guest %s: set deadline: %w", id, err)
	}

	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("cloudhypervisor: dial guest %s: send CONNECT: %w", id, err)
	}

	// bufio.Reader preserves stream bytes after "OK\n" for drain-first reads
	br := bufio.NewReader(conn)
	reply, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		if err == io.EOF {
			// EOF: guest closed before reply (race, not transport fault)
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

	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("cloudhypervisor: dial guest %s: clear deadline: %w", id, err)
	}

	return &vsockConn{Conn: conn, r: io.MultiReader(br, conn)}, nil
}

var _ driver.GuestDialer = (*CHDriver)(nil)
