package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/herdragent"
	"github.com/IniZio/nexus/internal/herdrworktree"
	gosdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// standingOrders is prepended to every brief delegate_agent_dispatch delivers
// into the guest. It tells the in-guest agent what kind of environment it is
// in and what the baseline for evidence is — facts a cold agent cannot
// discover on its own. Keep it in sync with the canonical copy in
// plugins/claude/skills/nexus/references/delegate-briefs.md.
const standingOrders = `STANDING ORDERS (nexus sandbox)
These orders define the environment and the completion contract, not the method. If a workflow plugin or harness is loaded (e.g. groundwork), deliver the brief through its process.
You are running inside a dedicated, isolated nexus microVM created for this task.
This VM is yours: you are root, it has its own kernel, disk and network, and CPU and
memory grow automatically under load. Use it fully.
The baseline for evidence is the project's full local stack running (e.g.
` + "`docker compose up`" + `) and tests executed against it. A mock, a stub, or a partial
setup is not acceptable evidence.
Resolve blockers yourself — install tools, pull images (use a mirror if a registry
denies you), fix env files, and fix code that prevents the stack from running. Do not
stop at the first obstacle or ask the operator for what you can do yourself.
Record any friction you hit — what happened, the evidence, the workaround — in your
report so the platform can be fixed.
Egress is policy-gated. A 403 from the proxy names the policy that denied you:
report it, do not route around it.
Containers built or run by docker inside this VM already trust the sandbox TLS perimeter (CA at /etc/nexus/ca, SSL_CERT_FILE and friends pre-set); a 403 from a TLS-intercepted host is egress policy, not a certificate problem — report it, do not work around it.
The repo's .git common dir is the host's shared git dir: never run ` + "`git config`" + ` writes or unsets, hook installers, or ` + "`make setup`" + `-style commands that mutate .git/config or hooks.
Write scratch and output files under /workspace/.scratch/, not /tmp. Never commit /workspace/.scratch.
Completion: commit your work (push only if the branch has an upstream; with no remote, commit locally). If a loaded workflow has its own completion gate, pass it first. Then, as your final act, write a one-line summary to ` + delegateDoneMarker + ` (e.g. ` + "`" + `echo "all tests green, PR opened" > ` + delegateDoneMarker + "`" + `); the host treats that file as done.
After writing the marker, also log a best-effort completion event (never let it fail or delay you; <SANDBOX_ID> is the sandbox id given below): ` + "`" + `timeout 1 logger --rfc5424 --sd-id ce@32473 --sd-param specversion=\"1.0\" --sd-param id=\"done-$(date +%s)\" --sd-param source=\"nexus:///delegate\" --sd-param type=\"delegate.done\" --sd-param subject=\"sandbox:<SANDBOX_ID>\" '{}' 2>/dev/null || true` + "`" + `

`

// delegateSandboxID resolves ref to a sandbox ID, falling back to ref itself.
func delegateSandboxID(ctx context.Context, svc SandboxService, ref string) string {
	sbs, err := svc.List(ctx)
	if err != nil {
		return ref
	}
	for _, sb := range sbs {
		if id := sb.ID.String(); id == ref || sb.Handle() == ref || (len(ref) >= 4 && strings.HasPrefix(id, ref)) {
			return id
		}
	}
	return ref
}

// delegateDoneMarker is the path the in-guest agent writes as its final act to
// signal completion. It lives outside /workspace so it never dirties git status.
// The agent runs as root inside the microVM, so /run/nexus is writable after
// "mkdir -p /run/nexus". If /run is not writable in a future non-root setup,
// fall back to ${XDG_RUNTIME_DIR:-/tmp}/nexus-delegate-done instead.
const delegateDoneMarker = "/run/nexus/delegate-done"

// runHostCLI, runHerdrCLI and runGitCLI are package variables so tests can
// substitute fake executors; they default to the herdrworktree runners.
var (
	runHostCLI  = herdrworktree.RunHostCLI
	runHerdrCLI = herdrworktree.RunHerdrCLI
	runGitCLI   = herdrworktree.RunGitCLI
)

var teardownPollInterval = herdrworktree.DefaultPollInterval
var teardownPollTimeout = herdrworktree.DefaultPollTimeout

// agentClientOpts overrides herdragent.New options in tests (e.g. zero settle sleep).
var agentClientOpts []herdragent.Option

