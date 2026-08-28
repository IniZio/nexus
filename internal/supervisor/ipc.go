package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/IniZio/nexus3/internal/core/service"
)

// allowEgressFunc is the callback type the IPC egress-allow handler uses to
// mutate the live perimeter. It is nil until the perimeter supervisor is ready.
type allowEgressFunc func(host string) error

// ipcStopPath is the HTTP path for the graceful-stop request.
const ipcStopPath = "/supervisor/stop"

// stopResponse is the JSON body returned on a successful stop request.
type stopResponse struct {
	OK bool `json:"ok"`
}

// serveIPC starts the Unix-domain HTTP IPC server at sockPath and returns a
// channel that is closed when a /supervisor/stop request is received. The
// server is shut down when ctx is cancelled.
//
// svc and sandboxRef are accepted for future extension (e.g. a /supervisor/status
// endpoint); the stop handler does not call svc.Stop directly — it signals
// the RunDetached select loop, which then calls svc.Stop after releasing the
// goroutine.
//
// allowEgress is a late-bound callback invoked by the /supervisor/egress-allow
// handler. It may be nil at call time (IPC starts before the perimeter
// supervisor is ready); a nil allowEgress causes the handler to return 503.
func serveIPC(ctx context.Context, sockPath string, _ *service.Service, _ string, allowEgress allowEgressFunc) (<-chan struct{}, error) {
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, fmt.Errorf("ipc: listen %s: %w", sockPath, err)
	}

	stopCh := make(chan struct{})
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

	return stopCh, nil
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
