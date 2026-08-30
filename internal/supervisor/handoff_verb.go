package supervisor

import (
	"context"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/IniZio/nexus3/internal/supervisor/handoff"
)

// handoffDialTimeout bounds how long performHandoff waits to reach the
// replacement's handoff socket. A replacement that never listens (crashed
// before Accept, wrong path) must not hang the outgoing supervisor forever —
// per D-HSH-08 the outgoing side falls back to "resume full ownership" the
// moment this deadline, or any other step, fails.
const handoffDialTimeout = 5 * time.Second

// handoffAckTimeout bounds how long performHandoff blocks in [handoff.Offer]
// waiting for the replacement's Ack after the payload is sent.
const handoffAckTimeout = 30 * time.Second

// payloadBuilder assembles the [handoff.Payload] and the perimeter fd to
// offer, using whatever live state RunDetached has at the moment
// /supervisor/handoff is called (the perimeter supervisor, credential broker,
// governor bounds). Returns a nil *os.File when there is no perimeter fd to
// transfer (Payload.Perimeter.Present is then false).
//
// Only Payload.Version, Payload.Perimeter and Payload.Governor are populated
// by [defaultPayloadBuilder] in this slice. Payload.CA, Payload.Credentials
// and Payload.Virtiofs require accessors this slice's owning packages
// (mitm.Proxy CA key export, cred.Broker placeholder-map dump, virtiofsd pid
// tracking) do not yet expose — see the ticket's "open items" section. A
// handoff attempted today therefore hands over network egress and the
// governor's boot bounds correctly, but the replacement must re-seed the MITM
// CA, credentials, and any live virtiofs mounts itself until a follow-up
// slice wires those fields.
type payloadBuilder func() (handoff.Payload, *os.File, error)

// performHandoff dials peerSock as a Unix STREAM socket (the replacement's
// handoff socket — a plain net.ListenUnix("unix", ...), the same network type
// [ipc.go]'s IPC socket already uses). handoff.Offer/Accept work over any
// *net.UnixConn regardless of SOCK_STREAM vs SOCK_DGRAM (WriteMsgUnix/
// ReadMsgUnix carry SCM_RIGHTS ancillary data either way); STREAM is used
// here specifically because it gives a naturally connected, symmetric
// two-way conn from Dial/Accept with no peer-address bookkeeping — a
// connectionless SOCK_DGRAM socket bound only via Listen cannot reply with a
// plain Write (the call [handoff.Confirm]/[handoff.Refuse] make) unless the
// far end is also explicitly connected, which a bare rendezvous-by-path
// dial/listen pair does not give you.
//
// performHandoff dials peerSock (the replacement's handoff socket), builds
// the payload via build, and offers it via [handoff.Offer]. It returns
// (true, "", nil) only when the replacement's Ack is OK — the caller (the
// /supervisor/handoff HTTP handler) may then treat this supervisor as safe to
// detach. Every other outcome — dial failure, build failure, Offer transport
// failure, or a negative Ack — returns (false, reason, err) and leaves every
// resource the caller owns untouched: performHandoff never closes the live
// perimeter fd (only a dup, via [perimeter.PerimeterSupervisor.PerimeterFD]),
// never mutates the broker, and never touches detachCh itself (the caller
// does that only after this function reports success). This is what makes a
// refused or failed handoff resumable per D-HSH-08.
func performHandoff(ctx context.Context, peerSock string, build payloadBuilder) (ok bool, reason string, err error) {
	dialCtx, cancel := context.WithTimeout(ctx, handoffDialTimeout)
	defer cancel()

	raw, dialErr := (&net.Dialer{}).DialContext(dialCtx, "unix", peerSock)
	if dialErr != nil {
		return false, "", fmt.Errorf("handoff: dial replacement at %s: %w", peerSock, dialErr)
	}
	conn, isUnix := raw.(*net.UnixConn)
	if !isUnix {
		raw.Close()
		return false, "", fmt.Errorf("handoff: dial replacement at %s: unexpected conn type %T", peerSock, raw)
	}
	defer conn.Close()

	payload, fdFile, buildErr := build()
	if buildErr != nil {
		return false, "", fmt.Errorf("handoff: build payload: %w", buildErr)
	}
	fd := -1
	if fdFile != nil {
		fd = int(fdFile.Fd())
		// The File wrapper (and the fd it names) is our dup, made solely for
		// this offer. Whether the peer accepted it or not, our copy is
		// disposable once Offer returns — SCM_RIGHTS gave the peer its own
		// independent copy on success; on failure nobody but us ever held it.
		defer fdFile.Close()
	}

	// handoff.Offer takes a *net.UnixConn, not a context; bound the blocking
	// Ack read with a wall-clock deadline instead.
	_ = conn.SetDeadline(time.Now().Add(handoffAckTimeout))

	ack, offerErr := handoff.Offer(conn, payload, fd)
	if offerErr != nil {
		return false, "", fmt.Errorf("handoff: offer: %w", offerErr)
	}
	if !ack.OK {
		return false, ack.Reason, nil
	}
	return true, "", nil
}
