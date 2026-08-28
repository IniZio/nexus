package perimeter

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/perimeter/mitm"
	"github.com/IniZio/nexus3/internal/core/perimeter/netfilter"
	"github.com/IniZio/nexus3/internal/core/perimeter/sni"
)

// dialerSetter is an optional interface a Perimeter implementation may satisfy
// to receive a post-construction outbound dialer. PerimeterSupervisor.Start
// type-asserts the Perimeter against this interface after the MITM listener
// address is known (so the dialer closure can capture it) and before the
// frame-pump goroutine starts (happens-before guarantee for the dialer field).
//
// The import-cycle note in PerimeterSupervisor's doc applies here too: this
// interface lives in package perimeter so that netstack.Stack can satisfy it
// without netstack importing perimeter in return.
type dialerSetter interface {
	SetDialer(func(ctx context.Context, network, addr string) (net.Conn, error))
}

// buildDialer returns the port-conditional outbound dialer for a supervisor.
//
// Port 443: intercepts the TCP flow with a net.Pipe. The forwarder-side end is
// returned to gvproxy's TCP forwarder as the outbound connection; the shim-side
// end is processed in a goroutine that calls sni.ParseSNI to extract the SNI
// hostname from the guest's TLS ClientHello, then hands the replayed connection
// to sni.Bridge which opens an HTTP CONNECT tunnel to mitmAddr and splices
// bytes bidirectionally until either side closes.
//
// ErrNoSNI (TLS ClientHello without an SNI extension) and all other ParseSNI
// errors: the connection is rejected. The allowlist and credential broker are
// both keyed on hostname; a flow with no SNI cannot be attributed to a
// credential scope, so passing it through would either leak a credential to an
// unintended host or hand the guest an uncredentialed pipe past the policy gate.
// When rejected, shimSide is closed via defer, which causes forwarderSide to
// error on its next read/write and gvproxy to tear down the guest TCP flow.
//
// Non-443: plain net.Dial — the flow is not HTTPS TLS and must not be routed
// through the MITM path.
//
// Goroutine lifetime: the bridge goroutine derives bridgeCtx from ctx (the
// supervisor's cancellable child context). A watcher goroutine selects on
// bridgeCtx.Done() and closes shimSide, which unblocks any in-flight
// sni.ParseSNI or sni.Bridge call. When the bridge goroutine exits normally,
// defer cancel() fires bridgeCtx, which unblocks the watcher.
func buildDialer(ctx context.Context, mitmAddr string) func(context.Context, string, string) (net.Conn, error) {
	return func(_ context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil || port != "443" || mitmAddr == "" {
			// Non-443, unparseable, or no MITM configured: plain dial, bypass the shim.
			return net.Dial(network, addr)
		}

		// Port 443: interpose net.Pipe for SNI peek → MITM bridge.
		forwarderSide, shimSide := net.Pipe()

		go func() {
			// bridgeCtx is cancelled when either the supervisor closes
			// (ctx.Done) or this goroutine exits (defer cancel), whichever
			// comes first.
			bridgeCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			defer shimSide.Close()

			// Watcher: close shimSide when the supervisor's context fires,
			// unblocking any in-flight sni.ParseSNI or sni.Bridge call.
			go func() {
				<-bridgeCtx.Done()
				shimSide.Close() // net.Pipe Close is idempotent
			}()

			host, replay, parseErr := sni.ParseSNI(shimSide)
			if parseErr != nil {
				// Both ErrNoSNI and non-TLS parse failures are rejected here.
				// See buildDialer doc for the ErrNoSNI rejection rationale.
				if !errors.Is(parseErr, sni.ErrNoSNI) {
					// Non-TLS or truncated record; shimSide.Close via defer handles cleanup.
					_ = parseErr
				}
				return
			}

			_ = sni.Bridge(replay, host, mitmAddr)
		}()

		return forwarderSide, nil
	}
}

// PerimeterSupervisor owns the lifetime of a sandbox's egress perimeter:
// the netfilter AllowList (with its DNS-refresh goroutine), the network frame
// pump (a [Perimeter] implementation, typically *netstack.Stack), and the MITM
// HTTP proxy server.
//
// # Import-cycle note
//
// supervisor.go lives in package perimeter. The netstack sub-package imports
// perimeter (for [AuditEvent] and the [Perimeter] interface), so perimeter
// cannot import netstack in return without creating a cycle.  The caller
// (service.go) constructs a *netstack.Stack and passes it here as the
// [Perimeter] interface.
//
// Use [Start] to obtain a PerimeterSupervisor; the zero value is not usable.
type PerimeterSupervisor struct {
	cancel   context.CancelFunc
	once     sync.Once
	wg       sync.WaitGroup
	srv      *http.Server
	ln       net.Listener
	fd       io.ReadWriteCloser
	al       *netfilter.AllowList
	proxy    *mitm.Proxy
	mitmAddr string
}

