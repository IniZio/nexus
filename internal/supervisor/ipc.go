package supervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/agent/wire"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/service"
)

// allowEgressFunc is the callback type the IPC egress-allow handler uses to
// mutate the live perimeter. It is nil until the perimeter supervisor is ready.
type allowEgressFunc func(host string) error

// ipcStopPath is the HTTP path for the graceful-stop request. /supervisor/stop
// means "tear the VM down" — svc.Stop/svc.Remove runs in RunDetached's shutdown
// switch. This is strictly distinct from ipcDetachPath, which means "exit
// without touching the VM" (D-HSH-09/hotswap slice 04). Keeping the two paths
// (and the two channels below) separate is load-bearing: collapsing them would
// silently turn every future /supervisor/detach caller into a VM-killer.
const ipcStopPath = "/supervisor/stop"

// ipcDetachPath is the HTTP path for the detach request: exit the supervisor
// process WITHOUT tearing the VM or perimeter down, leaving both running for a
// replacement supervisor to adopt. See ipcStopPath's doc comment for why this
// must never be unified with the stop path.
const ipcDetachPath = "/supervisor/detach"

// ipcHandoffPath is the HTTP path that drives a full handoff: offer the
// versioned payload (internal/supervisor/handoff) plus the perimeter fd to a
// connecting replacement, and detach ONLY on a confirmed positive Ack. Per
// D-HSH-08, any failure or refusal leaves this supervisor as the sole owner
// of the VM and perimeter — nothing is released until Confirm is observed.
const ipcHandoffPath = "/supervisor/handoff"

// ipcAgentHealthPath is the HTTP path for the guest-agent-RPC liveness check.
// It runs a LIVE, on-demand probe of both the gRPC control plane
// ([driver.AgentControlPort], used by Exec/Attach/Copy) and the wire data
// plane ([wire.DataPort], used by interactive PTY streams) every time it is
// called — it never answers from a cached/remembered status, because a
// remembered "healthy" is exactly the value that would paper over the defect
// this endpoint exists to catch (a wedged control plane behind a perimeter
// that still looks fine from the outside).
//
// `nexus3 supervisor-upgrade` uses this to decide whether a supervisor
// reporting the current binary hash (nothing to upgrade, by version) is
// nonetheless worth force-adopting because its agent channel is dead.
const ipcAgentHealthPath = "/supervisor/agent-health"

// AgentChannelState classifies the outcome of an agent-health probe. See
// [checkAgentHealth] for how each value is derived and why "unknown" must
// never be treated as healthy by a caller.
type AgentChannelState string

const (
	// AgentChannelHealthy means the control-plane gRPC Ping succeeded. The
	// guest agent is reachable end-to-end.
	AgentChannelHealthy AgentChannelState = "healthy"

	// AgentChannelDownGuestAlive means the control-plane Ping failed but a
	// raw dial to the data-plane port succeeded — the VM and its vsock
	// multiplexer are demonstrably up, so only the control-plane RPC path is
	// wedged. This is the exact shape of the incident this check exists to
	// catch: a reconnect (fresh dial/fresh grpc.ClientConn) is worth trying,
	// and a caller may safely force a re-adopt or retry without rebooting.
	AgentChannelDownGuestAlive AgentChannelState = "down_guest_alive"

	// AgentChannelGuestGone means BOTH probes failed with a "nothing is
	// listening" style error (connection refused, or the multiplexer socket
	// itself is absent) — the substrate side is gone, not merely the guest
	// agent's control-plane process. Reconnecting will not help; the caller
	// needs a real recovery path (stop/start), not a redial.
	AgentChannelGuestGone AgentChannelState = "guest_gone"

	// AgentChannelUnknown means the probe could not reach a confident
	// classification (e.g. both probes timed out without a definitive
	// refusal signal, or a probe input was unavailable). Per the fail-closed
	// rail, AgentChannelUnknown MUST be treated as "not proven healthy" by
	// every caller — never silently upgraded to Healthy just because the
	// probe didn't produce an outright refusal.
	AgentChannelUnknown AgentChannelState = "unknown"
)

