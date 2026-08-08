package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
)

const ringCapacity = defaultRingCap // 16 MiB per session

// controlServer implements agentpb.AgentServiceServer.
type controlServer struct {
	agentpb.UnimplementedAgentServiceServer
	a *Agent
}

func newControlServer(a *Agent) *controlServer { return &controlServer{a: a} }

// ---- Exec ---------------------------------------------------------------

func (cs *controlServer) Exec(_ context.Context, req *agentpb.ExecRequest) (*agentpb.ExecResponse, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id required")
	}
	if len(req.Argv) == 0 {
		return nil, status.Error(codes.InvalidArgument, "argv required")
	}

	// Build environment: start from host env, merge extras.
	env := os.Environ()
	for k, v := range req.Env {
		env = append(env, k+"="+v)
	}

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

// ---- Signal -------------------------------------------------------------

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

// ---- SessionStatus / ListSessions ---------------------------------------

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
