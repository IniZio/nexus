package agent_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
	"github.com/IniZio/nexus3/internal/core/agent/wire"
	"github.com/IniZio/nexus3/internal/core/domain"
)

// signalRecordingServer accepts Exec and records every Signal RPC it receives.
type signalRecordingServer struct {
	agentpb.UnimplementedAgentServiceServer
	mu       sync.Mutex
	sessions []string
	signums  []int32
}

func (s *signalRecordingServer) Exec(_ context.Context, _ *agentpb.ExecRequest) (*agentpb.ExecResponse, error) {
	return &agentpb.ExecResponse{Pid: 1234}, nil
}

func (s *signalRecordingServer) Signal(_ context.Context, req *agentpb.SignalRequest) (*agentpb.SignalResponse, error) {
	s.mu.Lock()
	s.sessions = append(s.sessions, req.SessionId)
	s.signums = append(s.signums, req.Signum)
	s.mu.Unlock()
	return &agentpb.SignalResponse{}, nil
}

// TestExec_ContextCancelSignalsGuestChild: after ctx cancel closes the data
// conn, Exec must still terminate the guest child via the Signal RPC (TERM
// then KILL) for its session id, without changing the returned error.
func TestExec_ContextCancelSignalsGuestChild(t *testing.T) {
	td := newTestDialer()
	srv := &signalRecordingServer{}
	startGRPCServer(t, td.controlLis, srv)

	guestConn := td.pushDataPipe()
	t.Cleanup(func() { guestConn.Close() })
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	runGuestDataServer(guestConn, func(_ string, _ uint64, _ *wire.Reader, _ *wire.Writer) {
		<-hold
	})

	c := agent.NewClient(td, domain.NewSandboxID())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const sessionID = "kill-me"
	errCh := make(chan error, 1)
	go func() {
		_, err := c.Exec(ctx, agent.ExecOptions{
			SessionID: sessionID,
			Argv:      []string{"/bin/sleep", "infinity"},
			Stdin:     strings.NewReader(""),
		})
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	var err error
	select {
	case err = <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("Exec did not return within 5s after cancel")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Exec err = %v, want errors.Is(err, context.Canceled)", err)
	}

	srv.mu.Lock()
	sessions, signums := append([]string(nil), srv.sessions...), append([]int32(nil), srv.signums...)
	srv.mu.Unlock()
	if len(sessions) == 0 {
		t.Fatal("no Signal RPC received after cancel: guest child left running")
	}
	for _, s := range sessions {
		if s != sessionID {
			t.Fatalf("Signal for session %q, want %q", s, sessionID)
		}
	}
	if signums[0] != 15 {
		t.Fatalf("first Signal signum = %d, want 15 (SIGTERM)", signums[0])
	}
	if len(signums) < 2 || signums[1] != 9 {
		t.Fatalf("signums = %v, want [15 9] (SIGTERM then SIGKILL)", signums)
	}
}

// TestExec_ContextTimeoutUnblocksPump: a guest peer that reads the handshake
// and then never sends a frame must not wedge Exec — a 200ms context deadline
// returns an error wrapping context.DeadlineExceeded within 1s.
func TestExec_ContextTimeoutUnblocksPump(t *testing.T) {
	td := newTestDialer()
	startGRPCServer(t, td.controlLis, &testAgentServer{})

	guestConn := td.pushDataPipe()
	t.Cleanup(func() { guestConn.Close() })
	hold := make(chan struct{})
	t.Cleanup(func() { close(hold) })
	runGuestDataServer(guestConn, func(_ string, _ uint64, _ *wire.Reader, _ *wire.Writer) {
		<-hold
	})

	c := agent.NewClient(td, domain.NewSandboxID())

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	type result struct {
		code int32
		err  error
	}
	resCh := make(chan result, 1)
	start := time.Now()
	go func() {
		code, err := c.Exec(ctx, agent.ExecOptions{
			Argv:  []string{"/bin/sleep", "infinity"},
			Stdin: strings.NewReader(""),
		})
		resCh <- result{code, err}
	}()

	select {
	case res := <-resCh:
		if !errors.Is(res.err, context.DeadlineExceeded) {
			t.Fatalf("Exec err = %v, want errors.Is(err, context.DeadlineExceeded)", res.err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("Exec returned after %v, want <= 1s", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Exec did not return within 3s after a 200ms context deadline: pump ignores ctx.Done")
	}
}