// WorktreeCreateArgs are the inputs of delegate_worktree_create and `nexus herdr worktree-create`.
type WorktreeCreateArgs = herdrworktree.CreateArgs

const briefFileName = herdrworktree.BriefFileName

func validateBriefPath(p string) error { return herdrworktree.ValidateBriefPath(p) }

func installBrief(ctx context.Context, briefPath, worktree string) error {
	return herdrworktree.InstallBrief(ctx, worktreeRunners(), briefPath, worktree)
}

func worktreePathForRef(ctx context.Context, ref string) (string, error) {
	return herdrworktree.PathForRef(ctx, worktreeRunners(), ref)
}

func resolveHerdrBin() (string, error) { return herdrworktree.ResolveHerdrBin() }

func parseHerdrListBindingByRef(out, ref string) (workspaceID, handle, sandboxID, paneID string, ok bool) {
	return herdrworktree.ParseListBindingByRef(out, ref)
}

type delegateAgentDispatchArgs struct {
	Ref       string `json:"ref"                  jsonschema:"sandbox reference: ID, ID prefix, or project/name handle (required)"`
	Brief     string `json:"brief,omitempty"      jsonschema:"task brief to deliver to the in-guest claude agent (required unless brief_path is set)"`
	BriefPath string `json:"brief_path,omitempty" jsonschema:"absolute host path to a brief file; copied into the worktree as .brief.md (excluded from commits) and the agent is told to read it (optional)"`
}

type delegateAgentWaitArgs struct {
	Ref      string `json:"ref"                 jsonschema:"sandbox reference: ID, ID prefix, or project/name handle (required)"`
	TimeoutS int    `json:"timeout_s,omitempty" jsonschema:"seconds to block before returning outcome=timeout (default 120, max 540)"`
}

type delegateAgentWaitResult struct {
	Outcome string `json:"outcome"`
	delegateAgentPollResult
}

type delegateAgentFollowupArgs struct {
	Ref  string `json:"ref"  jsonschema:"sandbox reference: ID, ID prefix, or project/name handle (required)"`
	Text string `json:"text" jsonschema:"message to type into the in-guest agent's pane and submit with Enter (required)"`
}

type delegateAgentPollArgs struct {
	Ref    string `json:"ref"               jsonschema:"sandbox reference: ID, ID prefix, or project/name handle (required)"`
	WaitMs int    `json:"wait_ms,omitempty"  jsonschema:"milliseconds to wait for the in-guest agent to reach a stable state via herdr (0 = instantaneous; max 600000)"`
}

type delegateAgentPollResult struct {
	GitLog           string `json:"git_log,omitempty"`
	GitStatus        string `json:"git_status,omitempty"`
	BranchName       string `json:"branch_name,omitempty"`
	DoneVia          string `json:"done_via,omitempty"`
	MarkerContent    string `json:"marker_content,omitempty"`
	AgentStatus      string `json:"agent_status,omitempty"`
	StateChangeSeq   uint64 `json:"state_change_seq,omitempty"`
	Settled          bool   `json:"settled"`
	Question         string `json:"question,omitempty"`
	AgentStateReason string `json:"agent_state_reason,omitempty"`
}

type delegateTeardownArgs struct {
	Ref   string `json:"ref"             jsonschema:"sandbox reference: ID, ID prefix, or project/name handle (required)"`
	Force bool   `json:"force,omitempty" jsonschema:"Remove the worktree even if it has uncommitted or untracked changes; those changes are discarded"`
}

const maxAgentWaitMs = 10 * 60 * 1000 // 10 minutes

const (
	defaultAgentWaitTimeout = 120 * time.Second
	maxAgentWaitTimeout     = 540 * time.Second
	followupEnterRetries    = 2
)

var (
	agentWaitInterval    = 3 * time.Second
	followupSettleDelay  = 400 * time.Millisecond
	followupConfirmDelay = 1500 * time.Millisecond
)

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func delegateMarkerWritten(ctx context.Context, svc SandboxService, ref string) bool {
	code, _, _, err := svc.Exec(ctx, ref, []string{"cat", delegateDoneMarker}, nil, "/", "")
	return err == nil && code == 0
}

