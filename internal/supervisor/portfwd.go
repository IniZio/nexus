package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/portfwd"
)

// portForwardDialer is satisfied by service.Service (avoids importing cli).
type portForwardDialer interface {
	DialGuestPortForward(ctx context.Context, ref string, guestPort uint32) (net.Conn, error)
}

type guestExecer interface {
	Exec(ctx context.Context, opts agent.ExecOptions) (int32, error)
}

// singleSandboxBackend implements portfwd.Backend via cat /proc/net/tcp[6] in the guest.
type singleSandboxBackend struct {
	ref    portfwd.SandboxRef
	id     domain.SandboxID
	client guestExecer
}

func (b *singleSandboxBackend) ListSandboxes(_ context.Context) ([]portfwd.SandboxRef, error) {
	return []portfwd.SandboxRef{b.ref}, nil
}

func (b *singleSandboxBackend) ReadProcNet(ctx context.Context, _ string) (tcp, tcp6 []byte, err error) {
	var tcpBuf, tcp6Buf captureWriter
	_, tcpErr := b.client.Exec(ctx, agent.ExecOptions{
		Argv:   []string{"cat", "/proc/net/tcp"},
		Stdout: &tcpBuf,
	})
	if tcpErr != nil {
		return nil, nil, fmt.Errorf("portfwd: exec cat /proc/net/tcp: %w", tcpErr)
	}

	_, tcp6Err := b.client.Exec(ctx, agent.ExecOptions{
		Argv:   []string{"cat", "/proc/net/tcp6"},
		Stdout: &tcp6Buf,
	})
	if tcp6Err != nil {
		slog.Debug("supervisor.portfwd.tcp6_unavailable", "err", tcp6Err)
	}

	return tcpBuf.Bytes(), tcp6Buf.Bytes(), nil
}

type captureWriter struct {
	buf []byte
}

func (w *captureWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *captureWriter) Bytes() []byte { return w.buf }

// portFwdStateJSON mirrors cli.ForwardsState without importing the cli package
// (import cycle guard: internal/cli imports internal/supervisor).
type portFwdStateJSON struct {
	WrittenBy string             `json:"written_by"`
	UpdatedAt time.Time          `json:"updated_at"`
	Forwards  []portFwdEntryJSON `json:"forwards"`
}

type portFwdEntryJSON struct {
	Port        uint16    `json:"port"`
	Sandbox     string    `json:"sandbox"`
	Status      string    `json:"status"`
	ConfirmedAt time.Time `json:"confirmed_at,omitempty"`
}

type portForwardSupervisor struct {
	sandboxRef      string
	backend         portfwd.Backend
	disc            *portfwd.Discoverer
	dialer          portForwardDialer
	stateDir        string
	interval        time.Duration
	discoverTimeout time.Duration // bounds each DiscoverOne call; zero means portFwdDiscoverTimeout
	listeners       map[uint16]net.Listener
}

// portFwdDiscoverTimeout bounds one guest /proc/net/tcp exec so a single hung
// exec cannot freeze reconcile for the life of the sandbox.
const portFwdDiscoverTimeout = 5 * time.Second

func startPortForwardSupervisor(
	ctx context.Context,
	sandboxRef string,
	sb domain.Sandbox,
	client guestExecer,
	dialer portForwardDialer,
) {
	ref := portfwd.SandboxRef{
		ID:     sb.ID.String(),
		Name:   sandboxRef,
		Status: portfwd.SandboxStatusRunning,
	}
	backend := &singleSandboxBackend{
		ref:    ref,
		id:     sb.ID,
		client: client,
	}
	sup := &portForwardSupervisor{
		sandboxRef: sandboxRef,
		backend:    backend,
		disc:       &portfwd.Discoverer{Backend: backend},
		dialer:     dialer,
		stateDir:   portfwd.StateDir(),
		interval:   5 * time.Second,
		listeners:  make(map[uint16]net.Listener),
	}
	go sup.run(ctx)
	slog.Info("supervisor.portfwd.started",
		"sandboxID", sb.ID,
		"sandboxRef", sandboxRef,
		"stateDir", sup.stateDir,
		"interval", sup.interval,
	)
}

func (p *portForwardSupervisor) run(ctx context.Context) {
	tick := time.NewTicker(p.interval)
	defer tick.Stop()
	defer p.teardownAll()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if err := p.reconcile(ctx); err != nil && ctx.Err() == nil {
				slog.Warn("supervisor.portfwd.reconcile_err",
					"sandboxRef", p.sandboxRef,
					"err", err,
				)
			}
		}
	}
}