// AgentHealth is the result of one [checkAgentHealth] probe.
type AgentHealth struct {
	State AgentChannelState `json:"state"`
	// ControlErr is the control-plane Ping error's message, empty on success.
	ControlErr string `json:"control_err,omitempty"`
	// DataErr is the data-plane dial error's message, empty on success.
	DataErr string `json:"data_err,omitempty"`
}

// Healthy reports whether h proves the agent channel is usable. Only
// [AgentChannelHealthy] counts — every other state (including Unknown) is
// treated as "not proven healthy" by design (fail-closed rail).
func (h AgentHealth) Healthy() bool {
	return h.State == AgentChannelHealthy
}

// agentHealthFunc is the callback type the IPC agent-health handler uses to
// run a live probe. It is nil until the driver/sandbox-id needed to build one
// are available (mirrors allowEgressFunc/handoffFunc's late-binding pattern).
type agentHealthFunc func(ctx context.Context) AgentHealth

// agentHealthProbeTimeout bounds each individual probe (control-plane Ping,
// data-plane dial) inside [checkAgentHealth]. Short and deliberate: this
// endpoint exists to be called when the caller ALREADY suspects a wedge, so
// it must return a verdict quickly rather than hanging as long as the thing
// it is diagnosing.
const agentHealthProbeTimeout = 5 * time.Second

// checkAgentHealth runs one live probe of the guest agent's control plane
// (gRPC Ping over [driver.AgentControlPort]) and data plane (raw dial over
// [wire.DataPort]) and classifies the result. It never returns a cached
// answer — every call re-dials both ports from scratch, which is what makes
// it a real "is the channel usable right now" check rather than a memory of
// some earlier success.
//
// gd or id being unusable (e.g. the driver does not implement GuestDialer)
// is treated as [AgentChannelUnknown], never as healthy — an absent input to
// a health check must never be read as "skip the check, assume fine".
func checkAgentHealth(ctx context.Context, gd driver.GuestDialer, id domain.SandboxID) AgentHealth {
	if gd == nil {
		return AgentHealth{State: AgentChannelUnknown, ControlErr: "no guest dialer available"}
	}

	ctx, cancel := context.WithTimeout(ctx, agentHealthProbeTimeout)
	defer cancel()

	client := agent.NewClient(gd, id)
	controlErr := client.Ping(ctx)
	if controlErr == nil {
		return AgentHealth{State: AgentChannelHealthy}
	}

	var dataErr error
	dataConn, dialErr := gd.DialGuest(ctx, id, wire.DataPort)
	if dialErr == nil {
		dataConn.Close()
	} else {
		dataErr = dialErr
	}

	return classifyAgentHealth(controlErr, dataErr)
}

// classifyAgentHealth is the pure decision function [checkAgentHealth] calls
// after running both live probes. Split out so the fail-closed classification
// rules can be mutation-tested directly, without standing up a real vsock
// dialer or guest agent: every input this function can receive (a nil error,
// a definite-refusal error, or an ambiguous error like a timeout) is
// representable as a plain Go error value.
//
// controlErr must be non-nil when this is called (a nil controlErr means
// Healthy and is handled by the caller before this function is reached).
// dataErr is nil when the data-plane dial succeeded.
func classifyAgentHealth(controlErr, dataErr error) AgentHealth {
	if dataErr == nil {
		// Data plane answered while control plane did not: the VM and its
		// vsock multiplexer are demonstrably alive, so this is specifically a
		// wedged control-plane channel, not a dead guest.
		return AgentHealth{
			State:      AgentChannelDownGuestAlive,
			ControlErr: controlErr.Error(),
		}
	}

	// Both probes failed. Distinguish "nothing is listening at all"
	// (multiplexer socket refused/absent — the guest is gone) from a
	// genuinely ambiguous result (e.g. both merely timed out, which does not
	// prove absence) by inspecting the error text for the multiplexer's own
	// refusal signatures. This is a text match on driver error wrapping, not
	// on error text from the guest, so it is stable across guest versions.
	//
	// Fail-closed: BOTH must independently show a definite refusal before this
	// classifies as guest_gone. A single definite refusal alongside an
	// ambiguous timeout on the other port is NOT enough — that combination
	// stays Unknown, never Healthy and never a false "gone" that would let a
	// caller give up on a channel that might still recover.
	if isDefiniteGuestGone(dataErr) && isDefiniteGuestGone(controlErr) {
		return AgentHealth{
			State:      AgentChannelGuestGone,
			ControlErr: controlErr.Error(),
			DataErr:    dataErr.Error(),
		}
	}

	return AgentHealth{
		State:      AgentChannelUnknown,
		ControlErr: controlErr.Error(),
		DataErr:    dataErr.Error(),
	}
}

