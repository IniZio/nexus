package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
)

// Run is the top-level entry point for the nexus3 CLI. args should be
// os.Args[1:]. Returns the exit code: 0 success, 1 operational failure,
// 2 usage error.
//
// Global flag --json is accepted both before and after the subcommand name so
// that callers who build command lines programmatically do not need to know the
// argument ordering constraint:
//
//	nexus3 --json version   ✓
//	nexus3 version --json   ✓
func Run(args []string) int {
	// Scan for the global --json flag. Accept it at any position and strip it
	// from the argument list before handing the remainder to the subcommand.
	jsonMode := false
	rest := args[:0:0] // length-zero slice sharing no backing array
	for _, arg := range args {
		if arg == "--json" {
			jsonMode = true
		} else {
			rest = append(rest, arg)
		}
	}

	out := NewOutput(os.Stdout, os.Stderr, jsonMode)

	if len(rest) == 0 {
		if jsonMode {
			out.EmitError(ErrCodeUsageError, "no command specified")
		} else {
			printUsage(os.Stderr)
		}
		return 2
	}

	name := rest[0]
	cmdArgs := rest[1:]

	cmd, ok := Lookup(name)
	if !ok {
		out.EmitError(ErrCodeUnknownCommand, fmt.Sprintf("unknown command: %s", name))
		if !jsonMode {
			printUsage(os.Stderr)
		}
		return 2
	}

	// Cancel the command context on SIGINT or SIGTERM so subcommands can
	// perform cooperative cleanup. RunEphemeral (used by "nexus3 run") relies
	// on context cancellation to trigger its deferred Remove call.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := cmd.Run(ctx, cmdArgs, out); err != nil {
		var usageErr *UsageError
		if errors.As(err, &usageErr) {
			if usageErr.Msg != "" {
				code := usageErr.Code
				if code == "" {
					code = ErrCodeUsageError
				}
				out.EmitError(code, usageErr.Msg)
			}
			return 2
		}
		var exitCodeErr *ExitCodeError
		if errors.As(err, &exitCodeErr) {
			return int(exitCodeErr.Code)
		}
		out.EmitError(codeOf(err), err.Error())
		return 1
	}
	return 0
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage: nexus3 [--json] <command> [args...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, cmd := range AllVisible() {
		fmt.Fprintf(w, "  %-16s %s\n", cmd.Name, cmd.Summary)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags:")
	fmt.Fprintln(w, "  --json    emit machine-readable JSON (may appear before or after <command>)")
}
