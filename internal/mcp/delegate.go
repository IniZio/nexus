package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// standingOrders is prepended to every brief delegate_agent_dispatch delivers
// into the guest. It tells the in-guest agent what kind of environment it is
// in and what the baseline for evidence is — facts a cold agent cannot
// discover on its own. Keep it in sync with the canonical copy in
// plugins/claude/skills/nexus3/references/delegate-briefs.md.
const standingOrders = `STANDING ORDERS (nexus3 sandbox)
You are running inside a dedicated, isolated nexus3 microVM created for this task.
This VM is yours: you are root, it has its own kernel, disk and network, and CPU and
memory grow automatically under load. Use it fully.
The baseline for any work here is the project's full local stack running (e.g.
` + "`docker compose up`" + `) and tests executed against it. A mock, a stub, or a partial
setup is not acceptable evidence.
You are expected to unblock yourself: install tools and packages, pull images (use
a mirror if a registry denies you), fix env files, fix the code. Never stop at the
first obstacle and never ask the operator for something you can do yourself.
Record any friction you hit — what happened, the evidence, the workaround — in your
report so the platform can be fixed.
Egress is policy-gated. A 403 from the proxy names the policy that denied you:
report it, do not route around it.
Containers built or run by docker inside this VM already trust the sandbox TLS perimeter (CA at /etc/nexus3/ca, SSL_CERT_FILE and friends pre-set); a 403 from a TLS-intercepted host is egress policy, not a certificate problem — report it, do not work around it.

`

// runHostCLI runs the nexus3 host binary with argv and returns its combined
// output. It is a package variable so tests can substitute a fake executor.
var runHostCLI = func(ctx context.Context, argv ...string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable: %w", err)
	}
	return runBinary(ctx, exe, argv...)
}

// runHerdrCLI runs the herdr binary (resolved by resolveHerdrBin) with argv
// and returns its combined output. Separate from runHostCLI so tests can tell
// herdr calls from nexus3 calls and fake each independently.
var runHerdrCLI = func(ctx context.Context, herdrBin string, argv ...string) (string, error) {
	return runBinary(ctx, herdrBin, argv...)
}