// buildPollResult combines an observed agent state with the done marker and,
// when the marker is absent, the git heuristic.
func buildPollResult(ctx context.Context, svc SandboxService, ref string, agentSt herdragent.State) (delegateAgentPollResult, error) {
	res := delegateAgentPollResult{
		AgentStatus:      string(agentSt.Status),
		StateChangeSeq:   agentSt.Seq,
		Settled:          agentSt.Settled,
		Question:         agentSt.Question,
		AgentStateReason: agentSt.Reason,
	}
	markerCode, markerOut, _, markerExecErr := svc.Exec(ctx, ref, []string{"cat", delegateDoneMarker}, nil, "/", "")
	if markerExecErr == nil && markerCode == 0 {
		res.DoneVia = "marker"
		res.MarkerContent = strings.TrimSpace(markerOut)
		return res, nil
	}
	runGit := func(gitArgv []string) (string, error) {
		code, stdout, stderr, execErr := svc.Exec(ctx, ref, gitArgv, nil, "/workspace", "")
		if execErr != nil {
			return "", fmt.Errorf("exec %v: %w", gitArgv, execErr)
		}
		if code != 0 {
			return fmt.Sprintf("[exit %d] %s", code, stderr), nil
		}
		return stdout, nil
	}
	var err error
	if res.GitLog, err = runGit([]string{"git", "-C", "/workspace", "log", "--oneline", "-10"}); err != nil {
		return res, err
	}
	if res.GitStatus, err = runGit([]string{"git", "-C", "/workspace", "status", "--short"}); err != nil {
		return res, err
	}
	if res.BranchName, err = runGit([]string{"git", "-C", "/workspace", "branch", "--show-current"}); err != nil {
		return res, err
	}
	res.DoneVia = "git"
	return res, nil
}

// followupSnippet is the tail of text's last line, short enough to survive
// input-box wrapping, used to spot the text still sitting unsubmitted.
func followupSnippet(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	last := []rune(strings.TrimSpace(lines[len(lines)-1]))
	if len(last) > 20 {
		last = last[len(last)-20:]
	}
	return string(last)
}

func tailLines(screen string, n int) string {
	var kept []string
	for _, l := range strings.Split(screen, "\n") {
		if strings.TrimSpace(l) != "" {
			kept = append(kept, l)
		}
	}
	if len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	return strings.Join(kept, "\n")
}

// inputBoxHolds reports whether snippet is still within the last few lines of
// the pane, where the input box and footer sit. Submitted text scrolls up into
// history as the agent responds.
func inputBoxHolds(screen, snippet string) bool {
	return snippet != "" && strings.Contains(tailLines(screen, 6), snippet)
}

// observeAgentState returns the herdragent state for ref, or unknown with a reason on any failure.
func observeAgentState(ctx context.Context, ref string, waitMs int) herdragent.State {
	herdrBin, err := resolveHerdrBin()
	if err != nil {
		return herdragent.State{Status: herdragent.StatusUnknown, Reason: "herdr_unavailable"}
	}
	listOut, err := runHostCLI(ctx, "herdr", "list")
	if err != nil {
		return herdragent.State{Status: herdragent.StatusUnknown, Reason: "herdr_list_error"}
	}
	_, _, _, paneID, bound := parseHerdrListBindingByRef(listOut, ref)
	if !bound {
		return herdragent.State{Status: herdragent.StatusUnknown, Reason: "no_herdr_binding"}
	}
	if paneID == "" {
		return herdragent.State{Status: herdragent.StatusUnknown, Reason: "no_pane_id"}
	}
	runner := func(ctx context.Context, argv ...string) (string, error) {
		return runHerdrCLI(ctx, herdrBin, argv...)
	}
	client := herdragent.New(runner, agentClientOpts...)
	wait := time.Duration(0)
	if waitMs > 0 {
		capped := waitMs
		if capped > maxAgentWaitMs {
			capped = maxAgentWaitMs
		}
		wait = time.Duration(capped) * time.Millisecond
	}
	st := client.Observe(ctx, paneID, wait)
	if st.Agent == "nexus-slice-agent" {
		return herdragent.State{Status: herdragent.StatusUnknown, Reason: "reported_override"}
	}
	return st
}

// WorktreeSandboxResult is the outcome of CreateWorktreeSandbox.
type WorktreeSandboxResult = herdrworktree.SandboxResult

// WorktreeRunners are the herdr and nexus CLI executors CreateWorktreeSandbox drives.
type WorktreeRunners = herdrworktree.Runners

// DefaultWorktreeRunners returns runners backed by the real binaries.
func DefaultWorktreeRunners() WorktreeRunners { return herdrworktree.DefaultRunners() }