// Start creates a [PerimeterSupervisor] and launches its two background
// goroutines (frame pump + MITM HTTP server).
//
// Parameters:
//   - ctx: parent context; cancellation propagates to the frame pump.
//   - id: the sandbox this supervisor serves.
//   - fd: raw network fd from driver.NetworkHook.GuestNetworkFD; its dynamic
//     type must satisfy net.Conn (required by the netstack frame pump).
//     Ownership is transferred to the supervisor; Close closes it.
//   - p: the frame pump. Typically *netstack.Stack, constructed by the caller
//     because netstack imports this package and would cause a cycle here.
//   - proxy: the per-sandbox MITM proxy, already constructed by the caller.
//   - al: the netfilter AllowList, already constructed by the caller.
//     Start calls al.Start(5 * time.Minute); Close calls al.Stop().
//
// On any error during Start, fd and al are cleaned up before returning so the
// caller need not track partial construction.
func Start(
	ctx context.Context,
	id domain.SandboxID,
	fd io.ReadWriteCloser,
	p Perimeter,
	proxy *mitm.Proxy,
	al *netfilter.AllowList,
) (*PerimeterSupervisor, error) {
	childCtx, cancel := context.WithCancel(ctx)

	s := &PerimeterSupervisor{
		cancel: cancel,
		fd:     fd,
		al:     al,
		proxy:  proxy,
	}

	// MITM listener: only started when a proxy is provided. When proxy is nil
	// (AllowAll / no-credential mode) port 443 is forwarded directly without
	// TLS interception so build tools (apt, wget, pip) trust the real server
	// certificates and the perimeter still enforces the netfilter ACL.
	if proxy != nil {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			cancel()
			fd.Close()
			al.Stop()
			return nil, fmt.Errorf("perimeter: listen for MITM proxy: %w", err)
		}
		s.ln = ln
		s.mitmAddr = ln.Addr().String()
		s.srv = &http.Server{Handler: proxy}
	}

	// al.Start runs an initial refreshDomains() synchronously (fast when
	// AllowedHosts is empty) then spawns the periodic-refresh goroutine.
	al.Start(5 * time.Minute)

	// Inject the port-conditional egress dialer into the frame pump before it
	// starts. SetDialer must happen-before p.Run; wg.Add(2) below provides the
	// ordering barrier. When mitmAddr is empty (no proxy), buildDialer uses a
	// plain net.Dial for port 443 (no MITM shim).
	if ds, ok := p.(dialerSetter); ok {
		ds.SetDialer(buildDialer(childCtx, s.mitmAddr))
	}

	goroutines := 1 // frame pump always runs
	if s.srv != nil {
		goroutines = 2
	}
	s.wg.Add(goroutines)
	go func() {
		defer s.wg.Done()
		_ = p.Run(childCtx, id, fd)
	}()
	if s.srv != nil {
		go func() {
			defer s.wg.Done()
			_ = s.srv.Serve(s.ln) // returns when ln is closed by Close
		}()
	}

	return s, nil
}

// AllowEgress opens host in both the L7 MITM allowlist and the L3/L4 netfilter
// AllowList. It is the single runtime mutation point for adding a host to a
// running sandbox's egress perimeter.
//
// Behaviour by mode:
//   - Full MITM mode (proxy != nil, al != nil): calls proxy.AllowHost and al.AddDomain.
//   - AllowAll mode (proxy == nil): the L7 gate is absent, so only al.AddDomain is
//     called. In AllowAll mode all HTTPS traffic already flows through unfiltered;
//     AddDomain ensures the L3/L4 forwarder also permits the destination.
//   - No perimeter (both nil): returns an error — there is nothing to open.
//
// host must be a non-empty domain name (e.g. "registry.npmjs.org"). Empty host
// is rejected before any layer is mutated.
func (s *PerimeterSupervisor) AllowEgress(host string) error {
	if host == "" {
		return fmt.Errorf("perimeter: AllowEgress: host is required")
	}
	if s.proxy == nil && s.al == nil {
		return fmt.Errorf("perimeter: AllowEgress: no perimeter layers configured")
	}
	if s.proxy != nil {
		s.proxy.AllowHost(host)
	}
	if s.al != nil {
		if err := s.al.AddDomain(host); err != nil {
			return fmt.Errorf("perimeter: AllowEgress: netfilter: %w", err)
		}
	}
	return nil
}

// MitmAddr returns the "host:port" address of the MITM proxy listener.
// The address is stable for the lifetime of the supervisor.
func (s *PerimeterSupervisor) MitmAddr() string { return s.mitmAddr }

// CACert returns the parsed CA certificate that the guest trust store must
// import before HTTPS traffic flows through the MITM proxy.
// Returns nil when the supervisor was started without a proxy (AllowAll mode).
func (s *PerimeterSupervisor) CACert() *x509.Certificate {
	if s.proxy == nil {
		return nil
	}
	return s.proxy.CACert()
}

// Close shuts down all supervisor goroutines and releases resources.
//
// Shutdown sequence:
//  1. cancel() — signals the frame pump to stop via context.
//  2. srv.Close() — closes the HTTP server and its listener, unblocking Serve.
//  3. fd.Close() — closes the network fd, unblocking any in-progress Read in
//     the frame pump that did not yet observe the context cancellation.
//  4. wg.Wait() — waits for both goroutines (frame pump + Serve) to exit.
//  5. al.Stop() — stops the AllowList DNS-refresh goroutine.
//
// The AllowList goroutine (step 5) exits asynchronously after the stop channel
// is closed; Close does not wait for it. Callers that need a hard guarantee
// should poll runtime.NumGoroutine() with a short deadline.
//
// Close is idempotent via [sync.Once].
func (s *PerimeterSupervisor) Close() error {
	s.once.Do(func() {
		s.cancel()
		if s.srv != nil {
			s.srv.Close()
		}
		s.fd.Close()
		s.wg.Wait()
		s.al.Stop()
	})
	return nil
}
