package service

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
)

// PortForwardMuxVsockPort is the fixed vsock port the in-guest agent binds for
// the generic port-forward multiplexer.  Must match portForwardMuxVsockPort in
// cmd/nexus3-agent/port_forward.go.
const PortForwardMuxVsockPort uint32 = 3001

// DialGuestPortForward resolves the sandbox identified by ref, dials the
// agent's port-forward multiplexer on vsock port 3001, writes the 4-byte
// big-endian guestPort, and returns the connection ready for bidirectional
// splicing to the guest-local TCP service on that port.
func (s *Service) DialGuestPortForward(ctx context.Context, ref string, guestPort uint32) (net.Conn, error) {
	conn, err := s.DialGuest(ctx, ref, PortForwardMuxVsockPort)
	if err != nil {
		return nil, fmt.Errorf("forward: dial guest mux: %w", err)
	}

	// Send the 4-byte big-endian guest TCP port number.
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], guestPort)
	if _, err := conn.Write(buf[:]); err != nil {
		conn.Close()
		return nil, fmt.Errorf("forward: write guest port %d: %w", guestPort, err)
	}

	return conn, nil
}