// isDefiniteGuestGone reports whether err carries one of the unambiguous
// "nothing is there" signatures DialGuest/net produce when the vsock
// multiplexer socket itself is absent or refusing connections — as opposed
// to a timeout, which only proves "did not answer in time" and must not be
// promoted to "gone".
func isDefiniteGuestGone(err error) bool {
	if err == nil {
		return false
	}
	// Deliberately narrow: only the host-side dial itself refusing outright
	// (the AF_UNIX vsock multiplexer socket does not exist, or nothing is
	// listening on it) counts as "gone". A timeout, an EOF mid-handshake, or
	// "closed network connection" all mean "something answered/connected and
	// then the exchange failed" — exactly the wedged-but-alive case this
	// function must NOT misclassify as gone.
	s := err.Error()
	return strings.Contains(s, "no such file or directory") ||
		strings.Contains(s, "connection refused")
}

// ipcVersionPath is the HTTP path for the read-only binary-identity request.
// `nexus3 supervisor-upgrade` uses this to decide whether the running
// supervisor already serves the same binary as the one it was invoked from —
// in which case there is nothing to upgrade. The identity is a content hash
// of the running process's own executable, not the ldflags-embedded version
// string: the latter defaults to "0.0.0-dev" in unreleased builds and would
// make every dev build compare equal to every other.
const ipcVersionPath = "/supervisor/version"

// stopResponse is the JSON body returned on a successful stop or detach request.
type stopResponse struct {
	OK bool `json:"ok"`
}

// versionResponse is the JSON body returned for a version request.
type versionResponse struct {
	// BinaryHash is the hex-encoded SHA-256 of the running supervisor's own
	// executable file, computed once at process start.
	BinaryHash string `json:"binary_hash"`
}

