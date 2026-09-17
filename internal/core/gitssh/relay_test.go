package gitssh_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus/internal/core/gitssh"
)

func buildPktLineRef(old, new_, ref string) []byte {
	line := old + " " + new_ + " " + ref + "\x00report-status\n"
	hexLen := fmt.Sprintf("%04x", len(line)+4)
	return []byte(hexLen + line)
}

var pktFlush = []byte("0000")

func makeFakeSSH(t *testing.T, stdoutContent string, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s' %q\nexit %d\n", stdoutContent, exitCode)
	path := filepath.Join(dir, "ssh")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	return path
}

func makeFakeSSHSpeaksFirst(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '" + fakeAdvertisementShell + "'\n" +
		"cat >/dev/null\n" +
		"printf '" + fakeReportStatusShell + "'\n" +
		"exit 0\n"
	path := filepath.Join(dir, "ssh")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	return path
}

const (
	fakeAdvertisementShell = `004b1111111111111111111111111111111111111111 refs/heads/main\0report-status\n0000`
	fakeAdvertisement      = "004b1111111111111111111111111111111111111111 refs/heads/main\x00report-status\n0000"
	fakeReportStatusShell  = `000eunpack ok\n0000`
	fakeReportStatus       = "000eunpack ok\n0000"
)

func makeFakeSSHAgentProbe(t *testing.T, exitCode int) string {
	t.Helper()
	dir := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nexit %d\n", exitCode)
	path := filepath.Join(dir, "ssh-add")
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ssh-add: %v", err)
	}
	return path
}

func makeAgentSocket(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen agent socket: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	return sockPath
}

