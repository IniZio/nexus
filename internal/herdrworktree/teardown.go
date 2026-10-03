package herdrworktree

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/herdrout"
)

// TeardownResult is the outcome of Teardown. Bound is false when no herdr
// workspace was bound to the ref and the sandbox was removed directly; in that
// case only Output is meaningful.
type TeardownResult struct {
	Bound       bool
	How         string
	WorkspaceID string
	Handle      string
	SandboxID   string
	Output      string
}

// Teardown reverses CreateSandbox for ref: it removes the herdr worktree bound to the
// sandbox (the worktree.removed hook reaps the sandbox), verifies the sandbox is gone,
// and falls back to `nexus sandbox rm` when needed. Errors are prefixed "delegate_teardown:".
func Teardown(ctx context.Context, ref string, force bool, r Runners) (TeardownResult, error) {
	pollInterval, pollTimeout := r.PollInterval, r.PollTimeout
	if pollInterval == 0 {
		pollInterval = DefaultPollInterval
	}
	if pollTimeout == 0 {
		pollTimeout = DefaultPollTimeout
	}
	listOut, err := r.Host(ctx, "herdr", "list")
	if err != nil {
		return TeardownResult{}, fmt.Errorf("delegate_teardown: nexus herdr list: %w\n%s", err, listOut)
	}
	ws, handle, sandboxID, _, bound := ParseListBindingByRef(listOut, ref)
	if !bound {
		out, runErr := r.Host(ctx, "sandbox", "rm", ref)
		if runErr != nil {
			return TeardownResult{}, fmt.Errorf("delegate_teardown: sandbox rm: %w\n%s", runErr, out)
		}
		return TeardownResult{Output: out}, nil
	}
	herdrBin, err := ResolveHerdrBin()
	if err != nil {
		return TeardownResult{}, fmt.Errorf("delegate_teardown: %w; workspace %s is bound to %s and must be removed through herdr", err, ws, ref)
	}
	rmArgv := []string{"worktree", "remove", "--workspace", ws}
	if force {
		rmArgv = append(rmArgv, "--force")
	}
	rmOut, err := r.Herdr(ctx, herdrBin, rmArgv...)
	if err != nil {
		if !force {
			errCode, errMsg, parsed := herdrout.ParseHerdrErrorCode(rmOut)
			if parsed && errCode == "dirty_worktree_requires_force" {
				wtPath := ""
				if wtOut, wtErr := r.Herdr(ctx, herdrBin, "worktree", "list", "--workspace", ws, "--json"); wtErr == nil {
					wtPath = herdrout.WorktreePathByWorkspaceID(wtOut, ws)
				}
				if wtPath == "" {
					if i := strings.Index(errMsg, "'"); i >= 0 {
						if j := strings.Index(errMsg[i+1:], "'"); j >= 0 {
							wtPath = errMsg[i+1 : i+1+j]
						}
					}
				}
				if wtPath != "" {
					statusOut, _ := r.git(ctx, "-C", wtPath, "status", "--porcelain", "--untracked-files=all")
					var lines []string
					for _, l := range strings.Split(strings.TrimRight(statusOut, "\n"), "\n") {
						if l != "" {
							lines = append(lines, l)
						}
					}
					n := len(lines)
					const porcelainCap = 50
					suffix := ""
					if n > porcelainCap {
						suffix = fmt.Sprintf("\n... and %d more", n-porcelainCap)
						lines = lines[:porcelainCap]
					}
					return TeardownResult{}, fmt.Errorf(
						"delegate_teardown: worktree %s has uncommitted changes (%d files):\n%s%s\nHarvest or commit them first, or call delegate_teardown again with force:true to discard them.",
						wtPath, n, strings.Join(lines, "\n"), suffix,
					)
				}
				return TeardownResult{}, fmt.Errorf(
					"delegate_teardown: herdr worktree remove --workspace %s: dirty worktree (code=%s: %s); "+
						"harvest or commit changes first, or call delegate_teardown again with force:true to discard them.",
					ws, errCode, errMsg,
				)
			}
			if parsed {
				return TeardownResult{}, fmt.Errorf("delegate_teardown: herdr worktree remove --workspace %s: code=%s: %s\n%s", ws, errCode, errMsg, rmOut)
			}
		}
		return TeardownResult{}, fmt.Errorf("delegate_teardown: herdr worktree remove --workspace %s: %w\n%s", ws, err, rmOut)
	}

	out := rmOut
	how := "removed-by-hook"
	deadline := time.Now().Add(pollTimeout)
	stillListed := false
	for {
		psOut, listErr := r.Host(ctx, "sandbox", "list")
		if listErr != nil {
			return TeardownResult{}, fmt.Errorf("delegate_teardown: nexus sandbox list: %w\n%s", listErr, psOut)
		}
		if !SandboxListed(psOut, handle, sandboxID) {
			break
		}
		if time.Now().After(deadline) {
			stillListed = true
			break
		}
		select {
		case <-ctx.Done():
			return TeardownResult{}, fmt.Errorf("delegate_teardown: context cancelled waiting for sandbox %s to disappear", handle)
		case <-time.After(pollInterval):
		}
	}
	if stillListed {
		fallbackOut, runErr := r.Host(ctx, "sandbox", "rm", ref)
		if runErr != nil {
			if IsSandboxNotFound(runErr, fallbackOut) {
				how = "already-gone"
				out += fallbackOut
			} else {
				return TeardownResult{}, fmt.Errorf("delegate_teardown: sandbox %s still listed after herdr worktree remove; sandbox rm: %w\n%s", handle, runErr, fallbackOut)
			}
		} else {
			how = "removed-by-fallback"
			out += fallbackOut
		}
	}
	return TeardownResult{Bound: true, How: how, WorkspaceID: ws, Handle: handle, SandboxID: sandboxID, Output: out}, nil
}
