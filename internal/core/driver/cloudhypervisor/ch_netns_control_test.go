//go:build linux

package cloudhypervisor

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// newControlHarness stands up the REAL production pair: a live tapPump
// bridging a fake TAP socketpair to a swappable pump conn, plus a real
// netnsControlServer bound on a real Unix socket serving that same pump.
//
// This is deliberately not a stand-in for the production call site. tapPump,
// newSwappableConn, startNetnsControlServer, and ReacquirePerimeter are the
// exact functions RunNetnsChild and the supervisor use; only the TAP fd is
// simulated (a socketpair, which is packet-mode like a real IFF_NO_PI tap),
// because opening a real tap needs CAP_NET_ADMIN and a netns.
type controlHarness struct {
	t         *testing.T
	tapGuest  net.Conn // stands in for the guest side of the TAP
	pump      *swappableConn
	srv       *netnsControlServer
	perimHost net.Conn // the supervisor's end, before any crash
	sandboxID string
	dir       string
}

// newTestConnPair is newTestSocketpair with the concrete net.Conn type
// retained, which these tests need for SetReadDeadline on both ends.
func newTestConnPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	a, b, err := unixgramPair()
	if err != nil {
		t.Fatalf("unixgramPair: %v", err)
	}
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})
	return a, b
}

// shortTempDir returns a temp dir with a short path, so the control socket
// bound inside it fits within AF_UNIX's 108-byte sun_path limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "nx3ctl")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func newControlHarness(t *testing.T) *controlHarness {
	t.Helper()

	tapChild, tapGuest := newTestConnPair(t)
	perimChild, perimHost := newTestConnPair(t)

	pump := newSwappableConn(perimChild)
	// A short dir, not t.TempDir(): AF_UNIX sun_path is 108 bytes, and Go's
	// subtest-derived temp dir names blow past it.
	dir := shortTempDir(t)
	sandboxID := "sbx0123456789ab"

	srv, err := startNetnsControlServer(dir, sandboxID, pump)
	if err != nil {
		t.Fatalf("startNetnsControlServer: %v", err)
	}
	go srv.Serve()

	pumpDone := make(chan struct{})
	go func() {
		defer close(pumpDone)
		tapPump(tapChild, pump)
	}()

	h := &controlHarness{
		t: t, tapGuest: tapGuest, pump: pump, srv: srv,
		perimHost: perimHost, sandboxID: sandboxID, dir: dir,
	}
	t.Cleanup(func() {
		srv.Close()
		tapChild.Close()
		pump.closePermanently()
		select {
		case <-pumpDone:
		case <-time.After(5 * time.Second):
			t.Error("tapPump did not return after closePermanently")
		}
	})
	return h
}

func (h *controlHarness) sockPath() string  { return ControlSocketPath(h.dir, h.sandboxID) }
func (h *controlHarness) tokenPath() string { return ControlTokenPath(h.dir, h.sandboxID) }

// reacquire calls the REAL production supervisor-side entry point.
func (h *controlHarness) reacquire() (*os.File, error) {
	st, err := ReadProcStat(os.Getpid())
	if err != nil {
		h.t.Fatalf("ReadProcStat(self): %v", err)
	}
	return ReacquirePerimeter(h.sockPath(), h.tokenPath(), h.sandboxID, os.Getpid(), st.StartTime)
}

// assertFrameReachesGuest writes a frame on perim and requires it to arrive
// on the guest side of the TAP — i.e. the host→guest direction is LIVE.
func assertFrameReachesGuest(t *testing.T, perim net.Conn, tapGuest net.Conn, payload string) {
	t.Helper()
	if _, err := perim.Write([]byte(payload)); err != nil {
		t.Fatalf("write frame to perimeter: %v", err)
	}
	_ = tapGuest.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, tapBufSize)
	n, err := tapGuest.Read(buf)
	if err != nil {
		t.Fatalf("frame did not reach the guest (host→guest direction is dead): %v", err)
	}
	if got := string(buf[:n]); got != payload {
		t.Fatalf("guest received %q, want %q", got, payload)
	}
}

