package supervisor

// TestAdoptedGovernorResizes proves AC-5 of the nexus3-host-supervisor-hotswap
// motive: the memory governor still resizes the guest after a supervisor
// replacement.
//
// Decision D-HSH-06: governor state is NOT transferred or persisted. The
// replacement supervisor constructs a fresh Governor from the same Config
// bounds and lets it rebuild control-law state from the next telemetry poll.
//
// What this test proves (driving the real call site serveAdoptedSupervisor):
//
//  1. The adopted path constructs a Governor via the PRODUCTION lines in
//     serve_adopted.go — NewSandboxResizer + govern.New + wireGovernorAxes +
//     go gov.Run — not a substitute. The fake stands in at the Unix-socket
//     driver boundary (CH REST API socket + vsock proxy socket), not above it.
//
//  2. A fresh Governor (no inherited counters, D-HSH-06) fires ResizeMemory on
//     its FIRST poll when telemetry demands growth: memoryGrowConsecutive=1
//     means one below-threshold sample is sufficient.
//
//  3. The resize target matches the control-law expectation: 1 GiB (4 × 256 MiB
//     hotplug blocks), computed from a 1 GiB guest at 10% MemAvailable with
//     min=512 MiB / max=4 GiB bounds.
//
// Mutation guards:
//   - "Bounds: cfg.GovBounds" → "Bounds: resize.Bounds{}" makes governor exit
//     immediately (bounds-not-configured guard in Run) → resizer never called
//     → test RED.
//   - "go gov.Run(ctx)" removed → governor never polls → resizer never called
//     → test RED.
//   - Note: wireGovernorAxes removal only drops the CPU and disk axes; the
//     built-in memory axis (registered by govern.New) still fires, so a
//     memory-only test cannot catch that mutation. The existing
//     TestWireGovernorAxes_* tests already cover axis-wiring correctness for
//     wireGovernorAxes itself.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	cloudhypervisor "github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/resize"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
)

// ── Minimal fake store (all lookups return ErrNotFound) ─────────────────────

// errStore is a store.Store where every method returns an error so that
// svc.Stop returns quickly without panicking on nil internal state.
type errStore struct{}

func (errStore) Create(_ context.Context, _ domain.Sandbox) error { return errors.New("errStore") }
func (errStore) Get(_ context.Context, _ domain.SandboxID) (domain.Sandbox, error) {
	return domain.Sandbox{}, store.ErrNotFound
}
func (errStore) List(_ context.Context) ([]domain.Sandbox, error) { return nil, errors.New("errStore") }
func (errStore) Update(_ context.Context, _ domain.SandboxID, _ func(*domain.Sandbox) error) error {
	return store.ErrNotFound
}
func (errStore) Delete(_ context.Context, _ domain.SandboxID) error { return errors.New("errStore") }
func (errStore) SetRemovalMarker(_ context.Context, _ domain.SandboxID) error {
	return errors.New("errStore")
}
func (errStore) ClearRemovalMarker(_ context.Context, _ domain.SandboxID) error {
	return errors.New("errStore")
}
func (errStore) ResolveByPrefix(_ context.Context, _ string) (domain.Sandbox, error) {
	return domain.Sandbox{}, store.ErrNotFound
}
func (errStore) ResolveByHandle(_ context.Context, _ string) (domain.Sandbox, error) {
	return domain.Sandbox{}, store.ErrNotFound
}
func (errStore) GetByLabels(_ context.Context, _ map[string]string) ([]domain.Sandbox, error) {
	return nil, errors.New("errStore")
}
func (errStore) GetByMotive(_ context.Context, _ string) ([]domain.Sandbox, error) {
	return nil, errors.New("errStore")
}

// ── Fake vsock proxy server ──────────────────────────────────────────────────

// serveFakeVsock listens on a Unix socket at path, performing the vsock
// multiplexer handshake (CONNECT <port>\n → OK 0\n) then serving one
// grow-demanding SampleResponse per connection. It runs until ctx is done.
//
// The CHDriver.DialGuest implementation dials this Unix socket and executes the
// handshake, so govern.NewVsockTelemetry(drv, id) polls through this server.
//
// Sample shape: 1 GiB total, 100 MiB available (9.8% < 20% grow threshold).
// This satisfies sampleWantsGrow signal 2 (MemAvailable ratio < defaultGrowThreshold).
func serveFakeVsock(ctx context.Context, t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Errorf("fake vsock listen: %v", err)
		return
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			go handleVsockConn(conn)
		}
	}()
}

func handleVsockConn(conn net.Conn) {
	defer conn.Close()

	// Vsock handshake: read "CONNECT <port>\n", write "OK 0\n".
	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil || len(line) == 0 {
		return
	}
	if _, err := fmt.Fprint(conn, "OK 0\n"); err != nil {
		return
	}

	// Telemetry protocol: read SampleRequest, write SampleResponse.
	if _, err := resize.DecodeSampleRequest(br); err != nil {
		return
	}

	sample := resize.Sample{
		Timestamp:         time.Now(),
		MemTotalBytes:     1 << 30,   // 1 GiB total
		MemAvailableBytes: 100 << 20, // 100 MiB available → ~9.8% < 20% grow threshold
		MemPSISupported:   true,
	}
	_ = resize.EncodeSampleResponse(conn, resize.SampleResponse{Sample: sample})
}

// ── Fake CH REST API server ──────────────────────────────────────────────────

