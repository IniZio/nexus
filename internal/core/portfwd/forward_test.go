package portfwd

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type runResp struct {
	stdout, stderr string
	code           int
	err            error
}

func seqRun(capture *[][]string, resps []runResp) Runner {
	i := 0
	return func(_ context.Context, argv []string) (string, string, int, error) {
		if capture != nil {
			*capture = append(*capture, append([]string(nil), argv...))
		}
		if i < len(resps) {
			r := resps[i]
			i++
			return r.stdout, r.stderr, r.code, r.err
		}
		return "", "", 0, nil
	}
}

func argvEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

const (
	testSock = "/run/user/1000/cm-portfwd-test.sock"
	testHost = "sandbox-host"
)

func TestMasterAliveExit0(t *testing.T) {
	var calls [][]string
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(&calls, []runResp{{code: 0}})}
	alive, err := f.MasterAlive(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !alive {
		t.Fatal("want alive=true for exit 0")
	}
	want := []string{"ssh", "-O", "check", "-o", "ControlPath=" + testSock, testHost}
	if !argvEq(calls[0], want) {
		t.Fatalf("check argv\n got  %v\n want %v", calls[0], want)
	}
}

func TestMasterAliveExit255(t *testing.T) {
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(nil, []runResp{{code: 255}})}
	alive, err := f.MasterAlive(context.Background())
	if err != nil || alive {
		t.Fatalf("want false,nil got %v,%v", alive, err)
	}
}

func TestMasterAliveUnexpectedExit(t *testing.T) {
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(nil, []runResp{{code: 1}})}
	_, err := f.MasterAlive(context.Background())
	if err == nil {
		t.Fatal("want error for unexpected exit code 1")
	}
}

func TestEnsureMasterWhenAlive(t *testing.T) {
	var calls [][]string
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(&calls, []runResp{{code: 0}})}
	if err := f.EnsureMaster(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("alive master: want 1 call, got %d", len(calls))
	}
}

func TestEnsureMasterWhenDead(t *testing.T) {
	var calls [][]string
	f := &Forwarder{
		ControlPath: testSock,
		SSHHost:     testHost,
		Run: seqRun(&calls, []runResp{
			{code: 255},
			{code: 0},
		}),
	}
	if err := f.EnsureMaster(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("dead master: want 2 calls, got %d", len(calls))
	}
	wantOpen := MasterArgv(testHost, testSock)
	if !argvEq(calls[1], wantOpen) {
		t.Fatalf("open master argv\n got  %v\n want %v", calls[1], wantOpen)
	}
}

type funcCloser struct{ fn func() error }

func (fc *funcCloser) Close() error { return fc.fn() }

func ephemeralPort(t *testing.T) uint16 {
	t.Helper()
	tmp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := uint16(tmp.Addr().(*net.TCPAddr).Port)
	tmp.Close()
	return port
}

func TestF18AC1_ApplyBindsAndSpawnsProxyArgv(t *testing.T) {
	port := ephemeralPort(t)

	var mu sync.Mutex
	var capturedArgv [][]string
	runConn := func(argv []string, conn net.Conn) (io.Closer, <-chan struct{}, error) {
		mu.Lock()
		capturedArgv = append(capturedArgv, append([]string(nil), argv...))
		mu.Unlock()
		done := make(chan struct{})
		go func() {
			defer close(done)
			buf := make([]byte, 4096)
			for {
				n, err := conn.Read(buf)
				if n > 0 {
					conn.Write(buf[:n])
				}
				if err != nil {
					return
				}
			}
		}()
		return &funcCloser{func() error { conn.Close(); return nil }}, done, nil
	}

	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, RunConn: runConn}
	lf, err := f.Apply(context.Background(), port)
	if err != nil {
		t.Fatal(err)
	}
	defer lf.Close()

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(capturedArgv)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	argv := capturedArgv
	mu.Unlock()
	if len(argv) == 0 {
		t.Fatal("AC1: no proxy spawned")
	}
	wantArgv := []string{"ssh", "-S", testSock, "-o", "BatchMode=yes",
		"-W", fmt.Sprintf("127.0.0.1:%d", port), testHost}
	if !argvEq(argv[0], wantArgv) {
		t.Fatalf("AC1: argv\n got  %v\n want %v", argv[0], wantArgv)
	}

	msg := []byte("hello-pipe")
	conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write(msg); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != string(msg) {
		t.Fatalf("AC1: echo got %q want %q", buf, msg)
	}
}

