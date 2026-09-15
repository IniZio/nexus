package gitssh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

func writePktErrFrame(conn net.Conn, msg string) {
	errLine := "ERR " + msg
	pkt := fmt.Sprintf("%04x", 4+len(errLine)) + errLine
	_ = WriteStdout(conn, []byte(pkt))
	_ = WriteExitFrame(conn, 1)
}

// writePktErrFrameAfterCommands sends a refusal post-command-section. Must wrap ERR in band-1
// when side-band is negotiated; bare ERR is misread as band 'E' ("bad band #69").
func writePktErrFrameAfterCommands(conn net.Conn, commandSection []byte, msg string) {
	if !bytes.Contains(commandSection, []byte("side-band")) {
		writePktErrFrame(conn, msg)
		return
	}
	errLine := "ERR " + msg
	inner := fmt.Sprintf("%04x", 4+len(errLine)) + errLine
	outer := fmt.Sprintf("%04x", 4+1+len(inner)) + "\x01" + inner
	_ = WriteStdout(conn, []byte(outer+"0000")) // trailing flush-pkt prevents "unexpected disconnect" message
	_ = WriteExitFrame(conn, 1)
}

// RelayConfig configures the host-side git SSH relay listener.
type RelayConfig struct {
	SandboxID       string                                           // log fields only
	VsockUDSPath    string                                           // CH vsock UDS: <socketDir>/<id>.vsock_1026
	Allowlist       []AllowedRepo                                    // (SSHHost, OwnerRepo) pairs
	AllowedBranches []string                                         // branch policy for receive-pack
	OnEgress        func(host, verdict, reason string, ts time.Time) // allow/deny callback; may be nil
	UID             int                                              // for fallback SSH agent in /run/user/<uid>/
	SSHAuthSock     string                                           // inherited SSH_AUTH_SOCK; may be empty or stale
	SSHExec         string                                           // path to ssh binary; defaults to "ssh"
}

// RunRelay listens on cfg.VsockUDSPath, validates each session against policy, and relays
// git SSH commands to the host ssh binary. Blocks until ctx is cancelled.
func RunRelay(ctx context.Context, cfg RelayConfig) error {
	sshExec := cfg.SSHExec
	if sshExec == "" {
		sshExec = "ssh"
	}

	_ = os.Remove(cfg.VsockUDSPath) // remove stale socket

	ln, err := net.Listen("unix", cfg.VsockUDSPath)
	if err != nil {
		return fmt.Errorf("gitssh relay: listen on %s: %w", cfg.VsockUDSPath, err)
	}
	defer func() {
		ln.Close()
		_ = os.Remove(cfg.VsockUDSPath)
	}()
	stop := context.AfterFunc(ctx, func() { ln.Close() }) // unblocks Accept on cancel
	defer stop()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // normal shutdown
			}
			return fmt.Errorf("gitssh relay: accept: %w", err)
		}
		go serveSession(conn, cfg, sshExec)
	}
}

