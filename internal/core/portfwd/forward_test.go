package portfwd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestApplyArgv(t *testing.T) {
	var calls [][]string
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(&calls, []runResp{{code: 0}})}
	if err := f.Apply(context.Background(), 3000); err != nil {
		t.Fatal(err)
	}
	want := []string{"ssh", "-O", "forward", "-L", "3000:127.0.0.1:3000", "-o", "ControlPath=" + testSock, testHost}
	if !argvEq(calls[0], want) {
		t.Fatalf("apply argv\n got  %v\n want %v", calls[0], want)
	}
}

func TestApplySamePortInvariant(t *testing.T) {
	var calls [][]string
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(&calls, []runResp{{code: 0}})}
	if err := f.Apply(context.Background(), 8080); err != nil {
		t.Fatal(err)
	}
	for i, arg := range calls[0] {
		if arg != "-L" || i+1 >= len(calls[0]) {
			continue
		}
		if calls[0][i+1] != "8080:127.0.0.1:8080" {
			t.Fatalf("same-port invariant violated: -L %q", calls[0][i+1])
		}
		return
	}
	t.Fatal("no -L argument in apply argv")
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
	want := []string{"ss", "-ltn"}
	if !argvEq(calls[0], want) {
		t.Fatalf("present argv\n got  %v\n want %v", calls[0], want)
	}
}

func TestPresentTrueIPv6(t *testing.T) {
	ssOut := "Netid State Local Address:Port\ntcp LISTEN [::1]:45456 *:*"
	f := &Forwarder{ControlPath: testSock, SSHHost: testHost, Run: seqRun(nil, []runResp{{stdout: ssOut, code: 0}})}
	ok, err := f.Present(context.Background(), 45456)
	if err != nil || !ok {
		t.Fatalf("want true,nil got %v,%v", ok, err)
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
	ok, err := f.Present(context.Background(), 45456)
	if err != nil || ok {
		t.Fatalf("want false,nil got %v,%v", ok, err)
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
	ok, err := f.Present(context.Background(), 45455)
	if err != nil || !ok {
		t.Fatalf("want true,nil after ss fail + netstat fallback; got %v,%v", ok, err)
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
	ok, err := f.Present(context.Background(), 80)
	if err == nil {
		t.Fatal("want non-nil error when both ss and netstat are unavailable")
	}
	if ok {
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
		Run: seqRun(&calls, []runResp{{code: 0}}),
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
