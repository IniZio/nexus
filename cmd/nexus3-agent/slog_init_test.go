package main

import (
	"bytes"
	"log"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// TestSlogHandlerWritesToLogWriter verifies two invariants after initSlogHandler():
//
//  1. slog.Info output appears in the writer log.Writer() targets.
//  2. The installed handler is *slog.TextHandler (not Go's default bridge handler).
//
// # Mutation proof
//
// Comment out the initSlogHandler() call and run the test:
//
//	// initSlogHandler()
//	slog.Info("probe")
//
// The handler-type assertion fails because slog.Default().Handler() is the
// package-level bridge defaultHandler (unexported type), not *slog.TextHandler:
//
//	slog_init_test.go:NN: slog default handler is not *slog.TextHandler after initSlogHandler(); got *slog.defaultHandler
//
// (The write-to-buf assertion still passes because Go 1.25's bridge handler
// also calls log.Output which respects log.SetOutput — the handler-type check
// is the definitive gate.)
func TestSlogHandlerWritesToLogWriter(t *testing.T) {
	// 1. Redirect log output to a buffer and restore when the test ends.
	var buf bytes.Buffer
	prev := log.Writer()
	prevSlog := slog.Default()
	log.SetOutput(&buf)
	t.Cleanup(func() {
		log.SetOutput(prev)
		slog.SetDefault(prevSlog)
	})

	// 2. Install the slog handler — this must point the handler at the current
	// log.Writer() (= &buf set in step 1).
	initSlogHandler()

	// 3. Verify the installed handler IS a *slog.TextHandler.
	// Without initSlogHandler() this assertion fails:
	//   "slog default handler is not *slog.TextHandler ... got *slog.defaultHandler"
	if _, ok := slog.Default().Handler().(*slog.TextHandler); !ok {
		t.Fatalf("slog default handler is not *slog.TextHandler after initSlogHandler(); got %T", slog.Default().Handler())
	}

	// 4. Call slog.Info — the message must appear in buf.
	slog.Info("probe")

	if !strings.Contains(buf.String(), "probe") {
		t.Fatalf("slog.Info did not write to log.Writer() buffer; got %q", buf.String())
	}
	_ = os.Stderr // referenced to keep import
}
