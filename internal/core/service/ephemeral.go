package service

import (
	"context"
	"fmt"
	"io"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/image"
)

// ExecOptions groups the per-execution parameters passed to RunEphemeral.
// Keeping argv, env, and cwd together avoids a 13-parameter function signature
// where the three exec-specific fields were interspersed between opts and the
// stdio params.
type ExecOptions struct {
	Argv []string
	Env  map[string]string
	Cwd  string
}

// RunEphemeral creates a sandbox, runs a command in it, removes the sandbox,
// and returns the exit code. Removal is guaranteed even when exec returns an
// error or the context is cancelled (SIGINT/SIGTERM).
//
// Cleanup invariant: svc.Remove is called with context.WithoutCancel(ctx) so
// that a cancelled ctx (from a signal) does not skip cleanup.
//
// If opts.Name is empty the caller should supply a generated unique suffix as
// the name parameter. If project is empty it defaults to "ephemeral".
func RunEphemeral(
	ctx context.Context,
	svc *Service,
	cache *image.Cache,
	newDriver DriverFactory,
	probe ProbeFunc,
	project, name string,
	opts CreateAndBootOptions,
	exec ExecOptions,
	stdin io.Reader,
	stdout, stderr io.Writer,
) (exitCode int32, err error) {
	if project == "" {
		project = "ephemeral"
	}

	sb, err := CreateAndBoot(ctx, svc, cache, newDriver, probe, project, name, opts)
	if err != nil {
		return 0, fmt.Errorf("run: create: %w", err)
	}

	// Guarantee removal even on exec error or context cancellation.
	// context.WithoutCancel produces a context that is never cancelled, so
	// Remove succeeds even when the parent ctx was cancelled by SIGINT.
	defer func() {
		rmCtx := context.WithoutCancel(ctx)
		if rmErr := svc.Remove(rmCtx, sb.ID.String()); rmErr != nil {
			// Log to stderr when available; do not mask the exec error.
			if stderr != nil {
				fmt.Fprintf(stderr, "run: remove sandbox %s: %v\n", sb.ID, rmErr)
			}
		}
	}()

	execOpts := agent.ExecOptions{
		Argv:   exec.Argv,
		Env:    exec.Env,
		Cwd:    exec.Cwd,
		Stdin:  stdin,
		Stdout: stdout,
		Stderr: stderr,
	}
	exitCode, err = svc.Exec(ctx, sb.ID.String(), execOpts)
	return exitCode, err
}