func TestF18AC2_CloseEOFWithin100ms(t *testing.T) {
	port := ephemeralPort(t)

	runConn := func(argv []string, conn net.Conn) (io.Closer, <-chan struct{}, error) {
		done := make(chan struct{})
		go func() { defer close(done); io.Copy(io.Discard, conn) }()
		return &funcCloser{func() error { return nil }}, done, nil
	}
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, RunConn: runConn}
	lf, err := f.Apply(context.Background(), port)
	if err != nil {
		t.Fatal(err)
	}

	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}

	wait := time.Now().Add(time.Second)
	for time.Now().Before(wait) && lf.ConnCount() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if lf.ConnCount() == 0 {
		t.Fatal("AC2: connection never tracked")
	}

	lf.Close()

	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 1)
		_, err := conn.Read(buf)
		readDone <- err
	}()
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("AC2: expected error after Close, got nil")
		}
		if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatalf("AC2: got deadline timeout instead of close: %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		conn.Close()
		t.Fatal("AC2: conn did not get EOF within 100ms after Close")
	}
	conn.Close()

	ln2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatalf("AC2: port not free after Close: %v", err)
	}
	ln2.Close()
}

func TestF18AC3_PresentOwnPidIsOurs(t *testing.T) {
	myPID := os.Getpid()
	ssOut := fmt.Sprintf("LISTEN 0 128 127.0.0.1:9900 0.0.0.0:* users:((\"nexus\",pid=%d,fd=7))\n", myPID)
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost,
		Run: seqRun(nil, []runResp{{stdout: ssOut, code: 0}})}
	p, err := f.Present(context.Background(), 9900)
	if err != nil {
		t.Fatal(err)
	}
	if p != PresenceOurs {
		t.Fatalf("AC3: own pid must be PresenceOurs, got %v", p)
	}
}

func TestCancelArgv(t *testing.T) {
	var calls [][]string
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(&calls, []runResp{{code: 0}})}
	if err := f.Cancel(context.Background(), 3000); err != nil {
		t.Fatal(err)
	}
	want := []string{"ssh", "-O", "cancel", "-L", "3000:127.0.0.1:3000", "-o", "ControlPath=" + testSock, testHost}
	if !argvEq(calls[0], want) {
		t.Fatalf("cancel argv\n got  %v\n want %v", calls[0], want)
	}
}

func TestPresentArgv(t *testing.T) {
	var calls [][]string
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(&calls, []runResp{{code: 0}})}
	_, _ = f.Present(context.Background(), 80)
	want := []string{"ss", "-ltnp"}
	if !argvEq(calls[0], want) {
		t.Fatalf("present argv\n got  %v\n want %v", calls[0], want)
	}
}

func TestPresentTrueIPv6(t *testing.T) {
	ssOut := "Netid State Local Address:Port\ntcp LISTEN [::1]:45456 *:*"
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(nil, []runResp{{stdout: ssOut, code: 0}})}
	p, err := f.Present(context.Background(), 45456)
	if err != nil || p == PresenceAbsent {
		t.Fatalf("want bound,nil got %v,%v", p, err)
	}
}

func TestPresentTrueIPv4(t *testing.T) {
	ssOut := "tcp LISTEN 127.0.0.1:45456 0.0.0.0:*"
	if !portInOutput(ssOut, 45456) {
		t.Fatal("want true for 127.0.0.1:45456 form")
	}
}

func TestPresentFalse(t *testing.T) {
	ssOut := "Netid State Local Address:Port\ntcp LISTEN 127.0.0.1:22 0.0.0.0:*"
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(nil, []runResp{{stdout: ssOut, code: 0}})}
	p, err := f.Present(context.Background(), 45456)
	if err != nil || p != PresenceAbsent {
		t.Fatalf("want absent,nil got %v,%v", p, err)
	}
}

