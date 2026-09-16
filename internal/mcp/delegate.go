package mcp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"

	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type delegateWorktreeCreateArgs struct {
	RepoPath        string   `json:"repo_path"             jsonschema:"absolute host path to the git repo or worktree (required)"`
	Handle          string   `json:"handle"                jsonschema:"sandbox handle in project/name format, e.g. myproject/slice-e (required)"`
	ImageRef        string   `json:"image_ref,omitempty"   jsonschema:"image tag or digest to boot; required for a running sandbox"`
	MemoryMiB       uint32   `json:"memory_mib,omitempty"  jsonschema:"guest RAM in MiB (optional; 0 = driver default)"`
	VCPUs           uint32   `json:"vcpus,omitempty"       jsonschema:"vCPU count (optional; 0 = driver default)"`
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
	var buf bytes.Buffer
	cmd := exec.CommandContext(ctx, exe, argv...)
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err = cmd.Run()
	return buf.String(), err
}

func validateDelegateWorktreeCreate(args delegateWorktreeCreateArgs) error {
	if args.RepoPath == "" {
		return fmt.Errorf("repo_path is required")
	}
	if args.Handle == "" {
		return fmt.Errorf("handle is required (project/name format)")
	}
	if len(args.AllowedBranches) > 0 {
		return fmt.Errorf(
			"delegate_worktree_create: allowed_branches cannot be set by the caller "+
				"(value %v rejected); branch policy is derived from the worktree's "+
				"current branch by the service layer and cannot be widened via this tool",
			args.AllowedBranches,
		)
	}
	return nil
}

func registerDelegateTools(srv *gosdk.Server, svc SandboxService) {
	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "delegate_worktree_create",
		Description: "Create a worktree-backed sandbox for delegating a coding slice. " +
			"Mounts repo_path at /workspace inside the guest and installs the claude-code agent. " +
			"allowed_branches MUST NOT be set — branch policy is derived from the worktree. " +
			"Returns {output, handle} on success.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args delegateWorktreeCreateArgs) (*gosdk.CallToolResult, any, error) {
		if err := validateDelegateWorktreeCreate(args); err != nil {
			return errorResult(err), nil, nil
		}
		exe, err := os.Executable()
		if err != nil {
			return errorResult(fmt.Errorf("delegate_worktree_create: resolve executable: %w", err)), nil, nil
		}
		argv := []string{"sandbox", "create"}
		if args.ImageRef != "" {
			argv = append(argv, "--image", args.ImageRef)
		}
		argv = append(argv, "--mount", args.RepoPath+":/workspace")
		argv = append(argv, "--agent", "claude-code")
		argv = append(argv, "--egress", "open")
		if args.MemoryMiB > 0 {
			argv = append(argv, "--memory", fmt.Sprintf("%d", args.MemoryMiB))
		}
		if args.VCPUs > 0 {
			argv = append(argv, "--vcpus", fmt.Sprintf("%d", args.VCPUs))
		}
		argv = append(argv, args.Handle)
		var buf bytes.Buffer
		cmd := exec.CommandContext(ctx, exe, argv...)
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		if runErr := cmd.Run(); runErr != nil {
			return errorResult(fmt.Errorf("delegate_worktree_create: %w\n%s", runErr, buf.String())), nil, nil
		}
		return successResult(map[string]string{"output": buf.String(), "handle": args.Handle}), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "delegate_agent_dispatch",
		Description: "Deliver a task brief to the claude agent running inside a worktree sandbox " +
			"via `nexus3 herdr space-agent --autonomous --no-focus`. " +
			"The in-guest claude runs in auto permission mode (--permission-mode auto). " +
			"A fixed standing-orders preamble (isolated VM, full stack is the baseline, " +
			"unblock yourself, report friction) is prepended to the brief. " +
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
		exe, err := os.Executable()
		if err != nil {
			return errorResult(fmt.Errorf("delegate_teardown: resolve executable: %w", err)), nil, nil
		}
		var buf bytes.Buffer
		cmd := exec.CommandContext(ctx, exe, "sandbox", "rm", args.Ref)
		cmd.Stdout = &buf
		cmd.Stderr = &buf
		if runErr := cmd.Run(); runErr != nil {
			return errorResult(fmt.Errorf("delegate_teardown: %w\n%s", runErr, buf.String())), nil, nil
		}
		return successResult(map[string]interface{}{"removed": true, "output": buf.String()}), nil, nil
	})
}
