package cli

import (
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/IniZio/nexus3/internal/core/agent/wire"
	"golang.org/x/term"
)

// enterRawMode puts fd into raw terminal mode and starts a SIGWINCH-forwarding
// goroutine that sends wire.Winsize events — including the initial size — on
// the returned channel.
//
// The caller MUST defer the returned cleanup function. cleanup restores the
// terminal, stops the SIGWINCH listener, waits for the forwarding goroutine to
// exit, and closes the channel so that the winsizeCh goroutine inside
// runDataPump exits cleanly (satisfying the channel-ownership contract on
// [agent.ExecOptions.WinsizeCh] and [agent.AttachOptions.WinsizeCh]).
//
// When fd is not a terminal (piped stdin, test harness, --json mode) the
// function returns nil, a no-op cleanup, and ok==false so the caller's
// behaviour is completely unchanged.
func enterRawMode(fd int) (winsizeCh <-chan wire.Winsize, cleanup func(), ok bool) {
	if !term.IsTerminal(fd) {
		return nil, func() {}, false
	}

	state, err := term.MakeRaw(fd)
	if err != nil {
		// Best-effort: proceed without raw mode rather than aborting.
		return nil, func() {}, false
	}

	ch := make(chan wire.Winsize, 4)

	// Deliver the initial terminal size before the pump starts so the guest
	// PTY is resized to the real dimensions as early as possible.
	if w, h, err := term.GetSize(fd); err == nil {
		ch <- wire.Winsize{Rows: uint16(h), Cols: uint16(w)}
	}

	// Subscribe to SIGWINCH so subsequent resize events are forwarded.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)

	stopCh := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-sigCh:
				if w, h, err := term.GetSize(fd); err == nil {
					select {
					case ch <- wire.Winsize{Rows: uint16(h), Cols: uint16(w)}:
					default: // Drop if the previous resize hasn't been consumed yet.
					}
				}
			case <-stopCh:
				return
			}
		}
	}()

	var once sync.Once
	cleanup = func() {
		once.Do(func() {
			signal.Stop(sigCh)
			close(stopCh)
			wg.Wait() // Ensure the goroutine has exited before closing ch.
			close(ch) // Satisfies the WinsizeCh ownership contract.
			_ = term.Restore(fd, state)
		})
	}

	return ch, cleanup, true
}
