package cli

import (
	"context"
	"flag"
	"os"

	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
	"golang.org/x/term"
)

func init() {
	Register(Command{
		Name:    "shell",
		Summary: "Open an interactive login shell in a sandbox (PTY, auto-sized)",
		Run:     runShell,
	})
}

// defaultShellArgv is the command executed by "nexus3 shell" when no trailing
// command is supplied. Guest images are expected to have bash.
var defaultShellArgv = []string{"/bin/bash", "--login"}

// shellArgv builds the command argv from the post-ref positional arguments.
// A single leading "--" is stripped so that
//
//	nexus3 shell <ref> -- /bin/bash -lc '...'
//
// behaves identically to
//
//	nexus3 shell <ref> /bin/bash -lc '...'
//
// When no arguments remain after the optional strip, defaultShellArgv is
// returned so the default-shell behavior is preserved.
func shellArgv(postRef []string) []string {
	if len(postRef) > 0 && postRef[0] == "--" {
		postRef = postRef[1:]
	}
	if len(postRef) == 0 {
		return defaultShellArgv
	}
	return postRef
}

// runShell opens a PTY-backed interactive shell in the named sandbox.
//
// Usage: shell <ref> [-- <cmd> [args...]]
//
// When no trailing command is given, /bin/bash --login is used. The terminal
// size is auto-detected from stdin; non-TTY stdin falls back to 80×24 and the
// PTY is still allocated (enterRawMode in rawmode.go gracefully degrades for
// non-TTY callers without disrupting the stream).
func runShell(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "shell: " + err.Error()}
	}

	positional := fs.Args()
	if len(positional) < 1 {
		return &UsageError{Msg: "shell: usage: shell <sandbox-ref> [-- <cmd> [args...]]"}
	}

	ref := positional[0]

	// Build the argv from everything after the sandbox ref.  flag.Parse stops
	// at the first non-flag positional argument, so it does NOT consume a "--"
	// separator — the "--" lands verbatim in fs.Args().  shellArgv strips it so
	// that "nexus3 shell <ref> -- /bin/bash -lc '...'" is identical to
	// "nexus3 shell <ref> /bin/bash -lc '...'".
	argv := shellArgv(positional[1:])

	// Auto-detect the controlling terminal size. term.GetSize returns
	// (width, height) — note: width=cols, height=rows.
	cols, rows := 80, 24
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
		cols, rows = w, h
	}

	ptyOpts := &agentpb.PtyOptions{
		Term: os.Getenv("TERM"),
		InitialSize: &agentpb.WinSize{
			Rows: uint32(rows),
			Cols: uint32(cols),
		},
	}

	svc, err := newSandboxService()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "shell: " + err.Error(), Err: err}
	}

	// Delegate to the same PTY-streaming path that "exec --pty" uses.
	// runExecWithSvc calls enterRawMode when ptyOpts != nil and handles
	// SIGWINCH forwarding, raw-mode setup, and exit-code propagation.
	return runExecWithSvc(ctx, ref, argv, "", ptyOpts, out, svc)
}
