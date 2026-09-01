package supervisor

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"github.com/IniZio/nexus3/internal/core/perimeter"
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
// offer, using whatever live state RunDetached/RunAdopt has at the moment
// /supervisor/handoff is called (the perimeter supervisor, credential broker,
// governor bounds). Returns a nil *os.File when there is no perimeter fd to
// transfer (Payload.Perimeter.Present is then false).
//
// Payload.Version, Payload.Perimeter, Payload.Governor and Payload.CA are
// populated by [buildHandoffPayload]. Payload.Credentials and
// Payload.Virtiofs still require accessors this motive's owning packages
// (cred.Broker placeholder-map dump, virtiofsd pid tracking) do not yet
// expose — see the ticket's "open items" section.
type payloadBuilder func() (handoff.Payload, *os.File, error)

// buildHandoffPayload is the ONE payload-construction implementation both
// RunDetached and RunAdopt call. It exists as a named, directly-callable
// function specifically so a test can invoke the exact code path a real
// handoff uses — the class of bug that let a motive-long, always-refusing
// handoff pass every unit suite was every test hand-rolling its own payload
// instead of calling this (motive nexus3-host-supervisor-hotswap, ticket 08
// gate finding). Any future field this function forgets to populate is a bug
// that same test will catch; a hand-rolled payload in a test would not.
//
// sup must be non-nil and have a live MITM proxy (CAKeyPair returns an
// error otherwise, which is logged and produces a payload with an empty CA —
// exactly the shape [handoff.Payload.Validate] refuses, which is correct:
// AllowAll sandboxes with no proxy have nothing to hand off account for
// here, and any other supervisor without a proxy will simply have every
// handoff attempt refused too).
func buildHandoffPayload(sup *perimeter.PerimeterSupervisor, sandboxRef string, bootVCPUs uint32, memoryMiB uint32) (handoff.Payload, *os.File, error) {
	var fdFile *os.File
	present := false
	if f, ferr := sup.PerimeterFD(); ferr == nil {
		fdFile = f
		present = true
	} else {
		slog.Warn("supervisor.handoff_no_perimeter_fd", "sandboxRef", sandboxRef, "err", ferr)
	}
	var ca handoff.CAMaterial
	if certPEM, keyPEM, caErr := sup.CAKeyPair(); caErr == nil {
		ca = handoff.CAMaterial{CertPEM: certPEM, KeyPEM: keyPEM}
	} else {
		slog.Warn("supervisor.handoff_no_ca", "sandboxRef", sandboxRef, "err", caErr)
	}
	return handoff.Payload{
		Version:   handoff.CurrentVersion,
		Perimeter: handoff.PerimeterHandle{Present: present},
		Governor: handoff.GovernorConfig{
			VCPUCount: int(bootVCPUs),
			MemoryMB:  uint64(memoryMiB),
		},
		CA: ca,
		// Credentials and Virtiofs are intentionally NOT populated in this
		// slice — see this function's doc comment for the missing
		// accessors this depends on (open item, ticket 04).
	}, fdFile, nil
}

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
func performHandoff(ctx context.Context, peerSock string, build payloadBuilder, hasMITMProxy bool) (ok bool, reason string, err error) {
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
	// Validate the payload before sending it. A partial handoff — where the
	// replacement inherits the perimeter fd but not the MITM CA key — is
	// worse than no handoff. The outgoing supervisor must stay alive until
	// all fields are wired (D-HSH-08).
	if reason := payload.Validate(hasMITMProxy); reason != "" {
		return false, reason, nil
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
