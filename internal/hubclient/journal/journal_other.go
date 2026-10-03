//go:build !linux

// Package journal writes hub events to the systemd journal; a no-op off Linux.
package journal

import (
	"context"

	"github.com/IniZio/nexus/internal/hubclient"
)

// SocketPath is unused off Linux.
var SocketPath = ""

// Emit is a no-op off Linux.
func Emit(context.Context, hubclient.Event) error { return nil }
