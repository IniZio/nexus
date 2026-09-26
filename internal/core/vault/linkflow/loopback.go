package linkflow

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
)

// LoopbackPort is the fixed registered port for the OAuth loopback redirect listener.
const LoopbackPort = 57842

var ErrStateMismatch = errors.New("linkflow: state parameter mismatch")

// LoopbackListener listens on a loopback address for an OAuth redirect callback.
type LoopbackListener struct {
	ln    net.Listener
	state string
	done  chan result
}

type result struct {
	code string
	err  error
}

// Listen starts a loopback HTTP listener on addr (e.g. ":57842" or ":0").
// expectedState is the CSRF state value that must appear in the callback URL.
func Listen(ctx context.Context, addr, expectedState string) (*LoopbackListener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("linkflow: listen: %w", err)
	}
	l := &LoopbackListener{
		ln:    ln,
		state: expectedState,
		done:  make(chan result, 1),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", l.handleCallback)
	srv := &http.Server{Handler: mux}
	go func() {
		_ = srv.Serve(ln)
	}()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return l, nil
}

func (l *LoopbackListener) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	code := q.Get("code")

	if state != l.state {
		http.Error(w, "state mismatch", http.StatusBadRequest)
		select {
		case l.done <- result{err: ErrStateMismatch}:
		default:
		}
		return
	}
	fmt.Fprintln(w, "Authorization complete. You may close this tab.")
	select {
	case l.done <- result{code: code}:
	default:
	}
}

// Port returns the TCP port the listener is bound to.
func (l *LoopbackListener) Port() int {
	addr := l.ln.Addr().(*net.TCPAddr)
	return addr.Port
}

// RedirectURL returns the loopback redirect URL for use as the OAuth redirect_uri.
func (l *LoopbackListener) RedirectURL() string {
	return "http://localhost:" + strconv.Itoa(l.Port())
}

// Wait blocks until the callback is received or ctx is cancelled.
func (l *LoopbackListener) Wait(ctx context.Context) (string, error) {
	select {
	case r := <-l.done:
		return r.code, r.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Close releases the listener.
func (l *LoopbackListener) Close() {
	_ = l.ln.Close()
}
