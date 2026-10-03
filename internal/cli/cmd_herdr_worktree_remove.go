package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"

	wt "github.com/IniZio/nexus/internal/herdrworktree"
)

var worktreeRemoveRunners = wt.DefaultRunners

func runHerdrWorktreeRemove(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("herdr worktree-remove", flag.ContinueOnError)
	fs.SetOutput(out.Stderr())
	ref := fs.String("ref", "", "sandbox handle, sandbox id prefix, or worktree path (required)")
	force := fs.Bool("force", false, "discard uncommitted changes and remove anyway")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "herdr worktree-remove: " + err.Error()}
	}
	if fs.NArg() > 0 || *ref == "" {
		return &UsageError{Msg: "herdr worktree-remove: usage: worktree-remove --ref <sandbox ref | worktree path> [--force]"}
	}
	r := worktreeRemoveRunners()
	target := *ref
	if strings.HasPrefix(target, "/") {
		target = wt.ResolveRefByWorktreePath(ctx, target, r)
	}
	res, err := wt.Teardown(ctx, target, *force, r)
	if err != nil {
		var dirty *wt.DirtyWorktreeError
		if errors.As(err, &dirty) {
			files := dirty.Files
			if files == nil {
				files = []string{}
			}
			_ = json.NewEncoder(out.Stdout()).Encode(map[string]any{
				"removed":       false,
				"error":         "dirty_worktree",
				"worktree_path": dirty.Path,
				"count":         dirty.Count,
				"files":         files,
				"more":          dirty.More,
				"message":       dirty.Error(),
			})
			return &CodedError{Code: ErrCodeInternalError, Msg: fmt.Sprintf("herdr worktree-remove: %s", dirty.Error()), Err: err}
		}
		return &CodedError{Code: ErrCodeInternalError, Msg: err.Error(), Err: err}
	}
	result := map[string]any{"removed": true, "output": res.Output}
	if res.Bound {
		result["how"] = res.How
		result["workspace_id"] = res.WorkspaceID
		result["handle"] = res.Handle
		result["sandbox_id"] = res.SandboxID
	}
	return json.NewEncoder(out.Stdout()).Encode(result)
}
