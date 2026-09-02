package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
)

// swapFunctions holds the injectable seams used by RestartAgent.
// Tests replace these; production code uses defaultSwapFns.
var swapFunctions = defaultSwapFns

const ringCapacity = defaultRingCap // 16 MiB per session

// controlServer implements agentpb.AgentServiceServer.
type controlServer struct {
	agentpb.UnimplementedAgentServiceServer
	a *Agent
}

func newControlServer(a *Agent) *controlServer { return &controlServer{a: a} }

// Exec

func (cs *controlServer) Exec(_ context.Context, req *agentpb.ExecRequest) (*agentpb.ExecResponse, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id required")
	}
	if len(req.Argv) == 0 {
		return nil, status.Error(codes.InvalidArgument, "argv required")
	}

	// Build environment: baseline, then req.Env wins over it.
	// When the agent runs as PID 1 (init=), the Linux kernel injects a few
	// variables into os.Environ() — notably HOME=/ — that are wrong for
	// interactive use. Rather than passing os.Environ() through (which would
	// propagate the wrong HOME), we start from guestBaselineEnv() which
	// supplies correct sane defaults (HOME=/root, PATH), then let the caller's
	// req.Env override anything. All useful agent-level env (credentials,
	// NODE_EXTRA_CA_CERTS, etc.) is injected through req.Env by the host.
	env := mergeEnv(guestBaselineEnv(agentScratchDisk), req.Env)

	// Use exec.Command (not CommandContext): the process must outlive the RPC.
	cmd := exec.Command(req.Argv[0], req.Argv[1:]...)
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}

	sess := &Session{
		id:     req.SessionId,
		cmd:    cmd,
		ring:   newRing(ringCapacity),
		exitCh: make(chan int32, 1),
	}

	if req.Pty != nil {
		if err := cs.execPTY(cmd, env, req.Pty, sess); err != nil {
			return nil, err
		}
	} else {
		if err := cs.execPipe(cmd, env, sess); err != nil {
			return nil, err
		}
	}

	return &agentpb.ExecResponse{Pid: int32(sess.pid)}, nil
}

// execPTY starts the command with a PTY.
func (cs *controlServer) execPTY(cmd *exec.Cmd, env []string, opts *agentpb.PtyOptions, sess *Session) error {
	if opts.Term != "" {
		env = append(env, "TERM="+opts.Term)
	}
	cmd.Env = env

	var sz *pty.Winsize
	if is := opts.GetInitialSize(); is != nil {
		sz = &pty.Winsize{
			Rows: uint16(is.Rows),
			Cols: uint16(is.Cols),
			X:    uint16(is.XPixels),
			Y:    uint16(is.YPixels),
		}
	}

	ptmx, err := pty.StartWithSize(cmd, sz)
	if err != nil {
		return status.Errorf(codes.Internal, "pty.StartWithSize: %v", err)
	}

	sess.ptmx = ptmx
	sess.pid = cmd.Process.Pid
	cs.a.sessions.add(sess)

	// Single goroutine: feed ring, then get exit code, then mark done.
	go func() {
		feedRingFromReader(ptmx, sess.ring)
		ptmx.Close()
		var code int32
		if cs.a.isPid1 {
			code = <-sess.exitCh // reap loop delivers
		} else {
			_ = cmd.Wait()
			if cmd.ProcessState != nil {
				code = int32(cmd.ProcessState.ExitCode())
			}
		}
		sess.setExited(code)
	}()

	return nil
}

