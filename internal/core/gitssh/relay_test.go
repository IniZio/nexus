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

	"github.com/IniZio/nexus3/internal/core/gitssh"
)

// buildPktLineRef returns a pkt-line encoded ref-update line.
// Format: <hex-len><old> <new> <ref>\x00<caps>\n
func buildPktLineRef(old, new_, ref string) []byte {
	line := old + " " + new_ + " " + ref + "\x00report-status\n"
	hexLen := fmt.Sprintf("%04x", len(line)+4)
	return []byte(hexLen + line)
}

// pktFlush is the git flush packet.
var pktFlush = []byte("0000")

// makeFakeSSH writes a shell script to a temp file and returns its path.
// The script writes stdout content then exits with exitCode.
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

// makeFakeSSHAgent writes a shell script that exits with the given code.
// For use as a fake ssh-add.
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

// makeAgentSocket creates a real listening Unix socket that ssh-add can
// "connect" to (it accepts and immediately closes). Returns the socket path.
// Used as a fake live SSH agent socket for tests.
func makeAgentSocket(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	sockPath := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen agent socket: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	// Accept connections in background and close them immediately.
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

// runRelayOnPipe starts RunRelay on a temp UDS and connects two ends with a
// net.Pipe-backed transport. Returns (clientConn, cancelFunc).
func runRelayOnPipe(t *testing.T, cfg gitssh.RelayConfig) net.Conn {
	t.Helper()

	udsPath := filepath.Join(t.TempDir(), "relay.sock")
	cfg.VsockUDSPath = udsPath

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	relayReady := make(chan struct{})
	go func() {
		// Signal readiness by checking when socket appears.
		close(relayReady)
		if err := gitssh.RunRelay(ctx, cfg); err != nil && ctx.Err() == nil {
			t.Logf("relay error: %v", err)
		}
	}()

	// Wait for socket to exist.
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

// readAllFrames reads all frames from conn until EOF or FrameTypeExit.
// Returns (stdoutBytes, exitCode).
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

	// We need ssh-add to report agent reachable (exit 1 = no keys but alive).
	// Override PATH to include a fake ssh-add.
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
		AllowedBranches: []string{"refs/heads/nexus3/**"},
		SSHAuthSock:     agentSock,
		SSHExec:         fakeSSH,
	})

	// Send request.
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

// TestRelayRefusalIsPktLineERR verifies that every refusal from the relay is
// delivered as a git pkt-line ERR packet so git prints
// "remote error: nexus3: ..." rather than a parse error.
//
// A valid pkt-line ERR must:
//   - start with exactly 4 ASCII hex digits that equal 4 + len(rest)
//   - have the payload begin with "ERR "
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

	// Request for an out-of-policy repo.
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

	// Verify pkt-line structure: first 4 bytes are hex length.
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

	// Payload (bytes 4 onward) must start with "ERR ".
	payload := string(stdout[4:])
	if !strings.HasPrefix(payload, "ERR ") {
		t.Errorf("pkt-line payload does not start with \"ERR \": %q", payload)
	}
	if !strings.Contains(payload, "nexus3:") {
		t.Errorf("pkt-line payload does not contain \"nexus3:\": %q", payload)
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

	// Send request for a repo NOT in the allowlist (inizio fork = out of policy).
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

func TestRelayReceivePack_RefBlocked(t *testing.T) {
	// MUTATION-PIN: removing the AllowedBranches check in ParseRefUpdates makes
	// this test pass when it must fail.

	fakeSSH := makeFakeSSH(t, "", 0)

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
		AllowedBranches: []string{"refs/heads/nexus3/**"}, // refs/heads/main is NOT allowed
		SSHAuthSock:     agentSock,
		SSHExec:         fakeSSH,
	})

	// Send receive-pack request for an in-policy repo.
	req := gitssh.Request{
		Argv: []string{"git@github.com", "git-receive-pack '/example-org/example-app.git'"},
		Cwd:  "/tmp",
	}
	if err := gitssh.WriteRequest(conn, req); err != nil {
		t.Fatalf("write request: %v", err)
	}

	// Send pkt-line ref update for refs/heads/main (forbidden).
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
