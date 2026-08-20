package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/IniZio/nexus3/internal/core/service"
)

func init() {
	Register(Command{
		Name:    "fork",
		Summary: "Fork a sandbox into N running children (--count N, default 1)",
		Run:     runFork,
	})
}

// ── dispatcher / flag parsing ────────────────────────────────────────────────

func runFork(ctx context.Context, args []string, out *Output) error {
	return runForkWith(ctx, args, out)
}

// runForkWith is the testable entry point. Callers may pass a pre-built svc
// to bypass newSandboxService (used by unit tests); if svc is nil the
// production constructor is used.
func runForkWith(ctx context.Context, args []string, out *Output, svcs ...*service.Service) error {
	count := 1
	force := false
	var positionals []string

	i := 0
	for i < len(args) {
		arg := args[i]
		switch {
		case arg == "--count":
			if i+1 >= len(args) {
				return &UsageError{Msg: "fork: --count requires an argument"}
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return &UsageError{Msg: fmt.Sprintf("fork: --count must be a positive integer, got %q", args[i])}
			}
			count = n
		case strings.HasPrefix(arg, "--count="):
			val := arg[len("--count="):]
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				return &UsageError{Msg: fmt.Sprintf("fork: --count must be a positive integer, got %q", val)}
			}
			count = n
		case arg == "--force":
			// Skip the disk-space preflight. See service.ForkForceDiskSpace:
			// the projection charges the full parent footprint per child, which
			// over-counts on reflink filesystems where the copy is near-free.
			force = true
		case len(arg) > 1 && arg[0] == '-':
			return &UsageError{Msg: fmt.Sprintf("fork: unknown flag %q", arg)}
		default:
			positionals = append(positionals, arg)
		}
		i++
	}

	if len(positionals) != 1 {
		return &UsageError{Msg: "fork: usage: fork <ref> [--count N] [--force]"}
	}
	ref := positionals[0]

	var svc *service.Service
	if len(svcs) > 0 && svcs[0] != nil {
		svc = svcs[0]
	} else {
		var err error
		svc, err = newSandboxService()
		if err != nil {
			return errSandbox("fork", err)
		}
	}

	var forkOpts []service.ForkOption
	if force {
		forkOpts = append(forkOpts, service.ForkForceDiskSpace())
	}
	children, err := svc.Fork(ctx, ref, count, forkOpts...)
	if err != nil {
		return errSandbox("fork", err)
	}

	infos := make([]sandboxInfoJSON, len(children))
	for i, ch := range children {
		infos[i] = toSandboxInfoJSON(ch)
	}
	out.EmitSuccess("fork.created", sandboxListDataJSON{Sandboxes: infos},
		fmt.Sprintf("forked %s into %d children", ref, len(children)))
	return nil
}
