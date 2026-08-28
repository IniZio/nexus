package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/config"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

func init() {
	// __herdr-plugin is the deprecated alias kept for backward compatibility with
	// installed herdr plugin builds that were compiled before the `herdr` group
	// existed. Hidden so it does not appear in the usage banner; still fully
	// runnable for any older installed shim that invokes it directly.
	Register(Command{
		Name:    "__herdr-plugin",
		Summary: "Deprecated alias for `herdr` (kept for installed plugin backward compat)",
		Hidden:  true,
		Run:     runHerdrPlugin,
	})
	// herdr is the primary command group for herdr-plugin operations. Hidden
	// because it is a plugin-private surface, not a public CLI command — it must
	// not appear in `nexus3 --help` alongside sandbox lifecycle verbs.
	Register(Command{
		Name:    "herdr",
		Summary: "herdr plugin operations (attach, create, list, agent, …)",
		Hidden:  true,
		Run:     runHerdrGroup,
	})
}

// herdrPluginABIVersion is the integer probed by build.sh to detect skew
// between the installed plugin manifest and the nexus3 binary. Bump this
// whenever the herdr subcommand surface changes in an incompatible way.
const herdrPluginABIVersion = "2"

// herdrGroupVerbToPluginSub maps a herdr-group verb (the public CLI sub) to
// the internal pluginSub name used by runHerdrPlugin. Returns ("", false) for
// unknown verbs. "default-shell" and "install-default-shell" are handled before
// plugin routing in runHerdrGroup (self-contained verbs that bypass the plugin
// machinery entirely); they appear in this switch only as the complete inventory.
//
// The usage string in runHerdrGroup is a hand-maintained duplicate of this set;
// TestHerdrGroupUsageString_containsAllPluginVerbs asserts that key verbs appear in both.
func herdrGroupVerbToPluginSub(sub string) (pluginSub string, known bool) {
	switch sub {
	// Self-contained verbs handled before plugin routing.
	case "default-shell", "install-default-shell":
		return sub, true
	// Non-space verbs: keep name unchanged.
	case "abi", "context-cwd", "workspaces", "attach", "create", "logs", "doctor",
		"open-pane", "launch", "shell-cwd", "new-tab":
		return sub, true
	// space-* verbs that dropped their prefix (no collision).
	case "create-from-file":
		return "space-create-from-file", true
	case "pause":
		return "space-pause", true
	case "resume":
		return "space-resume", true
	case "remove":
		return "space-remove", true
	case "list":
		return "space-list", true
	case "prune":
		return "space-prune", true
	case "agent":
		return "space-agent", true
	case "agent-from-file":
		return "space-agent-from-file", true
	// space-* verbs that KEEP their prefix because the bare name is already
	// taken by a non-space verb with different behaviour:
	//   `herdr create`          → __herdr-plugin create (sandbox create)
	//   `herdr space-create`    → __herdr-plugin space-create (herdr workspace create)
	//   `herdr open-pane`       → __herdr-plugin open-pane (raw pane open)
	//   `herdr space-open-pane` → __herdr-plugin space-open-pane (space pane open)
	case "space-create", "space-open-pane":
		return sub, true
	case "worktree-sandbox":
		return sub, true
	case "backfill-repo-root":
		return "backfill-repo-root", true
	default:
		return "", false
	}
}

// runHerdrGroup dispatches `nexus3 herdr <subcommand> [args...]`.
//
// Verb mapping from the deprecated __herdr-plugin surface:
//   - space-* prefix dropped where unambiguous (e.g. space-pause → pause)
//   - space-create and space-open-pane KEEP their space- prefix to avoid
//     collision with the non-space `create` and `open-pane` verbs, which
//     perform distinct operations (sandbox create and raw pane open respectively).
//
// All cases delegate to runHerdrPlugin so behaviour is unchanged.
func runHerdrGroup(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: "herdr: subcommand required (abi|context-cwd|workspaces|attach|create|logs|doctor|open-pane|launch|shell-cwd|new-tab|space-create|space-open-pane|create-from-file|pause|resume|remove|list|prune|agent|agent-from-file|default-shell|install-default-shell|worktree-sandbox|backfill-repo-root)"}
	}
	sub := args[0]
	rest := args[1:]

	// default-shell and install-default-shell are self-contained verbs handled
	// directly here; they do not route through the __herdr-plugin machinery.
	switch sub {
	case "default-shell":
		return runHerdrDefaultShell(ctx, rest, out)
	case "install-default-shell":
		return runHerdrInstallDefaultShell(ctx, rest, out)
	}

	// Map herdr-group verbs to the internal runHerdrPlugin dispatch names.
	pluginSub, known := herdrGroupVerbToPluginSub(sub)
	if !known {
		exe, _ := os.Executable()
		return &UsageError{Msg: fmt.Sprintf(
			"herdr: unknown subcommand %q\n\n"+
				"The binary is likely stale. Executed: %s\n"+
				"Rebuild: go build -o nexus3 ./cmd/nexus3 && nexus3 herdr install-default-shell",
			sub, exe)}
	}
	return runHerdrPlugin(ctx, append([]string{pluginSub}, rest...), out)
}

// runHerdrPlugin dispatches __herdr-plugin <subcommand> [args...].
//
// This is a private surface between nexus3 and its own in-repo herdr plugin
// (plugins/herdr/). It is NOT part of the versioned --json machine contract
// and carries no --json guarantees. Prefer the `herdr` group for new callers.
func runHerdrPlugin(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: "__herdr-plugin: subcommand required (abi|context-cwd|workspaces|attach|create|logs|doctor|open-pane|launch|new-tab|space-create|space-create-from-file|space-open-pane|space-pause|space-resume|space-remove|space-list|space-prune|shell-cwd|space-agent|space-agent-from-file)"}
	}
	sub := args[0]
	rest := args[1:]

	switch sub {
	case "abi":
		fmt.Fprintln(out.w, herdrPluginABIVersion)
		return nil

	case "context-cwd":
		return herdrPluginContextCwd(out.w)

	case "workspaces":
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin workspaces: " + err.Error(), Err: err}
		}
		return herdrPluginWorkspaces(ctx, out.w, svc)

	case "attach":
		ref := os.Getenv("NEXUS3_WORKSPACE")
		if ref == "" && len(rest) > 0 {
			ref = rest[0]
		}
		if ref == "" {
			return &UsageError{Msg: "__herdr-plugin attach: sandbox ref required (set NEXUS3_WORKSPACE or pass as argument)"}
		}
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin attach: " + err.Error(), Err: err}
		}
		return herdrPluginAttach(ctx, ref, out, svc)

	case "create":
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin create: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin create: resolve store: " + err.Error(), Err: err}
		}
		return herdrPluginCreate(ctx, os.Stdin, out.w, svc, storeRoot)

	case "logs":
		return runLog(ctx, rest, out)

	case "doctor":
		return herdrPluginDoctor(out.w)

	case "open-pane":
		if len(rest) == 0 {
			return &UsageError{Msg: "__herdr-plugin open-pane: workspace ref required"}
		}
		return herdrPluginOpenPane(rest[0], rest[1:])

	case "launch":
		agentEgress := false
		if len(rest) > 0 && rest[0] == "--agent-egress" {
			agentEgress = true
			rest = rest[1:]
		}
		if len(rest) < 2 {
			return &UsageError{Msg: "__herdr-plugin launch: usage: launch [--agent-egress] <image-ref> <command> [args...] (command must be an absolute path, e.g. /usr/local/bin/claude)"}
		}
		return herdrPluginLaunch(ctx, rest[0], rest[1:], agentEgress, out)

	case "space-create":
		// --no-focus suppresses pane focus, so binding a workspace does not
		// yank the operator away from whatever they are working in. Binding
		// several sandboxes in a row would otherwise steal focus once per
		// sandbox. Focus remains the default: an operator running this
		// interactively for a single sandbox expects to land in it.
		focus := true
		for len(rest) > 0 && rest[0] == "--no-focus" {
			focus = false
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return &UsageError{Msg: "__herdr-plugin space-create: usage: space-create [--no-focus] <sandbox-ref>"}
		}
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-create: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-create: resolve store: " + err.Error(), Err: err}
		}
		return herdrPluginSpaceCreate(ctx, rest[0], out.w, svc, storeRoot, focus)

	case "space-create-from-file":
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-create-from-file: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-create-from-file: resolve store: " + err.Error(), Err: err}
		}
		return herdrPluginSpaceCreateFromFile(ctx, os.Stdin, out.w, svc, storeRoot)

	case "space-open-pane":
		if len(rest) == 0 {
			return &UsageError{Msg: "__herdr-plugin space-open-pane: sandbox ref or space label required"}
		}
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-open-pane: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-open-pane: resolve store: " + err.Error(), Err: err}
		}
		return herdrPluginSpaceOpenPane(ctx, rest[0], storeRoot, svc, out.w)

	case "space-pause":
		if len(rest) == 0 {
			return &UsageError{Msg: "__herdr-plugin space-pause: sandbox ref or space label required"}
		}
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-pause: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-pause: resolve store: " + err.Error(), Err: err}
		}
		b, adopted, err := herdrSpaceResolveOrAdopt(ctx, svc, storeRoot, rest[0])
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-pause: " + err.Error(), Err: err}
		}
		if adopted {
			herdrAdoptNotice(b)
		}
		// Act on the already-resolved binding directly — no re-lookup by label.
		if _, err := svc.Pause(ctx, b.SandboxHandle); err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-pause: " + err.Error(), Err: err}
		}
		return nil

	case "space-resume":
		if len(rest) == 0 {
			return &UsageError{Msg: "__herdr-plugin space-resume: sandbox ref or space label required"}
		}
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-resume: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-resume: resolve store: " + err.Error(), Err: err}
		}
		b, adopted, err := herdrSpaceResolveOrAdopt(ctx, svc, storeRoot, rest[0])
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-resume: " + err.Error(), Err: err}
		}
		if adopted {
			herdrAdoptNotice(b)
		}
		// Act on the already-resolved binding directly — no re-lookup by label.
		if _, err := svc.Resume(ctx, b.SandboxHandle); err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-resume: " + err.Error(), Err: err}
		}
		return nil

	case "space-remove":
		if len(rest) == 0 {
			return &UsageError{Msg: "__herdr-plugin space-remove: sandbox ref or space label required"}
		}
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-remove: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-remove: resolve store: " + err.Error(), Err: err}
		}
		b, adopted, err := herdrSpaceResolveOrAdopt(ctx, svc, storeRoot, rest[0])
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-remove: " + err.Error(), Err: err}
		}
		if adopted {
			herdrAdoptNotice(b)
		}
		herdrBin, herdrBinErr := resolveHerdrBin()
		if herdrBinErr != nil {
			// herdr unavailable: workspaceClose will return herdrBinErr, causing
			// teardown to retain the binding for space-prune recovery.
			slog.Warn("__herdr-plugin space-remove: herdr not found; binding retained if workspace close fails", "err", herdrBinErr)
			herdrBin = ""
		}
		deps := txnDeps{
			workspaceClose: func(ctx context.Context, wsID string) error {
				if herdrBinErr != nil {
					return herdrBinErr
				}
				return herdrWorkspaceClose(ctx, herdrBin, wsID)
			},
			bindingDelete: func(ctx context.Context, label string) error {
				return HerdrSpaceDelete(ctx, storeRoot, label)
			},
			svcRemove: func(ctx context.Context, ref string) error {
				return svc.Remove(ctx, ref)
			},
		}
		return herdrSpaceTeardown(ctx, storeRoot, b.SandboxHandle, deps, teardownOpts{failOpen: false})

	case "shell-cwd":
		// Prints the guest working directory for a sandbox's workspace.
		// Used by pane.sh to pass --cwd to nexus3 exec for the shell pane.
		// Falls back to /root when no workspace is mounted.
		if len(rest) == 0 {
			return &UsageError{Msg: "__herdr-plugin shell-cwd: sandbox ref required"}
		}
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin shell-cwd: " + err.Error(), Err: err}
		}
		cwd := herdrShellCwd(ctx, rest[0], svc)
		fmt.Fprintln(out.w, cwd)
		return nil

	case "space-list":
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-list: resolve store: " + err.Error(), Err: err}
		}
		return herdrPluginSpaceList(ctx, out.w, storeRoot)

	case "space-prune":
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-prune: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-prune: resolve store: " + err.Error(), Err: err}
		}
		herdrBin, _ := resolveHerdrBin()
		return herdrPluginSpacePrune(ctx, rest, out.w, svc, storeRoot, herdrBin)

	case "space-agent":
		// Usage: space-agent [--autonomous] [--no-focus] <sandbox-ref> <brief>
		// Starts the sandbox, creates a herdr space (or reuses one), then launches
		// claude in the guest shell pane and delivers the brief.
		// --no-focus suppresses pane focus, allowing N concurrent runs without
		// each one stealing the operator's view from the previous.
		autonomous := false
		focus := true
		for len(rest) > 0 && strings.HasPrefix(rest[0], "--") {
			switch rest[0] {
			case "--autonomous":
				autonomous = true
			case "--no-focus":
				focus = false
			default:
				return &UsageError{Msg: "__herdr-plugin space-agent: unknown flag: " + rest[0]}
			}
			rest = rest[1:]
		}
		if len(rest) < 2 {
			return &UsageError{Msg: "__herdr-plugin space-agent: usage: space-agent [--autonomous] [--no-focus] <sandbox-ref> <brief>"}
		}
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-agent: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-agent: resolve store: " + err.Error(), Err: err}
		}
		ref := rest[0]
		brief := strings.Join(rest[1:], " ")
		return herdrPluginSpaceAgent(ctx, ref, brief, autonomous, focus, out.w, svc, storeRoot)

	case "space-agent-from-file":
		// Interactive stdin-based variant: prompts for sandbox ref and brief.
		// Invoked by pane.sh's space-agent case.
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-agent-from-file: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-agent-from-file: resolve store: " + err.Error(), Err: err}
		}
		return herdrPluginSpaceAgentFromFile(ctx, os.Stdin, out.w, svc, storeRoot)

	case "new-tab":
		if len(rest) == 0 {
			return &UsageError{Msg: "__herdr-plugin new-tab: herdr workspace ID required"}
		}
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin new-tab: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin new-tab: resolve store: " + err.Error(), Err: err}
		}
		return herdrPluginNewTab(ctx, rest[0], storeRoot, svc, out.w)

	case "worktree-sandbox":
		if len(rest) == 0 {
			return &UsageError{Msg: "__herdr-plugin worktree-sandbox: herdr workspace ID required"}
		}
		// Parse --auto / --conditional flags BEFORE the positional workspace ID.
		// --auto activates the repo-level predicate (c); --conditional activates
		// the legacy SourceWorkspaceID predicate. The two flags are distinct.
		rest, conditional, auto := herdrWorktreeSandboxParseArgs(rest)
		if len(rest) == 0 {
			return &UsageError{Msg: "__herdr-plugin worktree-sandbox: herdr workspace ID required"}
		}
		workspaceID := rest[0]
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin worktree-sandbox: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin worktree-sandbox: resolve store: " + err.Error(), Err: err}
		}
		exe, exeErr := os.Executable()
		if exeErr != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin worktree-sandbox: resolve executable: " + exeErr.Error(), Err: exeErr}
		}
		createFn := func(ctx context.Context, handle, mountSpec, imageFlag, imageVal string, extraMounts, secrets []string, allowedRepo string, pathPolicies domain.EgressPathPolicies) error {
			args := herdrWorktreeSandboxCreateArgs(handle, mountSpec, imageFlag, imageVal, extraMounts, secrets, allowedRepo, pathPolicies)
			cmd := herdrExecCommandContext(ctx, exe, append([]string{"sandbox", "create"}, args...)...)
			cmd.Stdout = out.w
			cmd.Stderr = out.w
			return cmd.Run()
		}
		getFn := func(ctx context.Context, handle string) (domain.Sandbox, error) {
			return svc.Get(ctx, handle)
		}
		return herdrWorktreeSandbox(ctx, workspaceID, out.w, storeRoot, true, conditional, auto, createFn, getFn)

	case "backfill-repo-root":
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin backfill-repo-root: resolve store: " + err.Error(), Err: err}
		}
		return herdrPluginBackfillRepoRoot(ctx, storeRoot, out.w)

	default:
		exe, _ := os.Executable()
		return &UsageError{Msg: fmt.Sprintf(
			"__herdr-plugin: unknown subcommand %q\n\n"+
				"The binary is likely stale. Executed: %s\n"+
				"Rebuild: go build -o nexus3 ./cmd/nexus3 && nexus3 herdr install-default-shell",
			sub, exe)}
	}
}

// herdrPluginContextCwd reads HERDR_PLUGIN_CONTEXT_JSON and prints workspace_cwd.
// Called by build.sh as a smoke test during plugin installation.
func herdrPluginContextCwd(w io.Writer) error {
	raw := os.Getenv("HERDR_PLUGIN_CONTEXT_JSON")
	if raw == "" {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin context-cwd: HERDR_PLUGIN_CONTEXT_JSON not set"}
	}
	var obj struct {
		WorkspaceCwd string `json:"workspace_cwd"`
	}
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin context-cwd: parse HERDR_PLUGIN_CONTEXT_JSON: " + err.Error(), Err: err}
	}
	fmt.Fprintln(w, obj.WorkspaceCwd)
	return nil
}

// herdrPluginWorkspaces renders the workspaces overlay: one aligned row per
// sandbox, whatever created it.
//
// svc.List is deliberately unfiltered, so a sandbox made with `nexus3 sandbox
// create` shows up here exactly like one herdr created. Handle and state alone
// were not enough to act on: the operator could see a row but not tell whether
// it was bound to a herdr space, whether it carried a mount worth attaching
// to, or which agent it was running. Every column below answers a question the
// operator would otherwise have to drop to a terminal to answer.
//
// Failure to read the bindings file is NOT fatal — the overlay degrades to
// showing "-" in the SPACE column rather than refusing to render, because an
// overlay that shows nothing is worse than one missing a column.
func herdrPluginWorkspaces(ctx context.Context, w io.Writer, svc *service.Service) error {
	sandboxes, err := svc.List(ctx)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin workspaces: " + err.Error(), Err: err}
	}

	bound := map[string]bool{}
	if storeRoot, rErr := store.DefaultRoot(); rErr == nil {
		if bindings, bErr := HerdrSpaceList(ctx, storeRoot); bErr == nil {
			for _, b := range bindings {
				bound[b.SandboxHandle] = true
			}
		}
	}

	rows := make([][]string, 0, len(sandboxes))
	for _, sb := range sandboxes {
		space := "-"
		if bound[sb.Handle()] {
			space = "bound"
		}
		rows = append(rows, []string{
			sb.Handle(),
			sb.State.String(),
			herdrWorkspaceAgent(sb),
			herdrWorkspaceMounts(sb),
			space,
			sb.ID.String(),
		})
	}

	fmt.Fprint(w, renderTable(workspaceTableHeaders[:], rows))
	if len(rows) == 0 {
		fmt.Fprintln(w, "(no sandboxes — use `nexus3: create sandbox space` to make one)")
	}
	return nil
}

