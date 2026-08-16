package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

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
// When --label KEY=VALUE is provided all sandboxes matching that label are
// exec'd in parallel (bounded by --parallel, default service.DefaultBatchParallel=2).
// The argv following "--" (or the first non-flag arg) is the command to run.
//
// NOTE — deliberate departure from the microsandbox reference: microsandbox has
// no batch exec equivalent. exec --label is kept because the bounded-parallelism
// default (2) encodes host-specific knowledge: at 2 concurrent nexus3 VMs the
// host measured 84% swap pressure, making the parallel cap a substrate safety
// primitive, not a workflow opinion. Callers that want different fan-out must
// set --parallel explicitly.
func runExec(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	var (
		labelFlag = fs.String("label", "",
			"run command across every sandbox matching label KEY=VALUE (e.g. --label motive=my-motive)")
		parallelFlag = fs.Int("parallel", service.DefaultBatchParallel,
			"max sandboxes to exec concurrently (derived from measured host swap pressure at ~84%;\n"+
				"\traising this beyond 2 risks swap thrashing — measure before changing)")
		ptyFlag  = fs.Bool("pty", false, "allocate a PTY for the session (single-sandbox only)")
		rowsFlag = fs.Uint("rows", 24, "terminal rows (requires --pty)")
		colsFlag = fs.Uint("cols", 80, "terminal columns (requires --pty)")
	)
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "exec: " + err.Error()}
	}

	svc, err := newSandboxService()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "exec: " + err.Error(), Err: err}
	}

	// Label path: fan-out over all sandboxes matching the label selector.
	if *labelFlag != "" {
		k, v, ok := strings.Cut(*labelFlag, "=")
		if !ok || k == "" || v == "" {
			return &UsageError{Msg: "exec --label: requires KEY=VALUE format; e.g. --label motive=my-motive"}
		}
		argv := fs.Args()
		if len(argv) == 0 {
			return &UsageError{Msg: "exec --label: usage: exec --label KEY=VALUE [--parallel N] -- <command> [args...]"}
		}
		return runExecBatchWithSvc(ctx, k, v, *parallelFlag, argv, out, svc)
	}

	// Single-sandbox path.
	positional := fs.Args()
	if len(positional) < 2 {
		return &UsageError{Msg: "exec: usage: exec <sandbox-ref> <command> [args...]\n" +
			"       exec --label motive=<id> [--parallel N] -- <command> [args...]"}
	}

	ref := positional[0]
	argv := positional[1:]

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

	return runExecWithSvc(ctx, ref, argv, ptyOpts, out, svc)
}

// runExecWithSvc executes a command in the guest sandbox and streams output.
// Extracted for testability — callers pass a pre-built service.
func runExecWithSvc(ctx context.Context, ref string, argv []string, ptyOpts *agentpb.PtyOptions, out *Output, svc *service.Service) error {
	// Enter raw mode only when a PTY was requested. Non-TTY stdin (pipes,
	// test harnesses, --json mode) is a no-op: enterRawMode returns ok==false
	// and a nil winsizeCh so nothing changes for non-interactive callers.
	opts := agent.ExecOptions{
		Argv:   argv,
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
