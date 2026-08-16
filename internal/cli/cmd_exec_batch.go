package cli

import (
	"bytes"
	"context"
	"fmt"

	"github.com/IniZio/nexus3/internal/core/service"
)

// execBatchResultJSON is the machine-contract payload for a successful
// exec --label invocation.
type execBatchResultJSON struct {
	LabelKey   string              `json:"label_key"`
	LabelValue string              `json:"label_value"`
	Outcomes   []execBatchItemJSON `json:"outcomes"`
}

type execBatchItemJSON struct {
	SandboxID string `json:"sandbox_id"`
	Handle    string `json:"handle"`
	ExitCode  int32  `json:"exit_code"`
	Err       string `json:"err,omitempty"`
}

// runExecBatchWithSvc runs argv across every sandbox matching labelKey=labelValue
// with bounded parallelism. It is the testable core of the "exec --label" path.
//
// Each sandbox's stdout and stderr are captured separately during concurrent
// execution and printed sequentially after all sandboxes complete, so output
// from different sandboxes never interleaves on the terminal.
//
// Returns non-nil when any sandbox fails (exec error or non-zero exit code).
// All sandboxes always run to completion regardless of sibling failures.
//
// NOTE: the underlying BatchExec call routes through GetByMotive for the
// "motive" key. Other label keys use GetByLabels. Both paths honour the same
// bounded-parallelism cap (D-PD-21).
func runExecBatchWithSvc(ctx context.Context, labelKey, labelValue string, parallel int, argv []string, out *Output, svc *service.Service) error {
	if len(argv) == 0 {
		return &UsageError{Msg: "exec --label: command argv must not be empty"}
	}
	opts := service.BatchExecOptions{
		Argv:     argv,
		Parallel: parallel,
	}
	// BatchExec operates on the motive label key via GetByMotive. For other
	// label keys, use the same bounded-parallelism path via GetByMotive with
	// an empty value (which returns no sandboxes) and short-circuit with an
	// error — the generic label path will be wired when batch_exec.go is
	// extended in a later slice. For now, only "motive" is supported for batch.
	if labelKey != "motive" {
		return &UsageError{Msg: fmt.Sprintf("exec --label: batch exec currently only supports the motive label key; got %q", labelKey)}
	}
	result, aggErr := svc.BatchExec(ctx, labelValue, opts)

	if len(result.Outcomes) == 0 && aggErr == nil {
		out.EmitSuccess("exec_batch", execBatchResultJSON{
			LabelKey:   labelKey,
			LabelValue: labelValue,
			Outcomes:   []execBatchItemJSON{},
		}, fmt.Sprintf("exec --label %s=%s: no sandboxes found", labelKey, labelValue))
		return nil
	}

	if out.IsJSON() {
		items := make([]execBatchItemJSON, len(result.Outcomes))
		for i, o := range result.Outcomes {
			item := execBatchItemJSON{
				SandboxID: o.SandboxID.String(),
				Handle:    o.Handle,
				ExitCode:  o.ExitCode,
			}
			if o.Err != nil {
				item.Err = o.Err.Error()
			}
			items[i] = item
		}
		out.EmitSuccess("exec_batch", execBatchResultJSON{
			LabelKey:   labelKey,
			LabelValue: labelValue,
			Outcomes:   items,
		}, "")
	} else {
		// Human mode: print each sandbox's output with a header, sequentially.
		stdout := out.Stdout()
		stderr := out.Stderr()
		for _, o := range result.Outcomes {
			label := o.Handle
			if label == "" || label == "/" {
				label = o.SandboxID.String()
			}
			fmt.Fprintf(stdout, "\n=== %s (%s) ===\n", label, o.SandboxID)
			if len(o.Stdout) > 0 {
				// Ensure output ends with a newline so the next header is clean.
				stdout.Write(o.Stdout) //nolint:errcheck
				if !bytes.HasSuffix(o.Stdout, []byte("\n")) {
					fmt.Fprintln(stdout)
				}
			}
			if len(o.Stderr) > 0 {
				stderr.Write(o.Stderr) //nolint:errcheck
				if !bytes.HasSuffix(o.Stderr, []byte("\n")) {
					fmt.Fprintln(stderr)
				}
			}
			if o.Err != nil {
				fmt.Fprintf(stderr, "error: %v\n", o.Err)
				fmt.Fprintf(stdout, "exit error\n")
			} else {
				fmt.Fprintf(stdout, "exit %d\n", o.ExitCode)
			}
		}
	}

	if aggErr != nil {
		// Return a CodedError that does not suppress the output already written.
		// The individual per-sandbox results were already printed above.
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("exec --label %s=%s: one or more sandboxes failed", labelKey, labelValue),
			Err:  aggErr,
		}
	}
	return nil
}