// workspaceTableHeaders is the overlay's column set.
var workspaceTableHeaders = [6]string{"WORKSPACE", "STATE", "AGENT", "MOUNTS", "SPACE", "ID"}

// herdrWorkspaceAgent names the agent profile a sandbox was created for, or
// "-" for a plain sandbox with no agent.
func herdrWorkspaceAgent(sb domain.Sandbox) string {
	if sb.AgentName == "" {
		return "-"
	}
	return sb.AgentName
}

// herdrWorkspaceMounts summarises what the sandbox has attached, because that
// is what decides whether a guest shell lands somewhere useful. Live host
// mounts are listed first since they are the ones with a host path the
// operator recognises.
func herdrWorkspaceMounts(sb domain.Sandbox) string {
	var parts []string
	for _, m := range sb.LiveMounts {
		parts = append(parts, m.GuestPath)
	}
	for _, v := range sb.MountedVolumes {
		parts = append(parts, v.Name+"→"+v.GuestPath)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ",")
}

// herdrPluginAttach runs an interactive PTY attach session.
//
// Env sealing: HERDR_* vars must never reach the guest VM. The service layer
// constructs the guest environment independently from an explicit allow-list
// (it does not read os.Environ()). We additionally strip HERDR_* from the
// current process environment here so that any subprocess forks within this
// __herdr-plugin invocation cannot inadvertently leak herdr credentials.
//
// Agent reporter calls are best-effort: failures are logged to stderr but
// do not abort the attach.
func herdrPluginAttach(ctx context.Context, ref string, out *Output, svc *service.Service) error {
	// Strip HERDR_* from process env — guest must never see herdr socket paths.
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HERDR_") {
			key, _, _ := strings.Cut(kv, "=")
			os.Unsetenv(key)
		}
	}

	herdrBin, _ := resolveHerdrBin() // best-effort: attach works without a reporter
	herdrPane := os.Getenv("HERDR_PANE_ID")
	haveReporter := herdrBin != "" && herdrPane != ""

	if haveReporter {
		// report-agent before the PTY loop begins.
		_ = runHerdrCmd(herdrBin, "pane", "report-agent", herdrPane,
			"--source", "nexus3", "--state", "working", "--seq", "1")
	}

	err := runAttachWithSvc(ctx, ref, "", 0, out, svc)

	if haveReporter {
		_ = runHerdrCmd(herdrBin, "pane", "release-agent", herdrPane,
			"--source", "nexus3")
	}

	return err
}

// runHerdrCmd runs a herdr CLI command and logs any error to stderr (best-effort).
func runHerdrCmd(bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "__herdr-plugin: herdr reporter: %v\n", err)
		return err
	}
	return nil
}

// herdrPrintImages lists cached images so the create prompt has visible
// choices. Best-effort: a failure to list is not a reason to refuse to create,
// so it degrades to a short notice and the prompt still runs.
func herdrPrintImages(ctx context.Context, w io.Writer) {
	isvc, err := newImageService()
	if err != nil {
		fmt.Fprintf(w, "(could not list images: %v)\n", err)
		return
	}
	imgs, err := isvc.ListImages(ctx)
	if err != nil {
		fmt.Fprintf(w, "(could not list images: %v)\n", err)
		return
	}
	rows := make([][]string, 0, len(imgs))
	for _, img := range imgs {
		r := toImageInfoJSON(img)
		if r.Kind != "base" {
			continue // builder artifacts are not bootable sandbox images
		}
		rows = append(rows, []string{r.Ref, r.Digest, r.CreatedAt.UTC().Format("2006-01-02 15:04")})
	}
	if len(rows) == 0 {
		fmt.Fprintln(w, "no base images cached — build one with: nexus3 image build")
		return
	}
	fmt.Fprintln(w, "available base images:")
	fmt.Fprint(w, renderTable([]string{"REF", "DIGEST", "CREATED"}, rows))
	fmt.Fprintln(w, "(paste a DIGEST if a REF appears more than once)")
	fmt.Fprintln(w)
}

// herdrRepoFlags prompts for the GitHub repo the sandbox may reach and returns
// the corresponding `sandbox create` flags.
//
// D-PD-36 refuses to create a sandbox that would carry an unbounded GitHub
// credential: the caller must scope it to one repo (--repo owner/name) or
// pass no GitHub flags at all. Asking makes the choice visible at the only
// point where the answer is known.
//
// Declining is the default (blank answer → no flags): a sandbox with no
// GitHub token is the safe posture, and the operator can create a scoped one
// deliberately. When a repo IS given the secret is auto-wired via
// --secret GH_TOKEN@… because the operator explicitly named a repo.
func herdrRepoFlags(scanner *bufio.Scanner) ([]string, error) {
	fmt.Fprint(os.Stderr, "github repo to allow (owner/name, blank for no GitHub access): ")
	if !scanner.Scan() {
		return nil, &CodedError{Code: ErrCodeInternalError, Msg: "failed to read github repo"}
	}
	repo := strings.TrimSpace(scanner.Text())
	if repo == "" {
		return []string{}, nil
	}
	// Reject anything that is not owner/name before spending a build on it.
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, &UsageError{Msg: fmt.Sprintf("invalid repo %q: expected owner/name", repo)}
	}
	return []string{"--repo", repo, "--secret", "GH_TOKEN@github.com,api.github.com,uploads.github.com"}, nil
}

// herdrDefaultImage is the image offered by default in the herdr create
// prompt. It matches the default used by the orca remote path.
const herdrDefaultImage = "nexus3-agent-base"

// herdrExecCommandContext is the exec.CommandContext seam. Production code
// always uses exec.CommandContext; tests override it to capture args without
// spawning a real subprocess.
var herdrExecCommandContext = exec.CommandContext

// worktreeGitRunner is the seam for running git subcommands in tests.
// Production code uses exec.Command("git", "-C", dir, args...); tests override
// to drive trusted-ref resolution without a real git repo.
var worktreeGitRunner = func(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	return cmd.Output()
}

// herdrReadMountSpec reads one line from scanner and returns the --mount flag
// pair when the user supplies a spec, or nil when the answer is blank.
//
// An empty answer is the safe default (no live mount). A non-empty answer is
// validated with parseMountLive so the operator gets a legible error before
// any VM is created. parseMountLive is the single authoritative parser — this
// function deliberately never duplicates its logic.
func herdrReadMountSpec(scanner *bufio.Scanner) ([]string, error) {
	fmt.Fprint(os.Stderr, "mount host:guest[:ro] (blank for none): ")
	if !scanner.Scan() {
		return nil, &CodedError{Code: ErrCodeInternalError, Msg: "failed to read mount spec"}
	}
	spec := strings.TrimSpace(scanner.Text())
	if spec == "" {
		return nil, nil
	}
	if _, err := parseMountLive(spec); err != nil {
		return nil, err
	}
	return []string{"--mount", spec}, nil
}

// herdrPluginCreate interactively prompts for an image and a handle, creates
// and BOOTS a sandbox, then opens a herdr space for it.
//
// It deliberately shells out to "sandbox create --image" rather than calling
// svc.Create directly. svc.Create is metadata-only: it mints a record in state
// Created with an empty Envelope, so the sandbox has no image and can never
// boot. An action titled "create a sandbox" that leaves a dead record behind —
// with no VM, no space, and no pane — is indistinguishable from doing nothing,
// which is exactly how it was reported. Delegating to the real verb also keeps
// flag handling, exit codes and preflight in one place.
func herdrPluginCreate(ctx context.Context, r io.Reader, w io.Writer, svc *service.Service, storeRoot string) error {
	// Preflight: validate the kernel path before prompting, so a missing
	// kernel is reported up front rather than after the interactive prompts.
	if _, err := resolveKernelPath(); err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin create: " + err.Error(), Err: err}
	}

	scanner := bufio.NewScanner(r)

	// Show what is actually available before asking. The default ref is not
	// guaranteed to be unique: several builds can carry the same ref, and
	// `sandbox create` then refuses it as ambiguous and asks for a digest.
	// Printing the candidates here is the difference between a prompt the
	// operator can answer and one they have to leave to go investigate.
	herdrPrintImages(ctx, w)

	fmt.Fprintf(os.Stderr, "image [%s]: ", herdrDefaultImage)
	if !scanner.Scan() {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin create: failed to read image"}
	}
	image := strings.TrimSpace(scanner.Text())
	if image == "" {
		image = herdrDefaultImage
	}

	fmt.Fprint(os.Stderr, "project: ")
	if !scanner.Scan() {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin create: failed to read project"}
	}
	project := strings.TrimSpace(scanner.Text())
	if project == "" {
		return &UsageError{Msg: "__herdr-plugin create: project name required"}
	}

	fmt.Fprint(os.Stderr, "name: ")
	if !scanner.Scan() {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin create: failed to read name"}
	}
	name := strings.TrimSpace(scanner.Text())
	if name == "" {
		return &UsageError{Msg: "__herdr-plugin create: sandbox name required"}
	}

	handle := project + "/" + name
	if _, _, err := domain.ParseHandle(handle); err != nil {
		fmt.Fprintf(w, "error: invalid sandbox name %q (must be project/name)\n", handle)
		return &UsageError{Msg: "__herdr-plugin create: " + err.Error()}
	}

	repoFlags, err := herdrRepoFlags(scanner)
	if err != nil {
		fmt.Fprintf(w, "error: %v\n", err)
		return err
	}

	mountArgs, err := herdrReadMountSpec(scanner)
	if err != nil {
		fmt.Fprintf(w, "error: %v\n", err)
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin create: resolve executable: " + err.Error(), Err: err}
	}

	fmt.Fprintf(w, "creating sandbox %q from image %s ...\n", handle, image)
	args := append([]string{"sandbox", "create", handle, "--image", image}, repoFlags...)
	args = append(args, mountArgs...)
	createCmd := herdrExecCommandContext(ctx, exe, args...)
	createCmd.Stdout = w
	createCmd.Stderr = w
	if err := createCmd.Run(); err != nil {
		fmt.Fprintf(w, "error: sandbox create failed: %v\n", err)
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin create: sandbox create: " + err.Error(), Err: err}
	}

	fmt.Fprintf(w, "opening herdr space for %s ...\n", handle)
	return herdrPluginSpaceCreate(ctx, handle, w, svc, storeRoot, true)
}

// resolveDockerfilePath resolves a Containerfile path using docker-style context/dockerfile semantics.
//
// Priority:
//  1. overridePath if non-empty (explicit --file flag)
//  2. contextDir/.nexus/Containerfile
//  3. contextDir/.nexus/Dockerfile (fallback)
//
// Returns an error (naming both tried paths) if no usable file is found.
// Also returns a warning string (non-empty) when:
//   - the resolved file is not at the standard .nexus/Containerfile location
//     (the nexus3 build engine always reads workspaceDir/.nexus/Containerfile)
func resolveDockerfilePath(contextDir, overridePath string) (resolved string, warning string, err error) {
	if overridePath != "" {
		if _, e := os.Stat(overridePath); e != nil {
			return "", "", fmt.Errorf("--file %q: %w", overridePath, e)
		}
		standard := filepath.Join(contextDir, ".nexus", "Containerfile")
		if overridePath != standard {
			warning = fmt.Sprintf(
				"note: --file %q is not the standard path %q; the build engine reads %q from the context dir",
				overridePath, standard, standard)
		}
		return overridePath, warning, nil
	}

	cf := filepath.Join(contextDir, ".nexus", "Containerfile")
	if _, e := os.Stat(cf); e == nil {
		return cf, "", nil
	}
	df := filepath.Join(contextDir, ".nexus", "Dockerfile")
	if _, e := os.Stat(df); e == nil {
		// Dockerfile found but the build engine only reads Containerfile.
		return df, fmt.Sprintf(
			"warning: found %q but the nexus3 build engine requires %q — please rename it",
			df, cf), nil
	}
	return "", "", fmt.Errorf(
		"no Containerfile found: tried\n  %s\n  %s\nCreate one of these files or specify --file",
		cf, df)
}

// deriveHandleFromContext derives a default sandbox handle from the context directory basename.
// Convention: "local/<basename>".
func deriveHandleFromContext(contextDir string) string {
	base := filepath.Base(contextDir)
	if base == "" || base == "." || base == "/" {
		base = "project"
	}
	return "local/" + base
}

// herdrPluginContextCwdValue reads workspace_cwd from HERDR_PLUGIN_CONTEXT_JSON.
// Returns empty string if the env var is absent or malformed.
func herdrPluginContextCwdValue() string {
	raw := os.Getenv("HERDR_PLUGIN_CONTEXT_JSON")
	if raw == "" {
		return ""
	}
	var obj struct {
		WorkspaceCwd string `json:"workspace_cwd"`
	}
	if err := json.Unmarshal([]byte(raw), &obj); err != nil {
		return ""
	}
	return obj.WorkspaceCwd
}

// herdrPluginSpaceCreateFromFile interactively prompts for a build context and
// Containerfile, boots a sandbox via "nexus3 sandbox create --file <dockerfile>",
// then calls the space-create flow to open a guest-shell herdr space.
//
// Docker-style semantics:
//   - context: the build context directory (default: workspace_cwd from HERDR_PLUGIN_CONTEXT_JSON)
//   - dockerfile: defaults to <context>/.nexus/Containerfile with fallback to <context>/.nexus/Dockerfile
//
// GAP vs. docker: the nexus3 build engine (builder.Build) always reads
// WorkspaceDir/.nexus/Containerfile regardless of the dockerfile argument.
// The --file flag to "sandbox create" derives workspaceDir from the dockerfile
// path (strips .nexus/ prefix). If the user supplies an override --file that
// lives outside the standard .nexus/ location, workspaceDir may differ from
// the context dir. This limitation is noted with a warning; no silent data loss occurs.
func herdrPluginSpaceCreateFromFile(ctx context.Context, r io.Reader, w io.Writer, svc *service.Service, storeRoot string) error {
	scanner := bufio.NewScanner(r)

	// ── Resolve context dir ───────────────────────────────────────────────────
	defaultContext := herdrPluginContextCwdValue()
	if defaultContext == "" {
		defaultContext, _ = os.Getwd()
	}

	fmt.Fprintf(os.Stderr, "context [%s]: ", defaultContext)
	if !scanner.Scan() {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-create-from-file: failed to read context"}
	}
	contextDir := strings.TrimSpace(scanner.Text())
	if contextDir == "" {
		contextDir = defaultContext
	}
	if fi, err := os.Stat(contextDir); err != nil || !fi.IsDir() {
		fmt.Fprintf(w, "error: context path %q does not exist or is not a directory\n", contextDir)
		return &UsageError{Msg: "space-create-from-file: invalid context path: " + contextDir}
	}

	// ── Resolve dockerfile ────────────────────────────────────────────────────
	defaultDockerfile := filepath.Join(contextDir, ".nexus", "Containerfile")
	fmt.Fprintf(os.Stderr, "dockerfile [%s]: ", defaultDockerfile)
	if !scanner.Scan() {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-create-from-file: failed to read dockerfile"}
	}
	dockerfileOverride := strings.TrimSpace(scanner.Text())

	dockerfile, warn, err := resolveDockerfilePath(contextDir, dockerfileOverride)
	if err != nil {
		fmt.Fprintln(w, "error: "+err.Error())
		return &UsageError{Msg: "space-create-from-file: " + err.Error()}
	}
	if warn != "" {
		fmt.Fprintln(w, warn)
	}
	// If the file resolves to Dockerfile (not Containerfile), the build engine
	// cannot use it — surface the limitation immediately before creating anything.
	standardCF := filepath.Join(contextDir, ".nexus", "Containerfile")
	if dockerfile != standardCF {
		fmt.Fprintf(w, "error: resolved dockerfile %q but the build engine only reads %q\n", dockerfile, standardCF)
		fmt.Fprintf(w, "       Rename the file to .nexus/Containerfile and retry.\n")
		return &UsageError{Msg: "space-create-from-file: dockerfile must be at " + standardCF}
	}

	// ── Derive sandbox handle ─────────────────────────────────────────────────
	defaultHandle := deriveHandleFromContext(contextDir)
	fmt.Fprintf(os.Stderr, "sandbox name [%s]: ", defaultHandle)
	if !scanner.Scan() {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-create-from-file: failed to read sandbox name"}
	}
	handle := strings.TrimSpace(scanner.Text())
	if handle == "" {
		handle = defaultHandle
	}
	// Validate handle format: must be project/name.
	if _, _, err := domain.ParseHandle(handle); err != nil {
		fmt.Fprintf(w, "error: invalid sandbox name %q (must be project/name, e.g. local/myapp)\n", handle)
		return &UsageError{Msg: "space-create-from-file: " + err.Error()}
	}

	// ── Create + boot sandbox via "nexus3 sandbox create <handle> --file <dir>" ──
	// We pass contextDir (the dir) so cmd_sandbox.go sets workspaceDir = contextDir.
	// The build engine then reads contextDir/.nexus/Containerfile.
	repoFlags, repoErr := herdrRepoFlags(scanner)
	if repoErr != nil {
		fmt.Fprintf(w, "error: %v\n", repoErr)
		return repoErr
	}

	mountArgs, mountErr := herdrReadMountSpec(scanner)
	if mountErr != nil {
		fmt.Fprintf(w, "error: %v\n", mountErr)
		return mountErr
	}

	fmt.Fprintf(w, "creating sandbox %q from %s ...\n", handle, dockerfile)
	exe, err := os.Executable()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-create-from-file: resolve executable: " + err.Error(), Err: err}
	}
	args := append([]string{"sandbox", "create", handle, "--file", contextDir}, repoFlags...)
	args = append(args, mountArgs...)
	createCmd := herdrExecCommandContext(ctx, exe, args...)
	createCmd.Stdout = w
	createCmd.Stderr = w
	if err := createCmd.Run(); err != nil {
		fmt.Fprintf(w, "error: sandbox create failed: %v\n", err)
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-create-from-file: sandbox create: " + err.Error(), Err: err}
	}

	// ── Open herdr space ──────────────────────────────────────────────────────
	fmt.Fprintf(w, "opening herdr space for %s ...\n", handle)
	return herdrPluginSpaceCreate(ctx, handle, w, svc, storeRoot, true)
}

