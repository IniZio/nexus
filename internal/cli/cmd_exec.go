package cli

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/agent/agentpb"
	"github.com/IniZio/nexus3/internal/core/service"
)

func init() {
	Register(Command{
		Name:    "exec",
		Summary: "Run a command in a sandbox via the in-guest agent",
		Run:     runExec,
	})
}

// execDoneJSON is the --json data payload emitted on successful exec completion.
type execDoneJSON struct {
	ExitCode int32 `json:"exit_code"`
}

// agentCodeFor maps agent/service errors to stable CLI error codes.
func agentCodeFor(err error) string {
	if err == nil {
		return ""
	}
	code := sandboxCodeFor(err)
	return code
}

// runExec is the registered Run function for the "exec" command.
// It builds the service then delegates to runExecWithSvc for testability.
//
// exec is single-sandbox only. Batch exec --label was retracted 2026-08-15
// (D-PD-30): no reference tool ships fleet exec — microsandbox deliberately
// excluded it from its label-driven fleet verbs. Fan-out across sandboxes is
// a host-side shell loop over `exec <ref>` (see docs/site/surface.md).
func runExec(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	var (
		ptyFlag  = fs.Bool("pty", false, "allocate a PTY for the session")
		rowsFlag = fs.Uint("rows", 24, "terminal rows (requires --pty)")
		colsFlag = fs.Uint("cols", 80, "terminal columns (requires --pty)")
		cwdFlag  = fs.String("cwd", "", "working directory for the command inside the guest")
	)
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "exec: " + err.Error()}
	}

	svc, err := newSandboxService()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "exec: " + err.Error(), Err: err}
	}

	positional := fs.Args()
	if len(positional) < 2 {
		return &UsageError{Msg: "exec: usage: exec [--cwd <dir>] <sandbox-ref> <command> [args...]"}
	}

	ref := positional[0]
	// Strip the conventional "--" separator: flag.Parse stops at the first
	// positional and never consumes it. See stripArgvSeparator.
	argv := stripArgvSeparator(positional[1:])

	var ptyOpts *agentpb.PtyOptions
	if *ptyFlag {
		ptyOpts = &agentpb.PtyOptions{
			Term: os.Getenv("TERM"),
			InitialSize: &agentpb.WinSize{
				Rows: uint32(*rowsFlag),
				Cols: uint32(*colsFlag),
			},
		}
	}

	return runExecWithSvc(ctx, ref, argv, *cwdFlag, ptyOpts, out, svc)
}

// runExecWithSvc executes a command in the guest sandbox and streams output.
// Extracted for testability — callers pass a pre-built service.
func runExecWithSvc(ctx context.Context, ref string, argv []string, cwd string, ptyOpts *agentpb.PtyOptions, out *Output, svc *service.Service) error {
	// Enter raw mode only when a PTY was requested. Non-TTY stdin (pipes,
	// test harnesses, --json mode) is a no-op: enterRawMode returns ok==false
	// and a nil winsizeCh so nothing changes for non-interactive callers.
	opts := agent.ExecOptions{
		Argv:   argv,
		Cwd:    cwd,
		Pty:    ptyOpts,
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}
	if ptyOpts != nil {
		if ch, cleanup, ok := enterRawMode(int(os.Stdin.Fd())); ok {
			defer cleanup()
			opts.WinsizeCh = ch
		}
	}

	exitCode, err := svc.Exec(ctx, ref, opts)
	if err != nil {
		return &CodedError{
			Code: agentCodeFor(err),
			Msg:  fmt.Sprintf("exec: %v", err),
			Err:  err,
		}
	}

	if out.IsJSON() {
		out.EmitSuccess("exec.done", execDoneJSON{ExitCode: exitCode}, "")
		return nil
	}

	if exitCode != 0 {
		return &ExitCodeError{Code: exitCode}
	}
	return nil
}
