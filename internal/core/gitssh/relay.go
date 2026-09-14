package gitssh

import (
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

// writePktErrFrame sends a git pkt-line ERR packet as a stdout wire frame,
// followed by an exit frame with code 1.
//
// Format: 4-byte hex length (length includes the 4-byte prefix) + "ERR " + msg
// Example: "0030ERR nexus3: refused by egress policy: ...\n"
//
// Git interprets pkt-line packets whose payload starts with "ERR " as a fatal
// remote error and prints "remote error: <rest>" to the user's stderr — making
// the refusal visible instead of triggering a "bad line length character" parse
// error from the raw ASCII.
//
// msg must already contain the full human-readable error (including trailing newline).
// The "ERR " prefix is prepended by this function.
func writePktErrFrame(conn net.Conn, msg string) {
	errLine := "ERR " + msg
	pkt := fmt.Sprintf("%04x", 4+len(errLine)) + errLine
	_ = WriteStdout(conn, []byte(pkt))
	_ = WriteExitFrame(conn, 1)
}

// RelayConfig configures the host-side git SSH relay listener.
type RelayConfig struct {
	// SandboxID is used only for log fields.
	SandboxID string
	// VsockUDSPath is the host-side Unix socket CH binds for guest-initiated
	// connections on GitSSHRelayPort: <socketDir>/<id>.vsock_1026.
	VsockUDSPath string
	// Allowlist is the set of (SSHHost, OwnerRepo) pairs that may be used.
	Allowlist []AllowedRepo
	// AllowedBranches is used to enforce branch policy on receive-pack pushes.
	AllowedBranches []string
	// OnEgress, when non-nil, is called for every relay verdict (allow/deny).
	OnEgress func(host, verdict, reason string, ts time.Time)
	// UID is used to find fallback SSH agent sockets in /run/user/<uid>/.
	UID int
	// SSHAuthSock is the SSH_AUTH_SOCK inherited from the supervisor process.
	// May be empty or stale; probed before use.
	SSHAuthSock string
	// SSHExec is the path to the SSH binary. Defaults to "ssh" when empty.
	SSHExec string
}

// RunRelay creates the Unix socket at cfg.VsockUDSPath and accepts one
// connection per relay session (concurrent sessions allowed). Each session
// reads a gitssh.Request, validates it against policy, and if allowed exec's
// `ssh` with the host ssh-agent, bridging stdin/stdout back to the guest.
//
// Blocks until ctx is cancelled. The socket is removed on return.
func RunRelay(ctx context.Context, cfg RelayConfig) error {
	sshExec := cfg.SSHExec
	if sshExec == "" {
		sshExec = "ssh"
	}

	// Remove stale socket if present.
	_ = os.Remove(cfg.VsockUDSPath)

	ln, err := net.Listen("unix", cfg.VsockUDSPath)
	if err != nil {
		return fmt.Errorf("gitssh relay: listen on %s: %w", cfg.VsockUDSPath, err)
	}
	defer func() {
		ln.Close()
		_ = os.Remove(cfg.VsockUDSPath)
	}()

	// Close listener when context is cancelled so the Accept loop returns.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

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

// serveSession handles one guest-initiated relay connection.
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

	// For receive-pack: parse pkt-line ref updates and enforce AllowedBranches.
	var stdinPrefix io.Reader
	if cmd.Service == "git-receive-pack" {
		buf, deniedRef, malformed := ParseRefUpdates(conn, cfg.AllowedBranches)
		if malformed {
			writePktErrFrame(conn, "nexus3: refused: push pkt-line header malformed or too large\n")
			if cfg.OnEgress != nil {
				cfg.OnEgress(cmd.BareHost, "deny", "git SSH receive-pack: malformed pkt-line", time.Now())
			}
			return
		}
		if deniedRef != "" {
			writePktErrFrame(conn, "nexus3: refused: ref "+deniedRef+" not in allowed branches\n")
			if cfg.OnEgress != nil {
				cfg.OnEgress(cmd.BareHost, "deny", "git SSH receive-pack: ref "+deniedRef+" not in AllowedBranches", time.Now())
			}
			slog.Info("gitssh.relay.deny_ref",
				"sandboxID", cfg.SandboxID,
				"ref", deniedRef,
			)
			return
		}
		stdinPrefix = buf
	}

	// Probe SSH agent.
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

	// Pipe stdout.
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

	// Copy stdin from conn to the SSH process asynchronously.
	// This goroutine finishes when either the conn returns io.EOF or the
	// SSH process exits and closes the pipe's read end (broken pipe).
	var stdinSrc io.Reader = conn
	if stdinPrefix != nil {
		stdinSrc = io.MultiReader(stdinPrefix, conn)
	}
	go func() {
		_, _ = io.Copy(stdinPipe, stdinSrc)
		stdinPipe.Close()
	}()

	if cfg.OnEgress != nil {
		cfg.OnEgress(cmd.BareHost, "allow", "git SSH: "+cmd.Service+" "+cmd.OwnerRepo, time.Now())
	}
	slog.Info("gitssh.relay.allow",
		"sandboxID", cfg.SandboxID,
		"service", cmd.Service,
		"ownerRepo", cmd.OwnerRepo,
	)

	// Bridge SSH stdout → WriteStdout frames.
	bridgeDone := make(chan struct{})
	go func() {
		defer close(bridgeDone)
		buf := make([]byte, 32*1024)
		for {
			n, rerr := sshStdout.Read(buf)
			if n > 0 {
				if werr := WriteStdout(conn, buf[:n]); werr != nil {
					break
				}
			}
			if rerr != nil {
				break
			}
		}
	}()

	<-bridgeDone
	_ = sshCmd.Wait()

	exitCode := 0
	if sshCmd.ProcessState != nil {
		exitCode = sshCmd.ProcessState.ExitCode()
	}
	_ = WriteExitFrame(conn, int32(exitCode)) //nolint:gosec // exit codes fit int32
}

// repoAllowed returns true if gitHost+ownerRepo appears in the allowlist.
func repoAllowed(allowlist []AllowedRepo, gitHost, ownerRepo string) bool {
	for _, a := range allowlist {
		if strings.EqualFold(a.SSHHost, gitHost) && strings.EqualFold(a.OwnerRepo, ownerRepo) {
			return true
		}
	}
	return false
}

// resolveSSHAuthSock probes candidate SSH agent sockets and returns the first
// live one. Liveness is determined by running `ssh-add -l`: exit 0 (keys
// present) or exit 1 (agent running, no keys) both count as reachable.
func resolveSSHAuthSock(inherited string, uid int) (string, bool) {
	candidates := []string{}
	if inherited != "" {
		candidates = append(candidates, inherited)
	}
	// Fallback: gnupg agent's SSH socket.
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

// probeSSHAgent returns true if the SSH agent at sockPath is reachable.
// ssh-add -l exits 0 (keys present) or 1 (no keys) for a live agent,
// and 2 for connection refused / socket not found.
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

// buildSSHEnv constructs the env for the ssh subprocess.
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