// computeBinaryHash returns the hex-encoded SHA-256 of the current process's
// own executable, as resolved by os.Executable(). Both RunDetached and
// RunAdopt call this once at startup to obtain the identity they serve over
// ipcVersionPath, and `nexus3 supervisor-upgrade` calls it a second time
// (over its own os.Executable()) to compare against the value the running
// supervisor reports.
func computeBinaryHash() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("binary hash: resolve executable: %w", err)
	}
	f, err := os.Open(exe)
	if err != nil {
		return "", fmt.Errorf("binary hash: open %s: %w", exe, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("binary hash: read %s: %w", exe, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// handoffRequest is the JSON body of a POST to ipcHandoffPath. PeerSock is the
// filesystem path of the incoming (replacement) supervisor's handoff socket —
// a Unix STREAM socket (net.ListenUnix("unix", ...)) the replacement is
// listening on, ready to Accept the connection this side dials and then
// drive [handoff.Accept] over it.
type handoffRequest struct {
	PeerSock string `json:"peer_sock"`
}

// handoffResponse is the JSON body returned for a handoff request. OK is true
// only when the replacement confirmed and this supervisor is about to detach.
type handoffResponse struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

// handoffFunc performs one handoff attempt against the replacement listening
// at peerSock and reports whether it succeeded. Injected into serveIPC so the
// IPC layer stays decoupled from how the payload is assembled (broker state,
// perimeter fd, governor config — all live in RunDetached's closure).
type handoffFunc func(ctx context.Context, peerSock string) (ok bool, reason string, err error)

// ipcHandles bundles what serveIPC hands back to its caller: the two
// shutdown-signal channels and the listener, so the caller can perform the
// inode-checked socket cleanup in [removeOwnSocket] instead of an
// unconditional os.Remove that can unlink a replacement's freshly bound
// socket (D-HSH-09).
type ipcHandles struct {
	// StopCh is closed on a /supervisor/stop request: tear the VM down.
	StopCh <-chan struct{}
	// DetachCh is closed on a /supervisor/detach request, or on a
	// /supervisor/handoff request whose replacement confirmed: exit without
	// tearing the VM down.
	DetachCh <-chan struct{}
	// Listener is the bound Unix listener backing sockPath.
	Listener *net.UnixListener
	// BindStat is os.Stat(sockPath) taken immediately after this listener
	// bound sockPath. [removeOwnSocket] compares it against a fresh
	// os.Stat(sockPath) at cleanup time via os.SameFile.
	//
	// This is NOT redundant with Listener: fstat(2) on a bound AF_UNIX
	// socket's own fd reports the SOCKET's identity (sockfs — a different
	// device number entirely), not the filesystem directory entry the bind
	// created at sockPath. Only stat(2) on the PATH, taken at two points in
	// time, can tell whether the path still names the inode this listener
	// created.
	BindStat os.FileInfo
}

// serveIPC starts the Unix-domain HTTP IPC server at sockPath and returns the
// shutdown-signal channels described in [ipcHandles]. The server is shut down
// when ctx is cancelled.
//
// svc and sandboxRef are accepted for future extension (e.g. a /supervisor/status
// endpoint); the stop and detach handlers do not call svc.Stop/svc.Remove
// directly — they signal the RunDetached select loop, which distinguishes the
// two by shutdownCause and acts accordingly.
//
// allowEgress is a late-bound callback invoked by the /supervisor/egress-allow
// handler. It may be nil at call time (IPC starts before the perimeter
// supervisor is ready); a nil allowEgress causes the handler to return 503.
//
// handoff is the late-bound callback invoked by the /supervisor/handoff
// handler. It may be nil (e.g. before the perimeter/broker state needed to
// build a payload exists); a nil handoff causes the handler to return 503.
//
// agentHealth is the late-bound callback invoked by the /supervisor/agent-health
// handler. It may be nil (e.g. the driver does not implement GuestDialer); a
// nil agentHealth causes the handler to return 503 rather than guessing —
// per the fail-closed rail, an absent health-check capability must never be
// read by a caller as "assume healthy".
func serveIPC(ctx context.Context, sockPath string, _ *service.Service, _ string, allowEgress allowEgressFunc, handoff handoffFunc, agentHealth agentHealthFunc, binaryHash string) (ipcHandles, error) {
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return ipcHandles{}, fmt.Errorf("ipc: listen %s: %w", sockPath, err)
	}
	unixLn, ok := ln.(*net.UnixListener)
	if !ok {
		ln.Close()
		return ipcHandles{}, fmt.Errorf("ipc: listen %s: unexpected listener type %T", sockPath, ln)
	}
	bindStat, statErr := os.Stat(sockPath)
	if statErr != nil {
		ln.Close()
		return ipcHandles{}, fmt.Errorf("ipc: stat freshly bound %s: %w", sockPath, statErr)
	}

	stopCh := make(chan struct{})
	detachCh := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc(ipcStopPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(stopResponse{OK: true})
		// Signal RunDetached to proceed with shutdown. Closing is idempotent
		// via the sync.Once pattern below; repeated stop requests are safe.
		select {
		case <-stopCh:
		default:
			close(stopCh)
		}
	})

	mux.HandleFunc(ipcDetachPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(stopResponse{OK: true})
		// Signal RunDetached to exit WITHOUT tearing the VM down. Distinct
		// channel from stopCh — see ipcStopPath/ipcDetachPath doc comments.
		select {
		case <-detachCh:
		default:
			close(detachCh)
		}
	})

	mux.HandleFunc(ipcHandoffPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		var req handoffRequest
		if decErr := json.NewDecoder(r.Body).Decode(&req); decErr != nil || req.PeerSock == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(handoffResponse{OK: false, Reason: "peer_sock is required"})
			return
		}

		fn := handoff
		if fn == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(handoffResponse{OK: false, Reason: "handoff not yet available"})
			return
		}

		ok, reason, hErr := fn(r.Context(), req.PeerSock)
		if hErr != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(handoffResponse{OK: false, Reason: hErr.Error()})
			return
		}
		if !ok {
			// Per D-HSH-08: a refused or failed handoff is NOT an error at the
			// HTTP layer — it is a definite, resumable "no" reported with 200.
			// This supervisor remains the sole owner; detachCh is untouched.
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(handoffResponse{OK: false, Reason: reason})
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(handoffResponse{OK: true})
		// Replacement confirmed ownership: safe to detach now.
		select {
		case <-detachCh:
		default:
			close(detachCh)
		}
	})

	mux.HandleFunc(ipcAgentHealthPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		fn := agentHealth
		if fn == nil {
			// Fail closed: no way to run the probe is NOT the same as "healthy".
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(AgentHealth{State: AgentChannelUnknown, ControlErr: "agent health probe not available"})
			return
		}
		health := fn(r.Context())
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(health)
	})

	mux.HandleFunc(ipcVersionPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(versionResponse{BinaryHash: binaryHash})
	})

	mux.HandleFunc(ipcEgressAllowPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")

		req, err := DecodeEgressAllowRequest(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(EgressAllowResponse{OK: false, Error: err.Error()})
			return
		}

		fn := allowEgress
		if fn == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(EgressAllowResponse{OK: false, Error: "perimeter not yet ready"})
			return
		}

		if mutErr := fn(req.Host); mutErr != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(EgressAllowResponse{OK: false, Error: mutErr.Error()})
			return
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(EgressAllowResponse{OK: true})
	})

	srv := &http.Server{Handler: mux}

	// Serve in background; shut down when ctx is cancelled.
	go func() {
		_ = srv.Serve(ln)
	}()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		ln.Close()
	}()

	return ipcHandles{StopCh: stopCh, DetachCh: detachCh, Listener: unixLn, BindStat: bindStat}, nil
}