// execPipe starts the command with stdin/stdout/stderr pipes.
func (cs *controlServer) execPipe(cmd *exec.Cmd, env []string, sess *Session) error {
	cmd.Env = env

	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		return status.Errorf(codes.Internal, "stdout pipe: %v", err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		return status.Errorf(codes.Internal, "stderr pipe: %v", err)
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		return status.Errorf(codes.Internal, "stdin pipe: %v", err)
	}

	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	cmd.Stdin = stdinR

	if err := cmd.Start(); err != nil {
		stdoutR.Close()
		stdoutW.Close()
		stderrR.Close()
		stderrW.Close()
		stdinR.Close()
		stdinW.Close()
		return status.Errorf(codes.Internal, "cmd.Start: %v", err)
	}

	// Close parent-side write ends; child holds them now.
	stdoutW.Close()
	stderrW.Close()
	stdinR.Close()

	sess.stdinW = stdinW
	sess.pid = cmd.Process.Pid
	cs.a.sessions.add(sess)

	// Feed ring from both output pipes. A WaitGroup ensures ring.Close() is
	// called only after all output has been written.
	var feedWg sync.WaitGroup
	feedWg.Add(2)
	go func() { defer feedWg.Done(); feedRingFromReader(stdoutR, sess.ring); stdoutR.Close() }()
	go func() { defer feedWg.Done(); feedRingFromReader(stderrR, sess.ring); stderrR.Close() }()

	if cs.a.isPid1 {
		// Reap loop delivers exit code; we wait for feeders first.
		go func() {
			feedWg.Wait()
			code := <-sess.exitCh
			sess.setExited(code)
		}()
	} else {
		// cmd.Wait() delivers the exit code; wait for feeders first.
		exitCodeCh := make(chan int32, 1)
		go func() {
			_ = cmd.Wait()
			code := int32(0)
			if cmd.ProcessState != nil {
				code = int32(cmd.ProcessState.ExitCode())
			}
			exitCodeCh <- code
		}()
		go func() {
			feedWg.Wait()
			code := <-exitCodeCh
			sess.setExited(code)
		}()
	}

	return nil
}

// etcEnvironmentPath is the path read by readEtcEnvironment. It is a variable
// so tests can redirect to a temporary file without touching the real system file.
var etcEnvironmentPath = "/etc/environment"

// readEtcEnvironment parses /etc/environment (KEY=VALUE pairs, one per line,
// no shell substitution) and returns the entries as a "KEY=VALUE" slice.
// Lines starting with '#' and empty lines are skipped. A missing or
// unreadable file is silently ignored — not every image writes this file.
func readEtcEnvironment() []string {
	data, err := os.ReadFile(etcEnvironmentPath)
	if err != nil {
		return nil
	}
	var env []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.ContainsRune(line, '=') {
			env = append(env, line)
		}
	}
	return env
}

