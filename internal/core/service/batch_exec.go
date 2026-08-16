package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/domain"
)

// DefaultBatchParallel is the default number of sandboxes that may exec
// concurrently during a batch motive exec. The value was derived from host
// measurements: at ~84% swap utilisation, simultaneous heavy work in more than
// 2 sandboxes drives the host into swap thrashing. Disk — not RAM — is the
// binding resource on concurrency; swap pressure imposes a separate scheduling
// limit on simultaneous activity. Do not raise this constant without new
// measurements showing the host sustains additional concurrent load without
// thrashing.
const DefaultBatchParallel = 2

// BatchExecOptions configures a BatchExec call.
type BatchExecOptions struct {
	// Argv is the command and its arguments to run in every sandbox.
	Argv []string
	// Parallel is the maximum number of sandboxes that may exec concurrently.
	// Zero or negative selects DefaultBatchParallel.
	Parallel int
}

// BatchExecOutcome records the result of one sandbox's exec.
type BatchExecOutcome struct {
	// SandboxID identifies the sandbox.
	SandboxID domain.SandboxID
	// Handle is the human-readable project/name handle.
	Handle string
	// ExitCode is the process exit code reported by the guest.
	// Zero when Err is non-nil.
	ExitCode int32
	// Stdout is the captured standard output from the exec.
	Stdout []byte
	// Stderr is the captured standard error from the exec.
	Stderr []byte
	// Err is non-nil when the exec itself failed (e.g. sandbox unreachable,
	// agent dial error). It is nil even when ExitCode != 0.
	Err error
}

// BatchExecResult aggregates per-sandbox exec outcomes.
type BatchExecResult struct {
	// Outcomes contains one entry per sandbox in the motive, in the order
	// returned by GetByMotive. Entries are present regardless of success or
	// failure — all sandboxes always run to completion.
	Outcomes []BatchExecOutcome
}

// HasFailures reports whether any outcome is a failure (Err non-nil or
// ExitCode non-zero).
func (r BatchExecResult) HasFailures() bool {
	for _, o := range r.Outcomes {
		if o.Err != nil || o.ExitCode != 0 {
			return true
		}
	}
	return false
}

// batchExecOneFn execs argv in a single sandbox and returns the exit code,
// captured stdout/stderr, and any transport-level error. It exists so tests
// can inject a fake without a real agent protocol.
type batchExecOneFn func(ctx context.Context, sb domain.Sandbox, argv []string) (exitCode int32, stdout, stderr []byte, err error)

// BatchExec runs opts.Argv in every sandbox of motiveID with bounded
// parallelism. At most opts.Parallel (or DefaultBatchParallel when zero)
// sandboxes exec concurrently. A sandbox failure (exec error or non-zero exit)
// never aborts its siblings — all run to completion. The returned result
// contains one outcome per sandbox; the returned error is non-nil when any
// sandbox failed.
func (s *Service) BatchExec(ctx context.Context, motiveID string, opts BatchExecOptions) (BatchExecResult, error) {
	return s.batchExecWith(ctx, motiveID, opts, s.execOneForBatch)
}

// batchExecWith is the testable core of BatchExec. Tests inject execFn to
// avoid needing a real guest agent.
func (s *Service) batchExecWith(ctx context.Context, motiveID string, opts BatchExecOptions, execFn batchExecOneFn) (BatchExecResult, error) {
	sandboxes, err := s.GetByMotive(ctx, motiveID)
	if err != nil {
		return BatchExecResult{}, fmt.Errorf("service: batch exec motive %q: list sandboxes: %w", motiveID, err)
	}

	parallel := opts.Parallel
	if parallel <= 0 {
		parallel = DefaultBatchParallel
	}

	outcomes := make([]BatchExecOutcome, len(sandboxes))

	// sem is a counting semaphore: sending acquires a slot, receiving releases
	// it. Its capacity equals parallel, so at most parallel goroutines may be
	// actively executing at any instant.
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup

	for i, sb := range sandboxes {
		wg.Add(1)
		i, sb := i, sb // per-iteration capture for the closure
		go func() {
			defer wg.Done()
			sem <- struct{}{}        // acquire: blocks until a slot is free
			defer func() { <-sem }() // release: unconditional even on panic

			exitCode, stdout, stderr, execErr := execFn(ctx, sb, opts.Argv)
			outcomes[i] = BatchExecOutcome{
				SandboxID: sb.ID,
				Handle:    sb.Handle(),
				ExitCode:  exitCode,
				Stdout:    stdout,
				Stderr:    stderr,
				Err:       execErr,
			}
		}()
	}
	wg.Wait()

	result := BatchExecResult{Outcomes: outcomes}

	// Collect all failures into an aggregate error; leave the result intact so
	// callers can inspect per-sandbox outcomes even on partial failure.
	var errs []error
	for _, o := range outcomes {
		if o.Err != nil {
			errs = append(errs, fmt.Errorf("sandbox %s: %w", o.SandboxID, o.Err))
		} else if o.ExitCode != 0 {
			errs = append(errs, fmt.Errorf("sandbox %s: exit %d", o.SandboxID, o.ExitCode))
		}
	}
	if len(errs) > 0 {
		return result, fmt.Errorf("service: batch exec motive %q: %w", motiveID, errors.Join(errs...))
	}
	return result, nil
}

// execOneForBatch is the production implementation of batchExecOneFn. It
// delegates to Service.Exec and captures stdout and stderr into buffers so
// concurrent sandbox execs do not interleave on the caller's output streams.
func (s *Service) execOneForBatch(ctx context.Context, sb domain.Sandbox, argv []string) (int32, []byte, []byte, error) {
	var outBuf, errBuf bytes.Buffer
	exitCode, err := s.Exec(ctx, sb.ID.String(), agent.ExecOptions{
		Argv:   argv,
		Stdout: &outBuf,
		Stderr: &errBuf,
	})
	return exitCode, outBuf.Bytes(), errBuf.Bytes(), err
}