// herdrPluginDoctor prints host preflight info and ABI version.
func herdrPluginDoctor(w io.Writer) error {
	exe, _ := os.Executable()
	herdrBin, herdrBinErr := resolveHerdrBin()
	herdrVer := os.Getenv("HERDR_VERSION")
	if herdrVer == "" {
		herdrVer = "(HERDR_VERSION not set)"
	}
	herdrBinStatus := "(not found)"
	if herdrBinErr == nil {
		herdrBinStatus = herdrBin
		if os.Getenv("HERDR_BIN_PATH") == "" {
			herdrBinStatus += "  (resolved from PATH; HERDR_BIN_PATH unset)"
		}
	}
	fmt.Fprintf(w, "nexus3 binary:  %s\n", exe)
	fmt.Fprintf(w, "plugin ABI:     %s\n", herdrPluginABIVersion)
	fmt.Fprintf(w, "herdr version:  %s\n", herdrVer)
	fmt.Fprintf(w, "HERDR_BIN_PATH: %s\n", herdrBinStatus)

	// ABI file check: compare HERDR_PLUGIN_ROOT/abi against herdrPluginABIVersion.
	// This catches a stale plugin installation where the nexus3 binary and the
	// herdr-plugin.toml were installed at different times.
	pluginRoot := os.Getenv("HERDR_PLUGIN_ROOT")
	if pluginRoot == "" {
		fmt.Fprintf(w, "ABI file check: HERDR_PLUGIN_ROOT unset (not running as a herdr plugin)\n")
	} else {
		abiPath := filepath.Join(pluginRoot, "abi")
		abiBytes, err := os.ReadFile(abiPath)
		if err != nil {
			fmt.Fprintf(w, "ABI file check: cannot read %s: %v\n", abiPath, err)
		} else {
			expected := strings.TrimSpace(string(abiBytes))
			if expected == herdrPluginABIVersion {
				fmt.Fprintf(w, "ABI file check: ok (%s)\n", expected)
			} else {
				fmt.Fprintf(w, "ABI file check: MISMATCH — file has %q, binary expects %q\n", expected, herdrPluginABIVersion)
			}
		}
	}
	return nil
}

// resolveHerdrBin returns the herdr binary these commands should invoke.
//
// HERDR_BIN_PATH is injected by herdr into PLUGIN processes only. An ordinary
// pane running inside herdr does NOT carry it — HERDR_ENV, HERDR_WORKSPACE_ID
// and HERDR_PANE_ID are set there, but not this one. Reading the variable
// alone therefore refused `space-create` and friends for anyone running nexus3
// from a shell inside herdr, which is the most natural way to use them and the
// case where herdr is provably installed. Falling back to PATH fixes exactly
// that case without weakening anything: if no herdr exists, this still fails.
func resolveHerdrBin() (string, error) {
	if p := os.Getenv("HERDR_BIN_PATH"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("herdr"); err == nil {
		return p, nil
	}
	return "", errors.New("herdr not found: HERDR_BIN_PATH is unset and no \"herdr\" binary is on PATH")
}

// herdrPluginOpenPane calls herdr to open a pane for the given workspace.
func herdrPluginOpenPane(ws string, extraArgs []string) error {
	herdrBin, binErr := resolveHerdrBin()
	if binErr != nil {
		fmt.Fprintln(os.Stderr, "__herdr-plugin open-pane: "+binErr.Error())
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin open-pane: " + binErr.Error(), Err: binErr}
	}
	args := []string{"plugin", "pane", "open",
		"--plugin", "nexus3",
		"--env", "NEXUS3_WORKSPACE=" + ws,
	}
	args = append(args, extraArgs...)
	cmd := exec.Command(herdrBin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin open-pane: " + err.Error(), Err: err}
	}
	return nil
}

// buildLaunchBootOpts builds CreateAndBootOptions for herdrPluginLaunch.
//
// When agentEgress is true the agent egress allowlist is frozen onto the
// sandbox Envelope (AgentEgressHosts, OpenEgress left false — agent sandboxes
// never get unrestricted egress, D-PD-33). That Envelope is what the detached
// perimeter supervisor reads back to build its MITM allowlist.
//
// Nothing credential-related is wired here. The broker, the MITM proxy, the CA
// seed and the placeholder seed all belong to the supervisor that takes
// ownership of the VM immediately after this boot (handoffLaunchSupervisor).
// A second CLI-side broker would mint placeholders that the supervisor's reboot
// invalidates on the spot: GuestCredEnvPath lives under /run (tmpfs), so the
// guest's copy does not survive the handoff, and the supervisor re-seeds from
// its own broker. Minting them twice guarantees the guest and the proxy
// disagree about which placeholder to swap.
func buildLaunchBootOpts(imageRef, cacheRoot string, agentEgress bool) service.CreateAndBootOptions {
	opts := service.CreateAndBootOptions{
		Image:               service.ImageSpec{Ref: imageRef},
		CacheRoot:           cacheRoot,
		ReachabilityTimeout: 60 * time.Second,
	}
	if agentEgress {
		// One profile drives both halves: the frozen egress allowlist, and the
		// AgentName recorded on the sandbox. UseAgentSeed stays false — the
		// credential seed belongs to the supervisor, per the comment above —
		// but the record must still say which agent this sandbox is for, or
		// nothing downstream can distinguish it from a plain sandbox.
		opts.AgentProfile = cred.ClaudeCodeProfile
		opts.AllowedHosts = service.AgentEgressHosts(opts.AgentProfile)
	}
	return opts
}

// launchCredSourcedArgv wraps argv in a /bin/sh -c preamble that sources the
// supervisor-seeded credential env file before exec'ing the real command.
//
// GuestCredEnvPath is the only place the guest's placeholder credential exists:
// the supervisor mints it (SeedGuestAgent) against the same broker instance the
// MITM proxy swaps against, so sourcing the file is what makes the placeholder
// the proxy expects and the placeholder the agent sends the same string. The
// host cannot inject it from its side — the CLI process holds no broker.
//
// "set -a" exports every variable the file defines (CLAUDE_CODE_OAUTH_TOKEN or
// ANTHROPIC_AUTH_TOKEN, NODE_EXTRA_CA_CERTS, CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC),
// matching the sourcing convention buildAgentSeedPayload documents. exec "$@"
// replaces the shell so the child's exit status is the launch exit status.
func launchCredSourcedArgv(argv []string) []string {
	script := "set -a\n" +
		". " + service.GuestCredEnvPath + "\n" +
		"set +a\n" +
		"exec \"$@\"\n"
	return append([]string{"/bin/sh", "-c", script, "nexus3-launch"}, argv...)
}

// handoffLaunchSupervisor stops the CLI-owned boot and hands the VM to a
// detached ephemeral supervisor that owns the egress perimeter for the rest of
// the launch.
//
// This is the step that makes --agent-egress mean anything. The perimeter is a
// process, not a set of options: netstack ACL, MITM proxy, credential broker,
// Refresher, CA seed, update-ca-certificates and the placeholder seed all live
// inside `nexus3 __supervisor`. CreateAndBoot starts none of them, so a sandbox
// booted by the CLI alone has no proxy to swap its bearer token and no CA to
// trust — every HTTPS call to api.anthropic.com fails at connect time.
//
// Ephemeral mode is what suits a launch: the supervisor exits on the /supervisor/stop
// verb and removes the sandbox record itself, and the returned watchdog pipe
// makes that teardown survive a SIGKILL'ed CLI.
//
// Returns the watchdog write end (to be closed after the supervisor exits) and
// the supervisor's IPC socket path.
func handoffLaunchSupervisor(
	ctx context.Context,
	svc *service.Service,
	sb domain.Sandbox,
	storeRoot, kernelPath, diskPath string,
	extraDisks []string,
) (*os.File, string, error) {
	if diskPath == "" {
		return nil, "", fmt.Errorf("boot driver captured no disk path")
	}
	socketDir, err := orcaSocketDir()
	if err != nil {
		return nil, "", fmt.Errorf("resolve socket dir: %w", err)
	}
	chBin, _ := exec.LookPath("cloud-hypervisor")
	stateDir := supervisor.DefaultStateDir(storeRoot, sb.ID)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return nil, "", fmt.Errorf("create supervisor state dir: %w", err)
	}

	// The supervisor re-boots the VM itself; the CLI-owned boot must be stopped
	// first or two cloud-hypervisor processes contend for the same disk.
	if _, err := svc.Stop(ctx, sb.ID.String()); err != nil {
		return nil, "", fmt.Errorf("stop before supervisor handoff: %w", err)
	}

	// Cmdline is left empty so the driver applies its disk-boot default, which
	// is exactly what the CLI-owned boot used (buildCHConfig sets no cmdline).
	// GovBounds is left zero: the governor starts in passive mode, matching the
	// launch path's existing behaviour — a launch VM is short-lived and is not
	// the place to introduce auto-resize.
	pid, watchdogW, err := supervisor.SpawnDetached(supervisor.SpawnConfig{
		Config: supervisor.Config{
			SandboxRef: sb.ID.String(),
			StoreRoot:  storeRoot,
			StateDir:   stateDir,
			CHBin:      chBin,
			SocketDir:  socketDir,
			KernelPath: kernelPath,
			DiskPath:   diskPath,
			ExtraDisks: extraDisks,
			// Real bearer tokens are read here, inside the supervisor, and never
			// leave it: the broker hands the MITM proxy the token at swap time.
			CredsFile: service.DefaultDedicatedCredStorePath(),
			Ephemeral: true,
		},
		ReadyTimeout: 5 * time.Minute,
	})
	if err != nil {
		return nil, "", fmt.Errorf("spawn supervisor: %w", err)
	}
	sockPath := supervisor.SockPath(stateDir)
	slog.Info("__herdr-plugin launch: perimeter supervisor ready",
		"sandbox", sb.ID, "pid", pid, "sock", sockPath)
	return watchdogW, sockPath, nil
}

// verifyLaunchPerimeterSeed checks that the supervisor actually delivered the
// two artefacts the agent cannot run without: the placeholder credential file
// and the MITM CA certificate.
//
// The supervisor writes its READY pidfile even when seeding exhausts its retry
// cap (supervisor.maxSeedAttempts) — the perimeter is live at that point and it
// would rather come up degraded than not at all. That is the right call for a
// persistent sandbox and the wrong one to inherit silently here: a launch that
// proceeds without these files reaches the API unauthenticated and fails with
// an error that says nothing about the cause. Check explicitly and say so.
func verifyLaunchPerimeterSeed(ctx context.Context, svc *service.Service, id string) error {
	script := "test -s " + service.GuestCredEnvPath + " || { echo MISSING_CRED_ENV; exit 11; }\n" +
		"test -s " + service.GuestCACertPath + " || { echo MISSING_CA_CERT; exit 12; }\n"
	var sink strings.Builder
	code, err := svc.Exec(ctx, id, agent.ExecOptions{
		Argv:   []string{"/bin/sh", "-c", script},
		Stdout: &sink,
		Stderr: &sink,
	})
	if err != nil {
		return fmt.Errorf("probe guest for perimeter seed: %w", err)
	}
	switch code {
	case 0:
		return nil
	case 11:
		return fmt.Errorf("%s is absent in the guest: the supervisor did not seed the "+
			"placeholder credential, so the agent would run unauthenticated", service.GuestCredEnvPath)
	case 12:
		return fmt.Errorf("%s is absent in the guest: the supervisor did not seed the MITM "+
			"CA certificate, so every HTTPS call would fail certificate validation", service.GuestCACertPath)
	default:
		return fmt.Errorf("perimeter seed probe exited %d: %s", code, strings.TrimSpace(sink.String()))
	}
}

// herdrPluginLaunch boots a sandbox from imageRef, runs argv in-guest via the
// agent exec path (vsock gRPC control + data plane), streams stdout to out,
// then tears the sandbox down.
//
// Without --agent-egress the sandbox is CLI-owned end to end: CreateAndBoot
// boots it, svc.Exec runs argv over the boot driver's vsock, and the deferred
// Remove stops it. No perimeter, no allowlist.
//
// With --agent-egress the VM changes hands. CreateAndBoot boots it once to
// materialise the disk and prove the guest agent answers; then
// handoffLaunchSupervisor stops that boot and re-boots the VM under a detached
// supervisor which owns the whole zero-credential perimeter — netstack ACL,
// MITM proxy, credential broker, CA seed, placeholder seed. The agent then runs
// with the guest's seeded placeholder, and the proxy swaps it for the real
// bearer token host-side, on the wire. The real token never enters the guest.
// launchDeps is the injectable surface of the launch path (TBD-PD-31).
//
// herdrPluginLaunch used to build its service, driver factory and supervisor
// handoff inline, which left no way to drive the real function from a test. The
// wiring was guarded instead by an AST tripwire that parsed this file and
// asserted the three calls appeared in it — which proves the call sites EXIST,
// not that they run, not that their arguments are right, and not that the
// perimeter comes up. That is the same blind spot as the options-shape tests it
// replaced, displaced one layer.
//
// With the dependencies injected, a fake-driver test drives runHerdrLaunch and
// observes whether the handoff actually happened.
type launchDeps struct {
	svc        *service.Service
	imgCache   *image.Cache
	newDriver  service.DriverFactory
	probe      service.ProbeFunc
	storeRoot  string
	kernelPath string
	cacheRoot  string

	// capturedDiskPath returns the per-sandbox ext4 path the driver factory
	// saw. It is a function because the value is only known after
	// CreateAndBoot has called the factory.
	capturedDiskPath func() string

	// handoff hands the booted VM to a detached perimeter supervisor and
	// returns the watchdog pipe and the supervisor's IPC socket path.
	handoff func(ctx context.Context, svc *service.Service, sb domain.Sandbox,
		storeRoot, kernelPath, diskPath string, extraDisks []string) (*os.File, string, error)

	// verifySeed checks the guest received the placeholder credential and the
	// CA certificate the agent cannot run without.
	verifySeed func(ctx context.Context, svc *service.Service, id string) error

	// stopSupervisor and waitForExit tear the detached supervisor down.
	stopSupervisor func(ctx context.Context, sock string) error
	waitForExit    func(ctx context.Context, stateDir string) error

	// execInGuest runs the command inside the booted sandbox.
	execInGuest func(ctx context.Context, ref string, opts agent.ExecOptions) (int32, error)
}

// newLaunchDeps builds the production dependency set: a real service, a
// cloud-hypervisor driver factory, and the real supervisor handoff.
func newLaunchDeps() (launchDeps, error) {
	var d launchDeps

	// Preflight: validate the kernel path before store/cache/service setup so
	// that a missing/misconfigured NEXUS3_KERNEL_PATH surfaces immediately with
	// an actionable error rather than after expensive work inside CreateAndBoot.
	kernelPath, err := resolveKernelPath()
	if err != nil {
		return d, err
	}
	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return d, fmt.Errorf("resolve store: %w", err)
	}
	cacheRoot := filepath.Join(storeRoot, "images")
	imgCache, err := image.NewCache(cacheRoot)
	if err != nil {
		return d, fmt.Errorf("open image cache: %w", err)
	}
	svc, err := newSandboxService()
	if err != nil {
		return d, err
	}

	// capturedDiskPath is the per-sandbox ext4 copy CreateAndBoot resolves. The
	// supervisor re-boots from the same file, so it must be captured here — the
	// factory is the only place the CLI sees it.
	var diskPath string

	// newDriver mirrors cmd_sandbox.go's factory: socket/log paths use default
	// locations so that svc.driver (from SelectSubstrate, same SocketDir) can
	// reach the vsock file for svc.Exec after CreateAndBoot returns.
	newDriver := service.DriverFactory(func(ext4Path string, _ []service.ExtraDisk) (driver.Driver, error) {
		diskPath = ext4Path
		cfg := buildCHConfig(kernelPath, ext4Path, 0, 0)
		if p, lookErr := exec.LookPath("cloud-hypervisor"); lookErr == nil {
			cfg.BinaryPath = p
		}
		return cloudhypervisor.New(cfg)
	})

	return launchDeps{
		svc:              svc,
		imgCache:         imgCache,
		newDriver:        newDriver,
		probe:            waitForGuestAgent,
		storeRoot:        storeRoot,
		kernelPath:       kernelPath,
		cacheRoot:        cacheRoot,
		capturedDiskPath: func() string { return diskPath },
		handoff:          handoffLaunchSupervisor,
		verifySeed:       verifyLaunchPerimeterSeed,
		stopSupervisor:   supervisor.StopSupervisor,
		waitForExit:      supervisor.WaitForExit,
		execInGuest:      svc.Exec,
	}, nil
}

// waitForGuestAgent polls vsock until the guest agent's listener accepts.
// Mirrors the probe in cmd_sandbox.go (runSandboxCreate).
func waitForGuestAgent(pCtx context.Context, drv driver.Driver, id domain.SandboxID) error {
	gd, ok := drv.(driver.GuestDialer)
	if !ok {
		return nil
	}
	for {
		if pCtx.Err() != nil {
			return pCtx.Err()
		}
		dialCtx, cancel := context.WithTimeout(pCtx, 2*time.Second)
		conn, dialErr := gd.DialGuest(dialCtx, id, driver.AgentControlPort)
		cancel()
		if dialErr == nil {
			_ = conn.Close()
			return nil
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func herdrPluginLaunch(ctx context.Context, imageRef string, argv []string, agentEgress bool, out *Output) error {
	d, err := newLaunchDeps()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin launch: " + err.Error(), Err: err}
	}
	return runHerdrLaunch(ctx, d, imageRef, argv, agentEgress, out)
}

// runHerdrLaunch is the launch path proper, driven entirely through d so a test
// can supply a fake driver and observe whether the perimeter handoff happens.
func runHerdrLaunch(ctx context.Context, d launchDeps, imageRef string, argv []string, agentEgress bool, out *Output) error {
	// Unique name per invocation to prevent ErrAlreadyExists on retry or concurrent runs.
	name := fmt.Sprintf("run-%x", time.Now().UnixNano())

	sb, err := service.CreateAndBoot(ctx, d.svc, d.imgCache, d.newDriver, d.probe,
		"herdr", name, buildLaunchBootOpts(imageRef, d.cacheRoot, agentEgress),
	)
	if err != nil {
		return &CodedError{Code: sandboxCodeFor(err), Msg: "__herdr-plugin launch: boot: " + err.Error(), Err: err}
	}

	// supSock/supWatchdog are set once the VM is handed to a supervisor; the
	// teardown below reads them, so they are declared before the defer.
	var (
		supSock     string
		supWatchdog *os.File
	)
	defer func() {
		// Ephemeral supervisor teardown: /supervisor/stop, then wait for the
		// process to exit. The supervisor calls svc.Remove itself on the way
		// out, so waiting is what guarantees the VM is down and the record gone
		// before the fallback Remove below runs.
		if supSock != "" {
			stopCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			if err := d.stopSupervisor(stopCtx, supSock); err != nil {
				fmt.Fprintf(os.Stderr, "__herdr-plugin launch: cleanup: stop supervisor: %v\n", err)
			} else if err := d.waitForExit(stopCtx, filepath.Dir(supSock)); err != nil {
				fmt.Fprintf(os.Stderr, "__herdr-plugin launch: cleanup: wait for supervisor exit: %v\n", err)
			}
			cancel()
			if supWatchdog != nil {
				_ = supWatchdog.Close()
			}
		}
		// Fallback for the no-supervisor path (and for a supervisor that died
		// before removing the record). Remove internally stops the VM.
		rmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := d.svc.Remove(rmCtx, sb.ID.String()); err != nil && !errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "__herdr-plugin launch: cleanup: remove %s: %v\n", sb.ID, err)
		}
	}()

	execArgv := argv
	if agentEgress {
		w, sock, err := d.handoff(ctx, d.svc, sb, d.storeRoot, d.kernelPath, d.capturedDiskPath(), nil)
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin launch: " + err.Error(), Err: err}
		}
		supWatchdog, supSock = w, sock

		seedCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
		seedErr := d.verifySeed(seedCtx, d.svc, sb.ID.String())
		cancel()
		if seedErr != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin launch: " + seedErr.Error(), Err: seedErr}
		}
		execArgv = launchCredSourcedArgv(argv)
	}

	// Exec via svc.Exec (the surface-layer path per agent_ops.go:28). svc.driver
	// shares defaultSocketDir() with the boot driver and with the supervisor's
	// CHDriver, so it can dial the vsock either way.
	//
	// Env semantics: the guest agent appends req.Env to os.Environ() — it is a
	// merge, not a replacement. On the plain path argv[0] must be an absolute
	// path because exec.Command resolves it via exec.LookPath in the agent
	// binary's own process environment (before cmd.Env applies); on the egress
	// path /bin/sh performs the lookup instead. PATH is still injected for the
	// child's own subprocess lookups (claude shells out to bash, git, etc.).
	// HOME is injected for credential path resolution (claude's OAuth files).
	execEnv := map[string]string{
		"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME": "/root",
	}

	exitCode, err := d.execInGuest(ctx, sb.ID.String(), agent.ExecOptions{
		Argv:   execArgv,
		Env:    execEnv,
		Stdout: out.w,
		Stderr: os.Stderr,
	})
	if err != nil {
		msg := "__herdr-plugin launch: exec: " + err.Error()
		if strings.Contains(err.Error(), "not found in $PATH") {
			msg += " (hint: command must be an absolute path, e.g. /usr/local/bin/claude)"
		}
		return &CodedError{Code: agentCodeFor(err), Msg: msg, Err: err}
	}
	if exitCode != 0 {
		return &ExitCodeError{Code: exitCode}
	}
	return nil
}