// guestBaselineEnv returns the minimum environment that every exec'd process
// should inherit when the agent runs as PID 1 (init=). The Linux kernel provides
// no environment to PID 1, so os.Environ() is nearly empty; this baseline fills
// in sane defaults. It is overridden by os.Environ() and then by req.Env.
//
// The hardcoded fallback (HOME, PATH) applies to any image. Values written by
// the image's Containerfile to /etc/environment are layered on top, so a
// single RUN in the Containerfile is the sole source of truth for image-specific
// variables (GOPATH, GOMODCACHE, CGO_ENABLED, …). OCI ENV metadata is not read
// here — it lives only in the image config and is never visible to the agent,
// which boots as init= directly from the ext4 rootfs.
func guestBaselineEnv(scratchDiskPresent bool) []string {
	base := []string{
		// uid 0 always maps to /root in the guest's /etc/passwd. Without HOME,
		// login shells start at "/" and "~" expands to "/".
		"HOME=/root",
		// Debian/Alpine-compatible default PATH so relative-binary exec calls
		// (e.g. exec.Command("bash", ...)) work without callers spelling it out.
		// Images that extend PATH (e.g. adding /usr/local/go/bin) write their
		// canonical PATH to /etc/environment; the merge below picks it up.
		"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	// TMPDIR rider (D-SD-04): when a scratch disk is present it is mounted at
	// scratchDiskGuestMount (/tmp). Emitting TMPDIR pointing there lets
	// well-behaved tools redirect large temporaries onto the scratch device.
	// This is a courtesy only — the mount itself is the real mechanism. A tool
	// that writes a literal /tmp/... path still lands on the scratch device
	// regardless of TMPDIR, because /tmp IS the scratch mount point.
	if scratchDiskPresent {
		base = append(base, "TMPDIR="+scratchDiskGuestMount)
	}
	etcEnv := readEtcEnvironment()
	if len(etcEnv) == 0 {
		return base
	}
	return mergeEnv(base, envToMap(etcEnv))
}

// envToMap converts a "KEY=VALUE" slice to a map.
// If a key appears more than once only the first value is kept, mirroring
// glibc getenv() first-match semantics.
func envToMap(env []string) map[string]string {
	m := make(map[string]string, len(env))
	for _, e := range env {
		idx := strings.IndexByte(e, '=')
		if idx < 0 {
			continue
		}
		k := e[:idx]
		if _, exists := m[k]; !exists {
			m[k] = e[idx+1:]
		}
	}
	return m
}

// mergeEnv returns a copy of base with each key in extra replaced or appended.
// Replace semantics: if a "KEY=..." entry already exists in base, the first
// such entry is overwritten and no duplicate is appended. This matches glibc
// getenv() semantics — the first match wins — so the caller's value is always
// visible to the process even when base contains a prior binding.
// Values may contain '=' (e.g. "FOO=a=b"); only the key prefix up to the
// first '=' is matched.
func mergeEnv(base []string, extra map[string]string) []string {
	env := make([]string, len(base))
	copy(env, base)
	for k, v := range extra {
		prefix := k + "="
		replaced := false
		for i, e := range env {
			if strings.HasPrefix(e, prefix) {
				env[i] = k + "=" + v
				replaced = true
				break
			}
		}
		if !replaced {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// feedRingFromReader reads from r into ring until error (EOF, EIO, etc.).
// EIO from a PTY master is the normal signal that the slave has closed.
func feedRingFromReader(r io.Reader, ring *Ring) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			ring.Write(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// Signal

func (cs *controlServer) Signal(_ context.Context, req *agentpb.SignalRequest) (*agentpb.SignalResponse, error) {
	sess, ok := cs.a.sessions.get(req.SessionId)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.SessionId)
	}
	if sess.exited.Load() {
		return nil, status.Error(codes.FailedPrecondition, "session already exited")
	}
	if err := sess.cmd.Process.Signal(syscall.Signal(req.Signum)); err != nil {
		return nil, status.Errorf(codes.Internal, "signal: %v", err)
	}
	return &agentpb.SignalResponse{}, nil
}

// SessionStatus / ListSessions

func (cs *controlServer) SessionStatus(_ context.Context, req *agentpb.SessionStatusRequest) (*agentpb.SessionStatusResponse, error) {
	sess, ok := cs.a.sessions.get(req.SessionId)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.SessionId)
	}
	return &agentpb.SessionStatusResponse{Info: sessionInfo(sess)}, nil
}

func (cs *controlServer) ListSessions(_ context.Context, _ *agentpb.ListSessionsRequest) (*agentpb.ListSessionsResponse, error) {
	sessions := cs.a.sessions.list()
	infos := make([]*agentpb.SessionInfo, len(sessions))
	for i, s := range sessions {
		infos[i] = sessionInfo(s)
	}
	return &agentpb.ListSessionsResponse{Sessions: infos}, nil
}

// AgentInfo

// AgentInfo returns the agent's build tag. This is the value stamped by
// -ldflags "-X main.agentBuildTag=…" at build time ("dev" in local builds).
func (cs *controlServer) AgentInfo(_ context.Context, _ *agentpb.AgentInfoRequest) (*agentpb.AgentInfoResponse, error) {
	return &agentpb.AgentInfoResponse{BuildTag: agentBuildTag}, nil
}

// RestartAgent — hot-swap the agent binary via syscall.Exec.

func (cs *controlServer) RestartAgent(_ context.Context, req *agentpb.RestartAgentRequest) (*agentpb.RestartAgentResponse, error) {
	if req.StagedPath == "" {
		return nil, status.Error(codes.InvalidArgument, "staged_path required")
	}
	if req.ExpectedBytes <= 0 {
		return nil, status.Error(codes.InvalidArgument, "expected_bytes must be > 0")
	}

	// ── Pre-flight: active exec sessions ─────────────────────────────────
	sessions := cs.a.sessions.list()
	var activeSessions []string
	for _, s := range sessions {
		if !s.exited.Load() {
			activeSessions = append(activeSessions, s.id)
		}
	}
	if !req.Force && len(activeSessions) > 0 {
		return nil, status.Errorf(codes.FailedPrecondition,
			"active exec sessions exist (%v); set force=true to override "+
				"(WARNING: those sessions and their PTYs will be killed by exec)",
			activeSessions)
	}

	// ── Pre-flight: in-flight copy transfers ─────────────────────────────
	// An interrupted Copy leaves a truncated file in the guest.  Refuse unless
	// force is set — the host should wait for copies to complete first.
	activeCopies := cs.a.copies.count()
	if !req.Force && activeCopies > 0 {
		return nil, status.Errorf(codes.FailedPrecondition,
			"%d in-flight copy transfer(s) active; wait for completion or set force=true",
			activeCopies)
	}

	// ── Warn about orphaned children (best-effort) ────────────────────────
	// After exec, any process with ppid==1 that is not tracked by cs.a.sessions
	// (e.g. the boot task, sshd, or user-started daemons) becomes an orphan
	// re-parented to the new agent's reap loop.  Log their PIDs so the operator
	// can see them in the console capture.
	if orphans := guestOrphanPIDs(activeSessions); len(orphans) > 0 {
		slog.Warn("nexus3-agent: hot-swap: orphaned guest children will be re-parented",
			"pids", orphans,
			"force", req.Force)
	}
	if len(activeSessions) > 0 && req.Force {
		slog.Warn("nexus3-agent: hot-swap: force=true; killing active sessions",
			"sessions", activeSessions)
	}

	// ── Perform swap ──────────────────────────────────────────────────────
	// performSwap verifies size, fsyncs, backs up old binary, renames,
	// dups fds, then calls syscall.Exec.  On exec success it does not return.
	// On any error the old binary is rolled back from the backup.
	err := performSwap(
		swapFunctions,
		req.StagedPath,
		req.ExpectedBytes,
		agentInstallPath,
		agentBackupPath,
		cs.a.ctrlLis,
		cs.a.dataLis,
	)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "agent swap: %v", err)
	}

	// Unreachable: performSwap either execs (replaces this image) or returns an error.
	return &agentpb.RestartAgentResponse{}, nil
}