func serveSession(conn net.Conn, cfg RelayConfig, sshExec string) {
	defer conn.Close()

	req, err := ReadRequest(conn)
	if err != nil {
		slog.Warn("gitssh.relay.read_request_failed", "sandboxID", cfg.SandboxID, "err", err)
		return
	}

	cmd, err := ParseCommand(req.Argv)
	if err != nil {
		writePktErrFrame(conn, "nexus3: "+err.Error()+"\n")
		slog.Warn("gitssh.relay.parse_command_failed", "sandboxID", cfg.SandboxID, "err", err)
		return
	}

	// Allowlist check: only github.com via git@ SSH is supported in v1.
	if !repoAllowed(cfg.Allowlist, cmd.GitHost, cmd.OwnerRepo) {
		writePktErrFrame(conn, "nexus3: refused by egress policy: "+cmd.GitHost+" "+cmd.OwnerRepo+" not in nexus3.yaml egress.policy\n")
		if cfg.OnEgress != nil {
			cfg.OnEgress(cmd.BareHost, "deny", "git SSH: "+cmd.OwnerRepo+" not in policy", time.Now())
		}
		slog.Info("gitssh.relay.deny_policy",
			"sandboxID", cfg.SandboxID,
			"gitHost", cmd.GitHost,
			"ownerRepo", cmd.OwnerRepo,
		)
		return
	}

	authSock, ok := resolveSSHAuthSock(cfg.SSHAuthSock, cfg.UID)
	if !ok {
		writePktErrFrame(conn, "nexus3: SSH agent not reachable; reconnect with ssh -A or start ssh-agent\n")
		if cfg.OnEgress != nil {
			cfg.OnEgress(cmd.BareHost, "deny", "git SSH: SSH agent not reachable", time.Now())
		}
		slog.Warn("gitssh.relay.no_ssh_agent", "sandboxID", cfg.SandboxID)
		return
	}

	// Build SSH argv. The remote command is passed as a single shell token.
	remoteCmd := cmd.Service + " '" + cmd.RawPath + "'"
	sshArgs := []string{
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"--",
		cmd.GitHost,
		remoteCmd,
	}

	sshCmd := exec.Command(sshExec, sshArgs...) //nolint:gosec // argv validated above
	sshCmd.Env = buildSSHEnv(authSock, req.GitProtocol)

	// Use explicit StdinPipe so sshCmd.Wait() doesn't block on stdin draining
	// (which would deadlock if the client conn never sends EOF).
	stdinPipe, err := sshCmd.StdinPipe()
	if err != nil {
		slog.Error("gitssh.relay.stdin_pipe_failed", "sandboxID", cfg.SandboxID, "err", err)
		_ = WriteExitFrame(conn, 1)
		return
	}

	sshStdout, err := sshCmd.StdoutPipe()
	if err != nil {
		slog.Error("gitssh.relay.stdout_pipe_failed", "sandboxID", cfg.SandboxID, "err", err)
		_ = WriteExitFrame(conn, 1)
		return
	}

	if err := sshCmd.Start(); err != nil {
		writePktErrFrame(conn, "nexus3: failed to start ssh: "+err.Error()+"\n")
		slog.Error("gitssh.relay.ssh_start_failed", "sandboxID", cfg.SandboxID, "err", err)
		return
	}

	// Bridge started BEFORE receive-pack command parsing: advertisement must flow before
	// commands arrive, or a push deadlocks (client waits for ad; relay waits for commands).
	bridgeDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		readBuf := make([]byte, 32*1024)
		for {
			n, rerr := sshStdout.Read(readBuf)
			if n > 0 {
				if werr := WriteStdout(conn, readBuf[:n]); werr != nil {
					break
				}
			}
			if rerr != nil {
				break
			}
		}
	}()

	if cmd.Service == "git-receive-pack" {
		// Parse in a goroutine: an ssh that dies before commands arrive (auth failure)
		// must still produce an Exit frame rather than wedging the session.
		type refParse struct {
			buf       *bytes.Buffer
			deniedRef string
			malformed bool
		}
		parseDone := make(chan refParse, 1)
		go func() {
			buf, deniedRef, malformed := ParseRefUpdates(conn, cfg.AllowedBranches)
			parseDone <- refParse{buf: buf, deniedRef: deniedRef, malformed: malformed}
		}()
		var rp refParse
		select {
		case rp = <-parseDone:
		case <-bridgeDone:
			// ssh exited before the client finished its command section.
			_ = sshCmd.Wait()
			exitCode := 1
			if sshCmd.ProcessState != nil {
				exitCode = sshCmd.ProcessState.ExitCode()
			}
			slog.Warn("gitssh.relay.ssh_exited_before_commands",
				"sandboxID", cfg.SandboxID,
				"exitCode", exitCode,
			)
			_ = WriteExitFrame(conn, int32(exitCode)) //nolint:gosec // exit codes fit int32
			return
		}
		refBuf, deniedRef, malformed := rp.buf, rp.deniedRef, rp.malformed
		if malformed || deniedRef != "" {
			_ = sshCmd.Process.Kill()
			<-bridgeDone
			_ = sshCmd.Wait()
			if malformed {
				if cfg.OnEgress != nil {
					cfg.OnEgress(cmd.BareHost, "deny", "git SSH receive-pack: malformed pkt-line", time.Now())
				}
				writePktErrFrameAfterCommands(conn, refBuf.Bytes(), "nexus3: refused: push pkt-line header malformed or too large\n")
				return
			}
			if cfg.OnEgress != nil {
				cfg.OnEgress(cmd.BareHost, "deny", "git SSH receive-pack: ref "+deniedRef+" not in AllowedBranches", time.Now())
			}
			slog.Info("gitssh.relay.deny_ref",
				"sandboxID", cfg.SandboxID,
				"ref", deniedRef,
			)
			writePktErrFrameAfterCommands(conn, refBuf.Bytes(), "nexus3: refused: ref "+deniedRef+" not in allowed branches\n")
			return
		}
		go func() { // allowed: replay buffered commands then stream packfile
			_, _ = io.Copy(stdinPipe, io.MultiReader(refBuf, conn))
			stdinPipe.Close()
		}()
	} else {
		go func() { // upload-pack: stream client stdin directly
			_, _ = io.Copy(stdinPipe, conn)
			stdinPipe.Close()
		}()
	}

	if cfg.OnEgress != nil {
		cfg.OnEgress(cmd.BareHost, "allow", "git SSH: "+cmd.Service+" "+cmd.OwnerRepo, time.Now())
	}
	slog.Info("gitssh.relay.allow",
		"sandboxID", cfg.SandboxID,
		"service", cmd.Service,
		"ownerRepo", cmd.OwnerRepo,
	)

	<-bridgeDone
	_ = sshCmd.Wait()

	exitCode := 0
	if sshCmd.ProcessState != nil {
		exitCode = sshCmd.ProcessState.ExitCode()
	}
	_ = WriteExitFrame(conn, int32(exitCode)) //nolint:gosec // exit codes fit int32
}