// herdrSpaceLabelForRef derives the canonical herdr workspace label for a sandbox ref.
// Convention: "nexus3:<handle>" where handle is the ref (sandbox handles ARE the ref).
func herdrSpaceLabelForRef(ref string) string {
	return "nexus3:" + ref
}

// herdrPluginSpaceCreate creates (or reuses) a herdr workspace for the sandbox,
// opens the primary guest-shell pane, and stores the binding.
func herdrPluginSpaceCreate(ctx context.Context, ref string, w io.Writer, svc herdrSpaceCreateSvc, storeRoot string, focus bool) error {
	herdrBin, binErr := resolveHerdrBin()
	if binErr != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-create: " + binErr.Error(), Err: binErr}
	}

	// Ensure sandbox is running; captures the sandbox for ID resolution.
	// If Start returns an illegal-transition error the sandbox is already running;
	// fall back to List to locate it.
	sb, startErr := svc.Start(ctx, ref)
	if startErr != nil {
		if !strings.Contains(startErr.Error(), "illegal transition") {
			return &CodedError{Code: ErrCodeInternalError, Msg: "space-create: start sandbox: " + startErr.Error(), Err: startErr}
		}
		// Sandbox already running: resolve via list.
		all, listErr := svc.List(ctx)
		if listErr != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "space-create: list sandboxes: " + listErr.Error(), Err: listErr}
		}
		found := false
		for _, s := range all {
			if s.Handle() == ref {
				sb = s
				found = true
				break
			}
		}
		if !found {
			return &CodedError{Code: ErrCodeInternalError, Msg: "space-create: sandbox not found: " + ref}
		}
	}

	label := herdrSpaceLabelForRef(ref)

	// focus is a caller decision: human-interactive callers (space-create,
	// herdrPluginCreate, herdrPluginSpaceCreateFromFile) pass true so the
	// operator's view lands on the new pane. Programmatic/N-way callers
	// (space-agent --no-focus) pass false to avoid stealing focus from a
	// concurrent run that is already visible.

	// Idempotency: reuse an existing binding for this handle — but only after
	// confirming the workspace it names still EXISTS. A binding is a stored
	// pointer and the operator can close a workspace in herdr at any time;
	// nothing tells nexus3. Trusting the pointer blindly made space-create
	// fail outright against a closed workspace:
	//
	//	reusing space: label=nexus3:ac3/vcpuctl workspace_id=w35
	//	{"error":{"code":"workspace_not_found",...}}
	//	error: space-create: open shell pane: exit status 1
	//
	// stranding the sandbox until someone pruned the binding by hand. When the
	// workspace is gone we mint a fresh one instead.
	//
	// The predicate is shared with space-prune and fails SAFE for both callers:
	// when herdr is unreachable or answers in an unexpected shape it reports
	// every workspace alive, so prune deletes nothing and we reuse rather than
	// minting a duplicate during a transient herdr outage.
	existing, existingErr := HerdrSpaceGetByLabel(ctx, storeRoot, label)
	reusable := existingErr == nil
	if reusable && !herdrSpacePruneWorkspaceExistsFn(ctx, herdrBin)(existing) {
		slog.Warn("space-create: bound herdr workspace no longer exists; minting a new one",
			"label", existing.SpaceLabel, "stale_workspace_id", existing.HerdrWorkspaceID)
		fmt.Fprintf(w, "space-create: workspace %s for %s is gone; creating a new one\n",
			existing.HerdrWorkspaceID, existing.SpaceLabel)
		reusable = false
	}
	if reusable {
		fmt.Fprintf(w, "reusing space: label=%s workspace_id=%s\n", existing.SpaceLabel, existing.HerdrWorkspaceID)
		paneID, err := herdrOpenGuestShellPane(ctx, herdrBin, ref, existing.HerdrWorkspaceID, "", focus)
		if err != nil {
			return err
		}
		existing.GuestPaneID = paneID
		if err := HerdrSpacePut(ctx, storeRoot, existing); err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "space-create: store binding: " + err.Error(), Err: err}
		}
		fmt.Fprintf(w, "opened pane: pane_id=%s\n", paneID)
		return nil
	}

	// Create herdr workspace and capture its ID and root pane ID. The cwd is
	// the sandbox's own host-side mount path, not whatever workspace happens
	// to be focused right now — see herdrShellHostCwd.
	hostCwd := herdrShellHostCwd(ctx, ref, svc)
	workspaceID, rootPaneID, err := herdrWorkspaceCreate(ctx, herdrBin, label, hostCwd)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-create: herdr workspace create: " + err.Error(), Err: err}
	}

	b := HerdrSpaceBinding{
		SpaceLabel:       label,
		HerdrWorkspaceID: workspaceID,
		SandboxHandle:    ref,
		SandboxID:        sb.ID.String(),
	}
	if err := HerdrSpacePut(ctx, storeRoot, b); err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-create: store binding: " + err.Error(), Err: err}
	}

	fmt.Fprintf(w, "created space: label=%s workspace_id=%s\n", label, workspaceID)
	// Opens the guest pane beside the root pane herdr just created
	// (--placement split --target-pane <root> --direction right — see
	// herdrOpenGuestShellPane), then closes the root pane to leave the
	// workspace guest-only.
	paneID, err := herdrOpenGuestShellPane(ctx, herdrBin, ref, workspaceID, rootPaneID, focus)
	if err != nil {
		return err
	}
	// Guest pane is open. Close the root host pane we were split beside.
	// A close failure is cosmetic: the sandbox is fully operational, so log
	// a warning and continue rather than failing space-create.
	herdrCloseRootPane(ctx, herdrBin, "space-create", rootPaneID)
	b.GuestPaneID = paneID
	if err := HerdrSpacePut(ctx, storeRoot, b); err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-create: store binding: " + err.Error(), Err: err}
	}
	fmt.Fprintf(w, "opened pane: pane_id=%s\n", paneID)
	return nil
}

// herdrWorkspaceCreate runs "herdr workspace create --label <label> [--cwd
// <cwd>]" and returns the new workspace's ID and its root pane's ID. herdr
// prints a JSON envelope; we parse result.workspace.workspace_id and
// result.root_pane.pane_id from it.
//
// The root pane ID is used by the caller to (a) split the guest pane beside
// it (--placement split --target-pane <root> --direction right) and then
// (b) close it, leaving the workspace guest-only. See herdrOpenGuestShellPane.
//
// herdrCloseRootPane closes the operator-facing root pane that was created
// alongside the guest pane. A failure is cosmetic — the sandbox is fully
// operational — so we log a warning and continue. caller is the subcommand
// name used in the warning message (e.g. "space-create", "space-open-pane").
func herdrCloseRootPane(ctx context.Context, herdrBin, caller, rootPaneID string) {
	if rootPaneID == "" {
		return
	}
	closeCmd := herdrExecCommandContext(ctx, herdrBin, "pane", "close", rootPaneID)
	closeCmd.Stderr = os.Stderr
	if closeErr := closeCmd.Run(); closeErr != nil {
		slog.Warn(caller+": herdr pane close failed; workspace has extra host pane",
			"pane_id", rootPaneID, "err", closeErr)
	}
}

// cwd is optional: an empty value omits --cwd entirely rather than passing an
// empty flag, which preserves herdr's own default-cwd behaviour.
func herdrWorkspaceCreate(ctx context.Context, herdrBin, label, cwd string) (workspaceID, rootPaneID string, err error) {
	args := []string{"workspace", "create", "--label", label, "--no-focus"}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	var buf strings.Builder
	cmd := herdrExecCommandContext(ctx, herdrBin, args...)
	cmd.Stdout = &buf
	cmd.Stderr = os.Stderr
	if runErr := cmd.Run(); runErr != nil {
		return "", "", runErr
	}
	// Parse {"id":...,"result":{"workspace":{"workspace_id":"wF",...},"root_pane":{"pane_id":"p1",...},...}}
	var envelope struct {
		Result struct {
			Workspace struct {
				WorkspaceID string `json:"workspace_id"`
			} `json:"workspace"`
			RootPane struct {
				PaneID string `json:"pane_id"`
			} `json:"root_pane"`
		} `json:"result"`
	}
	raw := strings.TrimSpace(buf.String())
	if jsonErr := json.Unmarshal([]byte(raw), &envelope); jsonErr != nil {
		// Fall back to treating the raw output as a plain ID (plain-text
		// mode). The root pane ID is not recoverable here, so the caller
		// falls back to opening the guest pane via --workspace — today's
		// separate-tab behaviour, a degradation rather than a failure.
		id := raw
		if id == "" {
			return "", "", fmt.Errorf("herdr workspace create: empty output")
		}
		return id, "", nil
	}
	id := envelope.Result.Workspace.WorkspaceID
	if id == "" {
		return "", "", fmt.Errorf("herdr workspace create: workspace_id not found in response: %s", raw)
	}
	return id, envelope.Result.RootPane.PaneID, nil
}

// herdrOpenGuestShellPane opens a guest-shell pane and returns its pane ID.
//
// When rootPaneID is known, the pane is opened as a horizontal split beside
// the workspace's root pane via --target-pane, rather than as a second tab.
// The caller then closes that root pane, leaving the workspace GUEST-ONLY —
// a nexus3:<handle> workspace represents a sandbox, so a host shell sitting
// in it is noise, and it made "new tab" land in a host path.
//
// Closing the root PANE is safe; closing the root TAB is not. An earlier
// version closed the tab after opening a second one, which SIGHUPs whatever
// is running in it. The root pane here is one herdr minted moments ago for a
// workspace we just created and nobody has typed into — a freshly-made empty
// shell, not someone's work. That distinction is the whole licence for
// closing it, so never generalise this into closing panes we did not just
// cause to exist.
//
// herdr accepts --workspace only with --placement tab. Split and zoomed
// reject --workspace unconditionally ("split and zoomed plugin panes target
// an existing pane; use target_pane_id") — the rejection is placement-driven,
// not caused by the presence of --target-pane. So rootPaneID present → split
// + --target-pane; rootPaneID absent → --workspace only (no --placement, so
// the server falls back to the manifest-declared placement for the shell
// entrypoint, which must be "tab" — see TestHerdrManifest_ShellPlacementIsTab).
//
// When rootPaneID is empty — no root pane id could be parsed, or an existing
// workspace is being reused rather than freshly created (its root pane id
// from creation time is not retained) — --workspace is used instead: today's
// separate-tab behaviour. That is a degradation, not a failure.
//
// focus controls whether --focus is passed. herdr agent start requires
// --pane <ID>, which is why this function's return value matters: without it
// the id nexus3 opens is thrown away and the rest of the agent-start chain
// can never be scripted (see the package doc comment on the chain this
// closes). focus is a separate concern from the pane ID: under N-way
// spawning, every new sandbox stealing the operator's focus away from
// whatever they are doing is its own bug, independent of whether the ID gets
// captured. Callers decide: interactive single-space use should keep
// stealing focus (that is what a human asking for one space wants); bulk/
// programmatic creation should not (see the two call sites for the actual
// per-caller decision and why).
func herdrOpenGuestShellPane(ctx context.Context, herdrBin, ref, workspaceID, rootPaneID string, focus bool) (string, error) {
	args := []string{"plugin", "pane", "open",
		"--plugin", "nexus3",
		"--entrypoint", "shell",
	}
	if rootPaneID != "" {
		args = append(args, "--placement", "split", "--target-pane", rootPaneID, "--direction", "right")
	} else {
		// No --placement: herdr falls back to the manifest-declared placement
		// for the shell entrypoint, which is "tab". Tab is the only placement
		// that accepts --workspace; if the manifest ever changes shell to split,
		// overlay, or zoomed, this call silently returns rc=1.
		// TestHerdrManifest_ShellPlacementIsTab pins that invariant.
		args = append(args, "--workspace", workspaceID)
	}
	args = append(args, "--env", "NEXUS3_WORKSPACE="+ref)
	if focus {
		args = append(args, "--focus")
	} else {
		args = append(args, "--no-focus")
	}
	cmd := herdrExecCommandContext(ctx, herdrBin, args...)
	cmd.Stdin = os.Stdin
	// Tee to the operator's terminal — that is today's behaviour and stays
	// unchanged — while also capturing the output so the pane ID can be
	// parsed out of it below.
	var buf strings.Builder
	cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", &CodedError{Code: ErrCodeInternalError, Msg: "space-create: open shell pane: " + err.Error(), Err: err}
	}
	return herdrParsePluginPaneID(buf.String()), nil
}

// herdrParsePluginPaneID extracts result.plugin_pane.pane.pane_id from a
// `herdr plugin pane open` JSON envelope, e.g.:
//
//	{"id":"cli:plugin","result":{"plugin_pane":{"entrypoint":"shell",
//	  "pane":{"pane_id":"w1V:p2","tab_id":"w1V:t1","workspace_id":"w1V",
//	          "label":"nexus3 guest shell", ...},
//	  "plugin_id":"nexus3"},"type":"plugin_pane_opened"}}
//
// Mirrors herdrWorkspaceCreate's tolerance for non-JSON output: unparseable
// text (plain-text mode, or any other shape) yields an empty pane ID, never
// an error — a pane that opens successfully but whose ID could not be
// captured is a degradation, not a failure.
func herdrParsePluginPaneID(raw string) string {
	var envelope struct {
		Result struct {
			PluginPane struct {
				Pane struct {
					PaneID string `json:"pane_id"`
				} `json:"pane"`
			} `json:"plugin_pane"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &envelope); err != nil {
		return ""
	}
	return envelope.Result.PluginPane.Pane.PaneID
}

// herdrPluginSpaceOpenPane resolves a space by ref, label, or workspace_id and opens another guest-shell pane.
func herdrPluginSpaceOpenPane(ctx context.Context, refOrLabel string, storeRoot string, svc herdrAdoptGetter, w io.Writer) error {
	herdrBin, binErr := resolveHerdrBin()
	if binErr != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-open-pane: " + binErr.Error(), Err: binErr}
	}

	b, adopted, err := herdrSpaceResolveOrAdopt(ctx, svc, storeRoot, refOrLabel)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-open-pane: " + err.Error(), Err: err}
	}
	if adopted {
		herdrAdoptNotice(b)
	}

	// A binding adopted from a sandbox created outside herdr has no workspace
	// yet. Mint one now — this is the only subcommand that needs one.
	b, rootPaneID, err := herdrSpaceEnsureWorkspace(ctx, svc, storeRoot, herdrBin, b)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-open-pane: " + err.Error(), Err: err}
	}

	// rootPaneID is only non-empty when this call just minted the workspace.
	// The guest pane is split beside the root pane (--placement split
	// --target-pane <root> --direction right); the root pane is then closed
	// to leave the workspace guest-only. A reused workspace has no fresh root
	// pane id, so this falls back to --workspace — see herdrOpenGuestShellPane.
	//
	// herdrPluginSpaceOpenPane is invoked interactively by a human (the
	// "space-open-pane" subcommand) — focus stays true, matching today's
	// focus-stealing behaviour, since that is what the user asked for by
	// running the command. A future programmatic/bulk caller (N-way
	// spawning) should call herdrOpenGuestShellPane directly with
	// focus=false rather than going through this human-facing entrypoint.
	paneID, err := herdrOpenGuestShellPane(ctx, herdrBin, b.SandboxHandle, b.HerdrWorkspaceID, rootPaneID, true)
	if err != nil {
		return err
	}
	// Guest pane is open. Close the root host pane we were split beside.
	// A close failure is cosmetic: the sandbox is fully operational, so log
	// a warning and continue rather than failing space-open-pane.
	herdrCloseRootPane(ctx, herdrBin, "space-open-pane", rootPaneID)
	b.GuestPaneID = paneID
	if err := HerdrSpacePut(ctx, storeRoot, b); err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-open-pane: store binding: " + err.Error(), Err: err}
	}
	fmt.Fprintf(w, "opened pane: pane_id=%s\n", paneID)
	return nil
}

// herdrPluginNewTab implements `herdr new-tab <workspace-id>`.
//
// It is designed to be wired to a key that the operator has overridden globally
// in herdr. Three paths:
//
//   - Binding found → open an additional guest-shell pane in the nexus3 space,
//     reusing herdrPluginSpaceOpenPane so the behaviour is identical to the
//     "open-guest-pane" action.
//   - Not found → fall through to herdr's built-in: `herdr tab create
//     --workspace <id> --focus`. This is what makes a global override safe in
//     non-nexus3 workspaces (the operator's own
//     nexus3 workspace that carries no binding).
//   - Lookup fails for a reason other than "not found" (transient store error)
//     → log a warning and fall through to the host-tab path. The operator
//     pressed new-tab and must get a tab; degrading to herdr's default is
//     recoverable, erroring out is not.
func herdrPluginNewTab(ctx context.Context, workspaceID, storeRoot string, svc herdrAdoptGetter, w io.Writer) error {
	herdrBin, binErr := resolveHerdrBin()

	// Look up a nexus3 binding for this herdr workspace ID.
	// herdrSpaceResolve's last-resort scan matches HerdrWorkspaceID, so
	// passing a workspace ID here works without a dedicated index.
	_, lookupErr := herdrSpaceResolve(ctx, storeRoot, workspaceID)
	if lookupErr != nil && !errors.Is(lookupErr, ErrHerdrSpaceNotFound) {
		// Transient store error: log and degrade to host-tab. The operator
		// pressed new-tab and must get a tab; degrading is recoverable,
		// erroring is not.
		slog.Warn("new-tab: binding lookup failed; falling back to host tab",
			"workspace_id", workspaceID, "err", lookupErr)
		lookupErr = ErrHerdrSpaceNotFound
	}

	if lookupErr == nil {
		// Binding found: open an additional guest-shell pane in this space.
		// Pass the workspace ID — herdrPluginSpaceOpenPane's internal resolve
		// finds it via the same HerdrWorkspaceID scan.
		return herdrPluginSpaceOpenPane(ctx, workspaceID, storeRoot, svc, w)
	}

	// No binding (or lookup error degraded above): fall through to herdr's
	// normal new-tab behaviour.
	if binErr != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "new-tab: herdr not found: " + binErr.Error(), Err: binErr}
	}
	tabCmd := herdrExecCommandContext(ctx, herdrBin, "tab", "create", "--workspace", workspaceID, "--focus")
	tabCmd.Stdout = w
	tabCmd.Stderr = os.Stderr
	if err := tabCmd.Run(); err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "new-tab: herdr tab create: " + err.Error(), Err: err}
	}
	return nil
}