// removeOwnSocket unlinks sockPath only if it still refers to the same
// filesystem entry described by bindStat — i.e. only if no other process has
// unlinked and rebound sockPath since bindStat was captured.
//
// Fixes D-HSH-09 (slice 01 FD-ownership audit): the previous unconditional
// `defer os.Remove(sockPath)` in RunDetached could unlink a REPLACEMENT
// supervisor's freshly bound socket. A hotswap replacement can rebind
// sockPath (unlink + Listen) while the outgoing supervisor is still mid-exit
// and holding its own already-open listener fd — the outgoing process does
// not need the pathname to keep serving, so nothing breaks for it in the
// moment, but its deferred cleanup used to run last and blindly remove
// whatever file currently sat at that path, which by then could be the
// replacement's.
//
// The comparison MUST be stat(sockPath) vs stat(sockPath) at two points in
// time — NOT fstat on the listener's own fd. fstat(2) on a bound AF_UNIX
// socket reports the socket's own identity in sockfs (a distinct device
// number from any real filesystem), not the directory entry bind(2) created;
// comparing that against os.Stat(sockPath) never matches, even for the
// unreplaced case. bindStat, captured via os.Stat(sockPath) immediately
// after this process's own successful bind (see [serveIPC]), is what makes
// the comparison meaningful: bind/unlink/rebind changes the path's inode.
func removeOwnSocket(sockPath string, bindStat os.FileInfo) {
	if bindStat == nil {
		return
	}
	pathStat, err := os.Stat(sockPath)
	if err != nil {
		// Already gone (or never existed) — nothing to do.
		return
	}
	if !os.SameFile(bindStat, pathStat) {
		// sockPath now names a different inode: a replacement already rebound
		// it. Removing it here would unlink the replacement's socket.
		return
	}
	_ = os.Remove(sockPath)
}