func repoAllowed(allowlist []AllowedRepo, gitHost, ownerRepo string) bool {
	for _, a := range allowlist {
		if strings.EqualFold(a.SSHHost, gitHost) && strings.EqualFold(a.OwnerRepo, ownerRepo) {
			return true
		}
	}
	return false
}

func resolveSSHAuthSock(inherited string, uid int) (string, bool) {
	candidates := []string{}
	if inherited != "" {
		candidates = append(candidates, inherited)
	}
	candidates = append(candidates,
		fmt.Sprintf("/run/user/%d/gnupg/S.gpg-agent.ssh", uid),
		fmt.Sprintf("/run/user/%d/ssh-agent.sock", uid),
	)
	for _, sock := range candidates {
		if sock != "" && probeSSHAgent(sock) {
			return sock, true
		}
	}
	return "", false
}

func probeSSHAgent(sockPath string) bool {
	cmd := exec.Command("ssh-add", "-l") //nolint:gosec
	cmd.Env = []string{"SSH_AUTH_SOCK=" + sockPath}
	err := cmd.Run()
	if err == nil {
		return true // exit 0: agent live, keys present
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode() == 1 // exit 1: agent live, no keys
	}
	return false // exit 2 or other: not reachable
}

func buildSSHEnv(authSock, gitProtocol string) []string {
	env := []string{"SSH_AUTH_SOCK=" + authSock}
	if gitProtocol != "" {
		env = append(env, "GIT_PROTOCOL="+gitProtocol)
	}
	// Pass through HOME so SSH can find ~/.ssh/known_hosts.
	if home := os.Getenv("HOME"); home != "" {
		env = append(env, "HOME="+home)
	}
	return env
}