// TestReacquire_RestoresHostToGuestAfterSupervisorDeath is the core proof:
// after the supervisor's perimeter end is destroyed (simulating kill -9), the
// host→guest direction is dead; after a re-acquisition through the control
// socket it is live again, and tapPump NEVER returned — which is what keeps
// the VM alive (a returned tapPump exits the child and kills CH via
// Pdeathsig).
func TestReacquire_RestoresHostToGuestAfterSupervisorDeath(t *testing.T) {
	h := newControlHarness(t)

	// Baseline: the original perimeter reaches the guest.
	assertFrameReachesGuest(t, h.perimHost, h.tapGuest, "before-crash")

	// The supervisor dies: its end of the socketpair goes away. This is what
	// leaves the guest network-dead in the host→guest direction today.
	h.perimHost.Close()

	// Give the pump goroutine a chance to observe the read error and park on
	// the generation channel rather than exiting.
	time.Sleep(200 * time.Millisecond)

	newPerim, err := h.reacquire()
	if err != nil {
		t.Fatalf("ReacquirePerimeter: %v", err)
	}
	defer newPerim.Close()
	conn, err := net.FileConn(newPerim)
	if err != nil {
		t.Fatalf("FileConn(new perimeter): %v", err)
	}
	defer conn.Close()

	assertFrameReachesGuest(t, conn, h.tapGuest, "after-reacquire")
}