// StopSupervisor sends a POST /supervisor/stop to the supervisor at sockPath
// and returns when the supervisor acknowledges. The caller should then poll
// for the supervisor PID to exit.
func StopSupervisor(ctx context.Context, sockPath string) error {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost"+ipcStopPath, nil)
	if err != nil {
		return fmt.Errorf("stop supervisor: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("stop supervisor: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("stop supervisor: unexpected status %d", resp.StatusCode)
	}
	var result stopResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("stop supervisor: decode response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("stop supervisor: server returned ok=false")
	}
	return nil
}

// DetachSupervisor sends a POST /supervisor/detach to the supervisor at
// sockPath and returns when the supervisor acknowledges. Unlike
// [StopSupervisor], this does NOT tear the VM or perimeter down — the caller
// is responsible for having a replacement ready to adopt them.
func DetachSupervisor(ctx context.Context, sockPath string) error {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost"+ipcDetachPath, nil)
	if err != nil {
		return fmt.Errorf("detach supervisor: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("detach supervisor: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("detach supervisor: unexpected status %d", resp.StatusCode)
	}
	var result stopResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("detach supervisor: decode response: %w", err)
	}
	if !result.OK {
		return fmt.Errorf("detach supervisor: server returned ok=false")
	}
	return nil
}

// RequestHandoff sends a POST /supervisor/handoff to the supervisor at
// sockPath, naming peerSock as the replacement's handoff socket. It returns
// (true, nil) only when the replacement confirmed and the outgoing supervisor
// has detached; (false, nil) on a clean refusal (the outgoing supervisor
// remains the sole owner); and a non-nil error only for a transport/protocol
// failure talking to sockPath itself.
func RequestHandoff(ctx context.Context, sockPath, peerSock string) (bool, error) {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
	body, err := json.Marshal(handoffRequest{PeerSock: peerSock})
	if err != nil {
		return false, fmt.Errorf("request handoff: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost"+ipcHandoffPath, bytes.NewReader(body))
	if err != nil {
		return false, fmt.Errorf("request handoff: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("request handoff: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// A 500 from the hErr branch encodes to OK=false but is a transport /
		// protocol error, not a clean refusal — surface the status code so the
		// caller can distinguish the two.
		return false, fmt.Errorf("request handoff: unexpected status %d", resp.StatusCode)
	}
	var result handoffResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("request handoff: decode response (status %d): %w", resp.StatusCode, err)
	}
	return result.OK, nil
}

// RequestSupervisorVersion sends a GET /supervisor/version to the supervisor
// at sockPath and returns its binary-identity hash. Used by
// `nexus3 supervisor-upgrade` to decide whether the running supervisor
// already serves the same binary the CLI was invoked from.
func RequestSupervisorVersion(ctx context.Context, sockPath string) (string, error) {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost"+ipcVersionPath, nil)
	if err != nil {
		return "", fmt.Errorf("request version: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request version: request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("request version: unexpected status %d", resp.StatusCode)
	}
	var result versionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("request version: decode response: %w", err)
	}
	return result.BinaryHash, nil
}

// HashOwnBinary returns the hex-encoded SHA-256 of the calling process's own
// executable. Exported so `nexus3 supervisor-upgrade` (in package cli) can
// compute the same identity computeBinaryHash gives the running supervisor,
// without duplicating the hashing logic.
func HashOwnBinary() (string, error) {
	return computeBinaryHash()
}

// RequestAgentHealth sends a GET /supervisor/agent-health to the supervisor
// at sockPath and returns the live [AgentHealth] verdict. A non-nil error
// means the request itself could not be completed (transport failure talking
// to sockPath, or the supervisor predates this endpoint) — the caller MUST
// NOT treat that as healthy; see [ReconnectAgent] and
// runSupervisorUpgradeWith for the fail-closed handling this feeds.
func RequestAgentHealth(ctx context.Context, sockPath string) (AgentHealth, error) {
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost"+ipcAgentHealthPath, nil)
	if err != nil {
		return AgentHealth{}, fmt.Errorf("request agent health: build request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return AgentHealth{}, fmt.Errorf("request agent health: request: %w", err)
	}
	defer resp.Body.Close()
	var result AgentHealth
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return AgentHealth{}, fmt.Errorf("request agent health: decode response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		// The 503 branch (no probe available) decodes to a well-formed
		// AgentHealth{State: Unknown, ...} body — return it AS Unknown rather
		// than converting the non-200 status into a transport error, so
		// callers see one consistent "not proven healthy" signal instead of
		// having to special-case this status code too.
		return result, nil
	}
	return result, nil
}

// reconnectAttempts bounds [ReconnectAgent]'s retry loop: enough attempts to
// ride out a transient vsock/CH hiccup without spinning forever against a
// channel that will never recover.
const reconnectAttempts = 6

// reconnectInterval is the delay between ReconnectAgent's probe attempts.
// Package-level var (not const) so tests can shrink it — mirrors
// agent.pingRetryInterval's pattern for the same reason.
var reconnectInterval = 2 * time.Second

// ReconnectAgent asks the supervisor at sockPath to retry its agent-health
// probe up to reconnectAttempts times, spaced reconnectInterval apart,
// stopping early on the first [AgentChannelHealthy] result. It exists
// because a single probe can catch the guest mid-recovery (e.g. immediately
// after the transient condition that wedged the control plane clears) —
// giving the channel a bounded number of fresh-dial attempts before reporting
// failure is what makes this a "reconnect" rather than a single Ping that a
// caller has to loop around itself.
//
// It returns the LAST probe's [AgentHealth] and, when every attempt failed to
// reach [AgentChannelHealthy], a non-nil error summarising the final
// classification. Per the fail-closed rail: if the supervisor can never even
// be asked (every RequestAgentHealth call errors — e.g. talking to a
// supervisor that predates this endpoint, or sockPath is unreachable),
// ReconnectAgent returns AgentHealth{State: AgentChannelUnknown} and a
// non-nil error — it never reports success on an unanswered probe.
func ReconnectAgent(ctx context.Context, sockPath string) (AgentHealth, error) {
	var last AgentHealth
	var lastErr error
	for attempt := 0; attempt < reconnectAttempts; attempt++ {
		health, reqErr := RequestAgentHealth(ctx, sockPath)
		if reqErr != nil {
			last = AgentHealth{State: AgentChannelUnknown, ControlErr: reqErr.Error()}
			lastErr = reqErr
		} else {
			last = health
			lastErr = nil
			if health.Healthy() {
				return health, nil
			}
			if health.State == AgentChannelGuestGone {
				// Do not keep retrying against a guest that is definitively
				// gone — that would mask "the VM died" behind a reconnect
				// loop that can never succeed. Report it immediately.
				return health, fmt.Errorf("reconnect agent: guest is gone (control_err=%q data_err=%q)",
					health.ControlErr, health.DataErr)
			}
		}

		if attempt < reconnectAttempts-1 {
			select {
			case <-ctx.Done():
				return last, ctx.Err()
			case <-time.After(reconnectInterval):
			}
		}
	}
	if lastErr != nil {
		return last, fmt.Errorf("reconnect agent: probe unreachable after %d attempts: %w", reconnectAttempts, lastErr)
	}
	return last, fmt.Errorf("reconnect agent: channel not healthy after %d attempts (state=%s control_err=%q data_err=%q)",
		reconnectAttempts, last.State, last.ControlErr, last.DataErr)
}