func runRelayOnPipe(t *testing.T, cfg gitssh.RelayConfig) net.Conn {
	t.Helper()

	udsPath := filepath.Join(t.TempDir(), "relay.sock")
	cfg.VsockUDSPath = udsPath

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	relayReady := make(chan struct{})
	go func() {
		close(relayReady)
		if err := gitssh.RunRelay(ctx, cfg); err != nil && ctx.Err() == nil {
			t.Logf("relay error: %v", err)
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(udsPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("relay socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = relayReady

	conn, err := net.Dial("unix", udsPath)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func readAllFrames(t *testing.T, conn net.Conn) ([]byte, int32) {
	t.Helper()
	var stdout []byte
	var exitCode int32
	for {
		ft, payload, err := gitssh.ReadFrame(conn)
		if err != nil {
			break
		}
		switch ft {
		case gitssh.FrameTypeStdout:
			stdout = append(stdout, payload...)
		case gitssh.FrameTypeExit:
			if len(payload) == 4 {
				exitCode = int32(payload[0])<<24 | int32(payload[1])<<16 | int32(payload[2])<<8 | int32(payload[3])
			}
			return stdout, exitCode
		}
	}
	return stdout, exitCode
}

func TestRelayE2E_UploadPack(t *testing.T) {
	fakeSSH := makeFakeSSH(t, "fake-upload-pack-output\n", 0)

	fakeSshAdd := makeFakeSSHAgentProbe(t, 1)
	origPath := os.Getenv("PATH")
	t.Setenv("PATH", filepath.Dir(fakeSshAdd)+":"+origPath)

	agentSock := makeAgentSocket(t)

	allowlist := []gitssh.AllowedRepo{
		{SSHHost: "git@github.com", OwnerRepo: "example-org/example-app"},
	}

	conn := runRelayOnPipe(t, gitssh.RelayConfig{
		SandboxID:       "test-sandbox",
		Allowlist:       allowlist,
		AllowedBranches: []string{"refs/heads/nexus/**"},
		SSHAuthSock:     agentSock,
		SSHExec:         fakeSSH,
	})

	req := gitssh.Request{
		Argv: []string{"git@github.com", "git-upload-pack '/example-org/example-app.git'"},
		Cwd:  "/tmp",
	}
	if err := gitssh.WriteRequest(conn, req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	stdout, code := readAllFrames(t, conn)
	if code != 0 {
		t.Errorf("exit code: got %d, want 0; stdout=%q", code, stdout)
	}
	if !strings.Contains(string(stdout), "fake-upload-pack-output") {
		t.Errorf("expected stdout to contain fake output, got %q", stdout)
	}
}

// TestRelayRefusalIsPktLineERR verifies refusals are delivered as git pkt-line ERR packets.
func TestRelayRefusalIsPktLineERR(t *testing.T) {
	fakeSSH := makeFakeSSH(t, "", 0)

	allowlist := []gitssh.AllowedRepo{
		{SSHHost: "git@github.com", OwnerRepo: "example-org/example-app"},
	}

	conn := runRelayOnPipe(t, gitssh.RelayConfig{
		SandboxID:   "test-sandbox",
		Allowlist:   allowlist,
		SSHAuthSock: "/dev/null",
		SSHExec:     fakeSSH,
	})

	req := gitssh.Request{
		Argv: []string{"git@github.com", "git-upload-pack 'inizio/example-app.git'"},
		Cwd:  "/tmp",
	}
	if err := gitssh.WriteRequest(conn, req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	stdout, code := readAllFrames(t, conn)
	if code == 0 {
		t.Fatal("expected non-zero exit for out-of-policy repo, got 0")
	}

	if len(stdout) < 4 {
		t.Fatalf("stdout too short to be a pkt-line: %q", stdout)
	}
	var pktLen int
	if _, err := fmt.Sscanf(string(stdout[:4]), "%04x", &pktLen); err != nil {
		t.Fatalf("stdout[0:4] %q is not a valid hex pkt-line length: %v", stdout[:4], err)
	}
	if pktLen != len(stdout) {
		t.Errorf("pkt-line length field says %d but stdout is %d bytes", pktLen, len(stdout))
	}

	payload := string(stdout[4:])
	if !strings.HasPrefix(payload, "ERR ") {
		t.Errorf("pkt-line payload does not start with \"ERR \": %q", payload)
	}
	if !strings.Contains(payload, "nexus:") {
		t.Errorf("pkt-line payload does not contain \"nexus:\": %q", payload)
	}
}

func TestRelayRefusesPolicy(t *testing.T) {
	fakeSSH := makeFakeSSH(t, "", 0)

	allowlist := []gitssh.AllowedRepo{
		{SSHHost: "git@github.com", OwnerRepo: "example-org/example-app"},
	}

	conn := runRelayOnPipe(t, gitssh.RelayConfig{
		SandboxID:   "test-sandbox",
		Allowlist:   allowlist,
		SSHAuthSock: "/dev/null",
		SSHExec:     fakeSSH,
	})

	req := gitssh.Request{
		Argv: []string{"git@github.com", "git-upload-pack '/inizio/example-app.git'"},
		Cwd:  "/tmp",
	}
	if err := gitssh.WriteRequest(conn, req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	stdout, code := readAllFrames(t, conn)
	if code == 0 {
		t.Error("expected non-zero exit for out-of-policy repo, got 0")
	}
	if !strings.Contains(string(stdout), "refused by egress policy") {
		t.Errorf("expected 'refused by egress policy' in stdout, got %q", stdout)
	}
}

func readStdoutUntil(t *testing.T, conn net.Conn, want string, timeout time.Duration) []byte {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	var stdout []byte
	for {
		ft, payload, err := gitssh.ReadFrame(conn)
		if err != nil {
			t.Fatalf("waiting for %q on stdout: %v (got so far %q)", want, err, stdout)
		}
		switch ft {
		case gitssh.FrameTypeStdout:
			stdout = append(stdout, payload...)
			if strings.Contains(string(stdout), want) {
				return stdout
			}
		case gitssh.FrameTypeExit:
			t.Fatalf("exit frame before %q appeared on stdout (got %q)", want, stdout)
		}
	}
}

/*
*
MUTATION-PIN: moving ParseRefUpdates back in front of sshCmd.Start (parse
before ssh) deadlocks this test — the advertisement never arrives because
ssh is never started — and it fails on the 5s deadline.
*/
func TestRelayReceivePack_ServerSpeaksFirst(t *testing.T) {
	fakeSSH := makeFakeSSHSpeaksFirst(t)

	agentSock := makeAgentSocket(t)
	origPath := os.Getenv("PATH")
	fakeSshAdd := makeFakeSSHAgentProbe(t, 1)
	t.Setenv("PATH", filepath.Dir(fakeSshAdd)+":"+origPath)

	allowlist := []gitssh.AllowedRepo{
		{SSHHost: "git@github.com", OwnerRepo: "example-org/example-app"},
	}

	conn := runRelayOnPipe(t, gitssh.RelayConfig{
		SandboxID:       "test-sandbox",
		Allowlist:       allowlist,
		AllowedBranches: []string{"refs/heads/nexus/**"},
		SSHAuthSock:     agentSock,
		SSHExec:         fakeSSH,
	})

	req := gitssh.Request{
		Argv: []string{"git@github.com", "git-receive-pack '/example-org/example-app.git'"},
		Cwd:  "/tmp",
	}
	if err := gitssh.WriteRequest(conn, req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	readStdoutUntil(t, conn, fakeAdvertisement, 5*time.Second)

	zeros40 := strings.Repeat("0", 40)
	ones40 := strings.Repeat("1", 40)
	if _, err := conn.Write(buildPktLineRef(zeros40, ones40, "refs/heads/nexus/proof")); err != nil {
		t.Fatalf("write pkt-line: %v", err)
	}
	if _, err := conn.Write(pktFlush); err != nil {
		t.Fatalf("write flush: %v", err)
	}
	if _, err := conn.Write([]byte("PACK-fake-bytes")); err != nil {
		t.Fatalf("write pack: %v", err)
	}
	if err := conn.(*net.UnixConn).CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	stdout, code := readAllFrames(t, conn)
	if code != 0 {
		t.Errorf("exit code: got %d, want 0; stdout=%q", code, stdout)
	}
	if !strings.Contains(string(stdout), fakeReportStatus) {
		t.Errorf("expected report-status %q from fake server after the pack, got %q", fakeReportStatus, stdout)
	}
}

/*
*
MUTATION-PIN: removing the AllowedBranches check in ParseRefUpdates makes
this test pass when it must fail.
*/
func TestRelayReceivePack_RefBlocked(t *testing.T) {
	fakeSSH := makeFakeSSHSpeaksFirst(t)

	agentSock := makeAgentSocket(t)
	origPath := os.Getenv("PATH")
	fakeSshAdd := makeFakeSSHAgentProbe(t, 1)
	t.Setenv("PATH", filepath.Dir(fakeSshAdd)+":"+origPath)

	allowlist := []gitssh.AllowedRepo{
		{SSHHost: "git@github.com", OwnerRepo: "example-org/example-app"},
	}

	conn := runRelayOnPipe(t, gitssh.RelayConfig{
		SandboxID:       "test-sandbox",
		Allowlist:       allowlist,
		AllowedBranches: []string{"refs/heads/nexus/**"},
		SSHAuthSock:     agentSock,
		SSHExec:         fakeSSH,
	})

	req := gitssh.Request{
		Argv: []string{"git@github.com", "git-receive-pack '/example-org/example-app.git'"},
		Cwd:  "/tmp",
	}
	if err := gitssh.WriteRequest(conn, req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	readStdoutUntil(t, conn, fakeAdvertisement, 5*time.Second)

	zeros40 := strings.Repeat("0", 40)
	ones40 := strings.Repeat("1", 40)
	pktLine := buildPktLineRef(zeros40, ones40, "refs/heads/main")
	if _, err := conn.Write(pktLine); err != nil {
		t.Fatalf("write pkt-line: %v", err)
	}
	if _, err := conn.Write(pktFlush); err != nil {
		t.Fatalf("write flush: %v", err)
	}

	stdout, code := readAllFrames(t, conn)
	if code == 0 {
		t.Error("expected non-zero exit for blocked ref, got 0")
	}
	if !strings.Contains(string(stdout), "refs/heads/main") {
		t.Errorf("expected denied ref name in stdout, got %q", stdout)
	}
	if !strings.Contains(string(stdout), "refused") {
		t.Errorf("expected 'refused' in stdout, got %q", stdout)
	}
}

/*
*
MUTATION-PIN: replacing writePktErrFrameAfterCommands with writePktErrFrame
in the deny_ref path makes the first stdout byte after the advertisement a
bare "00xxERR", which this test rejects (side-band-64k clients need a band-1
sideband packet wrapping the ERR pkt-line or git reports protocol error).
*/
func TestRelayReceivePack_RefBlocked_SidebandWrapped(t *testing.T) {
	fakeSSH := makeFakeSSHSpeaksFirst(t)

	agentSock := makeAgentSocket(t)
	origPath := os.Getenv("PATH")
	fakeSshAdd := makeFakeSSHAgentProbe(t, 1)
	t.Setenv("PATH", filepath.Dir(fakeSshAdd)+":"+origPath)

	conn := runRelayOnPipe(t, gitssh.RelayConfig{
		SandboxID:       "test-sandbox",
		Allowlist:       []gitssh.AllowedRepo{{SSHHost: "git@github.com", OwnerRepo: "example-org/example-app"}},
		AllowedBranches: []string{"refs/heads/nexus/**"},
		SSHAuthSock:     agentSock,
		SSHExec:         fakeSSH,
	})
	req := gitssh.Request{
		Argv: []string{"git@github.com", "git-receive-pack '/example-org/example-app.git'"},
		Cwd:  "/tmp",
	}
	if err := gitssh.WriteRequest(conn, req); err != nil {
		t.Fatalf("write request: %v", err)
	}
	readStdoutUntil(t, conn, fakeAdvertisement, 5*time.Second)

	line := strings.Repeat("0", 40) + " " + strings.Repeat("1", 40) + " refs/heads/main\x00report-status side-band-64k\n"
	pkt := fmt.Sprintf("%04x", len(line)+4) + line
	if _, err := conn.Write([]byte(pkt)); err != nil {
		t.Fatalf("write pkt-line: %v", err)
	}
	if _, err := conn.Write(pktFlush); err != nil {
		t.Fatalf("write flush: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	stdout, code := readAllFrames(t, conn)
	if code == 0 {
		t.Fatal("expected non-zero exit for blocked ref, got 0")
	}
	if len(stdout) < 13 {
		t.Fatalf("stdout too short for a sideband packet: %q", stdout)
	}
	if !strings.HasSuffix(string(stdout), "0000") {
		t.Fatalf("expected trailing flush-pkt, got %q", stdout)
	}
	stdout = stdout[:len(stdout)-4]
	var outerLen int
	if _, err := fmt.Sscanf(string(stdout[:4]), "%04x", &outerLen); err != nil || outerLen != len(stdout) {
		t.Fatalf("outer pkt-line length %q does not cover stdout (%d bytes): %v", stdout[:4], len(stdout), err)
	}
	if stdout[4] != 1 {
		t.Fatalf("expected sideband band 1, got byte %d (%q)", stdout[4], stdout)
	}
	inner := stdout[5:]
	var innerLen int
	if _, err := fmt.Sscanf(string(inner[:4]), "%04x", &innerLen); err != nil || innerLen != len(inner) {
		t.Fatalf("inner pkt-line length %q does not cover inner (%d bytes): %v", inner[:4], len(inner), err)
	}
	if !strings.HasPrefix(string(inner[4:]), "ERR nexus: refused: ref refs/heads/main") {
		t.Errorf("inner payload is not the ERR refusal: %q", inner[4:])
	}
}
