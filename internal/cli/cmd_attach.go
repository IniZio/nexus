package cli

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/service"
)

func init() {
	Register(Command{
		Name:    "attach",
		Summary: "Reattach to an existing guest session",
		Run:     runAttach,
	})
}

// attachDoneJSON is the --json data payload emitted on successful attach.
type attachDoneJSON struct {
	ExitCode int32 `json:"exit_code"`
}

// runAttach is the registered Run function for the "attach" command.
func runAttach(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("attach", flag.ContinueOnError)
	fromFlag := fs.Uint64("from", 0, "byte offset in the guest output ring to resume from")

	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "attach: " + err.Error()}
	}

	positional := fs.Args()
	if len(positional) != 2 {
		return &UsageError{Msg: "attach: usage: attach <sandbox-ref> <session-id>"}
	}

	ref := positional[0]
	sessionID := positional[1]

	svc, err := newSandboxService()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "attach: " + err.Error(), Err: err}
	}

	return runAttachWithSvc(ctx, ref, sessionID, *fromFlag, out, svc)
}

// runAttachWithSvc attaches to an existing guest session.
// Extracted for testability.
func runAttachWithSvc(ctx context.Context, ref, sessionID string, from uint64, out *Output, svc *service.Service) error {
	// Attach is always an interactive PTY session: enter raw mode when stdin
	// is a real terminal. Non-TTY stdin (pipes, test harnesses, --json mode)
	// is detected by enterRawMode and left unchanged (ok==false, winsizeCh==nil).
	winsizeCh, cleanup, _ := enterRawMode(int(os.Stdin.Fd()))
	defer cleanup()

	exitCode, err := svc.Attach(ctx, ref, agent.AttachOptions{
		SessionID:        sessionID,
		ResumeFromOffset: from,
		Stdin:            os.Stdin,
		Stdout:           os.Stdout,
		Stderr:           os.Stderr,
		WinsizeCh:        winsizeCh,
	})
	if err != nil {
		return &CodedError{
			Code: agentCodeFor(err),
			Msg:  fmt.Sprintf("attach: %v", err),
			Err:  err,
		}
	}

	if out.IsJSON() {
		out.EmitSuccess("attach.done", attachDoneJSON{ExitCode: exitCode}, "")
		return nil
	}

	if exitCode != 0 {
		return &ExitCodeError{Code: exitCode}
	}
	return nil
}
