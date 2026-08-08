// Package wire implements the nexus3 data-plane session wire protocol —
// the framed byte stream that carries interactive stdio for one session over
// a single connection.
//
// # Two-plane split
//
// nexus3 uses two independent protocol planes:
//
//  1. Control plane — gRPC over vsock (package agentpb). Small request/response
//     RPCs: Exec/Spawn, Signal, SessionStatus, Copy, etc. The control plane
//     does NOT carry stdio bytes; see internal/core/agent/agentpb.
//
//  2. Data plane — this package. One multiplexed, clawk-style typed-frame
//     connection per guest data port; session_id in the Handshake frame
//     demuxes concurrent sessions. No gRPC, no protobuf.
//
// # Frame format
//
// All integers are big-endian. Each frame begins with a 5-byte header:
//
//	[4 bytes] payload length (uint32) — number of bytes that follow the header
//	[1 byte]  frame type (FrameType)
//
// Payload layouts per type are defined by the frame structs in wire.go.
//
// # Reattach handshake
//
// When the host reconnects to a running (or recently exited) session it sends a
// [Handshake] frame containing session_id and resume_from_offset; the guest
// replies with a [HandshakeAck] (alive, or exited with exit code) and then
// streams [Data] frames beginning at resume_from_offset bytes into the
// guest-authoritative output ring.
//
// The output ring itself lives in the in-guest agent (cmd/nexus3-agent, a later
// slice); this package only defines the wire contract.  resume_from_offset is a
// byte offset into the combined (stdout+stderr interleaved) output history that
// the guest ring maintains.  The host tracks offsets; the guest is authoritative
// over the ring contents.
//
// # DataPort
//
// [DataPort] (1025) is the fixed vsock port the guest data-plane listener binds
// to.  It is one above driver.AgentControlPort (1024), which carries control
// traffic, keeping the two ports adjacent and legible in logs without risking a
// collision with well-known service ports.
package wire