func runBinary(ctx context.Context, bin string, argv ...string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

type delegateWorktreeCreateArgs struct {
	RepoPath        string   `json:"repo_path"                  jsonschema:"absolute host path to the git repo checkout that is open as a herdr workspace (required)"`
	Branch          string   `json:"branch"                     jsonschema:"branch name for the new linked git worktree (required)"`
	Base            string   `json:"base,omitempty"             jsonschema:"base ref for the new branch (optional; herdr default when empty)"`
	ImageRef        string   `json:"image_ref,omitempty"        jsonschema:"MUST NOT be set — the image comes from the checkout's .nexus/config.yaml (or .nexus/Containerfile); any value here returns an error"`
	MemoryMiB       uint32   `json:"memory_mib,omitempty"       jsonschema:"MUST NOT be set — not supported by the herdr worktree-sandbox path; any value here returns an error"`
	VCPUs           uint32   `json:"vcpus,omitempty"            jsonschema:"MUST NOT be set — not supported by the herdr worktree-sandbox path; any value here returns an error"`
	AllowedBranches []string `json:"allowed_branches,omitempty" jsonschema:"MUST NOT be set — branch policy is derived from the worktree; any value here returns an error"`
}

type delegateAgentDispatchArgs struct {
	Ref   string `json:"ref"   jsonschema:"sandbox reference: ID, ID prefix, or project/name handle (required)"`
	Brief string `json:"brief" jsonschema:"task brief to deliver to the in-guest claude agent (required)"`
}

type delegateAgentPollArgs struct {
	Ref string `json:"ref" jsonschema:"sandbox reference: ID, ID prefix, or project/name handle (required)"`
}

type delegateAgentPollResult struct {
	GitLog     string `json:"git_log"`
	GitStatus  string `json:"git_status"`
	BranchName string `json:"branch_name"`
}

type delegateTeardownArgs struct {
	Ref string `json:"ref" jsonschema:"sandbox reference: ID, ID prefix, or project/name handle (required)"`
}

func validateDelegateWorktreeCreate(args delegateWorktreeCreateArgs) error {
	if args.RepoPath == "" {
		return fmt.Errorf("repo_path is required")
	}
	if !filepath.IsAbs(args.RepoPath) {
		return fmt.Errorf("repo_path must be an absolute path (got %q)", args.RepoPath)
	}
	if st, err := os.Stat(args.RepoPath); err != nil {
		return fmt.Errorf("repo_path %q: %w", args.RepoPath, err)
	} else if !st.IsDir() {
		return fmt.Errorf("repo_path %q is not a directory", args.RepoPath)
	}
	if args.Branch == "" {
		return fmt.Errorf("branch is required")
	}
	if len(args.AllowedBranches) > 0 {
		return fmt.Errorf(
			"delegate_worktree_create: allowed_branches cannot be set by the caller "+
				"(value %v rejected); branch policy is derived from the worktree's "+
				"current branch by the service layer and cannot be widened via this tool",
			args.AllowedBranches,
		)
	}
	if args.ImageRef != "" {
		return fmt.Errorf(
			"delegate_worktree_create: image_ref is not supported (value %q rejected); "+
				"the image is taken from %s (or .nexus/Containerfile) exactly as a herdr worktree sandbox",
			args.ImageRef, filepath.Join(args.RepoPath, ".nexus", "config.yaml"),
		)
	}
	if args.MemoryMiB > 0 || args.VCPUs > 0 {
		return fmt.Errorf(
			"delegate_worktree_create: memory_mib/vcpus are not supported by the herdr worktree-sandbox path "+
				"(memory_mib=%d vcpus=%d rejected); the verb has no flags for them",
			args.MemoryMiB, args.VCPUs,
		)
	}
	return nil
}

// resolveHerdrBin mirrors the CLI's herdr binary lookup: HERDR_BIN_PATH, else PATH.
func resolveHerdrBin() (string, error) {
	if p := os.Getenv("HERDR_BIN_PATH"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("herdr"); err == nil {
		return p, nil
	}
	return "", fmt.Errorf(`herdr not found: HERDR_BIN_PATH is unset and no "herdr" binary is on PATH`)
}

// findHerdrWorkspaceID picks the herdr workspace whose checkout path matches
// repoPath: exact match first, then prefix, then HERDR_WORKSPACE_ID from env.
func findHerdrWorkspaceID(workspaceListOut, repoPath string) (string, error) {
	var parsed struct {
		Result struct {
			Workspaces []struct {
				WorkspaceID string `json:"workspace_id"`
				Worktree    struct {
					CheckoutPath string `json:"checkout_path"`
				} `json:"worktree"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(workspaceListOut), &parsed); err != nil {
		// Output may carry non-JSON lines; scan for the first JSON object line.
		for _, line := range strings.Split(workspaceListOut, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &parsed) == nil {
				break
			}
		}
	}
	want := filepath.Clean(repoPath)
	for _, ws := range parsed.Result.Workspaces {
		if ws.Worktree.CheckoutPath != "" && filepath.Clean(ws.Worktree.CheckoutPath) == want {
			return ws.WorkspaceID, nil
		}
	}
	for _, ws := range parsed.Result.Workspaces {
		if ws.Worktree.CheckoutPath == "" {
			continue
		}
		cp := filepath.Clean(ws.Worktree.CheckoutPath)
		if strings.HasPrefix(want, cp+string(filepath.Separator)) || strings.HasPrefix(cp, want+string(filepath.Separator)) {
			return ws.WorkspaceID, nil
		}
	}
	if env := os.Getenv("HERDR_WORKSPACE_ID"); env != "" {
		return env, nil
	}
	return "", fmt.Errorf("repo %s is not open as a herdr workspace; open it in herdr first", repoPath)
}

// parseHerdrWorktreeCreateWS scans `herdr worktree create` output for the
// JSON line {"ws":"<id>","linked":true} and returns the workspace ID.
func parseHerdrWorktreeCreateWS(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var v struct {
			WS string `json:"ws"`
		}
		if json.Unmarshal([]byte(line), &v) == nil && v.WS != "" {
			return v.WS
		}
	}
	return ""
}

// parseHerdrListBinding finds the `nexus3 herdr list` line for workspaceID
// and returns its handle and sandbox_id.
func parseHerdrListBinding(out, workspaceID string) (handle, sandboxID string, ok bool) {
	needle := "workspace_id=" + workspaceID
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		matched := false
		for _, f := range fields {
			if f == needle {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		for _, f := range fields {
			if v, found := strings.CutPrefix(f, "handle="); found {
				handle = v
			} else if v, found := strings.CutPrefix(f, "sandbox_id="); found {
				sandboxID = v
			}
		}
		return handle, sandboxID, true
	}
	return "", "", false
}

// parseHerdrWorktreePath returns the checkout path of the worktree on branch
// from `herdr worktree list --json`; empty when not found.
func parseHerdrWorktreePath(out, branch string) string {
	var parsed struct {
		Result struct {
			Worktrees []struct {
				Branch string `json:"branch"`
				Path   string `json:"path"`
			} `json:"worktrees"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &parsed) == nil {
				break
			}
		}
	}
	for _, wt := range parsed.Result.Worktrees {
		if wt.Branch == branch {
			return wt.Path
		}
	}
	return ""
}

func registerDelegateTools(srv *gosdk.Server, svc SandboxService) {
	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "delegate_worktree_create",
		Description: "Create a linked git worktree via herdr (`herdr worktree create --workspace <parent> --branch <branch>`), " +
			"then bind a nexus3 sandbox to it with the same `nexus3 herdr worktree-sandbox` path a herdr-created worktree gets: " +
			"image and egress from the checkout's .nexus/config.yaml (or .nexus/Containerfile), .git and .groundwork mounts, named volumes. " +
			"Requires herdr running and repo_path open as a herdr workspace. " +
			"image_ref, memory_mib and vcpus are rejected (the worktree-sandbox path has no flags for them); " +
			"allowed_branches MUST NOT be set — branch policy is derived from the worktree. " +
			"Returns {workspace_id, worktree_path, branch, handle, sandbox_id, output} on success.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args delegateWorktreeCreateArgs) (*gosdk.CallToolResult, any, error) {
		if err := validateDelegateWorktreeCreate(args); err != nil {
			return errorResult(err), nil, nil
		}
		herdrBin, err := resolveHerdrBin()
		if err != nil {
			return errorResult(fmt.Errorf("delegate_worktree_create: %w; delegate_worktree_create binds the sandbox to a herdr workspace and needs herdr running", err)), nil, nil
		}

		wsListOut, err := runHerdrCLI(ctx, herdrBin, "workspace", "list")
		if err != nil {
			return errorResult(fmt.Errorf("delegate_worktree_create: herdr workspace list: %w\n%s", err, wsListOut)), nil, nil
		}
		parent, err := findHerdrWorkspaceID(wsListOut, args.RepoPath)
		if err != nil {
			return errorResult(fmt.Errorf("delegate_worktree_create: %w", err)), nil, nil
		}

		createArgv := []string{"worktree", "create", "--workspace", parent, "--branch", args.Branch}
		if args.Base != "" {
			createArgv = append(createArgv, "--base", args.Base)
		}
		createArgv = append(createArgv, "--no-focus")
		createOut, err := runHerdrCLI(ctx, herdrBin, createArgv...)
		if err != nil {
			return errorResult(fmt.Errorf("delegate_worktree_create: herdr worktree create: %w\n%s", err, createOut)), nil, nil
		}
		ws := parseHerdrWorktreeCreateWS(createOut)
		if ws == "" {
			return errorResult(fmt.Errorf("delegate_worktree_create: herdr worktree create: no {\"ws\":...} line in output\n%s", createOut)), nil, nil
		}

		bindOut, err := runHostCLI(ctx, "herdr", "worktree-sandbox", ws)
		if err != nil {
			return errorResult(fmt.Errorf("delegate_worktree_create: nexus3 herdr worktree-sandbox: %w\n%s", err, bindOut)), nil, nil
		}

		listOut, err := runHostCLI(ctx, "herdr", "list")
		if err != nil {
			return errorResult(fmt.Errorf("delegate_worktree_create: nexus3 herdr list: %w\n%s", err, listOut)), nil, nil
		}
		handle, sandboxID, ok := parseHerdrListBinding(listOut, ws)
		if !ok {
			return errorResult(fmt.Errorf("delegate_worktree_create: sandbox binding for workspace %s not found after worktree-sandbox\n%s", ws, listOut)), nil, nil
		}

		// Best effort: the worktree path is informational only.
		worktreePath := ""
		if wtOut, wtErr := runHerdrCLI(ctx, herdrBin, "worktree", "list", "--json"); wtErr == nil {
			worktreePath = parseHerdrWorktreePath(wtOut, args.Branch)
		}

		return successResult(map[string]string{
			"workspace_id":  ws,
			"worktree_path": worktreePath,
			"branch":        args.Branch,
			"handle":        handle,
			"sandbox_id":    sandboxID,
			"output":        createOut + bindOut,
		}), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "delegate_agent_dispatch",
		Description: "Deliver a task brief to the claude agent running inside a worktree sandbox " +
			"via `nexus3 herdr space-agent --autonomous --no-focus`. " +
			"The in-guest claude runs in auto permission mode (--permission-mode auto). " +
			"Returns the dispatch log.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args delegateAgentDispatchArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return errorResult(fmt.Errorf("ref is required")), nil, nil
		}
		if args.Brief == "" {
			return errorResult(fmt.Errorf("brief is required")), nil, nil
		}
		brief := standingOrders + args.Brief
		out, runErr := runHostCLI(ctx, "herdr", "agent", "--autonomous", "--no-focus", args.Ref, brief)
		if runErr != nil {
			return errorResult(fmt.Errorf("delegate_agent_dispatch: %w\n%s", runErr, out)), nil, nil
		}
		return successResult(map[string]string{"output": out}), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "delegate_agent_poll",
		Description: "Poll the in-guest agent's progress by reading git state from /workspace. " +
			"Returns {git_log, git_status, branch_name}.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args delegateAgentPollArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return errorResult(fmt.Errorf("ref is required")), nil, nil
		}
		runGit := func(gitArgv []string) (string, error) {
			code, stdout, stderr, execErr := svc.Exec(ctx, args.Ref, gitArgv, nil, "/workspace", "")
			if execErr != nil {
				return "", fmt.Errorf("exec %v: %w", gitArgv, execErr)
			}
			if code != 0 {
				return fmt.Sprintf("[exit %d] %s", code, stderr), nil
			}
			return stdout, nil
		}
		gitLog, err := runGit([]string{"git", "-C", "/workspace", "log", "--oneline", "-10"})
		if err != nil {
			return errorResult(err), nil, nil
		}
		gitStatus, err := runGit([]string{"git", "-C", "/workspace", "status", "--short"})
		if err != nil {
			return errorResult(err), nil, nil
		}
		branchName, err := runGit([]string{"git", "-C", "/workspace", "branch", "--show-current"})
		if err != nil {
			return errorResult(err), nil, nil
		}
		return successResult(delegateAgentPollResult{
			GitLog:     gitLog,
			GitStatus:  gitStatus,
			BranchName: branchName,
		}), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name:        "delegate_teardown",
		Description: "Remove a worktree sandbox after its slice is complete. Returns {removed, output}.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args delegateTeardownArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return errorResult(fmt.Errorf("ref is required")), nil, nil
		}
		out, runErr := runHostCLI(ctx, "sandbox", "rm", args.Ref)
		if runErr != nil {
			return errorResult(fmt.Errorf("delegate_teardown: %w\n%s", runErr, out)), nil, nil
		}
		return successResult(map[string]interface{}{"removed": true, "output": out}), nil, nil
	})
}