// TestReacquire_GuestToHostSurvivesSwap proves the OTHER direction is
// re-pointed too: frames the guest sends after the swap arrive on the NEW
// perimeter, not the dead one. Without this, a swap could restore only
// half the perimeter — a partially-rebuilt perimeter, which the fail-closed
// rail exists to prevent.
func TestReacquire_GuestToHostSurvivesSwap(t *testing.T) {
	h := newControlHarness(t)
	h.perimHost.Close()
	time.Sleep(200 * time.Millisecond)

	newPerim, err := h.reacquire()
	if err != nil {
		t.Fatalf("ReacquirePerimeter: %v", err)
	}
	defer newPerim.Close()
	conn, err := net.FileConn(newPerim)
	if err != nil {
		t.Fatalf("FileConn: %v", err)
	}
	defer conn.Close()

	if _, err := h.tapGuest.Write([]byte("guest-to-host")); err != nil {
		t.Fatalf("guest write: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, tapBufSize)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("guest→host frame did not reach the new perimeter: %v", err)
	}
	if got := string(buf[:n]); got != "guest-to-host" {
		t.Fatalf("new perimeter received %q, want %q", got, "guest-to-host")
	}
}

// sendRawControlRequest bypasses ReacquirePerimeter so a test can present a
// deliberately-bad request. Returns the child's response.
func sendRawControlRequest(t *testing.T, sockPath string, req ControlRequest, attachFD bool) ControlResponse {
	t.Helper()
	conn, err := net.DialTimeout("unix", sockPath, 5*time.Second)
	if err != nil {
		t.Fatalf("dial control socket: %v", err)
	}
	defer conn.Close()
	uconn := conn.(*net.UnixConn)
	_ = uconn.SetDeadline(time.Now().Add(5 * time.Second))

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var oob []byte
	if attachFD {
		_, pumpFile, perr := netnsSocketpairFiles()
		if perr != nil {
			t.Fatalf("socketpair: %v", perr)
		}
		defer pumpFile.Close()
		oob = syscall.UnixRights(int(pumpFile.Fd()))
	}
	if _, _, err := uconn.WriteMsgUnix(data, oob, nil); err != nil {
		t.Fatalf("write request: %v", err)
	}
	buf := make([]byte, 64*1024)
	n, err := uconn.Read(buf)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var resp ControlResponse
	if err := json.Unmarshal(buf[:n], &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	return resp
}

// selfIdentity returns this process's pid and starttime, which the harness's
// control server reports as its own identity.
func selfIdentity(t *testing.T) (int, uint64) {
	t.Helper()
	st, err := ReadProcStat(os.Getpid())
	if err != nil {
		t.Fatalf("ReadProcStat: %v", err)
	}
	return os.Getpid(), st.StartTime
}

// TestControl_RefusesUnauthenticatedPeer asserts the NEGATIVE direction for
// every authentication gate: each bad request must be REFUSED, and — the
// part that actually matters — the pump must be left untouched, so the
// attacker receives no frames at all.
func TestControl_RefusesUnauthenticatedPeer(t *testing.T) {
	pid, start := selfIdentity(t)

	cases := []struct {
		name       string
		mutate     func(*ControlRequest)
		wantReason string
	}{
		{
			name:       "no token",
			mutate:     func(r *ControlRequest) { r.Token = "" },
			wantReason: "invalid control token",
		},
		{
			name:       "wrong token",
			mutate:     func(r *ControlRequest) { r.Token = hex.EncodeToString(make([]byte, controlTokenBytes)) },
			wantReason: "invalid control token",
		},
		{
			name:       "non-hex token",
			mutate:     func(r *ControlRequest) { r.Token = "not-hex-at-all" },
			wantReason: "invalid control token",
		},
		{
			name:       "wrong sandbox id",
			mutate:     func(r *ControlRequest) { r.SandboxID = "sbxdeadbeefdead" },
			wantReason: "sandbox id does not match",
		},
		{
			name:       "wrong child pid",
			mutate:     func(r *ControlRequest) { r.ExpectChildPID = pid + 100000 },
			wantReason: "expected child pid does not match",
		},
		{
			name:       "zero starttime (pid-reuse guard absent)",
			mutate:     func(r *ControlRequest) { r.ExpectChildStartTime = 0 },
			wantReason: "starttime does not match",
		},
		{
			name:       "mismatched starttime (pid recycled)",
			mutate:     func(r *ControlRequest) { r.ExpectChildStartTime = start + 1 },
			wantReason: "starttime does not match",
		},
		{
			name:       "unsupported version",
			mutate:     func(r *ControlRequest) { r.Version = ControlProtocolVersion + 99 },
			wantReason: "unsupported control version",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newControlHarness(t)
			// The legitimate perimeter is still live at this point; the
			// attacker must not be able to displace it.
			assertFrameReachesGuest(t, h.perimHost, h.tapGuest, "legit-before")

			tok, err := os.ReadFile(h.tokenPath())
			if err != nil {
				t.Fatalf("read token: %v", err)
			}
			req := ControlRequest{
				Version:              ControlProtocolVersion,
				Token:                string(tok),
				SandboxID:            h.sandboxID,
				ExpectChildPID:       pid,
				ExpectChildStartTime: start,
			}
			tc.mutate(&req)

			resp := sendRawControlRequest(t, h.sockPath(), req, true)
			if resp.OK {
				t.Fatalf("child ACCEPTED a request it must refuse (%s)", tc.name)
			}
			if !strings.Contains(resp.Reason, tc.wantReason) {
				t.Fatalf("refusal reason = %q, want it to contain %q", resp.Reason, tc.wantReason)
			}

			// THE critical assertion: the pump was not swapped, so the
			// legitimate perimeter still owns the guest's traffic.
			assertFrameReachesGuest(t, h.perimHost, h.tapGuest, "legit-after")
		})
	}
}

// TestControl_RefusesRequestWithoutFD asserts that a fully-authenticated
// request carrying NO fd is still refused. Accepting it would swap "nothing"
// into the pump, producing a perimeter that reads as working while carrying
// no traffic — the partially-rebuilt state the rail forbids.
func TestControl_RefusesRequestWithoutFD(t *testing.T) {
	h := newControlHarness(t)
	pid, start := selfIdentity(t)
	tok, err := os.ReadFile(h.tokenPath())
	if err != nil {
		t.Fatalf("read token: %v", err)
	}
	resp := sendRawControlRequest(t, h.sockPath(), ControlRequest{
		Version:              ControlProtocolVersion,
		Token:                string(tok),
		SandboxID:            h.sandboxID,
		ExpectChildPID:       pid,
		ExpectChildStartTime: start,
	}, false)
	if resp.OK {
		t.Fatal("child accepted a request with no pump fd")
	}
	if !strings.Contains(resp.Reason, "no pump fd") {
		t.Fatalf("reason = %q, want it to mention a missing pump fd", resp.Reason)
	}
	assertFrameReachesGuest(t, h.perimHost, h.tapGuest, "still-legit")
}

// TestReacquirePerimeter_RefusesAndAcquiresNothing asserts the SUPERVISOR
// side of the fail-closed rail: a refusal must surface as ErrControlRefused
// with no perimeter file returned, so a caller cannot mistake it for a
// partial success.
func TestReacquirePerimeter_RefusesAndAcquiresNothing(t *testing.T) {
	h := newControlHarness(t)
	_, start := selfIdentity(t)

	// A peer that found the socket path but presents a bogus token file.
	badToken := filepath.Join(t.TempDir(), "bad.token")
	if err := os.WriteFile(badToken, []byte(hex.EncodeToString(make([]byte, controlTokenBytes))), 0o600); err != nil {
		t.Fatalf("write bad token: %v", err)
	}

	f, err := ReacquirePerimeter(h.sockPath(), badToken, h.sandboxID, os.Getpid(), start)
	if f != nil {
		f.Close()
		t.Fatal("ReacquirePerimeter returned a perimeter file for a refused request")
	}
	if !errors.Is(err, ErrControlRefused) {
		t.Fatalf("err = %v, want it to wrap ErrControlRefused", err)
	}
	assertFrameReachesGuest(t, h.perimHost, h.tapGuest, "untouched")
}

// TestReacquirePerimeter_RefusesZeroStartTime asserts the supervisor refuses
// BEFORE making contact when it has no pid-reuse guard to present — matching
// AdoptNetnsRuntime's precedent that a zero starttime is refused rather than
// treated as "skip the check".
func TestReacquirePerimeter_RefusesZeroStartTime(t *testing.T) {
	h := newControlHarness(t)
	f, err := ReacquirePerimeter(h.sockPath(), h.tokenPath(), h.sandboxID, os.Getpid(), 0)
	if f != nil {
		f.Close()
		t.Fatal("returned a perimeter file despite a zero starttime")
	}
	if err == nil || !strings.Contains(err.Error(), "pid-reuse guard") {
		t.Fatalf("err = %v, want a refusal citing the missing pid-reuse guard", err)
	}
}

// TestControlServer_SocketAndTokenPermissions asserts the filesystem half of
// the authentication design: the directory is 0700 and the token is 0600. A
// looser mode would let any process of any uid reach the secret and defeat
// the SO_PEERCRED gate that depends on it.
func TestControlServer_SocketAndTokenPermissions(t *testing.T) {
	h := newControlHarness(t)

	dirInfo, err := os.Stat(h.dir)
	if err != nil {
		t.Fatalf("stat control dir: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("control dir mode = %#o, want 0700", got)
	}
	tokInfo, err := os.Stat(h.tokenPath())
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if got := tokInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("token mode = %#o, want 0600", got)
	}
}

// TestControlServer_TokenIsUnpredictable guards against a token that is
// constant, empty, or otherwise derivable — the shared secret is what
// distinguishes a legitimate replacement supervisor from any other process
// running as the same uid.
func TestControlServer_TokenIsUnpredictable(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		h := newControlHarness(t)
		tok, err := os.ReadFile(h.tokenPath())
		if err != nil {
			t.Fatalf("read token: %v", err)
		}
		raw, err := hex.DecodeString(string(tok))
		if err != nil {
			t.Fatalf("token is not hex: %v", err)
		}
		if len(raw) != controlTokenBytes {
			t.Fatalf("token length = %d, want %d", len(raw), controlTokenBytes)
		}
		if allZero(raw) {
			t.Fatal("token is all zero bytes")
		}
		if seen[string(raw)] {
			t.Fatal("token repeated across servers — it is not random")
		}
		seen[string(raw)] = true
	}
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// TestTapPump_ConnGoroutineSurvivesConnDeath is the hazard-1 guard.
//
// It deliberately does NOT assert "tapPump did not return": that property is
// held up by the OTHER goroutine (tapFd.Read is still blocked), so it stays
// true even if the conn→tap goroutine exits, and a test asserting it would
// pass against the very bug it is meant to catch.
//
// The invariant that actually matters is that the conn→tap goroutine is
// still ALIVE and serviceable after its conn dies — because a goroutine that
// has returned cannot be swapped into later; there is no stack left to
// resume. The only way to observe "still alive" from outside is to swap a
// fresh conn in and watch a frame come out of the tap.
func TestTapPump_ConnGoroutineSurvivesConnDeath(t *testing.T) {
	tapChild, tapGuest := newTestConnPair(t)
	perimChild, perimHost := newTestConnPair(t)

	pump := newSwappableConn(perimChild)
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		tapPump(tapChild, pump)
	}()

	// The supervisor dies: its end of the socketpair goes away.
	perimHost.Close()
	time.Sleep(200 * time.Millisecond)

	// A replacement hands the child a fresh pump end.
	newPerim, newPump := newTestConnPair(t)
	if err := pump.swap(newPump); err != nil {
		t.Fatalf("swap: %v", err)
	}

	// If the conn→tap goroutine had exited on the read error, nothing would
	// ever read newPump and this frame would never reach the guest.
	assertFrameReachesGuest(t, newPerim, tapGuest, "post-swap")

	// Real teardown still terminates promptly.
	tapChild.Close()
	pump.closePermanently()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("tapPump did not return after closePermanently — real teardown would hang")
	}
}

