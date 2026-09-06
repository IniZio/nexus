// ch_netns_control.go — the netns child's control socket: how a REPLACEMENT
// supervisor re-acquires the perimeter after the previous supervisor died,
// with no live sender to pass an fd from.
//
// # Why this exists (D-HSH-16, D-HSH-17)
//
// The perimeter fd is one end of an AF_UNIX SOCK_DGRAM socketpair. The netns
// child holds the pump end; the supervisor holds the perimeter end. When the
// supervisor is SIGKILLed, its end dies — but the child, the TAP fd, and the
// VM all survive. The guest goes network-dead in the host→guest direction
// only, because that pump goroutine exited on the read error.
//
// The tap CANNOT be re-opened by a new process: openHostTap sets
// IFF_TAP|IFF_NO_PI with no IFF_MULTI_QUEUE, so exactly one fd may be
// attached, and the surviving child holds it (TUNSETIFF returns EBUSY for
// anyone else). That is D-HSH-16 and it permanently closes tap re-entry.
//
// But the tap never needed to move. The child is already the per-workload
// shim that outlives its manager (the shape containerd-shim and conmon use);
// it just had no control channel. This file is that channel: an incoming
// supervisor creates a FRESH socketpair, connects, sends the pump end over
// SCM_RIGHTS, keeps the perimeter end, and the child swaps the new pump end
// into its live tapPump. No tap fd moves, no pidfd_getfd, no systemd.
//
// # Authentication (why the prior art is NOT inheritable)
//
// containerd and conmon authenticate reconnects by socket path alone — the
// path IS the identity. That is acceptable for stdio. It is NOT acceptable
// here: this channel reaches the privileged side of the egress-enforcement
// perimeter. A peer that succeeds in swapping its own conn into the pump
// reads and injects ALL guest traffic, bypassing the MITM proxy and the
// egress policy entirely — precisely what the perimeter exists to prevent.
//
// So the child authenticates the peer on three independent axes, all
// fail-closed, all evaluated before the fd is touched:
//
//  1. FILESYSTEM. The socket lives in a 0700 directory owned by the same uid
//     as the child, and the socket itself is bound at 0600 (umask-independent:
//     the listener is chmod'd before any connect can succeed). A different uid
//     cannot reach the inode at all. This is the cheap outer gate; it is not
//     sufficient alone, because every process of the SAME uid can still reach
//     the path — including a compromised in-sandbox helper.
//
//  2. SO_PEERCRED. The kernel reports the connecting peer's uid/gid/pid; the
//     values are stamped at connect(2) time by the kernel and cannot be forged
//     by the peer. The child refuses any peer whose uid is not its own uid.
//     This is what makes axis 1 enforceable rather than advisory, and it also
//     yields the peer PID that axis 3 needs — a pid obtained from the kernel
//     rather than one the peer claims for itself.
//
//  3. A SHARED SECRET the peer must prove it can read. The uid check alone is
//     too coarse: everything this user runs shares that uid. The child mints a
//     32-byte random token at startup and writes it to a 0600 file next to the
//     socket. A legitimate replacement supervisor is by definition a process
//     that can read the sandbox's state directory, so it can read the token; a
//     process that merely FOUND the socket path cannot present it. The token is
//     compared with crypto/subtle.ConstantTimeCompare.
//
// Axis 3 also carries the pid-reuse discipline the ticket requires. The
// request names the sandbox ID and the child pid + child starttime the
// replacement believes it is talking to, read from the persisted record. The
// child compares them against its OWN identity, read from its own
// /proc/self/stat. A mismatch REFUSES. This is the same fail-closed shape
// AdoptNetnsRuntime applies from the other direction — there, the supervisor
// verifies the child has not been recycled; here, the child verifies the
// supervisor is not addressing a recycled pid that happens to be this
// process. Both directions must hold for the swap to be safe.
package cloudhypervisor

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// ControlProtocolVersion is the wire version this binary speaks on the netns
// control socket. Like the handoff payload, this channel is by construction
// an old process talking to a new binary, so the version is explicit from
// day one; a child that does not recognise the version REFUSES rather than
// guessing at the layout.
const ControlProtocolVersion = 1