// startFakeCHServer starts a fake Cloud Hypervisor REST API server on a Unix
// socket at path. It handles PUT /api/v1/vm.resize, records the desired_ram
// field from each request body, and responds 204 No Content. It runs until
// ln.Close() is called (deferred in the test via t.Cleanup).
//
// The SandboxResizer.ResizeMemory implementation dials this socket via
// newClient(r.d.socketPath(r.id)), so the production ResizeMemory call hits
// this server.
func startFakeCHServer(t *testing.T, path string) *atomic.Int64 {
	t.Helper()

	var lastDesiredRAM atomic.Int64

	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("fake CH server listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/vm.resize", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		if err := json.Unmarshal(body, &m); err == nil {
			if v, ok := m["desired_ram"].(float64); ok {
				lastDesiredRAM.Store(int64(v))
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})

	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	return &lastDesiredRAM
}

// ── Test ─────────────────────────────────────────────────────────────────────

func TestAdoptedGovernorResizes(t *testing.T) {
	// Socket paths must fit within AF_UNIX sun_path (107 bytes). Use /tmp
	// directly to keep the base path short.
	socketDir, err := os.MkdirTemp("/tmp", "govtest")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })

	id := domain.NewSandboxID()

	// Predict the socket paths that CHDriver will use for this sandbox:
	//   socketPath(id)  = socketDir + "/" + id.String() + ".sock"  (CH REST API)
	//   vsockPath(id)   = socketDir + "/" + id.String() + ".vsock" (vsock proxy)
	// These mirror the unexported CHDriver methods in driver.go and ch_vsock.go.
	chSockPath := filepath.Join(socketDir, id.String()+".sock")
	vsockPath := filepath.Join(socketDir, id.String()+".vsock")

	// Bounds: 512 MiB min, 4 GiB max. Current starts at MemoryMiB (512 MiB).
	//
	// Expected first-resize target derivation (see memory.go):
	//   MemTotal=1 GiB, MemAvail=100 MiB → sampleWantsGrow=true (signal 2)
	//   growCount=1 >= memoryGrowConsecutive=1 → fires on first poll
	//   sampleIsCritical: 9.8% > 8% criticalGrowThreshold → not critical
	//   growStep: deficit = int64(1GiB × 0.45) − 100MiB ≈ 378326220
	//             step = 378326220 (> minGrowStepBytes 256 MiB)
	//             target = 512MiB + 378326220 ≈ 915197132
	//   alignUp(915197132, 256MiB) = 4 × 256MiB = 1073741824
	//   Clamped to [512MiB, 4GiB] → 1 GiB; target > current → proceed
	//   Headroom: /proc/meminfo on real host (test host has enough) → ok
	//   ResizeMemory(ctx, 1073741824) recorded by fake CH server.
	//
	// NOTE: the headroom check reads real /proc/meminfo. On a host with less
	// than ~1.1 GiB of MemAvailable the resize target (1 GiB) is rejected and
	// the test fails with a missing ResizeMemory call — a false RED, not a
	// correctness hole (the governor itself is working correctly; the host is
	// too loaded to grant the requested memory). This is not a flaky assertion;
	// it is a load-dependent precondition. Run on a host with ≥4 GiB free, or
	// use GOTEST_P=1 GOTEST_PARALLEL=1 via make to reduce concurrent memory
	// pressure during the suite.
	const (
		minBytes  int64  = 512 << 20
		maxBytes  int64  = 4096 << 20
		memoryMiB uint32 = 512
		wantBytes int64  = 1 << 30 // 1 GiB = 4 × 256 MiB hotplug blocks
	)

	bounds := resize.Bounds{
		MemMinBytes: minBytes,
		MemMaxBytes: maxBytes,
	}

	// Start fake servers BEFORE creating the CHDriver (which checks path
	// lengths but does not bind sockets itself).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	serveFakeVsock(ctx, t, vsockPath)
	lastRAM := startFakeCHServer(t, chSockPath)

	// Construct a real *CHDriver that will dial our fake servers.
	drv, err := cloudhypervisor.New(cloudhypervisor.Config{
		SocketDir: socketDir,
		MemoryMiB: memoryMiB,
	})
	if err != nil {
		t.Fatalf("cloudhypervisor.New: %v", err)
	}

	// Construct a real *service.Service backed by an error-returning store.
	// svc.Stop is called on context cancellation and returns an error (not
	// found), which serveAdoptedSupervisor logs and ignores.
	svc := service.New(errStore{}, nil, lifecycle.Machine{})

	stateDir := t.TempDir()

	errCh := make(chan error, 1)
	go func() {
		errCh <- serveAdoptedSupervisor(ctx, serveAdoptedInput{
			cfg: Config{
				StateDir:  stateDir,
				GovBounds: bounds,
				MemoryMiB: memoryMiB,
			},
			sb:        domain.Sandbox{ID: id},
			svc:       svc,
			drv:       drv,
			logPrefix: "supervisor.test.adopted_governor_resizes",
			startPerimeterFn: func(_ context.Context, _ domain.Sandbox, _ *service.CASeed) error {
				return nil
			},
		})
	}()

	// AC-1 + AC-2: poll until the fake CH server records a resize call.
	// The governor constructs its control-law state from scratch (no inherited
	// counters per D-HSH-06) and fires on the first poll because
	// memoryGrowConsecutive=1. The production boot delay (10 s) applies; the
	// test timeout (30 s) is deliberately larger.
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if got := lastRAM.Load(); got != 0 {
			if got != wantBytes {
				t.Errorf("ResizeMemory target = %d (%d MiB), want %d (%d MiB)",
					got, got>>20, wantBytes, wantBytes>>20)
			}
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if lastRAM.Load() == 0 {
		t.Fatal("timed out: ResizeMemory never called — governor did not fire on adopted path")
	}

	// Signal the supervisor to exit cleanly.
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("serveAdoptedSupervisor returned unexpected error: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for serveAdoptedSupervisor to exit after context cancel")
	}
}
