// Command nexus-hub is the session-hub store binary. Core nexus embeds and
// execs it; it must not link internal/core/hostbin embedded artifacts.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/IniZio/nexus/internal/hubclient"
)

// EnvDB overrides the database path (tests).
const EnvDB = "NEXUS_HUB_DB"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func dbPath() string {
	if p := os.Getenv(EnvDB); p != "" {
		return p
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "nexus", "hub", "hub.db")
}

// run parses `--protocol N [--db PATH] <verb> [flags]` and returns the exit code.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	proto, db := -1, ""
	for len(args) >= 2 && (args[0] == "--protocol" || args[0] == "--db") {
		if args[0] == "--db" {
			db = args[1]
		} else {
			n, err := strconv.Atoi(args[1])
			if err != nil {
				fmt.Fprintf(stderr, "nexus-hub: bad --protocol %q\n", args[1])
				return 2
			}
			proto = n
		}
		args = args[2:]
	}
	if proto != hubclient.ProtocolVersion {
		fmt.Fprintf(stderr, "nexus-hub: protocol mismatch: client %d, server %d\n", proto, hubclient.ProtocolVersion)
		return hubclient.ExitProtocolMismatch
	}
	if len(args) == 0 {
		fmt.Fprintln(stderr, "nexus-hub: missing verb")
		return 2
	}
	if db == "" {
		db = dbPath()
	}
	if err := dispatch(ctx, db, args[0], args[1:], stdin, stdout); err != nil {
		if ue, ok := err.(usageError); ok {
			fmt.Fprintf(stderr, "nexus-hub: %s\n", string(ue))
			return 2
		}
		fmt.Fprintf(stderr, "nexus-hub: %v\n", err)
		return 1
	}
	return 0
}