// TestPeerCred_ReportsRealKernelIdentity proves the SO_PEERCRED primitive is
// plumbed correctly: on a real accepted Unix connection it returns the
// kernel's view of the peer, not anything the peer supplied.
//
// The uid GATE itself cannot be exercised by a refusal test here — this test
// process has exactly one uid, so it can only ever connect as an authorised
// peer. What is provable without root is that the value the gate compares is
// the true kernel-stamped identity; that it is compared, and that the
// comparison is load-bearing, is established by mutation (inverting the
// comparison turns every control test RED).
func TestPeerCred_ReportsRealKernelIdentity(t *testing.T) {
	dir := shortTempDir(t)
	sockPath := filepath.Join(dir, "peercred.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	type result struct {
		uc  *syscall.Ucred
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		c, aerr := ln.(*net.UnixListener).AcceptUnix()
		if aerr != nil {
			resCh <- result{nil, aerr}
			return
		}
		defer c.Close()
		uc, cerr := peerCred(c)
		resCh <- result{uc, cerr}
	}()

	client, err := net.Dial("unix", sockPath)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	res := <-resCh
	if res.err != nil {
		t.Fatalf("peerCred: %v", res.err)
	}
	if got, want := int(res.uc.Uid), os.Getuid(); got != want {
		t.Fatalf("peer uid = %d, want %d", got, want)
	}
	if got, want := int(res.uc.Pid), os.Getpid(); got != want {
		t.Fatalf("peer pid = %d, want %d (kernel-stamped, not peer-claimed)", got, want)
	}
}