// controlTokenBytes is the length of the shared-secret token the child mints
// at startup. 32 bytes of crypto/rand is well beyond brute-force reach for a
// value that is only ever compared, never transmitted in the clear off-host.
const controlTokenBytes = 32

// controlRequestTimeout bounds how long the child will wait for a connected
// peer to send a complete, well-formed request. A peer that connects and
// then stalls must not be able to hold the accept loop or leak a connection
// indefinitely.
const controlRequestTimeout = 10 * time.Second

// controlMaxRequestBytes bounds a single control request. The request is
// small (a version, a sandbox id, two integers, a hex token); this is
// generous headroom that still refuses a hostile peer before it can force an
// unbounded allocation.
const controlMaxRequestBytes = 64 * 1024

// ControlRequest is the message an incoming supervisor sends on the netns
// control socket to re-acquire the perimeter. It travels as the regular
// (non-ancillary) data of a single SOCK_STREAM write; the fresh pump-end fd
// rides as SCM_RIGHTS ancillary data on that same write.
type ControlRequest struct {
	// Version identifies the shape of this request. A child that does not
	// recognise it refuses rather than guessing.
	Version int `json:"version"`

	// Token is the hex-encoded shared secret the child wrote to its token
	// file at startup. A peer that cannot read that file cannot produce it.
	Token string `json:"token"`

	// SandboxID is the sandbox the caller believes this child serves.
	// Refused if it does not match the child's own sandbox ID.
	SandboxID string `json:"sandbox_id"`

	// ExpectChildPID and ExpectChildStartTime are the netns child identity
	// the caller read from the persisted sandbox record. The child refuses
	// unless BOTH match its own live identity — the pid-reuse guard extended
	// to this channel, so a replacement that is addressing a recycled pid is
	// told so rather than handed the perimeter of an unrelated VM.
	ExpectChildPID       int    `json:"expect_child_pid"`
	ExpectChildStartTime uint64 `json:"expect_child_start_time"`
}

