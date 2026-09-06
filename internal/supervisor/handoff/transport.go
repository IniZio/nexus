package handoff

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"syscall"
)

// maxPayloadBytes bounds a single handoff datagram. The payload is small
// (pids, paths, a CA cert+key, a placeholder map); this is generous headroom
// while still catching a corrupt or hostile peer before it can force an
// unbounded allocation.
const maxPayloadBytes = 1 << 20 // 1 MiB

// Offer sends p to conn as the outgoing side of a handoff, attaching fd as
// SCM_RIGHTS ancillary data when fd >= 0 (fd < 0 means p.Perimeter.Present
// must be false — there is nothing to attach). It then blocks for the
// incoming side's [Ack] and returns it.
//
// Offer does not close fd and does not mutate any state belonging to the
// caller. Per D-HSH-08, the caller must treat any error (including a
// successfully-received Ack with OK: false) as "the incoming side owns
// nothing" and resume full ownership of fd and every other resource named
// in p — there is no partial-handoff state to unwind.
func Offer(conn *net.UnixConn, p Payload, fd int) (Ack, error) {
	if fd < 0 && p.Perimeter.Present {
		return Ack{}, fmt.Errorf("handoff: Perimeter.Present is true but fd is negative")
	}
	if fd >= 0 && !p.Perimeter.Present {
		return Ack{}, fmt.Errorf("handoff: fd given but Perimeter.Present is false")
	}

	data, err := json.Marshal(p)
	if err != nil {
		return Ack{}, fmt.Errorf("handoff: marshal payload: %w", err)
	}

	var oob []byte
	if fd >= 0 {
		oob = syscall.UnixRights(fd)
	}

	if _, _, err := conn.WriteMsgUnix(data, oob, nil); err != nil {
		return Ack{}, fmt.Errorf("handoff: send payload: %w", err)
	}

	return receiveAck(conn)
}

// Accept reads one [Payload] (and its attached fd, if [Payload.Perimeter]
// is present) from conn as the incoming side of a handoff. It does NOT send
// an [Ack] itself — the caller must inspect the payload's Version (and any
// other precondition) and call exactly one of [Confirm] or [Refuse] to
// complete the exchange. If the caller decides not to adopt the payload
// (including the version-mismatch case), it must close the returned fd
// (harmless: SCM_RIGHTS duplicated it, the sender's copy is unaffected) and
// call [Refuse].
func Accept(conn *net.UnixConn) (Payload, *os.File, error) {
	buf := make([]byte, maxPayloadBytes)
	// Space for one fd; a handoff transfers at most the perimeter fd today.
	oob := make([]byte, syscall.CmsgSpace(4))

	n, oobn, _, _, err := conn.ReadMsgUnix(buf, oob)
	if err != nil {
		return Payload{}, nil, fmt.Errorf("handoff: receive payload: %w", err)
	}

	var p Payload
	if err := json.Unmarshal(buf[:n], &p); err != nil {
		return Payload{}, nil, fmt.Errorf("handoff: unmarshal payload: %w", err)
	}

	var f *os.File
	if oobn > 0 {
		fd, err := parseSingleFD(oob[:oobn])
		if err != nil {
			return Payload{}, nil, err
		}
		if fd >= 0 {
			f = os.NewFile(uintptr(fd), "handoff-perimeter")
		}
	}

	if p.Perimeter.Present && f == nil {
		return Payload{}, nil, fmt.Errorf("handoff: payload declares a perimeter fd but none was received")
	}
	if !p.Perimeter.Present && f != nil {
		f.Close()
		return Payload{}, nil, fmt.Errorf("handoff: payload declares no perimeter fd but one was received")
	}

	return p, f, nil
}

// parseSingleFD extracts exactly one fd from the SCM_RIGHTS control message
// in oob. A well-behaved [Offer] caller never attaches more than one; a
// peer that does is treated as a protocol violation rather than silently
// truncated.
func parseSingleFD(oob []byte) (int, error) {
	scms, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return -1, fmt.Errorf("handoff: parse control message: %w", err)
	}
	var fds []int
	for _, scm := range scms {
		got, err := syscall.ParseUnixRights(&scm)
		if err != nil {
			return -1, fmt.Errorf("handoff: parse unix rights: %w", err)
		}
		fds = append(fds, got...)
	}
	switch len(fds) {
	case 0:
		return -1, nil
	case 1:
		return fds[0], nil
	default:
		for _, fd := range fds {
			_ = syscall.Close(fd)
		}
		return -1, fmt.Errorf("handoff: expected at most 1 fd, got %d", len(fds))
	}
}

// Confirm sends a positive [Ack]. The incoming side must only call this
// after it has durably taken ownership of every resource named in the
// payload (dup'd/retained the fd, recorded virtiofsd pids for adoption,
// etc) — once Confirm is sent, the outgoing side is entitled to release
// them.
func Confirm(conn *net.UnixConn) error {
	return sendAck(conn, Ack{OK: true})
}

// Refuse sends a negative [Ack] with the given human-readable reason. Per
// D-HSH-08 this tells the outgoing side to resume full ownership — Refuse
// must be called (rather than the incoming side just closing the
// connection) so the outgoing side gets a definite, fast answer instead of
// discovering the refusal via a timeout.
func Refuse(conn *net.UnixConn, reason string) error {
	return sendAck(conn, Ack{OK: false, Reason: reason, SupportedVersion: CurrentVersion})
}

func sendAck(conn *net.UnixConn, ack Ack) error {
	data, err := json.Marshal(ack)
	if err != nil {
		return fmt.Errorf("handoff: marshal ack: %w", err)
	}
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("handoff: send ack: %w", err)
	}
	return nil
}

func receiveAck(conn *net.UnixConn) (Ack, error) {
	buf := make([]byte, 64*1024)
	n, err := conn.Read(buf)
	if err != nil {
		return Ack{}, fmt.Errorf("handoff: receive ack: %w", err)
	}
	var ack Ack
	if err := json.Unmarshal(buf[:n], &ack); err != nil {
		return Ack{}, fmt.Errorf("handoff: unmarshal ack: %w", err)
	}
	return ack, nil
}
