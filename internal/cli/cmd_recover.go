package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/IniZio/nexus3/internal/core/recovery"
	"github.com/IniZio/nexus3/internal/core/store"
)

func init() {
	Register(Command{
		Name:    "recover",
		Summary: "Reconcile persisted sandbox records against the live substrate",
		Run:     runRecover,
	})
}

// runRecover is the implementation of the `nexus3 recover` subcommand.
//
// # Safety guarantee
//
// Recovery performs destructive reconciliation: it resolves paused→stopped,
// honours --rm, and may delete sandbox records. With a real substrate driver
// this is correct — Observe queries the live hypervisor before any write. With
// a fake or noop driver that always reports Absent it would destroy records for
// every running sandbox.
//
// This guarantee is structural, not conditional:
//   - SelectSubstrate() returns (nil, *SubstrateError) on any failure, and that
//     is the only path to a fake/noop driver. The noop driver is substituted
//     only in newSandboxService (for sandbox verbs), never here.
//   - runRecover returns immediately on a non-nil SubstrateError, before
//     recovery.New is ever called.
//   - Therefore recovery.New is only ever called with a real driver that can
//     observe actual VM state. The always-Absent noop driver is structurally
//     unreachable from this function.
func runRecover(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("recover", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: err.Error()}
	}

	// Substrate selection must succeed before we build a Recoverer. A failed
	// selection means the driver cannot observe actual VM state; running
	// recovery against such a driver would treat every sandbox as absent and
	// perform destructive reconciliation against live data.
	drv, serr := SelectSubstrate()
	if serr != nil {
		// Return CodedError so root.go emits the correct stable code. Do NOT
		// call out.EmitError here — that produces two envelopes on stdout in
		// --json mode, breaking json.load.
		return &CodedError{
			Code: sandboxErrCodeNoSubstrate,
			Msg:  serr.Msg,
			Err:  serr,
		}
	}

	root, err := store.DefaultRoot()
	if err != nil {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("recover: resolve state directory: %v", err),
		}
	}
	st, err := store.NewFileStore(root)
	if err != nil {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("recover: open state directory: %v", err),
		}
	}

	rec := recovery.New(st, drv)
	report, err := rec.Recover(ctx)
	if err != nil {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("recover: %v", err),
		}
	}

	out.EmitSuccess("recover", report,
		fmt.Sprintf("recovery complete: examined %d sandbox(es)", len(report.Outcomes)))
	return nil
}
