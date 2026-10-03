package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os/exec"
	"strings"

	wt "github.com/IniZio/nexus/internal/herdrworktree"
)

var worktreeCreateRunners = wt.DefaultRunners

func runHerdrWorktreeCreate(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("herdr worktree-create", flag.ContinueOnError)
	fs.SetOutput(out.Stderr())
	repo := fs.String("repo", "", "git checkout open as a herdr workspace (default: git toplevel of cwd)")
	branch := fs.String("branch", "", "branch name for the new linked git worktree (required)")
	base := fs.String("base", "", "base ref for the new branch (default: herdr default)")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "herdr worktree-create: " + err.Error()}
	}
	if fs.NArg() > 0 {
		return &UsageError{Msg: fmt.Sprintf("herdr worktree-create: unexpected argument %q; usage: worktree-create [--repo <path>] --branch <branch> [--base <ref>]", fs.Arg(0))}
	}
	if *repo == "" {
		top, err := exec.CommandContext(ctx, "git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			return &UsageError{Msg: "herdr worktree-create: --repo not given and cwd is not in a git checkout"}
		}
		*repo = strings.TrimSpace(string(top))
	}
	result, err := wt.CreateSandbox(ctx, wt.CreateArgs{RepoPath: *repo, Branch: *branch, Base: *base}, worktreeCreateRunners())
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: err.Error(), Err: err}
	}
	return json.NewEncoder(out.Stdout()).Encode(result)
}