// TestSwappableConn_SwapAfterCloseRefuses guards the shutdown race: a swap
// racing a real teardown must be refused rather than installing a conn
// nothing will ever read, which would silently strand the caller's fd.
func TestSwappableConn_SwapAfterCloseRefuses(t *testing.T) {
	a, b := newTestConnPair(t)
	defer b.Close()
	pump := newSwappableConn(a)
	pump.closePermanently()

	c, d := newTestConnPair(t)
	defer c.Close()
	defer d.Close()
	if err := pump.swap(c); !errors.Is(err, errPumpClosed) {
		t.Fatalf("swap after closePermanently returned %v, want errPumpClosed", err)
	}
}

// TestStartNetnsControlServer_RefusesEmptySandboxID guards the case where the
// child is started without a sandbox ID: binding a control socket that no
// caller can name, and that authenticates against an empty string, is worse
// than having no control socket at all.
func TestStartNetnsControlServer_RefusesEmptySandboxID(t *testing.T) {
	a, b := newTestConnPair(t)
	defer a.Close()
	defer b.Close()
	srv, err := startNetnsControlServer(t.TempDir(), "", newSwappableConn(a))
	if err == nil {
		srv.Close()
		t.Fatal("startNetnsControlServer accepted an empty sandbox ID")
	}
	if !strings.Contains(err.Error(), "sandboxID is empty") {
		t.Fatalf("err = %v, want it to name the empty sandbox ID", err)
	}
}