// herdrSpaceResolve looks up a binding by label, sandbox handle, derived label, or workspace ID.
func herdrSpaceResolve(ctx context.Context, storeRoot, key string) (HerdrSpaceBinding, error) {
	if b, err := HerdrSpaceGetByLabel(ctx, storeRoot, key); err == nil {
		return b, nil
	}
	if b, err := HerdrSpaceGetByHandle(ctx, storeRoot, key); err == nil {
		return b, nil
	}
	if b, err := HerdrSpaceGetByLabel(ctx, storeRoot, herdrSpaceLabelForRef(key)); err == nil {
		return b, nil
	}
	// Fall through: scan list for matching HerdrWorkspaceID (used by actions passing HERDR_WORKSPACE_ID).
	all, err := HerdrSpaceList(ctx, storeRoot)
	if err != nil {
		return HerdrSpaceBinding{}, err
	}
	for _, b := range all {
		if b.HerdrWorkspaceID == key {
			return b, nil
		}
	}
	return HerdrSpaceBinding{}, ErrHerdrSpaceNotFound
}

// sandboxGetter is the subset of *service.Service used by herdrShellCwd.
type sandboxGetter interface {
	Get(ctx context.Context, ref string) (domain.Sandbox, error)
}

// herdrSpaceCreateSvc is the subset of *service.Service used by
// herdrPluginSpaceCreate. Extracted as an interface so tests can inject a fake
// without spinning up a real sandbox service.
type herdrSpaceCreateSvc interface {
	Start(ctx context.Context, ref string) (domain.Sandbox, error)
	List(ctx context.Context) ([]domain.Sandbox, error)
	Get(ctx context.Context, ref string) (domain.Sandbox, error)
}

// herdrShellCwd returns the guest working directory for the shell pane.
// Priority: first LiveMount GuestPath, then first MountedVolume GuestPath,
// fallback /root. Never fails — on any error returns /root.
func herdrShellCwd(ctx context.Context, ref string, svc sandboxGetter) string {
	sb, err := svc.Get(ctx, ref)
	if err != nil {
		return "/root"
	}
	for _, m := range sb.LiveMounts {
		if m.GuestPath != "" {
			return m.GuestPath
		}
	}
	for _, v := range sb.MountedVolumes {
		if v.GuestPath != "" {
			return v.GuestPath
		}
	}
	return "/root"
}

// herdrShellHostCwd returns the HOST working directory to pass as
// `herdr workspace create --cwd`, so the workspace's root tab opens rooted in
// the sandbox's own directory instead of inheriting whatever workspace
// happens to be focused on the host at creation time.
//
// Priority: HostPath of the first live mount. MountedVolumes have no host
// path (they are store-backed, not host-shared), so there is no second tier
// here the way herdrShellCwd has one. Guest cwd and host cwd are different
// things — deliberately NOT delegating to herdrShellCwd, since conflating
// them is how the stray-tab defect started. Never fails: on any error, or
// when no host path is known, returns "" so the caller omits --cwd and lets
// herdr fall back to its own default.
func herdrShellHostCwd(ctx context.Context, ref string, svc sandboxGetter) string {
	sb, err := svc.Get(ctx, ref)
	if err != nil {
		return ""
	}
	for _, m := range sb.LiveMounts {
		if m.HostPath != "" {
			return m.HostPath
		}
	}
	return ""
}

// herdrWorkspaceClose runs `herdr workspace close <workspaceID>`.
// A "workspace_not_found" response is treated as success (already closed).
//
// herdrBin == "" returns an error — a close that did not happen is not a
// success; callers that retain the binding on error will keep it available for
// space-prune recovery.
//
// workspaceID == "" returns nil — there is no workspace to close, so the
// caller may safely delete the binding (no live resource was left behind).
func herdrWorkspaceClose(ctx context.Context, herdrBin, workspaceID string) error {
	if herdrBin == "" {
		return errors.New("herdr workspace close: herdr binary not available (HERDR_BIN_PATH unset and herdr not on PATH)")
	}
	if workspaceID == "" {
		return nil
	}
	out, err := exec.CommandContext(ctx, herdrBin, "workspace", "close", workspaceID).CombinedOutput()
	if err == nil {
		return nil
	}
	if strings.Contains(string(out), "workspace_not_found") {
		return nil
	}
	return fmt.Errorf("herdr workspace close %q: %w: %s", workspaceID, err, strings.TrimSpace(string(out)))
}

// herdrPluginSpaceList prints all space bindings.
func herdrPluginSpaceList(ctx context.Context, w io.Writer, storeRoot string) error {
	bindings, err := HerdrSpaceList(ctx, storeRoot)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-list: " + err.Error(), Err: err}
	}
	if len(bindings) == 0 {
		fmt.Fprintln(w, "(no herdr space bindings)")
		return nil
	}
	for _, b := range bindings {
		fmt.Fprintf(w, "label=%s\tworkspace_id=%s\thandle=%s\tsandbox_id=%s\tpane_id=%s\n",
			b.SpaceLabel, b.HerdrWorkspaceID, b.SandboxHandle, b.SandboxID, b.GuestPaneID)
	}
	return nil
}

// herdrPluginBackfillRepoRoot implements __herdr-plugin backfill-repo-root
// (exposed as `nexus3 herdr backfill-repo-root`).
//
// It calls `herdr workspace list` once and fills RepoRoot in every binding
// whose RepoRoot is currently empty. Non-empty RepoRoot values are never
// overwritten. Bindings whose workspace herdr does not report are skipped.
// The update is applied immediately (no --apply flag required) because it only
// fills empty fields and cannot overwrite operator-supplied values.
func herdrPluginBackfillRepoRoot(ctx context.Context, storeRoot string, w io.Writer) error {
	herdrBin, err := resolveHerdrBin()
	if err != nil {
		return fmt.Errorf("backfill-repo-root: resolve herdr binary: %w", err)
	}
	n, err := HerdrBackfillRepoRoot(ctx, storeRoot, herdrBin, w)
	if err != nil {
		return err
	}
	if n == 0 {
		fmt.Fprintln(w, "backfill-repo-root: nothing to update")
	} else {
		fmt.Fprintf(w, "backfill-repo-root: updated %d binding(s)\n", n)
	}
	return nil
}

// herdrPluginSpacePrune implements __herdr-plugin space-prune [--apply].
// It reads all herdr-space bindings and removes entries whose sandbox no longer
// exists in the store, or whose herdr workspace no longer exists.
// Dry-run by default; --apply required to delete anything.
func herdrPluginSpacePrune(ctx context.Context, args []string, w io.Writer, svc herdrSpacePruneLister, storeRoot, herdrBin string) error {
	fs := flag.NewFlagSet("space-prune", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "delete stale bindings (default: dry-run)")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "__herdr-plugin space-prune: " + err.Error()}
	}
	if fs.NArg() > 0 {
		return &UsageError{Msg: fmt.Sprintf("__herdr-plugin space-prune: unexpected argument %q; usage: space-prune [--apply]", fs.Arg(0))}
	}
	// Refuse --apply when herdr is unavailable: the workspace-exists predicate
	// would fail safe to all-alive (exec error → treat all alive), but a binding
	// whose sandbox is gone would still be pruned with its workspace unclosed and
	// unverifiable. Dry-run may still report (its predicate already fails safe).
	if *apply && herdrBin == "" {
		return &UsageError{Msg: "__herdr-plugin space-prune: --apply refused: herdr binary not found " +
			"(HERDR_BIN_PATH is unset and no \"herdr\" binary is on PATH); " +
			"install herdr or set HERDR_BIN_PATH and retry"}
	}
	sandboxExists := herdrSpacePruneSandboxExistsFn(ctx, svc)
	workspaceExists := herdrSpacePruneWorkspaceExistsFn(ctx, herdrBin)
	closer := func(ctx context.Context, workspaceID string) error {
		return herdrWorkspaceClose(ctx, herdrBin, workspaceID)
	}
	// removeSandbox is backed by svc.Remove when svc implements the full
	// sandbox service (always true in production — *service.Service does).
	// A type assertion is used so herdrSpacePruneLister stays minimal (List
	// only) and test fakes do not need to implement Remove.
	var removeSandbox func(context.Context, string) error
	if rem, ok := svc.(HerdrSpaceSandboxService); ok {
		removeSandbox = func(ctx context.Context, handle string) error {
			return rem.Remove(ctx, handle)
		}
	} else {
		removeSandbox = func(_ context.Context, handle string) error {
			return fmt.Errorf("space-prune: removeSandbox not available (svc does not implement HerdrSpaceSandboxService)")
		}
	}
	return herdrSpacePruneFull(ctx, w, storeRoot, herdrBin, sandboxExists, workspaceExists, closer, removeSandbox, *apply)
}

// herdrSpacePruneFull is the testable core of space-prune.
// sandboxExists(b) returns true when the sandbox recorded in b is still alive.
// workspaceExists(b) returns true when the herdr workspace recorded in b is
// still alive.  A binding is pruned when either returns false.
// closer is called for stale workspaces before the binding is deleted.
// If closer returns an error the binding is RETAINED for the next run —
// a close that did not happen must not authorise deleting the workspace
// record. The deleted count reported reflects ACTUAL deletions, not
// stale classifications.
//
// removeSandbox(ctx, handle) removes the sandbox VM for auto-bound worktree
// sandboxes (b.IsWorktreeManaged()) whose workspace is gone but VM is still
// running.  It is NEVER called for non-worktree handles or when the sandbox
// is already absent.  A removeSandbox failure retains the binding for the
// next prune run.
func herdrSpacePruneFull(
	ctx context.Context,
	w io.Writer,
	storeRoot string,
	herdrBin string,
	sandboxExists func(HerdrSpaceBinding) bool,
	workspaceExists func(HerdrSpaceBinding) bool,
	closer func(context.Context, string) error,
	removeSandbox func(context.Context, string) error,
	apply bool,
) error {
	bindings, err := HerdrSpaceList(ctx, storeRoot)
	if err != nil {
		return fmt.Errorf("space-prune: read bindings: %w", err)
	}

	var stale []HerdrSpaceBinding
	var keep []HerdrSpaceBinding
	for _, b := range bindings {
		if !sandboxExists(b) || !workspaceExists(b) {
			stale = append(stale, b)
		} else {
			keep = append(keep, b)
		}
	}

	mode := "dry-run"
	if apply {
		mode = "apply"
	}
	fmt.Fprintf(w, "Prune report (%s): %d stale binding(s), %d to keep\n", mode, len(stale), len(keep))
	for _, b := range stale {
		sbPresent := sandboxExists(b)
		wsPresent := workspaceExists(b)
		if b.IsWorktreeManaged() && sbPresent && !wsPresent {
			fmt.Fprintf(w, "  STALE  sandbox=%s  workspace=%s  (worktree VM would be reaped)\n", b.SandboxHandle, b.HerdrWorkspaceID)
		} else if sbPresent && !wsPresent {
			fmt.Fprintf(w, "  STALE  sandbox=%s  workspace=%s  (workspace-id would be cleared)\n", b.SandboxHandle, b.HerdrWorkspaceID)
		} else {
			fmt.Fprintf(w, "  STALE  sandbox=%s  workspace=%s\n", b.SandboxHandle, b.HerdrWorkspaceID)
		}
	}

	if len(stale) == 0 {
		return nil
	}
	if !apply {
		fmt.Fprintln(w, "Run with --apply to delete.")
		return nil
	}

	deleted := 0
	for _, b := range stale {
		sbPresent := sandboxExists(b)
		wsPresent := workspaceExists(b)

		// Case: sandbox running, workspace gone, non-worktree → clear stale workspace
		// ID and keep the binding so the next space-create can mint a fresh workspace.
		if sbPresent && !wsPresent && !b.IsWorktreeManaged() {
			if err := herdrSpaceBindingClearWorkspaceID(ctx, storeRoot, b.SpaceLabel); err != nil {
				slog.Warn("space-prune: clear stale workspace-id failed; binding retained",
					"label", b.SpaceLabel, "err", err)
				continue
			}
			fmt.Fprintf(w, "  CLEARED workspace-id for sandbox=%s (sandbox running, workspace gone)\n", b.SandboxHandle)
			deleted++ // counts as reconciled
			continue
		}

		// Case: worktree sandbox running, workspace gone → reap VM then delete binding.
		if b.IsWorktreeManaged() && sbPresent && !wsPresent {
			if err := removeSandbox(ctx, b.SandboxHandle); err != nil {
				slog.Warn("space-prune: reap worktree sandbox failed; binding retained for next run",
					"handle", b.SandboxHandle, "err", err)
				continue
			}
			fmt.Fprintf(w, "  REAPED sandbox=%s (workspace gone)\n", b.SandboxHandle)
		}

		// All other stale cases (both absent, or sandbox absent): close workspace + delete binding.
		if err := closer(ctx, b.HerdrWorkspaceID); err != nil {
			slog.Warn("space-prune: close workspace failed; binding retained for next run",
				"workspace_id", b.HerdrWorkspaceID, "err", err)
			continue
		}
		if err := HerdrSpaceDelete(ctx, storeRoot, b.SpaceLabel); err != nil && !errors.Is(err, ErrHerdrSpaceNotFound) {
			slog.Warn("space-prune: delete binding failed",
				"label", b.SpaceLabel, "err", err)
			continue
		}
		deleted++
	}
	fmt.Fprintf(w, "Deleted %d stale binding(s).\n", deleted)
	if apply {
		herdrSpaceSweepOrphanWorkspaces(ctx, w, storeRoot, herdrBin, closer)
	}
	return nil
}

