package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
)

// reapInterval is the backstop ticker period for the zombie-reaper goroutine.
// 30 s in production; tests override it to a short value so they do not wait.
var reapInterval = 30 * time.Second

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

	// reapSigCh, when non-nil, is used by reapLoop instead of registering
	// for SIGCHLD via signal.Notify. Nil in production (reapLoop creates and
	// registers its own channel). Tests inject a channel that is never written
	// to so the ticker backstop is the ONLY wakeup path, proving coverage of
	// that branch rather than the signal branch.
	reapSigCh chan os.Signal
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

// drainChildren reaps every currently-exited child in one call, delivering
// exit status to any tracked session. It calls Wait4(-1, WNOHANG) in a loop
// until no more zombies remain, so a single invocation handles bursts where
// multiple children died before the reap loop was woken — including the case
// where only one SIGCHLD arrived for N child exits (kernel coalescing) or
// where some SIGCHLDs were dropped because the signal channel was full.
//
// drainChildren must not hold any lock across the Wait4 calls.
func (a *Agent) drainChildren() {
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

// reapLoop is the zombie-reaper goroutine. It runs only when the agent is
// PID 1. It is the sole caller of Wait4(-1) so there is no race with
// per-session cmd.Wait() calls (which are skipped at pid==1).
//
// Correctness under signal loss: Go's signal.Notify silently drops SIGCHLDs
// when the channel buffer is full. Each wakeup (from either a signal or the
// backstop ticker) calls drainChildren, which loops until Wait4 returns 0, so
// one wakeup reaps all currently-exited children — coalescing and drops are
// both tolerated. The 30-second backstop ticker ensures that a fully-missed
// burst (buffer overflowed while the goroutine was preempted) is cleaned up
// within 30 seconds. The tick is cheap: Wait4(WNOHANG) returns immediately
// when no zombies are present.
func (a *Agent) reapLoop(ctx context.Context) {
	// Buffer of 1 is sufficient: drainChildren reaps all zombies on any wakeup,
	// so we only need to queue one pending notification at a time.
	var sigCh chan os.Signal
	if a.reapSigCh != nil {
		// Test-injected channel: no signal.Notify registration, so SIGCHLD
		// never reaches this loop. The ticker is then the sole wakeup path.
		sigCh = a.reapSigCh
	} else {
		sigCh = make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGCHLD)
		defer signal.Stop(sigCh)
	}

	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-sigCh:
		case <-ticker.C:
		}
		a.drainChildren()
	}
}
