package main

import (
	"log"
	"log/slog"
)

// initSlogHandler installs a default slog handler that writes to the same
// sink as log.Printf (log.Writer()). Without this, slog.* calls use Go's
// bridging default handler which also targets log.Writer() in theory, but
// the explicit TextHandler guarantees the same writer reference is captured
// at call time — critical in the builder-role subprocess where os.Stderr (fd 2)
// is the vsock exec pipe rather than the serial console.
//
// This must be called before any slog.* calls and before the build code runs.
// It is idempotent: calling it multiple times just re-installs the handler.
func initSlogHandler() {
	slog.SetDefault(slog.New(slog.NewTextHandler(log.Writer(), &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))
}
