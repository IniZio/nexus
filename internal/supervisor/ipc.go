package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

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

// stopResponse is the JSON body returned on a successful stop or detach request.
type stopResponse struct {
	OK bool `json:"ok"`
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
func serveIPC(ctx context.Context, sockPath string, _ *service.Service, _ string, allowEgress allowEgressFunc, handoff handoffFunc) (ipcHandles, error) {
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
	var result handoffResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return false, fmt.Errorf("request handoff: decode response (status %d): %w", resp.StatusCode, err)
	}
	return result.OK, nil
}
