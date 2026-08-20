package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/IniZio/nexus3/internal/core/artifact"
	"github.com/IniZio/nexus3/internal/core/service"
)

func init() {
	Register(Command{
		Name:    "restore",
		Summary: "Fan-out N running children from a retained snapshot (--count N, default 1)",
		Run:     runRestore,
	})
}

// ── dispatcher / flag parsing ────────────────────────────────────────────────

func runRestore(ctx context.Context, args []string, out *Output) error {
	return runRestoreWith(ctx, args, out)
}

// runRestoreWith is the testable entry point. Callers may pass a pre-built svc
// to bypass newSnapshotService (used by unit tests); if svc is nil the
// production constructor is used.
func runRestoreWith(ctx context.Context, args []string, out *Output, svcs ...*service.Service) error {
	count := 1
	force := false
	var positionals []string

	i := 0
	for i < len(args) {
		arg := args[i]
		switch {
		case arg == "--count":
			if i+1 >= len(args) {
				return &UsageError{Msg: "restore: --count requires an argument"}
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return &UsageError{Msg: fmt.Sprintf("restore: --count must be a positive integer, got %q", args[i])}
			}
			count = n
		case strings.HasPrefix(arg, "--count="):
			val := arg[len("--count="):]
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				return &UsageError{Msg: fmt.Sprintf("restore: --count must be a positive integer, got %q", val)}
			}
			count = n
		case arg == "--force":
			// Skip the disk-space preflight. See service.ForkForceDiskSpace:
			// the projection charges the full parent footprint per child, which
			// over-counts on reflink filesystems where the copy is near-free.
			force = true
		case len(arg) > 1 && arg[0] == '-':
			return &UsageError{Msg: fmt.Sprintf("restore: unknown flag %q", arg)}
		default:
			positionals = append(positionals, arg)
		}
		i++
	}

	if len(positionals) != 1 {
		return &UsageError{Msg: "restore: usage: restore <snapshot-id> [--count N] [--force]"}
	}
	snapID := artifact.SnapshotID(positionals[0])

	var svc *service.Service
	if len(svcs) > 0 && svcs[0] != nil {
		svc = svcs[0]
	} else {
		var err error
		svc, err = newSnapshotService()
		if err != nil {
			return errSandbox("restore", err)
		}
	}

	var forkOpts []service.ForkOption
	if force {
		forkOpts = append(forkOpts, service.ForkForceDiskSpace())
	}
	children, err := svc.RestoreFromSnapshot(ctx, snapID, count, forkOpts...)
	if err != nil {
		return errSandbox("restore", err)
	}

	infos := make([]sandboxInfoJSON, len(children))
	for i, ch := range children {
		infos[i] = toSandboxInfoJSON(ch)
	}
	out.EmitSuccess("restore.created", sandboxListDataJSON{Sandboxes: infos},
		fmt.Sprintf("restored %d children from snapshot %s", len(children), snapID))
	return nil
}