// guestOrphanPIDs scans /proc for processes whose parent is PID 1 (this
// process when isPid1) but are not tracked in the session table.
// Returns PIDs of untracked children; returns nil when not PID 1 or on error.
func guestOrphanPIDs(trackedSessions []string) []int {
	if os.Getpid() != 1 {
		return nil
	}
	tracked := make(map[string]struct{}, len(trackedSessions))
	for _, id := range trackedSessions {
		tracked[id] = struct{}{}
	}
	// /proc/<pid>/status line "PPid:\t<n>" identifies the parent.
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var orphans []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(e.Name(), "%d", &pid); err != nil || pid <= 1 {
			continue
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			continue
		}
		var ppid int
		for _, line := range strings.Split(string(data), "\n") {
			if _, err := fmt.Sscanf(line, "PPid:\t%d", &ppid); err == nil {
				break
			}
		}
		if ppid == 1 {
			orphans = append(orphans, pid)
		}
	}
	return orphans
}

func sessionInfo(s *Session) *agentpb.SessionInfo {
	info := &agentpb.SessionInfo{
		SessionId: s.id,
		Pid:       int32(s.pid),
		State:     agentpb.SessionState_SESSION_STATE_RUNNING,
	}
	if s.exited.Load() {
		info.State = agentpb.SessionState_SESSION_STATE_EXITED
		info.ExitCode = s.exitCode.Load()
	}
	return info
}
