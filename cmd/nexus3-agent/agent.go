package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"

	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
)

// Agent is the nexus3 in-guest PID-1 agent.
// It holds an injectable pair of net.Listeners (vsock in production,
// bufconn/net.Pipe in tests) for the gRPC control plane and the
// framed data plane.
type Agent struct {
	ctrlLis  net.Listener
	dataLis  net.Listener
	sessions *SessionTable
	copies   *CopyTable
	isPid1   bool
}

// New returns an Agent that uses the given listeners.
// Callers supply vsock listeners in production or bufconn listeners in tests.
func New(ctrlLis, dataLis net.Listener) *Agent {
	return &Agent{
		ctrlLis:  ctrlLis,
		dataLis:  dataLis,
		sessions: newSessionTable(),
		copies:   newCopyTable(),
		isPid1:   os.Getpid() == 1,
	}
}

// Run starts both planes and blocks until ctx is cancelled or a fatal error
// occurs. It returns nil on clean shutdown.
func (a *Agent) Run(ctx context.Context) error {
	if a.isPid1 {
		go a.reapLoop(ctx)
	}

	gs := grpc.NewServer()
	agentpb.RegisterAgentServiceServer(gs, newControlServer(a))

	errCh := make(chan error, 2)

	go func() {
		if err := gs.Serve(a.ctrlLis); err != nil {
			errCh <- err
		}
	}()

	go func() {
		if err := a.serveData(ctx); err != nil {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		gs.GracefulStop()
		a.dataLis.Close()
		return nil
	case err := <-errCh:
		gs.Stop()
		a.dataLis.Close()
		return err
	}
}

// reapLoop is the zombie-reaper goroutine. It runs only when the agent is
// PID 1. It is the sole caller of Wait4(-1) so there is no race with
// per-session cmd.Wait() calls (which are skipped at pid==1).
func (a *Agent) reapLoop(ctx context.Context) {
	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, syscall.SIGCHLD)
	defer signal.Stop(sigCh)

	for {
		select {
		case <-ctx.Done():
			return
		case <-sigCh:
		}
		// Drain all available child statuses.
		for {
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
			if pid <= 0 || err != nil {
				break
			}
			code := int32(0)
			switch {
			case ws.Exited():
				code = int32(ws.ExitStatus())
			case ws.Signaled():
				code = int32(128) + int32(ws.Signal())
			}
			a.sessions.notifyExit(pid, code)
		}
	}
}