func TestPresentPrefixNonMatch(t *testing.T) {
	if portInOutput("tcp LISTEN [::1]:45456 *:*", 4545) {
		t.Fatal("port 4545 must not match :45456")
	}
}

func TestPresentSuffixNonMatch(t *testing.T) {
	if portInOutput("tcp LISTEN [::1]:454560 *:*", 45456) {
		t.Fatal("port 45456 must not match :454560")
	}
}

func TestPresentMacOSListeningTrue(t *testing.T) {
	line := "tcp4       0      0  127.0.0.1.45455        *.*                    LISTEN"
	if !portInOutput(line, 45455) {
		t.Fatal("want true for macOS netstat LISTEN line with matching port")
	}
}

func TestPresentMacOSListeningFalse(t *testing.T) {
	line := "tcp4       0      0  127.0.0.1.45455        *.*                    LISTEN"
	if portInOutput(line, 45456) {
		t.Fatal("want false for macOS netstat LISTEN line with non-matching port")
	}
}

func TestPresentMacOSEstablished(t *testing.T) {
	line := "tcp4       0      0  127.0.0.1.45455        203.0.113.1.443        ESTABLISHED"
	if portInOutput(line, 45455) {
		t.Fatal("ESTABLISHED line must not count as listener")
	}
}

func TestPresentDotSuffixNonMatch(t *testing.T) {
	if portInOutput("tcp4       0      0  127.0.0.1.454560        *.*                    LISTEN", 45456) {
		t.Fatal("port 45456 must not match .454560")
	}
}

func TestPresentDotPrefixNonMatch(t *testing.T) {
	if portInOutput("tcp4       0      0  127.0.0.1.45456        *.*                    LISTEN", 4545) {
		t.Fatal("port 4545 must not match .45456")
	}
}

func TestPresentSSFailsFallback(t *testing.T) {
	macOut := "tcp4       0      0  127.0.0.1.45455        *.*                    LISTEN"
	f := &Forwarder{
		ControlPath: testSock,
		SSHHost:     testHost,
		Run: seqRun(nil, []runResp{
			{code: 1},
			{stdout: macOut, code: 0},
		}),
	}
	p, err := f.Present(context.Background(), 45455)
	if err != nil || p == PresenceAbsent {
		t.Fatalf("want bound,nil after ss fail + netstat fallback; got %v,%v", p, err)
	}
}

func TestPresentBothUnavailableError(t *testing.T) {
	probeErr := fmt.Errorf("exec: not found")
	f := &Forwarder{
		ControlPath: testSock,
		SSHHost:     testHost,
		Run: seqRun(nil, []runResp{
			{err: probeErr},
			{err: probeErr},
		}),
	}
	p, err := f.Present(context.Background(), 80)
	if err == nil {
		t.Fatal("want non-nil error when both ss and netstat are unavailable")
	}
	if p != PresenceAbsent {
		t.Fatal("must not report present when probe failed")
	}
}

func TestEnsureMasterStderrInError(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "nosock")
	knownStderr := "ssh: connect to host sandbox-host port 22: Connection refused"
	f := &Forwarder{
		ControlPath: sock,
		SSHHost:     testHost,
		Run: seqRun(nil, []runResp{
			{code: 255},
			{code: 255, stderr: knownStderr},
		}),
	}
	err := f.EnsureMaster(context.Background())
	if err == nil {
		t.Fatal("want error when master open exits 255")
	}
	if !strings.Contains(err.Error(), "Connection refused") {
		t.Fatalf("want stderr in error, got: %s", err.Error())
	}
}

func TestEnsureMasterStaleSocketUnlinked(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "stale.ctl")
	fh, err := os.Create(sock)
	if err != nil {
		t.Fatal(err)
	}
	fh.Close()
	var calls [][]string
	f := &Forwarder{
		ControlPath: sock,
		SSHHost:     testHost,
		Run: seqRun(&calls, []runResp{
			{code: 255},
			{code: 0},
		}),
	}
	if err := f.EnsureMaster(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(sock); !os.IsNotExist(statErr) {
		t.Fatal("stale socket must be removed before master open")
	}
	if len(calls) != 2 {
		t.Fatalf("want 2 calls, got %d", len(calls))
	}
}