// TestControlSocketPath_DerivedFromSandboxID documents that the path a
// replacement supervisor computes from the persisted record matches the one
// the child binds — they are the same function, so a drift here would break
// re-acquisition silently.
func TestControlSocketPath_DerivedFromSandboxID(t *testing.T) {
	dir := "/run/nexus3/netns-control"
	id := "sbx0123456789ab"
	if got, want := ControlSocketPath(dir, id), filepath.Join(dir, "netns-control-"+id+".sock"); got != want {
		t.Fatalf("ControlSocketPath = %q, want %q", got, want)
	}
	if got, want := ControlTokenPath(dir, id), filepath.Join(dir, "netns-control-"+id+".token"); got != want {
		t.Fatalf("ControlTokenPath = %q, want %q", got, want)
	}
	if ControlSocketPath(dir, id) == ControlTokenPath(dir, id) {
		t.Fatal("socket and token share a path")
	}
}

// TestRunNetnsChild_WiresControlSocket is the anti-"green with the wiring
// deleted" guard (the ticket-06 trap: deleting the production wiring left
// two whole packages passing).
//
// The tests above exercise startNetnsControlServer and tapPump directly. That
// proves the mechanism works, but NOT that RunNetnsChild actually builds it —
// delete the block in RunNetnsChild and every one of them still passes. This
// test reads the production source and asserts the wiring is present: the
// child must construct a swappable pump, pass THAT to tapPump, and start the
// control server on it.
//
// A source-level assertion is the right tool here specifically because the
// alternative (booting a real netns child) needs CAP_NET_ADMIN and a real
// VM, which is what the live proof covers. This catches the cheap regression;
// the live proof catches the rest.
func TestRunNetnsChild_WiresControlSocket(t *testing.T) {
	src, err := os.ReadFile("ch_netns.go")
	if err != nil {
		t.Fatalf("read ch_netns.go: %v", err)
	}
	body := string(src)
	idx := strings.Index(body, "func RunNetnsChild()")
	if idx < 0 {
		t.Fatal("RunNetnsChild not found in ch_netns.go")
	}
	fn := body[idx:]

	for _, want := range []string{
		"newSwappableConn(pumpConn)",
		"startNetnsControlServer(",
		"tapPump(hostTapFile, pump)",
		"go ctrl.Serve()",
		"netnsEnvControlDir",
		"netnsEnvSandboxID",
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("RunNetnsChild is missing production wiring %q — the control socket mechanism is built but never used, so a crashed supervisor cannot re-acquire", want)
		}
	}

	// The pre-swap form must be gone: passing the raw conn would compile but
	// silently disable every swap.
	if strings.Contains(fn, "tapPump(hostTapFile, pumpConn)") {
		t.Error("RunNetnsChild still passes the raw pumpConn to tapPump; swaps would be impossible")
	}
}

// TestStartNetnsRuntime_PassesControlEnv asserts the PARENT half of the same
// wiring: StartNetnsRuntime must tell the child where to bind its control
// socket and which sandbox it serves, and must report both paths back on the
// runtime so they can be persisted. Without this the child binds nothing and
// the record carries no way to find it.
func TestStartNetnsRuntime_PassesControlEnv(t *testing.T) {
	src, err := os.ReadFile("ch_netns.go")
	if err != nil {
		t.Fatalf("read ch_netns.go: %v", err)
	}
	body := string(src)
	idx := strings.Index(body, "func StartNetnsRuntime(")
	if idx < 0 {
		t.Fatal("StartNetnsRuntime not found")
	}
	fn := body[idx:]
	for _, want := range []string{
		"netnsEnvControlDir",
		"netnsEnvSandboxID",
		"ControlSocket:  ControlSocketPath(controlDir, id.String())",
		"ControlToken:   ControlTokenPath(controlDir, id.String())",
	} {
		if !strings.Contains(fn, want) {
			t.Errorf("StartNetnsRuntime is missing %q", want)
		}
	}
}
