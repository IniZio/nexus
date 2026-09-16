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
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/portfwd"
	"github.com/IniZio/nexus3/internal/core/store"
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

// ReadProcNet reads both tables in ONE guest exec (F5): each exec is an
// independent hang window and allocates a 16 MiB guest ring. The parser keys
// rows on address width, so the concatenation is returned as tcp.
func (b *singleSandboxBackend) ReadProcNet(ctx context.Context, _ string) (tcp, tcp6 []byte, err error) {
	var buf captureWriter
	_, execErr := b.client.Exec(ctx, agent.ExecOptions{
		Argv:   []string{"cat", "/proc/net/tcp", "/proc/net/tcp6"},
		Stdout: &buf,
	})
	if execErr != nil {
		return nil, nil, fmt.Errorf("portfwd: exec cat /proc/net/tcp /proc/net/tcp6: %w", execErr)
	}
	return buf.Bytes(), nil, nil
}

type captureWriter struct {
	buf []byte
}

func (w *captureWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func (w *captureWriter) Bytes() []byte { return w.buf }

type portForwardSupervisor struct {
	sandboxRef      string
	backend         portfwd.Backend
	disc            *portfwd.Discoverer
	dialer          portForwardDialer
	stateDir        string
	interval        time.Duration
	discoverTimeout time.Duration // bounds each DiscoverOne call; zero means portFwdDiscoverTimeout
	listeners       map[uint16]net.Listener
	bindErrs        map[uint16]error // last host-bind failure per port; retried every tick
	reporter        func(ctx context.Context, ports []uint16)
	lastReportedSet map[uint16]struct{}
}

// core/portfwd defines no entry-status constants; internal/cli/portfwd_state.go matches these strings.
const (
	portFwdStatusLive  = "live"
	portFwdStatusError = "error"
)

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
		reporter:   makePortForwardReporter(sandboxRef),
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

	if p.bindErrs == nil {
		p.bindErrs = make(map[uint16]error)
	}
	for port := range p.bindErrs {
		if _, ok := desired[port]; !ok {
			delete(p.bindErrs, port)
		}
	}

	for _, l := range result.Forwardable {
		if _, ok := p.listeners[l.Port]; ok {
			continue
		}
		lis, lisErr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", l.Port))
		if lisErr != nil {
			p.bindErrs[l.Port] = lisErr
			slog.Warn("supervisor.portfwd.listen_err",
				"sandboxRef", p.sandboxRef,
				"port", l.Port,
				"err", lisErr,
			)
			continue
		}
		delete(p.bindErrs, l.Port)
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
	now := time.Now().UTC()
	seen := make(map[uint16]struct{}, len(forwardable))
	entries := make([]portfwd.Entry, 0, len(forwardable))
	for _, l := range forwardable {
		if _, dup := seen[l.Port]; dup {
			continue
		}
		seen[l.Port] = struct{}{}
		e := portfwd.Entry{Port: l.Port, Sandbox: p.sandboxRef}
		if _, bound := p.listeners[l.Port]; bound {
			e.Status = portFwdStatusLive
			e.ConfirmedAt = now
		} else {
			e.Status = portFwdStatusError
			if bindErr := p.bindErrs[l.Port]; bindErr != nil {
				e.Error = bindErr.Error()
			}
		}
		entries = append(entries, e)
	}
	writeErr := portfwd.WriteSandboxState(p.stateDir, p.sandboxRef, entries, now)
	if writeErr == nil && p.reporter != nil && !portSetsEqual(seen, p.lastReportedSet) {
		p.lastReportedSet = seen
		ports := sortedPortSet(seen)
		rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
		go func() {
			defer rcancel()
			p.reporter(rctx, ports)
		}()
	}
	return writeErr
}

func portSetsEqual(a, b map[uint16]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func sortedPortSet(m map[uint16]struct{}) []uint16 {
	out := make([]uint16, 0, len(m))
	for p := range m {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

func supervisorSendReportMetadata(ctx context.Context, socketPath, workspaceID string, ports []uint16) error {
	var portVal any
	if len(ports) > 0 {
		strs := make([]string, len(ports))
		for i, p := range ports {
			strs[i] = strconv.Itoa(int(p))
		}
		portVal = strings.Join(strs, ",")
	}
	req := map[string]any{
		"id":     "1",
		"method": "workspace.report_metadata",
		"params": map[string]any{
			"workspace_id": workspaceID,
			"source":       "plugin:nexus3",
			"tokens":       map[string]any{"port_forward_status": portVal},
		},
	}
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl) //nolint:errcheck
	}
	_, err = fmt.Fprintf(conn, "%s\n", data)
	return err
}

func makePortForwardReporter(sandboxRef string) func(context.Context, []uint16) {
	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return nil
	}
	type minBinding struct {
		SandboxHandle    string `json:"sandbox_handle"`
		HerdrWorkspaceID string `json:"herdr_workspace_id"`
	}
	return func(ctx context.Context, ports []uint16) {
		bindingsPath := filepath.Join(storeRoot, "herdr-space-bindings.json")
		data, err := os.ReadFile(bindingsPath)
		if err != nil {
			return
		}
		var bindings []minBinding
		if err := json.Unmarshal(data, &bindings); err != nil {
			return
		}
		var workspaceID string
		for _, b := range bindings {
			if b.SandboxHandle == sandboxRef {
				workspaceID = b.HerdrWorkspaceID
				break
			}
		}
		if workspaceID == "" {
			return
		}
		home, _ := os.UserHomeDir()
		socketPath := filepath.Join(home, ".config", "herdr", "herdr.sock")
		if _, err := os.Stat(socketPath); err != nil {
			return
		}
		if err := supervisorSendReportMetadata(ctx, socketPath, workspaceID, ports); err != nil {
			slog.Debug("supervisor.portfwd.metadata_report_failed", "sandboxRef", sandboxRef, "err", err)
		}
	}
}

func (p *portForwardSupervisor) teardownAll() {
	for port, lis := range p.listeners {
		lis.Close()
		delete(p.listeners, port)
	}
	_ = portfwd.RemoveSandboxState(p.stateDir, p.sandboxRef, time.Now()) // best-effort: drop our ports from the merge
}
