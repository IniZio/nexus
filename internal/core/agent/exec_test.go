package agent_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/agent/wire"
	"github.com/IniZio/nexus3/internal/core/domain"
)

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