// herdrSpaceSweepOrphanWorkspaces closes herdr workspaces whose label starts
// with "nexus3:" but have no corresponding binding. These are created when
// herdrSpaceEnsureWorkspaceTxn fails after workspace creation (TBD-SHL-7).
// A failure to list workspaces is a no-op (fail-safe).
func herdrSpaceSweepOrphanWorkspaces(ctx context.Context, w io.Writer, storeRoot, herdrBin string, closer func(context.Context, string) error) {
	if herdrBin == "" {
		return
	}
	out, err := herdrExecCommandContext(ctx, herdrBin, "workspace", "list").Output()
	if err != nil {
		slog.Warn("space-prune: orphan sweep: workspace list failed", "err", err)
		return
	}
	var resp struct {
		Result struct {
			Workspaces []struct {
				WorkspaceID string `json:"workspace_id"`
				Label       string `json:"label"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		slog.Warn("space-prune: orphan sweep: parse workspace list", "err", err)
		return
	}
	for _, ws := range resp.Result.Workspaces {
		if !strings.HasPrefix(ws.Label, "nexus3:") {
			continue
		}
		// Derive the expected SpaceLabel and check for a binding.
		if _, err := HerdrSpaceGetByLabel(ctx, storeRoot, ws.Label); err == nil {
			continue // binding exists — not orphaned
		}
		// No binding: close the orphaned workspace.
		if err := closer(ctx, ws.WorkspaceID); err != nil {
			slog.Warn("space-prune: orphan sweep: close workspace failed",
				"workspace_id", ws.WorkspaceID, "label", ws.Label, "err", err)
			continue
		}
		fmt.Fprintf(w, "  ORPHAN closed workspace=%s label=%s\n", ws.WorkspaceID, ws.Label)
	}
}

// sealEnv returns a copy of env (key=value pairs) with all HERDR_* entries
// removed. The guest must never receive herdr socket paths or credentials.
//
// Note: for service method calls (Start, Attach, etc.), the service layer
// builds the guest environment from an explicit allow-list and never reads
// os.Environ(). This function covers subprocess forks within the
// __herdr-plugin subcommands themselves.
func sealEnv(env []string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, "HERDR_") {
			out = append(out, kv)
		}
	}
	return out
}

// claudeReadyMatch is the substring in claude's startup banner that signals
// the agent has fully initialised and its input box is live. It appears
// exactly once the prompt is ready for user input.
//
// Why not the prompt glyph (❯)?  ❯ is ALSO the selection glyph inside the
// first-run wizards (theme picker, folder-trust dialog), so matching it would
// report "ready" while claude is still blocking on a wizard — exactly the
// failure this guard is meant to prevent.
//
// Captured 2026-08-21 from guest loop/chain pane w1W:p2, claude v2.1.226.
// claudeReadyMatch returns the literal substring that means claude has
// finished starting and its input box is accepting text, for the permission
// mode it was launched in. Verbatim footers from a live guest pane
// (claude v2.1.226):
//
//	⏸ manual mode on · ? for shortcuts · ← for agents
//	⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
//
// The token is selected by mode rather than searched for, because the caller
// already knows which mode it launched. Three shorter tokens are all wrong,
// and each one cost a live run to find out:
//
//   - "? for shortcuts" holds only in the default mode; under
//     --dangerously-skip-permissions the footer replaces it, so the wait
//     times out against an agent that is already at its prompt.
//   - "for agents" appeared in both footers at first, but the "← for agents"
//     affordance comes and goes with pane state — it was observed absent from
//     a ready pane moments after being present in the same one.
//   - "❯" is the prompt glyph, but it is ALSO the selector glyph in all four
//     first-run wizards, so it reports ready while claude still sits on the
//     theme picker — precisely the failure this wait exists to prevent.
func claudeReadyMatch(autonomous bool) string {
	if autonomous {
		return "shift+tab to cycle"
	}
	return "? for shortcuts"
}

// guestAgentLaunchCommand returns the shell command typed into the guest pane
// to start the agent.
//
// autonomous == true  → "claude"          — the shell function (added by
//
//	SeedGuestShellProfile) supplies --dangerously-skip-permissions, and
//	IS_SANDBOX=1 is exported by the same profile. The flag is correct for an
//	autonomous slice agent whose blast radius is the sandbox.
//
// autonomous == false → "command claude"  — bypasses the shell function so
//
//	--dangerously-skip-permissions is genuinely absent. claude opens in manual
//	mode (footer: "? for shortcuts"), which is exactly what claudeReadyMatch
//	waits for on the non-autonomous path.
//
// The distinction matters because claudeReadyMatch selects its wait token by
// the permission mode claude actually starts in, not by the flag spelling. A
// function-bypassed `command claude` enters manual mode → "? for shortcuts".
// The wrapped `claude` enters bypass mode → "shift+tab to cycle".
// guestAgentLaunchCommand returns the shell command typed into the guest pane
// to start the agent.
//
// Both branches are EXPLICIT and neither relies on the `claude` shell function
// that SeedGuestShellProfile installs. That function exists for humans typing
// in a guest shell; depending on it here would make the launch depend on
// whether the pane's login shell has finished sourcing /etc/profile.d, which
// is not something this code can observe. The flag is idempotent (the function
// does not double it), so passing it explicitly is correct either way.
//
// What was actually OBSERVED, and what is NOT known: launching with a bare
// `claude` failed live — the pane echoed the command and returned immediately
// to a shell prompt, and the readiness wait then timed out. Typing the same
// command by hand a moment later worked. Adding the guest-shell readiness wait
// in herdrPluginSpaceAgent made it reliable. The precise mechanism for the
// immediate exit was never isolated, so this comment does not claim one; the
// explicit flag is defence in depth, and the readiness wait is the fix.
//
// autonomous adds --dangerously-skip-permissions, which makes the agent act
// without stopping to ask the operator to approve each tool call. IS_SANDBOX=1
// is required alongside it: claude refuses the flag when running as root
// (which the guest does) unless that variable marks the environment as
// already-isolated. The non-autonomous branch uses `command claude` to bypass
// the shell function, so the flag is genuinely absent rather than silently
// re-added by the profile.
func guestAgentLaunchCommand(autonomous bool) string {
	if autonomous {
		return "IS_SANDBOX=1 claude --dangerously-skip-permissions"
	}
	return "command claude"
}

// guestShellTimeoutMS bounds the wait for the pane's guest shell to attach.
const guestShellTimeoutMS = 60_000

// claudeReadyTimeoutMS is the wait-output timeout in milliseconds. 90 s gives
// claude enough time to load on a cold guest without blocking the operator
// indefinitely on a hung pane.
const claudeReadyTimeoutMS = 90_000

// briefSettleDelay is how long to wait between placing the brief in claude's
// input box and pressing Enter. See herdrPaneSubmitToAgent.
const briefSettleDelay = 750 * time.Millisecond

// herdrPaneSubmitToAgent puts text into a running agent's input box and then
// submits it, as two calls with a pause between them.
//
// `herdr pane run` — which sends text and Enter in one call — is correct for a
// shell prompt and WRONG here. Observed live: the brief arrived in claude's
// input box and simply sat there unsubmitted; a single Enter sent afterwards
// by hand submitted it and the agent answered immediately. claude's TUI needs
// to finish processing the pasted text before it will treat an Enter as
// "submit" rather than as part of the paste, and `pane run` gives it no gap in
// which to do that.
//
// This is why the step is its own function rather than another herdrPaneRun
// call: the difference is invisible in the argv and only shows up as an agent
// that looks started, looks prompted, and never does anything.
func herdrPaneSubmitToAgent(ctx context.Context, herdrBin, paneID, text string) error {
	cmd := herdrExecCommandContext(ctx, herdrBin, "pane", "send-text", paneID, text)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(briefSettleDelay):
	}

	enter := herdrExecCommandContext(ctx, herdrBin, "pane", "send-keys", paneID, "Enter")
	enter.Stdout = os.Stderr
	enter.Stderr = os.Stderr
	return enter.Run()
}

// herdrPaneRun sends text to a herdr pane and simulates Enter, equivalent to
// the operator typing the text at the pane's prompt.
//
// herdr pane run <paneID> <text> — sends text and Enter in one call.
func herdrPaneRun(ctx context.Context, herdrBin, paneID, text string) error {
	cmd := herdrExecCommandContext(ctx, herdrBin, "pane", "run", paneID, text)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// herdrPaneWaitOutput waits until the pane's output contains match or timeoutMS
// elapses. Returns an error on timeout or subprocess failure.
func herdrPaneWaitOutput(ctx context.Context, herdrBin, paneID, match string, timeoutMS int) error {
	cmd := herdrExecCommandContext(ctx, herdrBin, "pane", "wait-output", paneID,
		"--match", match, "--timeout", strconv.Itoa(timeoutMS))
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// herdrPaneReportAgent registers the claude process running in paneID with
// herdr's agent tracker so it shows in `herdr agent list`. Non-fatal; call
// sites log and continue on error.
func herdrPaneReportAgent(ctx context.Context, herdrBin, paneID, source string) error {
	cmd := herdrExecCommandContext(ctx, herdrBin, "pane", "report-agent", paneID,
		"--source", source, "--agent", "nexus3-slice-agent", "--state", "working")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// herdrAgentEnsureSandboxExists checks whether the sandbox named by ref exists.
// If get returns nil the sandbox is present and the function returns immediately.
// Only a definite store.ErrNotFound (wrapped by service.resolve as %w) causes
// create to be called to build the sandbox from nexus3.yaml. Every other error
// is returned as a CodedError. A transient store failure must not be mistaken
// for absence: falling through would attempt to create a sandbox that already
// exists precisely when the store is least able to say otherwise.
//
// Extracted as a standalone function (with injected dependencies) so the
// create-if-absent branch is testable without a real *service.Service or store.
func herdrAgentEnsureSandboxExists(
	ctx context.Context,
	ref string,
	w io.Writer,
	get func(context.Context, string) (domain.Sandbox, error),
	create func(context.Context, string, io.Writer) error,
) error {
	_, err := get(ctx, ref)
	if err == nil {
		return nil // sandbox already exists
	}
	// Create ONLY on a definite "does not exist". Any other error — a transient
	// store failure, a permissions problem — must propagate. Falling through on
	// every error would attempt to create a sandbox that already exists, and
	// would do so precisely when the store is least able to say otherwise.
	// service.resolve wraps store.ErrNotFound with %w, so errors.Is sees it.
	if !errors.Is(err, store.ErrNotFound) {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  "herdr agent: resolve " + ref + ": " + err.Error(),
			Err:  err,
		}
	}
	fmt.Fprintf(w, "herdr agent: sandbox %q not found; creating from nexus3.yaml ...\n", ref)
	return create(ctx, ref, w)
}

// herdrEnsureFn is the package-level hook called by herdrPluginSpaceAgent to
// check whether the target sandbox exists and create it if not. It is a var so
// that tests can swap it for a recording stub without a real *service.Service.
var herdrEnsureFn = herdrAgentEnsureSandboxExists

// herdrPluginSpaceAgent starts the named sandbox (or resumes it), opens a
// herdr space with a guest shell pane, then launches claude inside that pane
// and delivers brief to it.
//
// The sandbox must have at least one live mount or mounted volume so that the
// nexus3 source is present in the guest. If herdrShellCwd returns /root (no
// mount), the function refuses with an actionable error naming the flag the
// operator should have passed.
// herdrSpaceAgentProjectDir resolves the guest directory an agent should work
// in, or returns a *UsageError explaining why this sandbox cannot host one.
//
// It exists as its own function, taking the narrow sandboxGetter rather than
// *service.Service, so both refusals are reachable from a unit test with a
// stub. Inlined in herdrPluginSpaceAgent they were not: the function needs a
// real *service.Service and therefore a real store, and the test that tried
// ended up asserting herdrShellCwd's behaviour instead of the guard's.
//
// The two refusals are kept distinct on purpose. herdrShellCwd never fails and
// answers "/root" both for a sandbox with no mount and for a ref that does not
// resolve at all; collapsing them tells an operator who mistyped a handle to
// go re-create a sandbox that was never the problem.
func herdrSpaceAgentProjectDir(ctx context.Context, ref string, svc sandboxGetter) (string, error) {
	if _, getErr := svc.Get(ctx, ref); getErr != nil {
		return "", &UsageError{
			Msg: fmt.Sprintf("space-agent: no such sandbox %q: %v (list them with: nexus3 sandbox list)", ref, getErr),
		}
	}
	projectDir := herdrShellCwd(ctx, ref, svc)
	if projectDir == "/root" {
		return "", &UsageError{
			Msg: fmt.Sprintf("space-agent: sandbox %q has no mounted source directory, so an agent "+
				"started in it would have nothing to work on; re-create it with: "+
				"nexus3 create --mount <host-path>:<guest-path> %s", ref, ref),
		}
	}
	return projectDir, nil
}

func herdrPluginSpaceAgent(ctx context.Context, ref, brief string, autonomous, focus bool, w io.Writer, svc *service.Service, storeRoot string) error {
	// 0. Ensure the sandbox exists. If it has never been created, build it now
	//    from nexus3.yaml (same precedence rules as `sandbox create`). This runs
	//    before step 1 so herdrSpaceAgentProjectDir sees an existing record.
	if err := herdrEnsureFn(ctx, ref, w,
		func(ctx context.Context, r string) (domain.Sandbox, error) { return svc.Get(ctx, r) },
		func(ctx context.Context, r string, w io.Writer) error {
			out := NewOutput(w, w, false)
			return runSandboxCreate(ctx, []string{r}, out, svc)
		},
	); err != nil {
		return err
	}

	// 1. Check for a mounted source BEFORE starting the sandbox. Failing fast
	//    here avoids a started-but-useless sandbox and gives a clear message.
	//
	//    Resolve the sandbox first rather than relying on herdrShellCwd alone:
	//    herdrShellCwd never fails, and returns "/root" both for a sandbox with
	//    no mount AND for a ref that does not resolve at all. Collapsing those
	//    two cases would answer "your sandbox has no mounted source" to an
	//    operator who simply mistyped the handle, sending them to re-create a
	//    sandbox that was never the problem.
	if _, err := herdrSpaceAgentProjectDir(ctx, ref, svc); err != nil {
		return err
	}

	// 2. Start the sandbox and open/reuse the herdr workspace with its guest shell pane.
	fmt.Fprintf(w, "space-agent: opening space for %q ...\n", ref)
	if err := herdrPluginSpaceCreate(ctx, ref, w, svc, storeRoot, focus); err != nil {
		return err
	}

	// 3. Read the binding back to get the guest pane ID.
	label := herdrSpaceLabelForRef(ref)
	binding, err := HerdrSpaceGetByLabel(ctx, storeRoot, label)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError,
			Msg: "space-agent: read binding after space-create: " + err.Error(), Err: err}
	}
	paneID := binding.GuestPaneID
	if paneID == "" {
		return &CodedError{Code: ErrCodeInternalError,
			Msg: "space-agent: guest pane ID not recorded; space-create may have partially failed"}
	}

	herdrBin, err := resolveHerdrBin()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-agent: " + err.Error(), Err: err}
	}

	// 4. (Bypass-permissions consent is now seeded at boot by SeedGuestBypassConsent
	//    in probeAndSeedGuest, alongside the onboarding and shell-profile seeds.
	//    The shell-function `claude` always adds --dangerously-skip-permissions,
	//    so pre-answering at boot is correct and no per-launch seed is needed.)

	// 5. Wait for the guest shell itself before typing at it.
	//
	//    The pane runs `nexus3 exec --pty` and only then attaches a shell in
	//    the guest; keystrokes sent before that attaches go nowhere, and the
	//    launch silently does not happen. Waiting on the guest hostname in the
	//    prompt is what distinguishes the guest shell from the host pane the
	//    plugin was opened from.
	guestPrompt := sandboxHandleHostname(ref)
	fmt.Fprintf(w, "space-agent: waiting for the guest shell (match=%q) ...\n", guestPrompt)
	if err := herdrPaneWaitOutput(ctx, herdrBin, paneID, guestPrompt, guestShellTimeoutMS); err != nil {
		return &CodedError{Code: ErrCodeInternalError,
			Msg: fmt.Sprintf("space-agent: guest shell did not appear in pane %s within %ds: %v",
				paneID, guestShellTimeoutMS/1000, err), Err: err}
	}

	// 6. Launch claude in the guest shell pane.
	fmt.Fprintf(w, "space-agent: launching %s in pane %s ...\n", guestAgentLaunchCommand(autonomous), paneID)
	if err := herdrPaneRun(ctx, herdrBin, paneID, guestAgentLaunchCommand(autonomous)); err != nil {
		return &CodedError{Code: ErrCodeInternalError,
			Msg: "space-agent: launch claude: " + err.Error(), Err: err}
	}

	// 7. Wait for the claude prompt. See claudeReadyMatch for why this token
	//    and not the ❯ glyph.
	readyMatch := claudeReadyMatch(autonomous)
	fmt.Fprintf(w, "space-agent: waiting for claude prompt (match=%q, timeout=%ds) ...\n",
		readyMatch, claudeReadyTimeoutMS/1000)
	if err := herdrPaneWaitOutput(ctx, herdrBin, paneID, readyMatch, claudeReadyTimeoutMS); err != nil {
		return &CodedError{Code: ErrCodeInternalError,
			Msg: fmt.Sprintf("space-agent: claude did not reach its prompt within %ds: %v",
				claudeReadyTimeoutMS/1000, err), Err: err}
	}

	// 8. Deliver the slice brief.
	fmt.Fprintf(w, "space-agent: delivering brief ...\n")
	if err := herdrPaneSubmitToAgent(ctx, herdrBin, paneID, brief); err != nil {
		return &CodedError{Code: ErrCodeInternalError,
			Msg: "space-agent: deliver brief: " + err.Error(), Err: err}
	}

	// 9. Report the agent in herdr's agent tracker (non-fatal).
	if err := herdrPaneReportAgent(ctx, herdrBin, paneID, ref); err != nil {
		fmt.Fprintf(w, "space-agent: warning: report-agent failed: %v (continuing)\n", err)
	}

	fmt.Fprintf(w, "space-agent: agent running in pane %s for %q\n", paneID, ref)
	return nil
}

// herdrPluginSpaceAgentFromFile is the interactive stdin-based variant of
// herdrPluginSpaceAgent. It prompts for the sandbox ref (defaulting to
// NEXUS3_WORKSPACE or the sandbox bound to HERDR_WORKSPACE_ID) and the
// slice brief, then delegates to herdrPluginSpaceAgent.
func herdrPluginSpaceAgentFromFile(ctx context.Context, r io.Reader, w io.Writer, svc *service.Service, storeRoot string) error {
	scanner := bufio.NewScanner(r)

	// Resolve a sensible default sandbox ref: NEXUS3_WORKSPACE env var first,
	// then the sandbox bound to the currently-focused herdr workspace.
	defaultRef := os.Getenv("NEXUS3_WORKSPACE")
	if defaultRef == "" {
		if wsID := os.Getenv("HERDR_WORKSPACE_ID"); wsID != "" {
			if b, err := herdrSpaceResolve(ctx, storeRoot, wsID); err == nil {
				defaultRef = b.SandboxHandle
			}
		}
	}

	fmt.Fprintf(os.Stderr, "sandbox ref [%s]: ", defaultRef)
	if !scanner.Scan() {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-agent-from-file: failed to read sandbox ref"}
	}
	ref := strings.TrimSpace(scanner.Text())
	if ref == "" {
		ref = defaultRef
	}
	if ref == "" {
		return &UsageError{Msg: "space-agent-from-file: sandbox ref required (set NEXUS3_WORKSPACE or pass as argument)"}
	}

	fmt.Fprintf(os.Stderr, "slice brief: ")
	if !scanner.Scan() {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-agent-from-file: failed to read brief"}
	}
	brief := strings.TrimSpace(scanner.Text())
	if brief == "" {
		return &UsageError{Msg: "space-agent-from-file: brief must not be empty"}
	}

	// Autonomy is asked, not assumed. See guestAgentLaunchCommand for why this
	// is a per-invocation decision rather than a product default. Default is
	// no: the operator must type y, and an empty line (just pressing Enter)
	// selects the safe answer.
	fmt.Fprintf(os.Stderr, "run autonomously, without asking approval for each tool call? [y/N]: ")
	autonomous := false
	if scanner.Scan() {
		switch strings.ToLower(strings.TrimSpace(scanner.Text())) {
		case "y", "yes":
			autonomous = true
		}
	}

	return herdrPluginSpaceAgent(ctx, ref, brief, autonomous, true, w, svc, storeRoot)
}

// ── worktree-sandbox helpers ──────────────────────────────────────────────────

// herdrWorktreeInfo holds the information extracted from `herdr worktree list`
// for one workspace entry in the worktree list response.
type herdrWorktreeInfo struct {
	Branch            string
	Path              string
	IsLinkedWorktree  bool
	SourceWorkspaceID string
	// RepoKey identifies the repository (source.repo_key in the response, e.g.
	// "/repo/.git"). The parent directory of RepoKey is the main repo root used
	// by herdrRepoHasBoundSandbox (predicate c); an empty value is unknown.
	RepoKey string
}

// herdrRepoHasBoundSandbox is THE SINGLE MECHANISM for the question
// "does this repo have at least one nexus3-bound sandbox?"
//
// It returns true when at least one binding's RepoRoot equals mainRepo
// (after filepath.Clean normalisation). An empty mainRepo or an empty
// binding RepoRoot is NEVER a match — empty RepoRoot means a legacy binding
// that pre-dates repo tracking, and must fail toward the host shell.
//
// Both the dispatcher (herdrAutoCreatePredicateWith) and the subprocess
// (herdrWorktreeSandboxRepoCheck) call this function. Do not duplicate the
// comparison logic. Enforced by TestHerdrRepoHasBoundSandbox_BothCallSitesAgree.
func herdrRepoHasBoundSandbox(mainRepo string, bindings []HerdrSpaceBinding) bool {
	if mainRepo == "" {
		return false
	}
	clean := filepath.Clean(mainRepo)
	for _, b := range bindings {
		if b.RepoRoot == "" {
			continue
		}
		if filepath.Clean(b.RepoRoot) == clean {
			return true
		}
	}
	return false
}

// herdrWorktreeSandboxRepoCheck reports whether the repo identified by
// info.RepoKey has at least one binding in storeRoot whose RepoRoot matches.
//
// Predicate (c) of the auto-bind rule. Derives the main repo root from
// info.RepoKey (parent dir of the .git path) and delegates to
// herdrRepoHasBoundSandbox. If info.RepoKey is empty or bindings cannot be
// read, returns false (fail toward host shell, never guess).
func herdrWorktreeSandboxRepoCheck(ctx context.Context, storeRoot string, info herdrWorktreeInfo) bool {
	if info.RepoKey == "" {
		return false
	}
	mainRepo := filepath.Dir(filepath.Clean(info.RepoKey))
	bindings, err := HerdrSpaceList(ctx, storeRoot)
	if err != nil {
		return false
	}
	return herdrRepoHasBoundSandbox(mainRepo, bindings)
}

// herdrWorktreeListTimeout bounds the `herdr worktree list` probe in step 3.
// A hung herdr daemon must not wedge every new pane on the machine.
const herdrWorktreeListTimeout = 2 * time.Second

// herdrWorktreeCreateTimeout bounds the sandbox create call in step 7.
// Sandbox creation involves image pull, ext4 setup, and VM boot — 90 s is
// generous for typical fast hardware with a warm image cache.
const herdrWorktreeCreateTimeout = 90 * time.Second

// herdrWorktreeCreateLockTimeout bounds how long the second concurrent caller
// waits for the per-handle create-intent lock.  120 s > herdrWorktreeCreateTimeout
// (90 s) so the first caller always finishes before the waiter gives up.
const herdrWorktreeCreateLockTimeout = 120 * time.Second

// herdrWorktreeCreateLockPath returns the path to the per-handle create-intent
// lock file.  The lock serialises concurrent auto-create attempts for the same
// sandbox handle (e.g. two panes opening in the same worktree workspace within
// the ~60 s create window).
//
// Safe filename: "/" → "_".  After herdrWorktreeSandboxHandle's sanitisation,
// handles contain only [A-Za-z0-9._-/] with at most one "/", so this mapping
// is collision-free.
func herdrWorktreeCreateLockPath(storeRoot, handle string) string {
	safe := strings.ReplaceAll(handle, "/", "_")
	return filepath.Join(storeRoot, "herdr-wt-create-"+safe+".lock")
}

// herdrListWorktreeForWorkspaceFn is the injectable function for listing worktrees.
// Replaced in tests to avoid calling the live herdr binary.
var herdrListWorktreeForWorkspaceFn = herdrListWorktreeForWorkspace

// herdrWorkspaceRenameFn is the injectable function for renaming a herdr workspace.
// Replaced in tests to avoid calling the live herdr binary.
var herdrWorkspaceRenameFn = herdrWorkspaceRename

// herdrParseWorktreeListForWorkspace parses the JSON response from
// `herdr worktree list --workspace <id>` and returns the
// herdrWorktreeInfo for the entry whose open_workspace_id matches workspaceID.
// Returns an error if workspaceID is not found or the JSON is malformed.
func herdrParseWorktreeListForWorkspace(data []byte, workspaceID string) (herdrWorktreeInfo, error) {
	var resp struct {
		Result struct {
			Source struct {
				SourceWorkspaceID string `json:"source_workspace_id"`
				RepoKey           string `json:"repo_key"`
			} `json:"source"`
			Worktrees []struct {
				Branch           string `json:"branch"`
				IsLinkedWorktree bool   `json:"is_linked_worktree"`
				OpenWorkspaceID  string `json:"open_workspace_id"`
				Path             string `json:"path"`
			} `json:"worktrees"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return herdrWorktreeInfo{}, fmt.Errorf("herdr worktree list: parse response: %w", err)
	}
	sourceWorkspaceID := resp.Result.Source.SourceWorkspaceID
	repoKey := resp.Result.Source.RepoKey
	for _, wt := range resp.Result.Worktrees {
		if wt.OpenWorkspaceID == workspaceID {
			return herdrWorktreeInfo{
				Branch:            wt.Branch,
				Path:              wt.Path,
				IsLinkedWorktree:  wt.IsLinkedWorktree,
				SourceWorkspaceID: sourceWorkspaceID,
				RepoKey:           repoKey,
			}, nil
		}
	}
	return herdrWorktreeInfo{}, fmt.Errorf("herdr worktree list: workspace %q not found in response", workspaceID)
}

// herdrListWorktreeForWorkspace calls `herdr worktree list --workspace <id>` and
// returns the herdrWorktreeInfo for the given workspaceID.
func herdrListWorktreeForWorkspace(ctx context.Context, herdrBin, workspaceID string) (herdrWorktreeInfo, error) {
	cmd := herdrExecCommandContext(ctx, herdrBin, "worktree", "list", "--workspace", workspaceID)
	out, err := cmd.Output()
	if err != nil {
		return herdrWorktreeInfo{}, fmt.Errorf("herdr worktree list: %w", err)
	}
	return herdrParseWorktreeListForWorkspace(out, workspaceID)
}

// herdrWorkspaceRename calls `herdr workspace rename` to update the workspace
// label to label for the given workspaceID.
func herdrWorkspaceRename(ctx context.Context, herdrBin, workspaceID, label string) error {
	cmd := herdrExecCommandContext(ctx, herdrBin, "workspace", "rename", workspaceID, label)
	return cmd.Run()
}

// herdrWorktreeSandboxCreateArgs returns the argument list for
// `nexus3 sandbox create` that creates a worktree sandbox.
//
// imageFlag and imageVal must be exactly one of:
//   - "--image", "<ref>"   — use a pre-built cached image
//   - "--file", "<dir>"    — build from a nexus3.yaml in that directory
//   - "--rootfs", "<path>" — use a raw rootfs (not currently produced by this
//     path, reserved for future use)
//
// Exactly one bootable flag is always present: the caller is responsible for
// resolving imageFlag/imageVal via herdrResolveWorktreeImage before calling
// this function. An empty imageFlag produces an unbootable argv that
// `sandbox create` will reject with exit status 2.
//
// secrets are "--secret ENV@host1,host2" binds derived from the egress.secrets
// section of the nexus3.yaml on the trusted ref (D-PDE-17). allowedRepo, when
// non-empty, sets the per-repo GitHub path allowlist (--repo owner/name).
//
// --agent claude-code --egress open makes a worktree sandbox a full agent dev
// environment: the operator's curated ~/.claude config (skills, CLAUDE.md,
// settings) and MCP server definitions are shared in, and Claude's credential
// is brokered host-side — WHILE keeping open dev egress so npm/apt/registry
// pulls still work. This is the "broad-allow + selective MITM" posture: the
// perimeter forwards every host but MITM-swaps only the credentialed ones
// (anthropic + http-MCP), so a placeholder never reaches the real API and no
// real token lives in the guest. Without --egress open, --agent would narrow
// egress to the agent allowlist (D-PD-33) and break dev tooling; the two flags
// must travel together for a worktree sandbox.
//
// extraMounts is a slice of additional "host:guest[:ro]" mount specs (one
// --mount flag pair per element). For a linked worktree sandbox, it carries
// the main repo's .git directory so git is fully functional inside the guest
// (D-PD-99-git: worktree .git resolution requires the main .git to be
// reachable at its host absolute path inside the VM).
func herdrWorktreeSandboxCreateArgs(handle, mountSpec, imageFlag, imageVal string, extraMounts, secrets []string, allowedRepo string, pathPolicies domain.EgressPathPolicies) []string {
	args := []string{imageFlag, imageVal, "--mount", mountSpec}
	for _, m := range extraMounts {
		args = append(args, "--mount", m)
	}
	// Docker storage disk. A custom .nexus/Containerfile (the --file path) is the
	// ONLY way docker lands in a worktree sandbox — the base image ships none by
	// design — so a base-image sandbox needs no docker disk. When we DO build from
	// a Containerfile, give /var/lib/docker its own ext4 disk: docker's overlay2
	// storage driver cannot run on the virtiofs /workspace mount, and the small
	// root disk fills fast under image layers. The volume is named per-sandbox
	// (from the handle) so two worktree sandboxes of the same repo never contend
	// on one disk under the D-PD-93 attach guard, while a given worktree keeps its
	// docker layer cache across re-creations. A 20 GiB sparse disk costs only the
	// blocks docker actually writes, so it is cheap even when a project's
	// Containerfile turns out not to use docker.
	if imageFlag == "--file" {
		args = append(args, "--mount-named", herdrDockerDiskVolumeName(handle)+":/var/lib/docker:size=20g")
	}
	for _, s := range secrets {
		args = append(args, "--secret", s)
	}
	if allowedRepo != "" {
		args = append(args, "--repo", allowedRepo)
	}
	// Convey generic path policies via --egress-policy-json when present.
	// This is the channel that carries EgressPathPolicies from the herdr
	// worktree path through the `sandbox create` subprocess boundary, replacing
	// the discarded-variable gap (D-PDE-16 worktree path). JSON round-trips the
	// exact policy including method-prefixed globs.
	if len(pathPolicies) > 0 {
		ppJSON, err := json.Marshal(pathPolicies)
		if err == nil {
			args = append(args, "--egress-policy-json", string(ppJSON))
		}
	}
	args = append(args, "--agent", "claude-code", "--egress", "open", handle)
	return args
}

// herdrDockerDiskVolumeName derives a VolumeStore-legal name
// ([a-z0-9][a-z0-9._-]*, per D-PD-84) for a worktree sandbox's docker storage
// disk from its handle. The handle is "<repo>/<branch>" with case preserved and
// a "/" separator, neither of which the grammar allows, so this lower-cases,
// maps every out-of-grammar byte (including "/") to a single "-", trims to a
// legal leading char, and appends "-docker".
func herdrDockerDiskVolumeName(handle string) string {
	var b strings.Builder
	prev := byte('-')
	for i := 0; i < len(handle); i++ {
		c := handle[i]
		switch {
		case c >= 'A' && c <= 'Z':
			lc := c + ('a' - 'A')
			b.WriteByte(lc)
			prev = lc
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '.' || c == '_':
			b.WriteByte(c)
			prev = c
		default: // '/', '-', whitespace, anything else → collapse to one '-'
			if prev != '-' {
				b.WriteByte('-')
				prev = '-'
			}
		}
	}
	// First char must be [a-z0-9]; trim any leading '.', '_', '-' the grammar
	// forbids in that position, plus trailing separators for tidiness.
	slug := strings.Trim(b.String(), "-._")
	if slug == "" {
		slug = "wt"
	}
	return slug + "-docker"
}

// herdrWorktreeGitDirMount returns the extra --mount spec needed to make git
// functional inside a linked-worktree sandbox, or "" if it cannot be derived.
//
// A linked worktree's checkout/.git is a file containing:
//
//	gitdir: <main>/.git/worktrees/<name>
//
// Inside the guest, the checkout is at /workspace but the gitdir pointer
// still holds the host absolute path. Mounting <main>/.git at its host
// path inside the VM makes all three legs of the resolution chain reachable:
//
//	/workspace/.git → gitdir file → <main>/.git/worktrees/<name>/
//	                                commondir → ../.. → <main>/.git ✓
//
// The returned spec is "<mainGitDir>:<mainGitDir>" (same path host and guest).
// Read-write so `git commit` can write new objects and update refs.
func herdrWorktreeGitDirMount(worktreePath string) string {
	data, err := os.ReadFile(filepath.Join(worktreePath, ".git"))
	if err != nil {
		return "" // not a linked worktree or unreadable
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir: "
	if !strings.HasPrefix(line, prefix) {
		return "" // main checkout (directory) or malformed file
	}
	target := strings.TrimPrefix(line, prefix)
	// target = <commondir>/worktrees/<name>. git normally writes an absolute
	// path, but a relative gitdir: pointer is valid and is resolved against the
	// directory holding the .git file. Normalise to an absolute host path so
	// the mount spec is never a relative "<rel>:<rel>".
	if !filepath.IsAbs(target) {
		target = filepath.Join(worktreePath, target)
	}
	target = filepath.Clean(target)
	worktreesDir := filepath.Dir(target) // <commondir>/worktrees
	gitDir := filepath.Dir(worktreesDir) // <commondir> (normally <main>/.git)
	// The "worktrees" segment is the reliable structural marker git guarantees;
	// the parent's name is NOT (a bare repo's common dir is "<name>.git", a
	// non-bare repo's is ".git"). Anchor on "worktrees" only, then mount the
	// common dir at its host path so commondir/../.. resolution succeeds.
	if filepath.Base(worktreesDir) != "worktrees" {
		return "" // unexpected structure; bail rather than mount the wrong dir
	}
	return gitDir + ":" + gitDir
}

// worktreeCommonGitDir returns the main repo's common git directory from a
// linked worktree path, or "" if it cannot be derived. This is the same
// directory computed by herdrWorktreeGitDirMount but returned as a plain path
// rather than a mount spec.
func worktreeCommonGitDir(worktreePath string) string {
	data, err := os.ReadFile(filepath.Join(worktreePath, ".git"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir: "
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	target := strings.TrimPrefix(line, prefix)
	if !filepath.IsAbs(target) {
		target = filepath.Join(worktreePath, target)
	}
	target = filepath.Clean(target)
	worktreesDir := filepath.Dir(target)
	gitDir := filepath.Dir(worktreesDir)
	if filepath.Base(worktreesDir) != "worktrees" {
		return ""
	}
	return gitDir
}

// readTrustedRefBytes reads the nexus3.yaml content from the operator-controlled
// trusted ref in commonGitDir. The trusted ref is the origin default branch
// (refs/remotes/origin/HEAD), never the worktree's checked-out branch.
//
// Returns (data, nil) when the file exists on the trusted ref.
// Returns (nil, nil) — FAIL CLOSED — in all non-error conditions where the
// config cannot be read:
//   - no origin/HEAD (remote not fetched or no origin configured)
//   - nexus3.yaml absent on the trusted ref
//
// Returns (nil, err) only for unexpected git failures that are not simply
// "ref or file not found".
func readTrustedRefBytes(commonGitDir string) ([]byte, error) {
	// Resolve the origin default branch.
	out, err := worktreeGitRunner(commonGitDir, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err != nil || len(bytes.TrimSpace(out)) == 0 {
		// No origin/HEAD → fail closed; no auto-grant.
		return nil, nil
	}
	ref := strings.TrimSpace(string(out))

	// Read nexus3.yaml from the trusted ref — NEVER from the worktree branch.
	data, err := worktreeGitRunner(commonGitDir, "show", ref+":nexus3.yaml")
	if err != nil {
		// File absent on trusted ref → fail closed; no auto-grant.
		return nil, nil
	}
	return data, nil
}

// buildWorktreeEgressArgs derives the --secret and --repo CLI args from the
// egress.policy and egress.secrets sections of the nexus3.yaml read from the
// trusted ref.
//
// Algorithm (three steps):
//
//  1. Build pathPolicies and allowedRepo from egress.policy:
//     - preset:github(+repo) → EgressGitHubPolicy keyed under [""][host].
//       repo is derived from the origin remote when absent.
//     - paths → EgressHostPolicy{Paths} keyed under [""][host].
//
//  2. Build secrets from egress.secrets (env→hosts only; no path info).
//
//  3. D-PDE-16 check: every GitHub host in a secret bind must be covered
//     by a PathPolicies entry or AllowedRepo. Non-GitHub hosts are exempt.
//
// T3 hardening: derived or explicit owner/name equal to "." or ".." is rejected.
// buildWorktreeEgressArgs derives the --secret and --repo CLI args from the
// egress.policy and egress.secrets sections of the nexus3.yaml read from the
// trusted ref.
//
// Algorithm (three steps):
//
//  1. Build pathPolicies from egress.policy (generic paths only):
//     each entry maps host → EgressHostPolicy{Paths}.
//
//  2. Build secrets from egress.secrets (env→hosts only).
//
//  3. D-PDE-16 check: every GitHub host in a secret bind must be covered
//     by a PathPolicies entry or AllowedRepo (CLI shim). Non-GitHub exempt.
//
// NOTE: the CLI --repo/AllowedRepo shim is NOT set from the config path.
// AllowedRepo is only set when the user passes --repo explicitly at the CLI.
// The config path uses generic paths policies for all hosts including GitHub.
func buildWorktreeEgressArgs(cfg config.Config) (secrets []string, allowedRepo string, pathPolicies domain.EgressPathPolicies, err error) {
	// Step 1: policy entries → pathPolicies (generic paths for all hosts).
	for _, p := range cfg.Egress.Policy {
		if len(p.Paths) > 0 {
			pathPolicies = egressAddHostPolicy(pathPolicies, p.Host,
				domain.EgressHostPolicy{Paths: p.Paths})
		}
	}

	// Step 2: secrets → "ENV@host1,host2" strings.
	for _, s := range cfg.Egress.Secrets {
		if len(s.Hosts) == 0 {
			continue
		}
		secrets = append(secrets, s.Env+"@"+strings.Join(s.Hosts, ","))
	}

	// Step 3: D-PDE-16 — GitHub secret hosts require a covering policy.
	// Non-GitHub hosts are exempt (D-PDE-16 asymmetry).
	// allowedRepo is always "" from the config path (only set by CLI --repo).
	for _, s := range cfg.Egress.Secrets {
		for _, h := range s.Hosts {
			if domain.IsGitHubHost(strings.ToLower(h)) && !egressGitHubHostBound(h, allowedRepo, pathPolicies) {
				return nil, "", nil, fmt.Errorf(
					"egress secret %q targets GitHub host %q but no egress.policy entry covers it "+
						"(D-PDE-16: refusing create to avoid unscoped access)",
					s.Env, h,
				)
			}
		}
	}

	return secrets, allowedRepo, pathPolicies, nil
}

// egressGitHubHostBound is the D-PDE-16 CLI-time check: is h covered by
// allowedRepo (shim) or a PathPolicies entry under the WILDCARD key "" only.
//
// SECURITY: Only pp[""] is checked — not all top-level keys.  See
// githubHostBoundCLI (cmd_sandbox.go) for the full rationale.
// Revert this to an all-keys loop and TestBogusKeyGitHubSecret_Refused fails.
func egressGitHubHostBound(h, allowedRepo string, pp domain.EgressPathPolicies) bool {
	if allowedRepo != "" {
		return true
	}
	if hostMap, ok := pp[""]; ok {
		if _, ok := hostMap[strings.ToLower(h)]; ok {
			return true
		}
	}
	return false
}

// egressAddHostPolicy adds a path policy for one host under the wildcard
// placeholder key "" (resolves to all authenticated requests for that host).
func egressAddHostPolicy(pp domain.EgressPathPolicies, host string, policy domain.EgressHostPolicy) domain.EgressPathPolicies {
	if pp == nil {
		pp = make(domain.EgressPathPolicies)
	}
	if pp[""] == nil {
		pp[""] = make(map[string]domain.EgressHostPolicy)
	}
	pp[""][strings.ToLower(host)] = policy
	return pp
}

// herdrResolveWorktreeImage resolves the bootable image flag pair for a
// worktree sandbox created from checkoutPath.
//
// Resolution order:
//  1. If checkoutPath (or any ancestor up to the .git root) contains a
//     nexus3.yaml, return --file <dir-containing-nexus3.yaml> so the full
//     project config (image, egress rules, etc.) is applied during the build.
//  2. Otherwise return --image <herdrDefaultImage>.
//
// Never returns an imageFlag/imageVal pair that would produce an unbootable
// sandbox create argv. Returns a non-nil error only when config.Load itself
// fails (e.g., the nexus3.yaml file is present but malformed).
func herdrResolveWorktreeImage(checkoutPath string) (imageFlag, imageVal string, err error) {
	_, cfgPath, loadErr := config.Load(checkoutPath)
	if loadErr != nil {
		return "", "", fmt.Errorf("resolve worktree image: load config: %w", loadErr)
	}
	if cfgPath != "" {
		// nexus3.yaml found: use --file so the build applies the full project config.
		return "--file", filepath.Dir(cfgPath), nil
	}
	// No nexus3.yaml, but a .nexus/Containerfile (or .nexus/Dockerfile) is itself
	// a complete build definition — the `--file` build engine reads exactly that
	// file from the context dir. So its presence ALONE is enough to build the
	// worktree sandbox from it; requiring a separate nexus3.yaml sentinel would be
	// a surprising extra step (the Containerfile is the thing that matters).
	if dir := nexusContainerfileDir(checkoutPath); dir != "" {
		return "--file", dir, nil
	}
	// No project build definition at all: fall back to the named default base image.
	return "--image", herdrDefaultImage, nil
}

// nexusContainerfileDir walks up from startDir toward the repository root (the
// .git boundary, the same stopping rule config.Load uses) and returns the first
// directory that contains a .nexus/Containerfile or .nexus/Dockerfile. Returns
// "" when none is found up to the repo root or filesystem root.
//
// The returned directory is the build CONTEXT dir (the one holding .nexus/), so
// it can be passed straight to `sandbox create --file`, which reads
// <dir>/.nexus/Containerfile (see resolveContainerfilePath).
func nexusContainerfileDir(startDir string) string {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return ""
	}
	dir := abs
	for {
		for _, rel := range []string{".nexus/Containerfile", ".nexus/Dockerfile"} {
			if _, statErr := os.Stat(filepath.Join(dir, rel)); statErr == nil {
				return dir
			}
		}
		// Stop at the repository root (a directory containing a .git entry).
		if _, gitErr := os.Lstat(filepath.Join(dir, ".git")); gitErr == nil {
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "" // filesystem root
		}
		dir = parent
	}
}

// herdrWorktreeSandboxHandle derives a deterministic, collision-free sandbox
// handle from a repository name and git branch name.
//
// The handle format is "<repoName>/<branchSlug>", which is a valid nexus3
// handle (exactly one "/", both sides non-empty).  Case is PRESERVED so that
// handles like "example-app/EX-871" remain human-readable.
//
// Sanitisation rules (applied to both repoName and branch independently):
//  1. Any char NOT in [A-Za-z0-9._-] (including "/") is replaced with "-".
//  2. Consecutive "-" are collapsed to one; leading/trailing "-" are trimmed.
//  3. An empty result falls back: repoName → "repo", branch → "worktree".
func herdrWorktreeSandboxHandle(repoName, branch string) string {
	sanitize := func(s, fallback string) string {
		var b strings.Builder
		prev := '-'
		for _, r := range s {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
				b.WriteRune(r)
				prev = r
			} else {
				if prev != '-' {
					b.WriteByte('-')
				}
				prev = '-'
			}
		}
		slug := strings.Trim(b.String(), "-")
		if slug == "" {
			return fallback
		}
		return slug
	}
	return sanitize(repoName, "repo") + "/" + sanitize(branch, "worktree")
}

// herdrWorktreeHandlePrefix is the legacy handle prefix used by bindings
// written before the WorktreeManaged flag was introduced.  It is retained so
// that HerdrSpaceBinding.IsWorktreeManaged can identify old records via a
// prefix fallback and avoid leaking their VMs after an upgrade.
//
// New bindings no longer use this prefix — they carry WorktreeManaged=true
// and a semantic "<repo>/<branch>" handle instead.
const herdrWorktreeHandlePrefix = "wt/"

// isHerdrWorktreeHandle reports whether handle was produced by the legacy
// herdrWorktreeSandboxHandle that prepended "wt/".  Used only as a fallback
// inside HerdrSpaceBinding.IsWorktreeManaged for pre-WorktreeManaged bindings.
func isHerdrWorktreeHandle(handle string) bool {
	return strings.HasPrefix(handle, herdrWorktreeHandlePrefix)
}

// herdrWorktreeSandboxParseArgs strips --auto / --conditional flags from the
// beginning of args and returns the remaining positional args and mode flags.
//
// --auto    activates the repo-level predicate (predicate c): at least one
//
//	sibling workspace in the same repo must be nexus3-bound. This is
//	the mode used by the guest-shell dispatcher (herdrDefaultShellCore).
//
// --conditional activates the legacy SourceWorkspaceID predicate. Kept for
//
//	backward compatibility with any scripts that relied on the old
//	behaviour; the two flags are mutually exclusive in practice.
//
// Called by the "worktree-sandbox" case in runHerdrPlugin so flag parsing
// happens before the workspace ID is read, preventing the flags from being
// consumed as the workspace ID.
func herdrWorktreeSandboxParseArgs(args []string) (rest []string, conditional bool, auto bool) {
	for len(args) > 0 {
		switch args[0] {
		case "--auto":
			auto = true
			args = args[1:]
		case "--conditional":
			conditional = true
			args = args[1:]
		default:
			return args, conditional, auto
		}
	}
	return args, conditional, auto
}

// herdrWorktreeSandbox orchestrates the worktree-sandbox flow for one herdr workspace.
//
// Steps (performed in order):
//
//  1. Idempotency: if a binding already exists for workspaceID, return early.
//
//  2. Resolve herdr binary path via resolveHerdrBin.
//
//  3. List worktrees for workspaceID via herdrListWorktreeForWorkspaceFn.
//     On error: log and return nil (fail-safe — workspace stays a host shell).
//
//  4. Linked-worktree guard: if !info.IsLinkedWorktree, workspace is the main
//     checkout; return nil (no sandbox created).
//
//  5. Conditional source check (only when conditional=true):
//     a. If SourceWorkspaceID is empty, return nil (ambiguous source).
//     b. If the source workspace has no nexus3 binding, return nil (source
//     is not nexus3-managed; worktree workspace stays a host shell).
//
//  6. Derive sandbox handle from branch name via herdrWorktreeSandboxHandle.
//
//  7. Create sandbox via createFn(ctx, handle, mountSpec).
//     On error (explicit, conditional==false): return error.
//     On error (conditional mode): log and return nil (fail-safe).
//
//  8. Look up sandbox ID via getFn; write the HerdrSpaceBinding (without GuestPaneID).
//     Binding is written BEFORE the pane opens so idempotency is safe on crash.
//     On error: same policy as step 7.
//
//  9. Open guest shell pane; if successful, patch GuestPaneID into the stored binding.
//     On error: always printed. Explicit mode (conditional==false) returns the error
//     (sandbox+binding exist and are recoverable, but silence is not acceptable).
//     Conditional mode continues — the binding committed and the workspace is usable.
//
//  10. Rename the herdr workspace to the space label via herdrWorkspaceRenameFn.
//     On error: printed, never returned (best-effort step).
func herdrWorktreeSandbox(
	ctx context.Context,
	workspaceID string,
	w io.Writer,
	storeRoot string,
	openPane bool,
	conditional bool,
	auto bool,
	createFn func(context.Context, string, string, string, string, []string, []string, string, domain.EgressPathPolicies) error,
	getFn func(context.Context, string) (domain.Sandbox, error),
) error {
	// Step 1: idempotency.
	if _, err := herdrSpaceResolve(ctx, storeRoot, workspaceID); err == nil {
		fmt.Fprintf(w, "worktree-sandbox: workspace %s already bound\n", workspaceID)
		return nil
	}

	// Step 2: resolve herdr binary.
	herdrBin, err := resolveHerdrBin()
	if err != nil {
		fmt.Fprintf(w, "worktree-sandbox: resolve herdr binary: %v\n", err)
		return nil
	}

	// Step 3: list worktrees (fail-safe on error). A 2 s context bounds the
	// herdr probe so a hung daemon cannot wedge every new pane on the machine.
	listCtx, listCancel := context.WithTimeout(ctx, herdrWorktreeListTimeout)
	defer listCancel()
	info, err := herdrListWorktreeForWorkspaceFn(listCtx, herdrBin, workspaceID)
	if err != nil {
		fmt.Fprintf(w, "worktree-sandbox: herdr worktree list: %v\n", err)
		return nil
	}

	// Step 4: linked-worktree guard (predicates a+b).
	if !info.IsLinkedWorktree {
		fmt.Fprintf(w, "worktree-sandbox: workspace %s is main checkout, skipping\n", workspaceID)
		return nil
	}

	// Step 5: mode-specific source check.
	//
	//  auto mode (--auto): repo-level predicate (c). At least one binding's
	//  RepoRoot must match the main repo root derived from info.RepoKey. If
	//  the repo cannot be identified (RepoKey empty) or no binding matches,
	//  fail safe.
	//
	//  conditional mode (--conditional): legacy SourceWorkspaceID predicate.
	//  The source workspace (the main checkout that owns this worktree) must
	//  have a nexus3 binding. Kept for backward compatibility.
	//
	//  explicit mode (neither flag): no source check — always bind.
	switch {
	case auto:
		if !herdrWorktreeSandboxRepoCheck(ctx, storeRoot, info) {
			fmt.Fprintf(w, "worktree-sandbox: no nexus3-bound workspace in repo, skipping\n")
			return nil
		}
	case conditional:
		srcID := info.SourceWorkspaceID
		if srcID == "" {
			fmt.Fprintf(w, "worktree-sandbox: source workspace unknown, skipping\n")
			return nil
		}
		if _, err := herdrSpaceResolve(ctx, storeRoot, srcID); err != nil {
			fmt.Fprintf(w, "worktree-sandbox: source workspace %s not nexus3-bound, skipping\n", srcID)
			return nil
		}
	}

	// Step 6: derive handle — "<repoName>/<branchSlug>".
	// repoName is derived from info.RepoKey (e.g. "/repo/.git" → "repo").
	// Strip a trailing "/.git" or ".git", take the basename, sanitise.
	// Falls back to "repo" when RepoKey is empty or the basename is blank.
	repoNameForHandle := "repo"
	if info.RepoKey != "" {
		key := strings.TrimSuffix(info.RepoKey, "/.git")
		key = strings.TrimSuffix(key, ".git")
		if base := filepath.Base(key); base != "" && base != "." && base != "/" {
			repoNameForHandle = base // herdrWorktreeSandboxHandle sanitises it
		}
	}
	handle := herdrWorktreeSandboxHandle(repoNameForHandle, info.Branch)
	mountSpec := info.Path + ":/workspace"

	// Extra mounts for linked worktrees: mount the main repo's .git dir at its
	// host absolute path so the gitdir: pointer in <checkout>/.git resolves
	// inside the VM. Without this git fails with "not a git repository".
	var extraMounts []string
	if gitMount := herdrWorktreeGitDirMount(info.Path); gitMount != "" {
		extraMounts = []string{gitMount}
	}

	// Step 6.1: per-handle create-intent lock.
	//
	// Two concurrent callers for the same worktree workspace both pass the
	// unlocked step-1 idempotency check before either writes a binding.  A
	// second create succeeds because the sandbox store does NOT enforce handle
	// uniqueness — producing an orphaned VM that holds memory with no reference.
	//
	// Fix: serialise on a per-handle flock.  Only callers racing for the SAME
	// handle contend; different handles use different lock files and never block
	// each other.  herdr-space-bindings.lock (acquired briefly inside
	// HerdrSpacePut) is a separate file, so no deadlock is possible.
	//
	// Protocol:
	//   winner — acquires lock, re-checks (not bound), runs create, writes
	//            binding, opens pane, releases lock.
	//   loser  — blocks on Exclusive, acquires after winner releases, re-checks
	//            (bound), logs and returns nil → pane gets a host shell.
	//            The workspace is already mapped to the sandbox via the binding.
	//
	// Fail-open in auto/conditional mode: a lock error logs and returns nil so
	// the pane falls back to a host shell (a deadlock here would freeze every
	// new pane on the machine, since this binary is herdr's default_shell). In
	// explicit mode the operator asked for a sandbox directly, so a lock error
	// is a real failure and must surface.
	//
	// failSafe controls error-handling mode for the lock block and steps 6.5/7.
	// Explicit mode (neither flag) returns errors; auto/conditional is fail-safe.
	failSafe := conditional || auto
	{
		lk, lkErr := store.OpenLock(herdrWorktreeCreateLockPath(storeRoot, handle))
		if lkErr != nil {
			fmt.Fprintf(w, "worktree-sandbox: open create-intent lock: %v\n", lkErr)
			if !failSafe {
				return fmt.Errorf("worktree-sandbox: open create-intent lock: %w", lkErr)
			}
			return nil
		}
		defer lk.Close()
		lkCtx, lkCancel := context.WithTimeout(ctx, herdrWorktreeCreateLockTimeout)
		defer lkCancel()
		if lkErr := lk.Exclusive(lkCtx); lkErr != nil {
			fmt.Fprintf(w, "worktree-sandbox: acquire create-intent lock: %v\n", lkErr)
			if !failSafe {
				return fmt.Errorf("worktree-sandbox: acquire create-intent lock: %w", lkErr)
			}
			return nil
		}
		defer lk.Unlock() //nolint:errcheck
		// Re-check by handle under the lock — closes the TOCTOU window between
		// step 1 (keyed on workspaceID) and step 7 (sandbox create).  Two
		// different workspace IDs that resolve to the same handle must converge
		// on one sandbox; the loser sees the binding the winner wrote. Reuse is
		// idempotent success in every mode, so this returns nil unconditionally.
		if _, boundErr := HerdrSpaceGetByHandle(ctx, storeRoot, handle); boundErr == nil {
			fmt.Fprintf(w, "worktree-sandbox: handle %s already bound (concurrent create race), reusing existing sandbox\n", handle)
			return nil
		}
	}

	// Step 6.5: resolve the bootable image for this worktree checkout.
	// config.Load walks from info.Path up to the .git boundary; an absent
	// nexus3.yaml is not an error. A malformed nexus3.yaml IS an error — the
	// operator must fix it before the sandbox can be created.
	imageFlag, imageVal, imgErr := herdrResolveWorktreeImage(info.Path)
	if imgErr != nil {
		fmt.Fprintf(w, "worktree-sandbox: resolve image: %v\n", imgErr)
		if !failSafe {
			return fmt.Errorf("worktree-sandbox: resolve image: %w", imgErr)
		}
		return nil
	}

	// Step 6.6: read egress config from operator-controlled trusted ref (Finding A /
	// D-PDE-17). Never read from the worktree branch or working tree bytes.
	// Fail closed: no trusted ref or absent nexus3.yaml → no auto-grant.
	var (
		egressSecrets      []string
		egressAllowedRepo  string
		egressPathPolicies domain.EgressPathPolicies
	)
	commonGitDir := worktreeCommonGitDir(info.Path)
	if commonGitDir != "" {
		cfgBytes, refErr := readTrustedRefBytes(commonGitDir)
		if refErr != nil {
			fmt.Fprintf(w, "worktree-sandbox: read trusted ref config: %v\n", refErr)
			if !failSafe {
				return fmt.Errorf("worktree-sandbox: read trusted ref config: %w", refErr)
			}
			return nil
		}
		if cfgBytes != nil {
			parsedCfg, parseErr := config.Parse(cfgBytes)
			if parseErr != nil {
				fmt.Fprintf(w, "worktree-sandbox: parse trusted ref nexus3.yaml: %v\n", parseErr)
				if !failSafe {
					return fmt.Errorf("worktree-sandbox: parse trusted ref nexus3.yaml: %w", parseErr)
				}
				return nil
			}
			egressSecrets, egressAllowedRepo, egressPathPolicies, err = buildWorktreeEgressArgs(parsedCfg)
			if err != nil {
				fmt.Fprintf(w, "worktree-sandbox: build egress args: %v\n", err)
				if !failSafe {
					return fmt.Errorf("worktree-sandbox: build egress args: %w", err)
				}
				return nil
			}
		}
	}
	// Step 7: create sandbox. A 90 s context covers image pull, ext4 setup,
	// and VM boot on typical hardware. Explicit mode failures are real errors;
	// auto/conditional mode is fail-safe (workspace stays a host shell).
	// egressPathPolicies is conveyed to the subprocess via --egress-policy-json
	// so the generic policy reaches MITM enforcement (D-PDE-16 worktree gap fix).
	createCtx, createCancel := context.WithTimeout(ctx, herdrWorktreeCreateTimeout)
	defer createCancel()
	if err := createFn(createCtx, handle, mountSpec, imageFlag, imageVal, extraMounts, egressSecrets, egressAllowedRepo, egressPathPolicies); err != nil {
		fmt.Fprintf(w, "worktree-sandbox: sandbox create: %v\n", err)
		if !failSafe {
			return fmt.Errorf("worktree-sandbox: sandbox create: %w", err)
		}
		return nil
	}

	// Step 8: look up sandbox ID and write the binding (without GuestPaneID).
	// The binding is written BEFORE opening the pane so the idempotency check
	// on the next run sees it even if the process dies during pane open.
	sb, err := getFn(ctx, handle)
	if err != nil {
		fmt.Fprintf(w, "worktree-sandbox: get sandbox %s: %v\n", handle, err)
		if !failSafe {
			return fmt.Errorf("worktree-sandbox: get sandbox %s: %w", handle, err)
		}
		return nil
	}
	label := "nexus3:" + handle
	// Derive the main repo root from info.RepoKey (e.g. "/repo/.git" → "/repo").
	// Empty RepoKey → empty RepoRoot → NO MATCH in the predicate (fail-open).
	repoRoot := ""
	if info.RepoKey != "" {
		repoRoot = filepath.Dir(filepath.Clean(info.RepoKey))
	}
	binding := HerdrSpaceBinding{
		SpaceLabel:       label,
		HerdrWorkspaceID: workspaceID,
		SandboxHandle:    handle,
		SandboxID:        sb.ID.String(),
		RepoRoot:         repoRoot,
		WorktreeManaged:  true,
	}
	if err := HerdrSpacePut(ctx, storeRoot, binding); err != nil {
		fmt.Fprintf(w, "worktree-sandbox: write binding: %v\n", err)
		if !failSafe {
			return fmt.Errorf("worktree-sandbox: write binding: %w", err)
		}
		return nil
	}

	// Opportunistic backfill: heal sibling bindings whose RepoRoot is still
	// empty. Best-effort — errors are logged but never returned; the binding
	// we just wrote is already correct and must not be blocked on this.
	// NOTE: must NOT be called from the guest-shell dispatcher (herdrDefaultShellCore)
	// hot path; here in herdrWorktreeSandbox it is safe because herdr calls
	// are already made in this flow.
	if _, bfErr := HerdrBackfillRepoRoot(ctx, storeRoot, herdrBin, io.Discard); bfErr != nil {
		slog.Warn("worktree-sandbox: backfill-repo-root", "err", bfErr)
	}

	// Step 9: open guest shell pane and patch GuestPaneID into the stored binding.
	// Error policy: sandbox+binding already exist and are recoverable on the next run,
	// so pane failure is always printed. Explicit mode also returns it (non-zero exit);
	// auto/conditional mode continues because the binding committed and the workspace is usable.
	if openPane {
		paneID, paneErr := herdrOpenGuestShellPane(ctx, herdrBin, handle, workspaceID, "", false)
		if paneErr != nil {
			fmt.Fprintf(w, "worktree-sandbox: open guest pane: %v\n", paneErr)
			if !failSafe {
				return fmt.Errorf("worktree-sandbox: open guest pane: %w", paneErr)
			}
		}
		if paneID != "" {
			binding.GuestPaneID = paneID
			_ = HerdrSpacePut(ctx, storeRoot, binding) // best-effort patch
		}
	}

	// Step 10: rename workspace to space label.
	if err := herdrWorkspaceRenameFn(ctx, herdrBin, workspaceID, label); err != nil {
		fmt.Fprintf(w, "worktree-sandbox: rename workspace: %v\n", err)
	}

	fmt.Fprintf(w, "worktree-sandbox: bound workspace %s → sandbox %s\n", workspaceID, handle)
	return nil
}