// worktreeRunners builds runners from the package-level CLI vars so tests can fake them.
func worktreeRunners() WorktreeRunners {
	return WorktreeRunners{
		Herdr: runHerdrCLI, Host: runHostCLI, Git: runGitCLI,
		PollInterval: teardownPollInterval, PollTimeout: teardownPollTimeout,
	}
}

// CreateWorktreeSandbox creates a herdr worktree for args.Branch and binds a nexus sandbox to it.
func CreateWorktreeSandbox(ctx context.Context, args WorktreeCreateArgs, r WorktreeRunners) (WorktreeSandboxResult, error) {
	if r.Git == nil {
		r.Git = runGitCLI
	}
	return herdrworktree.CreateSandbox(ctx, args, r)
}

func registerDelegateTools(srv *gosdk.Server, svc SandboxService) {
	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "delegate_worktree_create",
		Description: "Create a linked git worktree via herdr (`herdr worktree create --workspace <parent> --branch <branch>`), " +
			"then bind a nexus sandbox to it with the same `nexus herdr worktree-sandbox` path a herdr-created worktree gets: " +
			"image and egress from the checkout's .nexus/config.yaml (or .nexus/Containerfile), .git and .groundwork mounts, named volumes. " +
			"Optional brief_path (absolute host file) is copied into the worktree as .brief.md; .brief.md and .slice-report.md are appended to the repo's git info/exclude " +
			"(git has no per-worktree exclude, so the shared common-dir file is used). " +
			"Requires herdr running and repo_path open as a herdr workspace. " +
			"image_ref, memory_mib and vcpus are rejected (the worktree-sandbox path has no flags for them); " +
			"allowed_branches MUST NOT be set — branch policy is derived from the worktree. " +
			"Returns {workspace_id, worktree_path, branch, handle, sandbox_id, output} on success.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args WorktreeCreateArgs) (*gosdk.CallToolResult, any, error) {
		result, err := CreateWorktreeSandbox(ctx, args, worktreeRunners())
		if err != nil {
			return errorResult(err), nil, nil
		}
		return successResult(result), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "delegate_agent_dispatch",
		Description: "Deliver a task brief to the claude agent running inside a worktree sandbox " +
			"via `nexus herdr space-agent --autonomous --no-focus`. " +
			"The in-guest claude runs with permissions skipped (--permission-mode bypassPermissions); the microVM is the isolation boundary. " +
			"Optional brief_path (absolute host file) is installed as .brief.md in the worktree (excluded from commits) and the agent is told to read it; brief then becomes optional extra text. " +
			"Returns {delivered, output}: delivered=true iff herdr agent exits 0 (brief accepted); " +
			"delivered=false with output on non-zero exit (not IsError — caller decides how to react).",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args delegateAgentDispatchArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return errorResult(fmt.Errorf("ref is required")), nil, nil
		}
		if args.Brief == "" && args.BriefPath == "" {
			return errorResult(fmt.Errorf("brief or brief_path is required")), nil, nil
		}
		if err := validateBriefPath(args.BriefPath); err != nil {
			return errorResult(fmt.Errorf("delegate_agent_dispatch: %w", err)), nil, nil
		}
		if args.BriefPath != "" {
			wt, err := worktreePathForRef(ctx, args.Ref)
			if err != nil {
				return errorResult(fmt.Errorf("delegate_agent_dispatch: brief_path: %w", err)), nil, nil
			}
			if err := installBrief(ctx, args.BriefPath, wt); err != nil {
				return errorResult(fmt.Errorf("delegate_agent_dispatch: brief_path: %w", err)), nil, nil
			}
		}
		_, _, _, _ = svc.Exec(ctx, args.Ref, []string{"rm", "-f", delegateDoneMarker}, nil, "/", "")
		brief := standingOrders + "Sandbox id: " + delegateSandboxID(ctx, svc, args.Ref) + "\n\n"
		if args.BriefPath != "" {
			brief += "Your task brief is in /workspace/" + briefFileName + " — read it first and follow it.\n\n"
		}
		brief += args.Brief
		out, runErr := runHostCLI(ctx, "herdr", "agent", "--autonomous", "--no-focus", args.Ref, brief)
		delivered := runErr == nil
		const maxOut = 4000
		if len(out) > maxOut {
			out = "...(truncated)\n" + out[len(out)-maxOut:]
		}
		return successResult(map[string]any{"delivered": delivered, "output": out}), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "delegate_agent_poll",
		Description: "Poll the in-guest agent's progress. Reports herdr native agent state " +
			"(agent_status: idle|working|blocked|done|unknown; state_change_seq; settled; " +
			"question when blocked; agent_state_reason when unknown). " +
			"Checks " + delegateDoneMarker + " first (done_via:marker); falls back to git log/status heuristic (done_via:git). " +
			"Marker and git are the completion proof; herdr agent state is informational only. " +
			"herdr unavailable or sandbox unbound → agent_status:unknown, marker/git unchanged. " +
			"Optional wait_ms>0 passes a bounded herdr agent wait before sampling. " +
			"Returns {git_log, git_status, branch_name, done_via, marker_content, agent_status, state_change_seq, settled, question, agent_state_reason}.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args delegateAgentPollArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return errorResult(fmt.Errorf("ref is required")), nil, nil
		}
		agentSt := observeAgentState(ctx, args.Ref, args.WaitMs)
		res, err := buildPollResult(ctx, svc, args.Ref, agentSt)
		if err != nil {
			return errorResult(err), nil, nil
		}
		return successResult(res), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "delegate_agent_wait",
		Description: "Block until the in-guest agent finishes or needs attention, replacing host poll loops. " +
			"Returns {outcome, ...delegate_agent_poll fields} with outcome one of: " +
			"done (" + delegateDoneMarker + " written; marker_content set), " +
			"blocked (permission dialog or question pending; question holds its text), " +
			"idle_without_marker (agent idle/done on two consecutive samples without the marker — finished without signalling, or stopped), " +
			"timeout (timeout_s elapsed; default 120, max 540). " +
			"Uses the same herdr state observation and marker check as delegate_agent_poll.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args delegateAgentWaitArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return errorResult(fmt.Errorf("ref is required")), nil, nil
		}
		timeout := time.Duration(args.TimeoutS) * time.Second
		if args.TimeoutS <= 0 {
			timeout = defaultAgentWaitTimeout
		}
		if timeout > maxAgentWaitTimeout {
			timeout = maxAgentWaitTimeout
		}
		deadline := time.Now().Add(timeout)
		var prevIdleSeq uint64
		idleSamples := 0
		for {
			agentSt := observeAgentState(ctx, args.Ref, 0)
			outcome := ""
			switch {
			case delegateMarkerWritten(ctx, svc, args.Ref):
				outcome = "done"
			case agentSt.Status == herdragent.StatusBlocked && agentSt.Settled:
				outcome = "blocked"
			case (agentSt.Status == herdragent.StatusIdle || agentSt.Status == herdragent.StatusDone) && agentSt.Settled:
				if idleSamples > 0 && agentSt.Seq == prevIdleSeq {
					idleSamples++
				} else {
					idleSamples = 1
				}
				prevIdleSeq = agentSt.Seq
				if idleSamples >= 2 {
					outcome = "idle_without_marker"
				}
			default:
				idleSamples = 0
			}
			if outcome == "" && !time.Now().Before(deadline) {
				outcome = "timeout"
			}
			if outcome != "" {
				res, err := buildPollResult(ctx, svc, args.Ref, agentSt)
				if err != nil {
					return errorResult(err), nil, nil
				}
				return successResult(delegateAgentWaitResult{Outcome: outcome, delegateAgentPollResult: res}), nil, nil
			}
			select {
			case <-ctx.Done():
				return errorResult(fmt.Errorf("delegate_agent_wait: %w", ctx.Err())), nil, nil
			case <-time.After(agentWaitInterval):
			}
		}
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "delegate_agent_followup",
		Description: "Send a follow-up message to the in-guest agent: types text into the bound herdr pane, " +
			"submits with Enter (capital E; lowercase `enter` does not submit), then confirms the pane shows the agent working " +
			"or the text left the input area, re-pressing Enter up to " + fmt.Sprint(followupEnterRetries) + " times. " +
			"Returns {submitted, agent_status, output}; submitted=false (not IsError) when submission could not be confirmed.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args delegateAgentFollowupArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return errorResult(fmt.Errorf("ref is required")), nil, nil
		}
		if strings.TrimSpace(args.Text) == "" {
			return errorResult(fmt.Errorf("text is required")), nil, nil
		}
		herdrBin, err := resolveHerdrBin()
		if err != nil {
			return errorResult(fmt.Errorf("delegate_agent_followup: %w", err)), nil, nil
		}
		listOut, err := runHostCLI(ctx, "herdr", "list")
		if err != nil {
			return errorResult(fmt.Errorf("delegate_agent_followup: nexus herdr list: %w\n%s", err, listOut)), nil, nil
		}
		_, _, _, paneID, bound := parseHerdrListBindingByRef(listOut, args.Ref)
		if !bound || paneID == "" {
			return errorResult(fmt.Errorf("delegate_agent_followup: no herdr pane bound to %q", args.Ref)), nil, nil
		}
		if out, err := runHerdrCLI(ctx, herdrBin, "pane", "send-text", paneID, args.Text); err != nil {
			return errorResult(fmt.Errorf("delegate_agent_followup: herdr pane send-text: %w\n%s", err, out)), nil, nil
		}
		if err := sleepCtx(ctx, followupSettleDelay); err != nil {
			return errorResult(err), nil, nil
		}
		snippet := followupSnippet(args.Text)
		for attempt := 0; attempt <= followupEnterRetries; attempt++ {
			if out, err := runHerdrCLI(ctx, herdrBin, "pane", "send-keys", paneID, "Enter"); err != nil {
				return errorResult(fmt.Errorf("delegate_agent_followup: herdr pane send-keys Enter: %w\n%s", err, out)), nil, nil
			}
			if err := sleepCtx(ctx, followupConfirmDelay); err != nil {
				return errorResult(err), nil, nil
			}
			st := observeAgentState(ctx, args.Ref, 0)
			screen, _ := runHerdrCLI(ctx, herdrBin, "pane", "read", paneID, "--source", "recent-unwrapped", "--lines", "40")
			if st.Status == herdragent.StatusWorking || !inputBoxHolds(screen, snippet) {
				return successResult(map[string]any{"submitted": true, "agent_status": string(st.Status)}), nil, nil
			}
			if attempt == followupEnterRetries {
				return successResult(map[string]any{
					"submitted":    false,
					"agent_status": string(st.Status),
					"output":       "text still in the input box after Enter; pane tail:\n" + tailLines(screen, 8),
				}), nil, nil
			}
		}
		return errorResult(fmt.Errorf("delegate_agent_followup: unreachable")), nil, nil
	})

	gosdk.AddTool(srv, &gosdk.Tool{
		Name: "delegate_teardown",
		Description: "Reverse delegate_worktree_create: resolve the herdr workspace bound to the sandbox (`nexus herdr list`), " +
			"run `herdr worktree remove --workspace <ws>` (closes the workspace, removes the git worktree, and reaps the sandbox via the worktree.removed hook), " +
			"then verify the sandbox is gone. Falls back to `nexus sandbox rm <ref>` only when no workspace is bound or the sandbox is still listed afterwards. " +
			"When the worktree has uncommitted or untracked changes the remove fails with a structured error listing them; " +
			"pass force:true to discard those changes and remove anyway. " +
			"Returns {removed, workspace_id, handle, sandbox_id, output}.",
	}, func(ctx context.Context, _ *gosdk.CallToolRequest, args delegateTeardownArgs) (*gosdk.CallToolResult, any, error) {
		if args.Ref == "" {
			return errorResult(fmt.Errorf("ref is required")), nil, nil
		}
		res, err := herdrworktree.Teardown(ctx, args.Ref, args.Force, worktreeRunners())
		if err != nil {
			var dirty *herdrworktree.DirtyWorktreeError
			if errors.As(err, &dirty) {
				more := ""
				if dirty.More > 0 {
					more = fmt.Sprintf("\n... and %d more", dirty.More)
				}
				return errorResult(fmt.Errorf("worktree %s has uncommitted changes (%d files):\n%s%s\nHarvest or commit them first, or pass force:true to discard them.",
					dirty.Path, dirty.Count, strings.Join(dirty.Files, "\n"), more)), nil, nil
			}
			return errorResult(err), nil, nil
		}
		if !res.Bound {
			return successResult(map[string]interface{}{"removed": true, "output": res.Output}), nil, nil
		}
		return successResult(map[string]interface{}{
			"removed":      true,
			"how":          res.How,
			"workspace_id": res.WorkspaceID,
			"handle":       res.Handle,
			"sandbox_id":   res.SandboxID,
			"output":       res.Output,
		}), nil, nil
	})
}