// ControlResponse is the child's answer. OK true means the fd was swapped
// into the live pump and the caller's retained perimeter end is now the
// authoritative one; OK false means NOTHING changed — the child did not
// touch its pump, and the caller must close the fd it sent and treat the
// perimeter as un-acquired (the fail-closed rail: a partial perimeter is
// worse than none).
type ControlResponse struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`

	// SupportedVersion is the version this child speaks, set on a version
	// mismatch refusal so the caller can report something actionable.
	SupportedVersion int `json:"supported_version,omitempty"`
}

// netnsControlDir returns the directory the netns control socket and token
// live in, given the driver's CH socket directory. It is a dedicated
// subdirectory rather than socketDir itself because the child asserts 0700
// on it, and socketDir holds the CH API sockets whose permissions this
// mechanism does not own.
func netnsControlDir(socketDir string) string {
	return filepath.Join(socketDir, "netns-control")
}

// ControlSocketPath returns the path of the control socket for a sandbox,
// given the directory the child was told to place it in. Deriving it from
// the sandbox ID (rather than, say, the pid) keeps the path stable across
// the child's whole life and lets a replacement supervisor compute it from
// the persisted record alone.
func ControlSocketPath(dir string, id string) string {
	return filepath.Join(dir, "netns-control-"+id+".sock")
}

// ControlTokenPath returns the path of the shared-secret token file matching
// ControlSocketPath.
func ControlTokenPath(dir string, id string) string {
	return filepath.Join(dir, "netns-control-"+id+".token")
}

// netnsControlServer is the child-side listener. It owns the control socket,
// the token, and the reference to the live pump it swaps conns into.
type netnsControlServer struct {
	ln        *net.UnixListener
	sockPath  string
	tokenPath string
	token     []byte
	sandboxID string
	selfPID   int
	selfStart uint64
	pump      *swappableConn
	onSwap    func() // optional; called after a successful swap (tests)
}

// startNetnsControlServer binds the control socket for sandboxID under dir,
// mints and writes the shared-secret token, and returns a server ready to
// serve. It performs the filesystem half of the authentication design:
//
//   - dir is created 0700 and its mode is asserted (a pre-existing dir with
//     looser permissions is a refusal, not something to silently tighten and
//     proceed on — another process may already hold a descriptor to it).
//   - the socket is chmod'd 0600 immediately after bind and BEFORE any
//     connect can be accepted, so the permission window is not umask-dependent.
//   - the token file is written 0600 via O_EXCL through a fresh temp name and
//     renamed, so a pre-existing attacker-planted file cannot be inherited.
//
// The caller must call Close when the child exits.
func startNetnsControlServer(dir, sandboxID string, pump *swappableConn) (*netnsControlServer, error) {
	if pump == nil {
		return nil, fmt.Errorf("cloudhypervisor: netns control: pump is nil")
	}
	if sandboxID == "" {
		return nil, fmt.Errorf("cloudhypervisor: netns control: sandboxID is empty")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cloudhypervisor: netns control: mkdir %s: %w", dir, err)
	}
	// Assert the mode rather than trusting MkdirAll's result: MkdirAll is a
	// no-op on an existing directory, so a directory some other process
	// created 0777 would otherwise pass silently and defeat axis 1.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("cloudhypervisor: netns control: chmod %s: %w", dir, err)
	}

	selfStat, err := ReadProcStat(os.Getpid())
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: netns control: read own /proc stat: %w", err)
	}

	token := make([]byte, controlTokenBytes)
	if _, err := rand.Read(token); err != nil {
		return nil, fmt.Errorf("cloudhypervisor: netns control: mint token: %w", err)
	}

	sockPath := ControlSocketPath(dir, sandboxID)
	tokenPath := ControlTokenPath(dir, sandboxID)

	// A stale socket inode from a previous child of the same sandbox would
	// make bind fail with EADDRINUSE; that child is gone (this process is
	// the live one), so removing it is correct rather than destructive.
	_ = os.Remove(sockPath)

	rawLn, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: netns control: listen %s: %w", sockPath, err)
	}
	ln, ok := rawLn.(*net.UnixListener)
	if !ok {
		rawLn.Close()
		return nil, fmt.Errorf("cloudhypervisor: netns control: unexpected listener type %T", rawLn)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		ln.Close()
		_ = os.Remove(sockPath)
		return nil, fmt.Errorf("cloudhypervisor: netns control: chmod socket: %w", err)
	}

	if err := writeTokenFile(tokenPath, token); err != nil {
		ln.Close()
		_ = os.Remove(sockPath)
		return nil, err
	}

	return &netnsControlServer{
		ln:        ln,
		sockPath:  sockPath,
		tokenPath: tokenPath,
		token:     token,
		sandboxID: sandboxID,
		selfPID:   os.Getpid(),
		selfStart: selfStat.StartTime,
		pump:      pump,
	}, nil
}

// writeTokenFile writes tok to path with 0600, atomically, refusing to reuse
// any pre-existing inode at the temp name (O_EXCL).
func writeTokenFile(path string, tok []byte) error {
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: netns control: create token file: %w", err)
	}
	if _, err := f.WriteString(hex.EncodeToString(tok)); err != nil {
		f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("cloudhypervisor: netns control: write token file: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cloudhypervisor: netns control: close token file: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("cloudhypervisor: netns control: install token file: %w", err)
	}
	return nil
}

// Close stops the listener and removes the socket and token file.
func (s *netnsControlServer) Close() {
	if s.ln != nil {
		s.ln.Close()
	}
	_ = os.Remove(s.sockPath)
	_ = os.Remove(s.tokenPath)
}

// Serve accepts control connections until the listener is closed. It is
// intended to run in a goroutine for the whole life of the netns child.
//
// Every connection is handled to completion (authenticate, then either swap
// or refuse) before the next is accepted. The swap mutates the single live
// pump, so serialising is both simpler and correct; the connection deadline
// bounds how long any one peer can occupy the loop.
func (s *netnsControlServer) Serve() {
	for {
		conn, err := s.ln.AcceptUnix()
		if err != nil {
			// Listener closed (child shutting down) or a transient accept
			// error; either way there is nothing to serve.
			return
		}
		s.handle(conn)
		conn.Close()
	}
}

// handle authenticates one peer and, only if every gate passes, swaps the
// received fd into the live pump.
//
// FAIL-CLOSED CONTRACT: every early return leaves the pump untouched and any
// received fd closed. The pump is mutated at exactly one place in this
// function, after all gates, and a swap failure there is still reported as a
// refusal. A caller that receives OK=false has acquired nothing.
func (s *netnsControlServer) handle(conn *net.UnixConn) {
	// A panic here would otherwise unwind through Serve's goroutine and
	// terminate the netns child — which kills CH via Pdeathsig and destroys
	// the VM. This handler parses input from a peer that may be hostile, so
	// the blast radius of an unexpected panic is the whole guest. Contain it
	// and refuse: a refused re-acquisition leaves the VM running, which is
	// the fail-closed outcome; a dead child is the one outcome this whole
	// mechanism exists to avoid.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("cloudhypervisor: netns control: panic handling control request; refusing",
				"panic", r)
			s.refuse(conn, "internal error handling control request", 0)
		}
	}()

	_ = conn.SetDeadline(time.Now().Add(controlRequestTimeout))

	// ── Gate 2: SO_PEERCRED, before reading a single byte of peer-supplied
	// data. A peer that fails this never gets to influence anything else. ──
	ucred, err := peerCred(conn)
	if err != nil {
		s.refuse(conn, "peer credentials unavailable", 0)
		return
	}
	if ucred.Uid != uint32(os.Getuid()) { //nolint:gosec // getuid is always in uint32 range
		s.refuse(conn, "peer uid is not authorised for this control socket", 0)
		return
	}

	req, fdFile, err := readControlRequest(conn)
	if err != nil {
		if fdFile != nil {
			fdFile.Close()
		}
		s.refuse(conn, "malformed control request", 0)
		return
	}
	// From here on every refusal must dispose of the fd the peer sent: it is
	// our own SCM_RIGHTS duplicate, so closing it cannot affect the peer's
	// copy, and NOT closing it would leak a descriptor per refused attempt.
	defer func() {
		if fdFile != nil {
			fdFile.Close()
		}
	}()

	if req.Version != ControlProtocolVersion {
		s.refuse(conn, fmt.Sprintf("unsupported control version %d", req.Version), ControlProtocolVersion)
		return
	}

	// ── Gate 3a: shared secret, constant-time. ──
	presented, err := hex.DecodeString(req.Token)
	if err != nil || subtle.ConstantTimeCompare(presented, s.token) != 1 {
		s.refuse(conn, "invalid control token", 0)
		return
	}

	// ── Gate 3b: this child is the one the caller means. ──
	if req.SandboxID != s.sandboxID {
		s.refuse(conn, "sandbox id does not match this netns child", 0)
		return
	}

	// ── Gate 3c: pid-reuse discipline, from the child's side. The caller
	// tells us which pid+starttime it read from the record; we compare
	// against our own live identity. A replacement addressing a recycled pid
	// is refused rather than handed a perimeter into the wrong VM. ──
	if req.ExpectChildPID != s.selfPID {
		s.refuse(conn, "expected child pid does not match this process", 0)
		return
	}
	if req.ExpectChildStartTime == 0 || req.ExpectChildStartTime != s.selfStart {
		s.refuse(conn, "expected child starttime does not match this process (pid recycled or identity lost)", 0)
		return
	}

	// ── The fd itself must be present. A request that passed every gate but
	// carries no fd is still a refusal: swapping in nothing would leave the
	// pump pointed at a dead conn, which reads as working. ──
	if fdFile == nil {
		s.refuse(conn, "request carries no pump fd", 0)
		return
	}
	newConn, err := net.FileConn(fdFile)
	if err != nil {
		s.refuse(conn, "received fd is not a usable connection", 0)
		return
	}

	// ── The swap. The single mutation point. ──
	if err := s.pump.swap(newConn); err != nil {
		_ = newConn.Close()
		s.refuse(conn, "pump is shutting down", 0)
		return
	}

	_ = writeControlResponse(conn, ControlResponse{OK: true})
	if s.onSwap != nil {
		s.onSwap()
	}
}

func (s *netnsControlServer) refuse(conn *net.UnixConn, reason string, supported int) {
	_ = writeControlResponse(conn, ControlResponse{OK: false, Reason: reason, SupportedVersion: supported})
}

// peerCred reads SO_PEERCRED from a connected Unix socket. The values are
// stamped by the kernel at connect(2) time and cannot be forged by the peer.
func peerCred(conn *net.UnixConn) (*syscall.Ucred, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: netns control: syscall conn: %w", err)
	}
	var ucred *syscall.Ucred
	var credErr error
	ctrlErr := raw.Control(func(fd uintptr) {
		ucred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if ctrlErr != nil {
		return nil, fmt.Errorf("cloudhypervisor: netns control: raw control: %w", ctrlErr)
	}
	if credErr != nil {
		return nil, fmt.Errorf("cloudhypervisor: netns control: getsockopt SO_PEERCRED: %w", credErr)
	}
	return ucred, nil
}

// readControlRequest reads one JSON request plus its optional SCM_RIGHTS fd.
func readControlRequest(conn *net.UnixConn) (ControlRequest, *os.File, error) {
	buf := make([]byte, controlMaxRequestBytes)
	oob := make([]byte, syscall.CmsgSpace(4))

	n, oobn, _, _, err := conn.ReadMsgUnix(buf, oob)
	if err != nil {
		return ControlRequest{}, nil, fmt.Errorf("cloudhypervisor: netns control: read request: %w", err)
	}

	var f *os.File
	if oobn > 0 {
		fd, perr := parseSingleControlFD(oob[:oobn])
		if perr != nil {
			return ControlRequest{}, nil, perr
		}
		if fd >= 0 {
			f = os.NewFile(uintptr(fd), "netns-control-pump")
		}
	}

	var req ControlRequest
	if err := json.Unmarshal(buf[:n], &req); err != nil {
		return ControlRequest{}, f, fmt.Errorf("cloudhypervisor: netns control: unmarshal request: %w", err)
	}
	return req, f, nil
}

// parseSingleControlFD extracts exactly one fd from the SCM_RIGHTS control
// message in oob. A peer that attaches more than one is a protocol violation
// rather than something to silently truncate: every extra fd is closed and
// the request is refused, so a hostile peer cannot use a multi-fd message to
// leak descriptors into this process.
func parseSingleControlFD(oob []byte) (int, error) {
	scms, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return -1, fmt.Errorf("cloudhypervisor: netns control: parse control message: %w", err)
	}
	var fds []int
	for _, scm := range scms {
		got, err := syscall.ParseUnixRights(&scm)
		if err != nil {
			return -1, fmt.Errorf("cloudhypervisor: netns control: parse unix rights: %w", err)
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
		return -1, fmt.Errorf("cloudhypervisor: netns control: expected at most 1 fd, got %d", len(fds))
	}
}

func writeControlResponse(conn *net.UnixConn, resp ControlResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("cloudhypervisor: netns control: marshal response: %w", err)
	}
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("cloudhypervisor: netns control: write response: %w", err)
	}
	return nil
}

// ErrControlRefused is returned by ReacquirePerimeter when the child
// answered but REFUSED. It is distinguished from a transport error because
// the two demand different operator responses: a refusal is a definite "the
// perimeter was not acquired and the VM is untouched", whereas a transport
// error leaves the outcome unknown and the caller must still treat it as
// un-acquired.
var ErrControlRefused = errors.New("cloudhypervisor: netns control: child refused re-acquisition")

// ReacquirePerimeter is the SUPERVISOR side of the control channel: the
// crash-path counterpart to handoff.Offer.
//
// It creates a FRESH socketpair, sends the pump end to the surviving netns
// child over its control socket (with the shared-secret token read from
// tokenPath and the identity the caller read from the persisted record), and
// on a positive response returns the perimeter end as an *os.File — exactly
// the shape AdoptNetnsRuntime already takes, so the crash path converges
// with the handoff path at the same seam.
//
// FAIL-CLOSED: on ANY error, including a refusal, both ends of the fresh
// socketpair are closed and nil is returned. The caller acquires nothing
// and must not proceed to adopt. The child's pump is likewise untouched on
// every refusal path, so the VM is left exactly as it was — a partially
// rebuilt perimeter is never produced.
func ReacquirePerimeter(sockPath, tokenPath, sandboxID string, childPID int, childStartTime uint64) (*os.File, error) {
	if childPID <= 0 {
		return nil, fmt.Errorf("cloudhypervisor: ReacquirePerimeter: childPID=%d must be positive", childPID)
	}
	if childStartTime == 0 {
		return nil, fmt.Errorf("cloudhypervisor: ReacquirePerimeter: childStartTime is 0; refusing to re-acquire without a pid-reuse guard")
	}
	if sandboxID == "" {
		return nil, fmt.Errorf("cloudhypervisor: ReacquirePerimeter: sandboxID is empty")
	}

	tokenHex, err := os.ReadFile(tokenPath)
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: ReacquirePerimeter: read control token: %w", err)
	}

	conn, err := net.DialTimeout("unix", sockPath, controlRequestTimeout)
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: ReacquirePerimeter: dial %s: %w", sockPath, err)
	}
	defer conn.Close()
	uconn, ok := conn.(*net.UnixConn)
	if !ok {
		return nil, fmt.Errorf("cloudhypervisor: ReacquirePerimeter: unexpected conn type %T", conn)
	}
	_ = uconn.SetDeadline(time.Now().Add(controlRequestTimeout))

	perimFile, pumpFile, err := netnsSocketpairFiles()
	if err != nil {
		return nil, fmt.Errorf("cloudhypervisor: ReacquirePerimeter: %w", err)
	}
	// pumpFile is transferred by value (SCM_RIGHTS duplicates it); this
	// process closes its own copy either way. perimFile is closed on every
	// failure path below and returned to the caller only on success.
	defer pumpFile.Close()

	req := ControlRequest{
		Version:              ControlProtocolVersion,
		Token:                string(tokenHex),
		SandboxID:            sandboxID,
		ExpectChildPID:       childPID,
		ExpectChildStartTime: childStartTime,
	}
	data, err := json.Marshal(req)
	if err != nil {
		perimFile.Close()
		return nil, fmt.Errorf("cloudhypervisor: ReacquirePerimeter: marshal request: %w", err)
	}
	oob := syscall.UnixRights(int(pumpFile.Fd()))
	if _, _, err := uconn.WriteMsgUnix(data, oob, nil); err != nil {
		perimFile.Close()
		return nil, fmt.Errorf("cloudhypervisor: ReacquirePerimeter: send request: %w", err)
	}

	respBuf := make([]byte, controlMaxRequestBytes)
	n, err := uconn.Read(respBuf)
	if err != nil {
		perimFile.Close()
		return nil, fmt.Errorf("cloudhypervisor: ReacquirePerimeter: read response: %w", err)
	}
	var resp ControlResponse
	if err := json.Unmarshal(respBuf[:n], &resp); err != nil {
		perimFile.Close()
		return nil, fmt.Errorf("cloudhypervisor: ReacquirePerimeter: unmarshal response: %w", err)
	}
	if !resp.OK {
		perimFile.Close()
		return nil, fmt.Errorf("%w: %s", ErrControlRefused, resp.Reason)
	}

	return perimFile, nil
}