func (p *portForwardSupervisor) reconcile(ctx context.Context) error {
	refs, _ := p.backend.ListSandboxes(ctx)
	if len(refs) == 0 {
		return nil
	}

	lsnrs, err := p.discoverBounded(ctx, refs[0])
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}

	result := portfwd.FilterListeners(lsnrs, nil)

	desired := make(map[uint16]struct{}, len(result.Forwardable))
	for _, l := range result.Forwardable {
		desired[l.Port] = struct{}{}
	}

	for port, lis := range p.listeners {
		if _, ok := desired[port]; !ok {
			lis.Close()
			delete(p.listeners, port)
			slog.Info("supervisor.portfwd.stopped", "sandboxRef", p.sandboxRef, "port", port)
		}
	}

	for _, l := range result.Forwardable {
		if _, ok := p.listeners[l.Port]; ok {
			continue
		}
		lis, lisErr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", l.Port))
		if lisErr != nil {
			slog.Warn("supervisor.portfwd.listen_err",
				"sandboxRef", p.sandboxRef,
				"port", l.Port,
				"err", lisErr,
			)
			continue
		}
		p.listeners[l.Port] = lis
		slog.Info("supervisor.portfwd.listening",
			"sandboxRef", p.sandboxRef,
			"port", l.Port,
		)
		go p.acceptLoop(ctx, lis, l.Port)
	}

	return p.writeState(result.Forwardable)
}

// discoverBounded abandons DiscoverOne on timeout: a guest exec that ignores ctx
// cancellation must wedge only this tick, never the reconcile loop.
func (p *portForwardSupervisor) discoverBounded(ctx context.Context, ref portfwd.SandboxRef) ([]portfwd.Listener, error) {
	timeout := p.discoverTimeout
	if timeout <= 0 {
		timeout = portFwdDiscoverTimeout
	}
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		lsnrs []portfwd.Listener
		err   error
	}
	done := make(chan result, 1)
	go func() {
		lsnrs, err := p.disc.DiscoverOne(dctx, ref)
		done <- result{lsnrs, err}
	}()

	select {
	case r := <-done:
		return r.lsnrs, r.err
	case <-dctx.Done():
		if ctx.Err() == nil {
			slog.Warn("supervisor.portfwd.discover_timeout",
				"sandboxRef", p.sandboxRef,
				"timeout", timeout,
			)
		}
		return nil, dctx.Err()
	}
}

func (p *portForwardSupervisor) acceptLoop(ctx context.Context, lis net.Listener, port uint16) {
	// Close the listener when ctx is cancelled so Accept unblocks.
	go func() {
		<-ctx.Done()
		lis.Close()
	}()

	for {
		conn, err := lis.Accept()
		if err != nil {
			// Listener was closed — expected on shutdown or port removal.
			return
		}
		go p.forwardConn(ctx, conn, port)
	}
}

func (p *portForwardSupervisor) forwardConn(ctx context.Context, hostConn net.Conn, port uint16) {
	defer hostConn.Close()

	guestConn, err := p.dialer.DialGuestPortForward(ctx, p.sandboxRef, uint32(port))
	if err != nil {
		slog.Debug("supervisor.portfwd.dial_err",
			"sandboxRef", p.sandboxRef,
			"port", port,
			"err", err,
		)
		return
	}
	defer guestConn.Close()

	done := make(chan struct{}, 2)
	splice := func(dst, src net.Conn) {
		io.Copy(dst, src) //nolint:errcheck
		if tc, ok := dst.(interface{ CloseWrite() error }); ok {
			tc.CloseWrite() //nolint:errcheck
		}
		done <- struct{}{}
	}

	go splice(hostConn, guestConn)
	go splice(guestConn, hostConn)
	<-done
}

func (p *portForwardSupervisor) writeState(forwardable []portfwd.Listener) error {
	if err := os.MkdirAll(p.stateDir, 0o750); err != nil {
		return fmt.Errorf("portfwd state dir: %w", err)
	}

	now := time.Now().UTC()
	entries := make([]portFwdEntryJSON, 0, len(forwardable))
	for _, l := range forwardable {
		entries = append(entries, portFwdEntryJSON{
			Port:        l.Port,
			Sandbox:     p.sandboxRef,
			Status:      "live",
			ConfirmedAt: now,
		})
	}
	state := portFwdStateJSON{
		WrittenBy: "nexus3/" + p.sandboxRef,
		UpdatedAt: now,
		Forwards:  entries,
	}

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("portfwd state marshal: %w", err)
	}

	stateFile := filepath.Join(p.stateDir, portfwd.StateFileName)
	tmpFile := stateFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0o640); err != nil {
		return fmt.Errorf("portfwd state write: %w", err)
	}
	if err := os.Rename(tmpFile, stateFile); err != nil {
		return fmt.Errorf("portfwd state rename: %w", err)
	}
	return nil
}

func (p *portForwardSupervisor) teardownAll() {
	for port, lis := range p.listeners {
		lis.Close()
		delete(p.listeners, port)
	}
	_ = p.writeState(nil) // best-effort empty state on shutdown
}