func TestEnsureMasterLiveSocketNotUnlinked(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "live.ctl")
	fh, err := os.Create(sock)
	if err != nil {
		t.Fatal(err)
	}
	fh.Close()
	var calls [][]string
	f := &Forwarder{
		ControlPath: sock,
		SSHHost:     testHost,
		Run:         seqRun(&calls, []runResp{{code: 0}}),
	}
	if err := f.EnsureMaster(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(sock); statErr != nil {
		t.Fatal("live socket must not be removed")
	}
	if len(calls) != 1 {
		t.Fatalf("want 1 call (check only), got %d", len(calls))
	}
}

func TestMasterArgv_CanonicalForm(t *testing.T) {
	got := MasterArgv("user@host", "/run/ctl")
	wantContains := []string{
		"ssh", "-M", "-N", "-f",
		"ControlPath=/run/ctl",
		"ControlPersist=yes",
		"BatchMode=yes",
		"GatewayPorts=no",
		"ConnectTimeout=10",
		"ServerAliveInterval=15",
		"ServerAliveCountMax=3",
		"user@host",
	}
	for _, want := range wantContains {
		found := false
		for _, a := range got {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("MasterArgv: missing %q in %v", want, got)
		}
	}
	last := got[len(got)-1]
	if last != "user@host" {
		t.Fatalf("MasterArgv: target must be last arg, got %q", last)
	}
}

func TestCancelNonZeroExitReturnsError(t *testing.T) {
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(nil, []runResp{
		{code: 1, stderr: "cancel: no such forward"},
	})}
	err := f.Cancel(context.Background(), 3000)
	if err == nil {
		t.Fatal("Cancel with non-zero exit must return error")
	}
	if !strings.Contains(err.Error(), "cancel: no such forward") {
		t.Fatalf("error must include stderr; got: %v", err)
	}
}

func TestCancelZeroExitNoError(t *testing.T) {
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(nil, []runResp{{code: 0}})}
	if err := f.Cancel(context.Background(), 3000); err != nil {
		t.Fatalf("Cancel with exit 0 must not error: %v", err)
	}
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

func TestLocalForward_HandleConnRaceWithClose(t *testing.T) {
	ready := make(chan struct{})
	unblock := make(chan struct{})
	var killed sync.Mutex
	var killedFlag bool

	runner := ConnRunner(func(_ []string, conn net.Conn) (io.Closer, <-chan struct{}, error) {
		close(ready)
		<-unblock
		done := make(chan struct{})
		k := closerFunc(func() error {
			killed.Lock()
			killedFlag = true
			killed.Unlock()
			close(done)
			return nil
		})
		return k, done, nil
	})

	lf := &LocalForward{
		ControlPath: "/tmp/test-race.ctl",
		SSHHost:     "host",
		RunConn:     runner,
	}

	server, client := net.Pipe()
	defer client.Close()

	go lf.handleConn(server)
	<-ready

	lf.Close()
	close(unblock)

	time.Sleep(50 * time.Millisecond)

	lf.mu.Lock()
	count := len(lf.conns)
	lf.mu.Unlock()

	killed.Lock()
	gotKilled := killedFlag
	killed.Unlock()

	if !gotKilled {
		t.Error("kill must be called when handleConn races with Close")
	}
	if count != 0 {
		t.Errorf("conns not empty after Close race: got %d", count)
	}
}

func TestForwarderEnsureMasterUsesCanonicalArgv(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "test.ctl")
	var capturedArgv []string
	callN := 0
	f := &Forwarder{
		ControlPath: sock,
		SSHHost:     "user@host",
		Run: func(_ context.Context, argv []string) (string, string, int, error) {
			callN++
			if callN == 1 {
				return "", "", 255, nil
			}
			capturedArgv = append([]string(nil), argv...)
			return "", "", 0, nil
		},
	}
	if err := f.EnsureMaster(context.Background()); err != nil {
		t.Fatal(err)
	}
	if capturedArgv == nil {
		t.Fatal("EnsureMaster: no master spawn call captured")
	}
	want := MasterArgv("user@host", sock)
	if !argvEq(capturedArgv, want) {
		t.Fatalf("EnsureMaster argv\n got  %v\n want %v", capturedArgv, want)
	}
}
