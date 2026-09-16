package cli

import (
	"bufio"
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
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/clientagent"
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
	ociname "github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func init() {
	Register(Command{
		Name:    "__herdr-plugin",
		Summary: "Deprecated alias for `herdr` (kept for installed plugin backward compat)",
		Hidden:  true,
		Run:     runHerdrPlugin,
	})
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
const herdrPluginABIVersion = "3"

func herdrGroupVerbToPluginSub(sub string) (pluginSub string, known bool) {
	switch sub {
	case "default-shell", "install-default-shell":
		return sub, true
	case "abi", "context-cwd", "workspaces", "attach", "create", "logs", "doctor",
		"open-pane", "launch", "shell-cwd", "new-tab":
		return sub, true
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
	// space-* verbs KEEP prefix (collision: bare name used by non-space verb)
	case "space-create", "space-open-pane":
		return sub, true
	case "worktree-sandbox":
		return sub, true
	case "backfill-repo-root":
		return "backfill-repo-root", true
	case "local-agent-startup":
		return sub, true
	default:
		return "", false
	}
}

func runHerdrGroup(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: "herdr: subcommand required (abi|context-cwd|workspaces|attach|create|logs|doctor|open-pane|launch|shell-cwd|new-tab|space-create|space-open-pane|create-from-file|pause|resume|remove|list|prune|agent|agent-from-file|default-shell|install-default-shell|worktree-sandbox|backfill-repo-root|local-agent-startup|version-check|focus-changed)"}
	}
	sub := args[0]
	rest := args[1:]

	switch sub {
	case "default-shell":
		return runHerdrDefaultShell(ctx, rest, out)
	case "install-default-shell":
		return runHerdrInstallDefaultShell(ctx, rest, out)
	case "version-check":
		return herdrVersionCheck(ctx, rest, out.w)
	case "focus-changed":
		return runHerdrFocusChanged(ctx, rest, out)
	case "report-forward-status":
		return runHerdrReportForwardStatus(ctx, rest, out)
	}

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

func runHerdrPlugin(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: "__herdr-plugin: subcommand required (abi|context-cwd|workspaces|attach|create|logs|doctor|open-pane|launch|new-tab|space-create|space-create-from-file|space-open-pane|space-pause|space-resume|space-remove|space-list|space-prune|shell-cwd|space-agent|space-agent-from-file|local-agent-startup)"}
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
		// --nested operator opt-in for nested virtualisation (D-N3N-02).
		rest, conditional, auto, nestedFlag := herdrWorktreeSandboxParseArgs(rest)
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
		createFn := func(ctx context.Context, handle, mountSpec, imageFlag, imageVal string, extraMounts, secrets []string, allowedRepo string, pathPolicies domain.EgressPathPolicies, nested bool) error {
			args := herdrWorktreeSandboxCreateArgs(handle, mountSpec, imageFlag, imageVal, extraMounts, secrets, allowedRepo, pathPolicies, nested)
			cmd := herdrExecCommandContext(ctx, exe, append([]string{"sandbox", "create"}, args...)...)
			cmd.Stdout = out.w
			cmd.Stderr = out.w
			return cmd.Run()
		}
		getFn := func(ctx context.Context, handle string) (domain.Sandbox, error) {
			return svc.Get(ctx, handle)
		}
		return herdrWorktreeSandbox(ctx, workspaceID, out.w, storeRoot, true, conditional, auto, nestedFlag, createFn, getFn)

	case "backfill-repo-root":
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin backfill-repo-root: resolve store: " + err.Error(), Err: err}
		}
		return herdrPluginBackfillRepoRoot(ctx, storeRoot, out.w)

	case "local-agent-startup":
		return herdrPluginLocalAgentStartup(ctx)

	default:
		exe, _ := os.Executable()
		return &UsageError{Msg: fmt.Sprintf(
			"__herdr-plugin: unknown subcommand %q\n\n"+
				"The binary is likely stale. Executed: %s\n"+
				"Rebuild: go build -o nexus3 ./cmd/nexus3 && nexus3 herdr install-default-shell",
			sub, exe)}
	}
}

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

var workspaceTableHeaders = [6]string{"WORKSPACE", "STATE", "AGENT", "MOUNTS", "SPACE", "ID"}

func herdrWorkspaceAgent(sb domain.Sandbox) string {
	if sb.AgentName == "" {
		return "-"
	}
	return sb.AgentName
}

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

// Env sealing: HERDR_* vars must never reach guest VM.
// Service constructs guest env independently from allow-list.
// Strip HERDR_* from process env so subprocess forks cannot leak credentials.
func herdrPluginAttach(ctx context.Context, ref string, out *Output, svc *service.Service) error {
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

func runHerdrCmd(bin string, args ...string) error {
	cmd := exec.Command(bin, args...)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "__herdr-plugin: herdr reporter: %v\n", err)
		return err
	}
	return nil
}

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

// herdrRepoFlags prompts for repo scope. D-PD-36: unbounded GitHub credentials
// are refused; caller must scope to one repo or pass no flags.
func herdrRepoFlags(scanner *bufio.Scanner) ([]string, error) {
	fmt.Fprint(os.Stderr, "github repo to allow (owner/name, blank for no GitHub access): ")
	if !scanner.Scan() {
		return nil, &CodedError{Code: ErrCodeInternalError, Msg: "failed to read github repo"}
	}
	repo := strings.TrimSpace(scanner.Text())
	if repo == "" {
		return []string{}, nil
	}
	parts := strings.Split(repo, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, &UsageError{Msg: fmt.Sprintf("invalid repo %q: expected owner/name", repo)}
	}
	return []string{"--repo", repo, "--secret", "GH_TOKEN@github.com,api.github.com,uploads.github.com"}, nil
}

const herdrDefaultImageRepo = "ghcr.io/inizio/nexus3-base"

var herdrDefaultImageTag = "latest"

var herdrDefaultImage = herdrDefaultImageRepo + ":" + herdrDefaultImageTag

var herdrExecCommandContext = exec.CommandContext

var herdrPaneExistsFn = herdrPaneExists

var herdrBaseImageListFn = func(ctx context.Context) ([]domain.Image, error) {
	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return nil, err
	}
	cache, err := image.NewCache(filepath.Join(storeRoot, "images"))
	if err != nil {
		return nil, err
	}
	return cache.List(ctx)
}

var herdrBaseImageRegistryReachableFn = func(ref string) error {
	r, err := ociname.ParseReference(ref)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = remote.Head(r, remote.WithContext(ctx))
	return err
}

func herdrPaneExists(ctx context.Context, herdrBin, workspaceID, paneID string) bool {
	if herdrBin == "" || workspaceID == "" || paneID == "" {
		return false
	}
	out, err := herdrExecCommandContext(ctx, herdrBin, "pane", "list", "--workspace", workspaceID).Output()
	if err != nil {
		slog.Warn("space-create: pane-exists: pane list failed; assuming pane gone",
			"workspace_id", workspaceID, "pane_id", paneID, "err", err)
		return false
	}
	var resp struct {
		Result struct {
			Panes []struct {
				PaneID string `json:"pane_id"`
			} `json:"panes"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		slog.Warn("space-create: pane-exists: parse pane list; assuming pane gone",
			"workspace_id", workspaceID, "pane_id", paneID, "err", err)
		return false
	}
	for _, p := range resp.Result.Panes {
		if p.PaneID == paneID {
			return true
		}
	}
	return false
}

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

func herdrPluginCreate(ctx context.Context, r io.Reader, w io.Writer, svc *service.Service, storeRoot string) error {
	if _, err := resolveKernelPath(); err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin create: " + err.Error(), Err: err}
	}

	imgs, _ := herdrBaseImageListFn(ctx)
	found := false
	for _, img := range imgs {
		if img.Ref == herdrDefaultImage && img.Size > 0 {
			found = true
			break
		}
	}
	if !found {
		if err := herdrBaseImageRegistryReachableFn(herdrDefaultImage); err != nil {
			return &CodedError{
				Code: ErrCodeInternalError,
				Msg:  fmt.Sprintf("__herdr-plugin create: base image %q not cached and registry unreachable (%v); run: nexus3 sandbox create --image %s", herdrDefaultImage, err, herdrDefaultImage),
			}
		}
	}

	scanner := bufio.NewScanner(r)
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
		return df, fmt.Sprintf(
			"warning: found %q but the nexus3 build engine requires %q — please rename it",
			df, cf), nil
	}
	return "", "", fmt.Errorf(
		"no Containerfile found: tried\n  %s\n  %s\nCreate one of these files or specify --file",
		cf, df)
}

func deriveHandleFromContext(contextDir string) string {
	base := filepath.Base(contextDir)
	if base == "" || base == "." || base == "/" {
		base = "project"
	}
	return "local/" + base
}

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

func herdrPluginSpaceCreateFromFile(ctx context.Context, r io.Reader, w io.Writer, svc *service.Service, storeRoot string) error {
	scanner := bufio.NewScanner(r)

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
	standardCF := filepath.Join(contextDir, ".nexus", "Containerfile")
	if dockerfile != standardCF {
		fmt.Fprintf(w, "error: resolved dockerfile %q but the build engine only reads %q\n", dockerfile, standardCF)
		fmt.Fprintf(w, "       Rename the file to .nexus/Containerfile and retry.\n")
		return &UsageError{Msg: "space-create-from-file: dockerfile must be at " + standardCF}
	}

	defaultHandle := deriveHandleFromContext(contextDir)
	fmt.Fprintf(os.Stderr, "sandbox name [%s]: ", defaultHandle)
	if !scanner.Scan() {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-create-from-file: failed to read sandbox name"}
	}
	handle := strings.TrimSpace(scanner.Text())
	if handle == "" {
		handle = defaultHandle
	}
	if _, _, err := domain.ParseHandle(handle); err != nil {
		fmt.Fprintf(w, "error: invalid sandbox name %q (must be project/name, e.g. local/myapp)\n", handle)
		return &UsageError{Msg: "space-create-from-file: " + err.Error()}
	}

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

	fmt.Fprintf(w, "opening herdr space for %s ...\n", handle)
	return herdrPluginSpaceCreate(ctx, handle, w, svc, storeRoot, true)
}

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

	// ABI file check catches stale plugin installations (binary/manifest skew).
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

	if pluginRoot != "" {
		pinPath := filepath.Join(pluginRoot, "nexus3-version")
		pinBytes, err := os.ReadFile(pinPath)
		if err != nil {
			fmt.Fprintf(w, "version check: cannot read %s: %v\n", pinPath, err)
		} else {
			res := herdrCheckVersionSkew(version, string(pinBytes))
			fmt.Fprintf(w, "version check: %s\n", res.Message)
		}
	} else {
		fmt.Fprintf(w, "version check: HERDR_PLUGIN_ROOT unset (not running as a herdr plugin)\n")
	}

	fmt.Fprintln(w, "")
	checks, drv := runAllChecks(defaultProbes())
	substrate := "none"
	if drv != nil {
		substrate = drv.Name()
	}
	fmt.Fprint(w, formatDoctorHuman(substrate, drv != nil, checks))
	fmt.Fprintln(w, "")

	return nil
}

// HERDR_BIN_PATH injected by herdr into PLUGIN only, not ordinary pane shells.
// Fall back to PATH: fixes shells inside herdr (natural use case) without weakening.
func resolveHerdrBin() (string, error) {
	if p := os.Getenv("HERDR_BIN_PATH"); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath("herdr"); err == nil {
		return p, nil
	}
	return "", errors.New("herdr not found: HERDR_BIN_PATH is unset and no \"herdr\" binary is on PATH")
}

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

// D-PD-33: With agentEgress, freeze allowlist onto Envelope.
// Credentials wired by supervisor (handoffLaunchSupervisor).
func buildLaunchBootOpts(imageRef, cacheRoot string, agentEgress bool) service.CreateAndBootOptions {
	opts := service.CreateAndBootOptions{
		Image:               service.ImageSpec{Ref: imageRef},
		CacheRoot:           cacheRoot,
		ReachabilityTimeout: 60 * time.Second,
	}
	if agentEgress {
		opts.AgentProfile = cred.ClaudeCodeProfile
		opts.AllowedHosts = service.AgentEgressHosts(opts.AgentProfile)
	}
	return opts
}

func launchCredSourcedArgv(argv []string) []string {
	script := "set -a\n" +
		". " + service.GuestCredEnvPath + "\n" +
		"set +a\n" +
		"exec \"$@\"\n"
	return append([]string{"/bin/sh", "-c", script, "nexus3-launch"}, argv...)
}

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

	if _, err := svc.Stop(ctx, sb.ID.String()); err != nil {
		return nil, "", fmt.Errorf("stop before supervisor handoff: %w", err)
	}

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
			CredsFile: service.DedicatedCredStorePathForProfile(cred.ClaudeCodeProfile),
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

// herdrPluginLaunch boots sandbox, runs argv in-guest, tears down. With
// --agent-egress, hands off to supervisor (launchDeps injectable, TBD-PD-31).
type launchDeps struct {
	svc        *service.Service
	imgCache   *image.Cache
	newDriver  service.DriverFactory
	probe      service.ProbeFunc
	storeRoot  string
	kernelPath string
	cacheRoot  string

	capturedDiskPath func() string

	// handoff hands the booted VM to a detached perimeter supervisor and
	// returns the watchdog pipe and the supervisor's IPC socket path.
	handoff func(ctx context.Context, svc *service.Service, sb domain.Sandbox,
		storeRoot, kernelPath, diskPath string, extraDisks []string) (*os.File, string, error)
	verifySeed func(ctx context.Context, svc *service.Service, id string) error

	stopSupervisor func(ctx context.Context, sock string) error
	waitForExit    func(ctx context.Context, stateDir string) error

	// execInGuest runs the command inside the booted sandbox.
	execInGuest func(ctx context.Context, ref string, opts agent.ExecOptions) (int32, error)
}

func newLaunchDeps() (launchDeps, error) {
	var d launchDeps

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

	var diskPath string
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
	// 	reusing space: label=nexus3:ac3/vcpuctl workspace_id=w35
	// 	{"error":{"code":"workspace_not_found",...}}
	// 	error: space-create: open shell pane: exit status 1
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
		// If the binding records a guest pane that is still alive, adopt it
		// rather than opening another. A stored pane ID is a pointer the
		// operator can invalidate at any time (pane closed, workspace restarted).
		// Nothing tells nexus3 when that happens, so we probe liveness first.
		//
		// Fail-safe: if the pane probe errors (herdr unreachable, unexpected
		// shape), herdrPaneExistsFn returns false and we fall through to open a
		// new pane. This degrades to the pre-fix cosmetic extra-pane behaviour
		// rather than silently dispatching the agent into a pane that does not
		// exist (a silent no-op where nothing visible happens).
		if existing.GuestPaneID != "" && herdrPaneExistsFn(ctx, herdrBin, existing.HerdrWorkspaceID, existing.GuestPaneID) {
			fmt.Fprintf(w, "pane already live: pane_id=%s\n", existing.GuestPaneID)
			return nil
		}
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

// herdrPluginNewTab opens a tab: new guest pane if bound, else herdr default.
func herdrPluginNewTab(ctx context.Context, workspaceID, storeRoot string, svc herdrAdoptGetter, w io.Writer) error {
	herdrBin, binErr := resolveHerdrBin()

	// Look up a nexus3 binding for this herdr workspace ID.
	// herdrSpaceResolve's last-resort scan matches HerdrWorkspaceID, so
	// passing a workspace ID here works without a dedicated index.
	_, lookupErr := herdrSpaceResolve(ctx, storeRoot, workspaceID)
	if lookupErr != nil && !errors.Is(lookupErr, ErrHerdrSpaceNotFound) {
		slog.Warn("new-tab: binding lookup failed; falling back to host tab",
			"workspace_id", workspaceID, "err", lookupErr)
		lookupErr = ErrHerdrSpaceNotFound
	}

	if lookupErr == nil {
		return herdrPluginSpaceOpenPane(ctx, workspaceID, storeRoot, svc, w)
	}

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

// herdrPluginSpacePrune removes stale bindings (dry-run; --apply to delete).
func herdrPluginSpacePrune(ctx context.Context, args []string, w io.Writer, svc herdrSpacePruneLister, storeRoot, herdrBin string) error {
	fs := flag.NewFlagSet("space-prune", flag.ContinueOnError)
	apply := fs.Bool("apply", false, "delete stale bindings (default: dry-run)")
	workspace := fs.String("workspace", "", "only the binding for this herdr workspace id (the worktree.removed hook path); the workspace is taken as gone")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "__herdr-plugin space-prune: " + err.Error()}
	}
	if fs.NArg() > 0 {
		return &UsageError{Msg: fmt.Sprintf("__herdr-plugin space-prune: unexpected argument %q; usage: space-prune [--apply] [--workspace <id>]", fs.Arg(0))}
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
	// Snapshot the bindings BEFORE the binding-indexed pass runs. The fallback
	// sweep below must not treat a binding that THIS run deleted as evidence
	// that its sandbox was never bound — the binding-indexed path owns anything
	// it saw, for the whole run. A read failure here is not fatal to the main
	// prune (herdrSpacePruneFull does its own read and reports it); it only
	// disables the fallback.
	bindingsBefore, bindingsErr := HerdrSpaceList(ctx, storeRoot)

	if *workspace != "" {
		// Scoped mode (worktree.removed hook): the event names the closed
		// workspace, so only its binding is a candidate and the workspace is
		// taken as gone without consulting `herdr workspace list`. The
		// global orphan and bindingless sweeps never run here — they are what
		// reaped unrelated live sandboxes when the hook ran the full prune.
		if bindingsErr != nil {
			return fmt.Errorf("space-prune: read bindings: %w", bindingsErr)
		}
		var scoped []HerdrSpaceBinding
		for _, b := range bindingsBefore {
			if b.HerdrWorkspaceID == *workspace {
				scoped = append(scoped, b)
			}
		}
		gone := func(HerdrSpaceBinding) bool { return false }
		return herdrSpacePruneBindings(ctx, w, storeRoot, herdrBin, scoped, sandboxExists, gone, closer, removeSandbox, *apply, false)
	}

	if err := herdrSpacePruneFull(ctx, w, storeRoot, herdrBin, sandboxExists, workspaceExists, closer, removeSandbox, *apply); err != nil {
		return err
	}
	if bindingsErr != nil {
		slog.Warn("space-prune: bindingless sweep: read bindings", "err", bindingsErr)
		fmt.Fprintln(w, "Bindingless sweep skipped: bindings could not be read.")
		return nil
	}
	herdrSpacePruneBindinglessStep(ctx, w, herdrBin, svc, bindingsBefore, removeSandbox, *apply)
	return nil
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
	return herdrSpacePruneBindings(ctx, w, storeRoot, herdrBin, bindings, sandboxExists, workspaceExists, closer, removeSandbox, apply, true)
}

// herdrSpacePruneBindings classifies and (under apply) reconciles the given
// bindings; sweepOrphans additionally closes unbound "nexus3:" workspaces.
func herdrSpacePruneBindings(
	ctx context.Context,
	w io.Writer,
	storeRoot string,
	herdrBin string,
	bindings []HerdrSpaceBinding,
	sandboxExists func(HerdrSpaceBinding) bool,
	workspaceExists func(HerdrSpaceBinding) bool,
	closer func(context.Context, string) error,
	removeSandbox func(context.Context, string) error,
	apply bool,
	sweepOrphans bool,
) error {
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
	if apply && sweepOrphans {
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
		if _, err := HerdrSpaceGetByLabel(ctx, storeRoot, ws.Label); err == nil {
			continue // binding exists — not orphaned
		}
		if err := closer(ctx, ws.WorkspaceID); err != nil {
			slog.Warn("space-prune: orphan sweep: close workspace failed",
				"workspace_id", ws.WorkspaceID, "label", ws.Label, "err", err)
			continue
		}
		fmt.Fprintf(w, "  ORPHAN closed workspace=%s label=%s\n", ws.WorkspaceID, ws.Label)
	}
}

// ── Bindingless worktree-sandbox fallback sweep ─────────────────────────────
// WHY THIS EXISTS, AND WHY IT IS A FALLBACK AND NOT A REPLACEMENT.
//
// herdrSpacePruneFull enumerates BINDINGS. That is deliberate: the binding
// store is the index of what nexus3 is permitted to reap, and it is the reason
// a reaper cannot classify a live developer's VM as an orphan. This repo has a
// proven incident where exactly that happened, so nothing below widens what the
// binding-indexed path reaps, and nothing below runs on a sandbox that HAS a
// binding row.
//
// The gap this closes is narrow and real: a sandbox that never got a binding
// row (create succeeded, the binding write did not) is structurally invisible
// to a binding-indexed prune, forever. Three of five sandboxes examined on the
// host at diagnosis were in exactly that state and leaked until they were
// removed by hand.
//
// The sweep therefore derives collectability from the SANDBOX RECORD, and only
// when all four of these are POSITIVELY established. Every one of them is an
// affirmative observation; none is inferred from a missing input, and any
// input that cannot be obtained aborts the whole sweep:
//
// 	G1  the sandbox has NO binding row (handle and ID both absent from the
// 	    binding snapshot taken BEFORE the binding-indexed pass ran)
// 	G2  its live-mount set positively matches the shape herdrWorktreeSandbox
// 	    writes: exactly one /workspace mount, and exactly one self-mapped mount
// 	    that is a git common dir STILL PRESENT on disk with a worktrees/ subdir
// 	G3  the /workspace host path is positively absent (os.ErrNotExist — a
// 	    permission or I/O error is ambiguous and keeps the sandbox)
// 	G4  no live herdr workspace refers to it, established against a workspace
// 	    list that was actually obtained and was non-empty
//
// Ambiguity resolves to KEEP in every case. A wrong reap costs someone's
// working VM; a missed reap costs disk.

// herdrWorkspaceGuestPath is the guest mount point herdrWorktreeSandbox gives
// the worktree checkout ("<checkout>:/workspace", see mountSpec there).
const herdrWorkspaceGuestPath = "/workspace"

// herdrWorkspaceRef is one live herdr workspace, reduced to the fields the
// bindingless sweep needs to answer "does anything live still refer to this
// checkout?".
type herdrWorkspaceRef struct {
	WorkspaceID string
	Label       string
	// CheckoutPath is worktree.checkout_path from herdr's WorkspaceInfo. It is
	// empty when the workspace is not a worktree workspace (herdr models the
	// whole `worktree` object as nullable).
	CheckoutPath string
}

// herdrListWorkspaceRefs runs `herdr workspace list` and returns every live
// workspace with its label and worktree checkout path.
//
// An error is returned — never a partial or empty success — when herdr is
// unavailable, the call fails, or the response does not parse. The caller must
// treat that as "guard G4 cannot be established" and skip the sweep entirely.
func herdrListWorkspaceRefs(ctx context.Context, herdrBin string) ([]herdrWorkspaceRef, error) {
	if herdrBin == "" {
		return nil, fmt.Errorf("herdr workspace list: no herdr binary")
	}
	out, err := herdrExecCommandContext(ctx, herdrBin, "workspace", "list").Output()
	if err != nil {
		return nil, fmt.Errorf("herdr workspace list: %w", err)
	}
	return herdrParseWorkspaceRefs(out)
}

// herdrParseWorkspaceRefs parses a `herdr workspace list` response body.
// Split out from herdrListWorkspaceRefs so the parse is testable without exec.
func herdrParseWorkspaceRefs(data []byte) ([]herdrWorkspaceRef, error) {
	var resp struct {
		Result struct {
			Workspaces []struct {
				WorkspaceID string `json:"workspace_id"`
				Label       string `json:"label"`
				Worktree    *struct {
					CheckoutPath string `json:"checkout_path"`
				} `json:"worktree"`
			} `json:"workspaces"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("herdr workspace list: parse response: %w", err)
	}
	refs := make([]herdrWorkspaceRef, 0, len(resp.Result.Workspaces))
	for _, ws := range resp.Result.Workspaces {
		ref := herdrWorkspaceRef{WorkspaceID: ws.WorkspaceID, Label: ws.Label}
		if ws.Worktree != nil {
			ref.CheckoutPath = ws.Worktree.CheckoutPath
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// herdrWorktreeMountShape is the live-mount signature herdrWorktreeSandbox
// writes into every worktree sandbox record.
type herdrWorktreeMountShape struct {
	// CheckoutPath is the host path of the linked worktree checkout, mounted
	// at /workspace.
	CheckoutPath string
	// CommonGitDir is the main repo's git common directory, mounted
	// self-mapped ("<dir>:<dir>") so the checkout's gitdir: pointer resolves
	// inside the guest (herdrWorktreeGitDirMount).
	CommonGitDir string
}

// herdrIsGitCommonDir positively identifies dir as a git common directory that
// hosts linked worktrees, by requiring dir/worktrees to exist on disk as a
// directory. "worktrees" is the structural marker git guarantees — the same
// anchor herdrWorktreeGitDirMount uses when it builds the mount.
//
// This is an affirmative on-disk check: it is satisfiable only while the MAIN
// repo still exists, which is what makes it usable as evidence after the linked
// worktree itself has been deleted.
func herdrIsGitCommonDir(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, "worktrees"))
	return err == nil && fi.IsDir()
}

// herdrWorktreeMountShapeOf reports whether sb's record positively identifies
// it as a worktree-managed sandbox, and if so returns the paths involved.
//
// It requires EXACTLY ONE /workspace mount and EXACTLY ONE self-mapped git
// common-dir mount. "Exactly one" and not "at least one" on purpose: a record
// carrying two candidates is a record this function does not understand, and a
// record this function does not understand must not authorise a reap.
//
// The .groundwork mount herdrWorktreeSandbox also adds is self-mapped too, but
// it is not a git common dir (no worktrees/ subdir), so it is not a candidate.
func herdrWorktreeMountShapeOf(sb domain.Sandbox) (herdrWorktreeMountShape, bool) {
	var checkouts, commons []string
	for _, m := range sb.LiveMounts {
		if !filepath.IsAbs(m.HostPath) {
			continue
		}
		if m.GuestPath == herdrWorkspaceGuestPath {
			checkouts = append(checkouts, filepath.Clean(m.HostPath))
			continue
		}
		if m.HostPath == m.GuestPath && herdrIsGitCommonDir(m.HostPath) {
			commons = append(commons, filepath.Clean(m.HostPath))
		}
	}
	if len(checkouts) != 1 || len(commons) != 1 {
		return herdrWorktreeMountShape{}, false
	}
	shape := herdrWorktreeMountShape{CheckoutPath: checkouts[0], CommonGitDir: commons[0]}
	// A linked worktree is never the main checkout. If the /workspace mount IS
	// the main repo root, this is a main-checkout sandbox wearing a similar
	// mount set — refuse it rather than reap someone's primary checkout VM.
	//
	// Defence in depth, and deliberately kept as such: today G3 already covers
	// this case, because herdrIsGitCommonDir only succeeds while CommonGitDir
	// exists on disk, and the parent of an existing directory necessarily
	// exists too — so a main-checkout /workspace path is never "positively
	// gone". That makes this refusal unreachable THROUGH
	// herdrClassifyBindinglessSandbox, which is why it is pinned by a test
	// against this function directly (TestWorktreeMountShapeOf_RefusesMainCheckout)
	// rather than through the classifier. Do not delete it on the strength of
	// that reachability argument: it holds only as long as the common-dir probe
	// stays an on-disk one.
	if shape.CheckoutPath == filepath.Dir(shape.CommonGitDir) {
		return herdrWorktreeMountShape{}, false
	}
	return shape, true
}

// herdrPathPositivelyGone reports whether p is affirmatively absent from the
// filesystem. Only os.ErrNotExist counts. A permission error, an I/O error, or
// any other stat failure means "cannot tell" — which is ambiguity, and
// ambiguity keeps the sandbox.
//
// Lstat, not Stat: a dangling symlink at the checkout path is a thing that
// still EXISTS, and must not read as gone.
func herdrPathPositivelyGone(p string) bool {
	if p == "" {
		return false
	}
	_, err := os.Lstat(p)
	return errors.Is(err, os.ErrNotExist)
}

// herdrBindinglessVerdict is the classification of one sandbox by the
// bindingless sweep. Reason is populated in both directions so a dry-run can
// explain a KEEP as well as a COLLECT.
type herdrBindinglessVerdict struct {
	Handle   string
	Checkout string
	Collect  bool
	Reason   string
}

// herdrClassifyBindinglessSandbox applies guards G1..G4 to a single sandbox.
// It is pure apart from the filesystem probes in G2/G3, and it never mutates
// anything — the caller decides what to do with the verdict.
//
// workspaces MUST be a list that was actually obtained from a live herdr; the
// caller is responsible for not calling this at all when it could not be. This
// function cannot distinguish "herdr says there are no workspaces" from "herdr
// could not be reached", which is precisely why that decision lives upstream.
func herdrClassifyBindinglessSandbox(
	sb domain.Sandbox,
	bindings []HerdrSpaceBinding,
	workspaces []herdrWorkspaceRef,
) herdrBindinglessVerdict {
	v := herdrBindinglessVerdict{}
	if sb.Project == "" || sb.Name == "" {
		v.Reason = "sandbox record carries no usable handle"
		return v
	}
	v.Handle = sb.Project + "/" + sb.Name

	// G1 — no binding row. If there is one, the binding-indexed path owns this
	// sandbox and the fallback stays out of it.
	sbID := sb.ID.String()
	for _, b := range bindings {
		if b.SandboxHandle == v.Handle || (b.SandboxID != "" && b.SandboxID == sbID) {
			v.Reason = "has a binding row; owned by the binding-indexed prune path"
			return v
		}
	}

	// G2 — positively worktree-managed, from the record's live mounts.
	shape, ok := herdrWorktreeMountShapeOf(sb)
	if !ok {
		v.Reason = "record does not positively identify a worktree sandbox"
		return v
	}
	v.Checkout = shape.CheckoutPath

	// G3 — the worktree checkout is positively gone.
	if !herdrPathPositivelyGone(shape.CheckoutPath) {
		v.Reason = "worktree checkout still present, or its absence could not be established"
		return v
	}

	// G4 — nothing live in herdr still refers to it.
	label := herdrSpaceLabelForRef(v.Handle)
	for _, ws := range workspaces {
		if ws.CheckoutPath != "" && filepath.Clean(ws.CheckoutPath) == shape.CheckoutPath {
			v.Reason = "live herdr workspace " + ws.WorkspaceID + " still opens this checkout"
			return v
		}
		if ws.Label != "" && ws.Label == label {
			v.Reason = "live herdr workspace " + ws.WorkspaceID + " is labelled for this sandbox"
			return v
		}
	}

	v.Collect = true
	v.Reason = "no binding, worktree-managed, checkout gone, no live workspace"
	return v
}

// herdrSpaceSweepBindinglessSandboxes classifies every sandbox and, under
// --apply, reaps the ones all four guards clear.
//
// It returns how many sandboxes cleared every guard, and how many were ACTUALLY
// reaped. The two are reported separately because they differ in the cases that
// matter: a dry run reaps nothing, and a removeSandbox failure leaves the
// sandbox for the next run. Only real removals are counted as reaped.
//
// With apply=false this function is read-only: it prints and stats, and calls
// nothing that mutates.
func herdrSpaceSweepBindinglessSandboxes(
	ctx context.Context,
	w io.Writer,
	bindings []HerdrSpaceBinding,
	sandboxes []domain.Sandbox,
	workspaces []herdrWorkspaceRef,
	removeSandbox func(context.Context, string) error,
	apply bool,
) (collectable, reaped int) {
	for _, sb := range sandboxes {
		v := herdrClassifyBindinglessSandbox(sb, bindings, workspaces)
		if !v.Collect {
			continue
		}
		collectable++
		if !apply {
			fmt.Fprintf(w, "  BINDINGLESS sandbox=%s worktree=%s (%s; would be reaped)\n",
				v.Handle, v.Checkout, v.Reason)
			continue
		}
		if err := removeSandbox(ctx, v.Handle); err != nil {
			slog.Warn("space-prune: bindingless sweep: reap failed; sandbox retained for next run",
				"handle", v.Handle, "worktree", v.Checkout, "err", err)
			fmt.Fprintf(w, "  BINDINGLESS reap FAILED sandbox=%s: %v\n", v.Handle, err)
			continue
		}
		fmt.Fprintf(w, "  BINDINGLESS reaped sandbox=%s worktree=%s\n", v.Handle, v.Checkout)
		reaped++
	}
	return collectable, reaped
}

// herdrSpacePruneBindinglessStep is the wiring between herdrPluginSpacePrune and
// the sweep. It gathers the two inputs the sweep cannot establish for itself —
// the live sandbox list and the live herdr workspace list — and REFUSES to run
// the sweep if either is missing, rather than letting a missing input read as
// "nothing refers to this sandbox".
//
// bindings is the snapshot taken BEFORE the binding-indexed pass, so a binding
// that pass deleted during this same run cannot make its sandbox look
// bindingless to the fallback.
func herdrSpacePruneBindinglessStep(
	ctx context.Context,
	w io.Writer,
	herdrBin string,
	svc herdrSpacePruneLister,
	bindings []HerdrSpaceBinding,
	removeSandbox func(context.Context, string) error,
	apply bool,
) {
	skip := func(reason string) {
		fmt.Fprintf(w, "Bindingless sweep skipped: %s.\n", reason)
	}
	workspaces, err := herdrListWorkspaceRefs(ctx, herdrBin)
	if err != nil {
		slog.Warn("space-prune: bindingless sweep: workspace list", "err", err)
		skip("live herdr workspaces could not be listed")
		return
	}
	// An empty workspace list is indistinguishable from an unexpected response
	// shape, and it is the single input whose emptiness would clear G4 for
	// EVERY sandbox at once. Refuse it. This mirrors the same refusal in
	// herdrSpacePruneWorkspaceExistsFn.
	if len(workspaces) == 0 {
		skip("herdr reported no workspaces (indistinguishable from an unexpected response shape)")
		return
	}
	sandboxes, err := svc.List(ctx)
	if err != nil {
		slog.Warn("space-prune: bindingless sweep: sandbox list", "err", err)
		skip("sandbox list unavailable")
		return
	}
	collectable, reaped := herdrSpaceSweepBindinglessSandboxes(ctx, w, bindings, sandboxes, workspaces, removeSandbox, apply)
	// Always report, including the all-clear. A sweep that prints nothing when
	// it finds nothing is indistinguishable from a sweep that never ran — and
	// "the reaper silently never ran" is the defect this whole path exists to
	// close. The operator must be able to see that it looked.
	if apply {
		fmt.Fprintf(w, "Bindingless sweep (apply): %d sandbox(es) examined, %d collectable, %d reaped.\n",
			len(sandboxes), collectable, reaped)
		return
	}
	fmt.Fprintf(w, "Bindingless sweep (dry-run): %d sandbox(es) examined, %d collectable.\n",
		len(sandboxes), collectable)
	if collectable > 0 {
		fmt.Fprintln(w, "Run with --apply to reap them.")
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

// claudeReadyMatch returns the literal substring that means claude has
// finished starting and its input box is accepting text. Verbatim footer from
// a live guest pane (claude v2.1.272, 2026-09-15):
//
//	⏵⏵ auto mode on (shift+tab to cycle) · ← 1 agent
//
// The token is mode-invariant and the autonomous argument is ignored, because
// under D-2 a guest claude ALWAYS runs in permissions.defaultMode auto:
// guestAgentLaunchCommand passes --permission-mode auto on every launch, and
// since D-1 the guest's ~/.claude IS the host's (live rw mount), so the mode a
// bare `claude` starts in is whatever the operator's settings.json says —
// not something this code can select by flag spelling. The old two-token
// scheme ("? for shortcuts" for manual, "auto mode on" for auto) timed out
// live against an agent already at its prompt in auto mode.
//
// Tokens that are wrong, each found by a live run:
//
//   - "? for shortcuts" is the manual-mode footer; it never appears in auto.
//   - "for agents" comes and goes with pane state — observed absent from a
//     ready pane moments after being present in the same one.
//   - "❯" is the prompt glyph, but it is ALSO the selector glyph in all four
//     first-run wizards, so it reports ready while claude still sits on the
//     theme picker — precisely the failure this wait exists to prevent.
func claudeReadyMatch(_ bool) string {
	return "auto mode on"
}

// guestAgentLaunchCommand returns the shell command typed into the guest pane
// to start claude: "IS_SANDBOX=1 claude --permission-mode auto", regardless
// of the autonomous argument (D-2: the guest always runs in auto mode).
// IS_SANDBOX=1 is required alongside it: claude refuses root execution unless
// that variable marks the environment as already-isolated.
//
// The command is EXPLICIT and does not rely on the `claude` shell function
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
func guestAgentLaunchCommand(_ bool) string {
	return "IS_SANDBOX=1 claude --permission-mode auto"
}

// cursorReadyMatch is the substring in cursor-agent's TUI input box that means
// the agent has finished starting and its input box is accepting text.
//
// Verified LIVE (2026-09-04) against cursor-agent v2026.09.02-c22c1a3 running
// on the host under a tmux PTY with an authenticated operator account. The
// tmux-rendered pane content at the ready state
// is (verbatim, trimmed):
//
//	Cursor Agent
//	v2026.09.02-c22c1a3
//	Tip: Type ? in the prompt bar to show in-app hints.
//
//	→ Plan, search, build anything
//
//	Cursor Grok 4.5 High Fast
//	~/magic/nexus3/... · nexus3/cursor-s6-readymatch
//
// "Plan, search, build anything" is the placeholder text in the input box.
// It is assembled by cursor-addressing escape sequences (not emitted as a
// contiguous literal in the raw PTY byte stream), so it appears only in the
// tmux-rendered pane view — which is exactly what herdrPaneWaitOutput inspects.
//
// The prior static-analysis guess ("shift+tab to cycle") does NOT appear
// anywhere in a real running session.
//
// Unlike claudeReadyMatch, the token is mode-invariant: cursor-agent shows the
// same input placeholder whether or not --force/--yolo is passed, so this
// function ignores the autonomous argument.
func cursorReadyMatch(_ bool) string {
	return "Plan, search, build anything"
}

// guestCursorLaunchCommand returns the shell command typed into the guest
// pane to start cursor-agent.
//
// autonomous == true  → "cursor-agent --force" — --force (alias --yolo) is
// cursor's skip-permissions equivalent ("Run Everything"), confirmed via
// `cursor-agent --help` on a freshly installed 2026.08.25-3e8eec8 binary.
//
// autonomous == false → "cursor-agent" — default mode, which prompts for
// approval per tool call.
//
// Unlike claude, cursor-agent does NOT refuse to run as root: confirmed
// empirically by running it as root in the environment this slice was
// implemented in, where it failed only on API-key validation, never on uid.
// No IS_SANDBOX-style escape is needed or emitted here.
//
// cursor-agent has no shell-function equivalent of the `claude` wrapper that
// SeedGuestShellProfile installs (see guestAgentLaunchCommand), so there is no
// "command " prefix needed here to bypass one.
func guestCursorLaunchCommand(autonomous bool) string {
	if autonomous {
		return "cursor-agent --force"
	}
	return "cursor-agent"
}

// agentLaunchDescriptor is the per-agent, declarative launch contract used by
// herdr space-agent dispatch: how to start the agent in a guest shell pane,
// and how to recognise it has reached its ready-for-input prompt.
//
// Unlike cred.AgentProfile (pure data, no logic branches — see profile.go's
// own doc comment), this descriptor IS a pair of functions, and that is a
// deliberate, stated design choice rather than an oversight: readiness
// detection and launch invocation genuinely differ per agent in ways that do
// not compress into declarative fields without losing load-bearing nuance
// (cursorReadyMatch is mode-invariant while cursor's --force is not; guestAgentLaunchCommand's
// shell-function-bypass requirement). Putting agent-specific FUNCTIONS behind
// a name-keyed registry, instead of hardcoding claude's, is the seam this
// comment's presence marks as intentionally non-declarative: adding a third
// dispatchable agent means adding one command()/readyMatch() pair and one
// registry entry here, not modifying herdrPluginSpaceAgent.
type agentLaunchDescriptor struct {
	command    func(autonomous bool) string
	readyMatch func(autonomous bool) string
}

// agentLaunchDescriptors is the dispatch registry. Adding an agent here does
// NOT change any existing entry's behaviour: claude's functions are untouched
// and this map is the only new call surface.
var agentLaunchDescriptors = map[string]agentLaunchDescriptor{
	cred.ClaudeCodeProfileName: {
		command:    guestAgentLaunchCommand,
		readyMatch: claudeReadyMatch,
	},
	cred.CursorAgentProfileName: {
		command:    guestCursorLaunchCommand,
		readyMatch: cursorReadyMatch,
	},
}

// resolveAgentLaunchDescriptor returns the launch descriptor for agentName.
// An empty or unrecognised name falls back to Claude Code's descriptor,
// matching cred.DefaultProfileName: a plain sandbox created without --agent,
// or one predating this registry, still gets the pre-existing claude
// behaviour rather than a dispatch failure. An unregistered --agent name is
// already refused at `sandbox create` time by cred.ProfileByName, so this
// fallback is a defensive default, not the primary validation point.
func resolveAgentLaunchDescriptor(agentName string) agentLaunchDescriptor {
	if d, ok := agentLaunchDescriptors[agentName]; ok {
		return d
	}
	return agentLaunchDescriptors[cred.ClaudeCodeProfileName]
}

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

	return herdrPaneSendEnter(ctx, herdrBin, paneID)
}

// herdrPaneSendEnter presses Enter in paneID. Extracted from
// herdrPaneSubmitToAgent so the submission-confirmation loop can re-press it
// without re-pasting the brief (a second send-text would APPEND to a buffer
// that already holds the brief, producing a doubled prompt).
func herdrPaneSendEnter(ctx context.Context, herdrBin, paneID string) error {
	enter := herdrExecCommandContext(ctx, herdrBin, "pane", "send-keys", paneID, "Enter")
	enter.Stdout = os.Stderr
	enter.Stderr = os.Stderr
	return enter.Run()
}

// herdrPaneReadText reads paneID's rendered text.
//
// `--source recent-unwrapped` is the sharper source — it excludes the wrapping
// artefacts that make naive diffing noisy — but it returns an EMPTY string on a
// pane that has not scrolled yet, which is the normal state of a freshly
// dispatched agent. Falling through on that empty answer makes a live pane read
// as blank, and blank compares equal to blank, so a working agent reads as
// static. Fall back to `--source visible`, which always renders something for a
// live pane.
//
// A read that produces no text from EITHER source returns ok=false rather than
// an empty string, so callers cannot mistake "could not read" for "read an
// empty pane". Every caller here treats ok=false as undecidable, never as a
// negative answer.
func herdrPaneReadText(ctx context.Context, herdrBin, paneID string) (text string, ok bool) {
	for _, source := range []string{"recent-unwrapped", "visible"} {
		var buf strings.Builder
		cmd := herdrExecCommandContext(ctx, herdrBin, "pane", "read", paneID, "--source", source)
		cmd.Stdout = &buf
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			continue
		}
		if strings.TrimSpace(buf.String()) != "" {
			return buf.String(), true
		}
	}
	return "", false
}

// herdrPaneReadFn is the pane-read seam. Production reads a live pane; tests
// swap in a scripted transcript sequence so the confirmation loop can be driven
// without herdr.
var herdrPaneReadFn = herdrPaneReadText

// briefSubmissionVerdict is the outcome of asking "did the brief actually leave
// claude's input box?".
//
// There are three answers and not two. The dispatch defect this type exists to
// close was reported as success precisely because the code had only two states
// and mapped "no evidence" onto the good one.
type briefSubmissionVerdict int

const (
	// briefSubmissionUnknown — the pane could not be read, or it could be read
	// and carries no evidence either way. This is a REFUSAL, never a pass. See
	// herdrDeliverBriefConfirmed.
	briefSubmissionUnknown briefSubmissionVerdict = iota
	// briefSubmissionStranded — positive evidence the brief is still sitting in
	// the input box unsent.
	briefSubmissionStranded
	// briefSubmissionSubmitted — positive evidence the brief left the box.
	briefSubmissionSubmitted
)

func (v briefSubmissionVerdict) String() string {
	switch v {
	case briefSubmissionStranded:
		return "STRANDED"
	case briefSubmissionSubmitted:
		return "SUBMITTED"
	default:
		return "UNKNOWN"
	}
}

// briefStrandedMarkers are the input-box residues observed on a pane whose
// brief was pasted but never submitted.
//
// Captured live 2026-09-02 while dispatching a three-slice wave: two briefs
// submitted, the third sat in the box rendering as
//
//	[Pasted text #1 +79 lines]
//
// with the footer offering "paste again to expand" and NO working indicator,
// while the CLI printed "space-agent: agent running in pane w7P:p2" and exited
// 0. A single `herdr pane send-keys <pane> Enter` submitted it.
//
// These markers are the STRANDED discriminator, not the submitted one. They are
// only meaningful in the positive: their presence proves the buffer still holds
// the brief; their absence proves nothing, because a pane can be unreadable,
// mid-repaint, or rendering a layout nobody has seen yet.
var briefStrandedMarkers = []*regexp.Regexp{
	regexp.MustCompile(`\[Pasted text #\d+`),
	regexp.MustCompile(`paste again to expand`),
}

// briefWorkingMarkers are working-agent affordances. FAST PATH ONLY.
//
// The same rule the pane watcher lives by applies here: movement decides, and a
// marker's ABSENCE proves nothing — every one of these has been observed to
// vanish from a pane whose agent was working (a slash-command overlay repaints
// the footer away; an agent blocked on its own subagent stops running a tool and
// loses the interrupt affordance permanently). They are listed so an obviously
// working pane is confirmed on the first read instead of waiting out a repaint,
// and for no other purpose.
//
// NOTE what is deliberately NOT here: "auto mode on" and "? for shortcuts".
// Those are claudeReadyMatch's tokens — permission-mode footers present BEFORE
// and AFTER submission alike. Matching on them is what made the original
// dispatch report success on a stranded brief.
var briefWorkingMarkers = []string{
	"esc to interrupt",
	"ctrl+b to run in background",
}

// classifyBriefSubmission decides whether a brief left the input box, from two
// pane reads taken a beat apart.
//
// Precedence, and why each step is in this order:
//
//  1. Either read unobtainable → UNKNOWN. Refusing here is the whole point: a
//     pane we cannot read is a pane we cannot vouch for.
//  2. A stranded marker in the LATER read → STRANDED. Positive evidence beats
//     movement, because a stranded pane still repaints (the placeholder blinks,
//     the footer cycles) and would otherwise pass step 3.
//  3. The pane changed between the two reads → SUBMITTED. Movement is the
//     decider: a working agent repaints, a pane holding an unsent buffer that
//     shows no stranded marker at all is not a shape that has been observed.
//  4. A working affordance in the later read → SUBMITTED (fast path).
//  5. Otherwise → UNKNOWN. Static, no markers, nothing to go on. NOT a pass.
func classifyBriefSubmission(before, after string, beforeOK, afterOK bool) (briefSubmissionVerdict, string) {
	if !beforeOK || !afterOK {
		return briefSubmissionUnknown, "pane read returned no text (both --source recent-unwrapped and --source visible were empty or failed)"
	}
	for _, re := range briefStrandedMarkers {
		if re.MatchString(after) {
			return briefSubmissionStranded, "input box still holds the pasted brief (matched " + re.String() + ")"
		}
	}
	if before != after {
		return briefSubmissionSubmitted, "pane repainted between reads (agent is working)"
	}
	for _, m := range briefWorkingMarkers {
		if strings.Contains(after, m) {
			return briefSubmissionSubmitted, "working indicator present (" + m + ")"
		}
	}
	return briefSubmissionUnknown, "pane is static, shows no working indicator, and shows no stranded-input marker"
}

// briefConfirmSettle is the gap between the two reads classifyBriefSubmission
// compares. It must exceed claude's repaint period — the spinner's elapsed
// timer ticks once a second — or a working agent reads as static.
//
// A var, not a const: the pane reads are a seam (herdrPaneReadFn), and a test
// driving that seam with scripted reads has nothing to wait for.
var briefConfirmSettle = 1500 * time.Millisecond

// briefSubmitAttempts is how many times Enter is pressed in total before the
// dispatch is failed. The first press happens in herdrPaneSubmitToAgent; each
// unconfirmed round after that presses again.
const briefSubmitAttempts = 3

// herdrDeliverBriefConfirmed pastes the brief, submits it, and then CONFIRMS it
// was submitted — retrying Enter and finally failing loudly rather than
// reporting a success it did not observe.
//
// Reporting success at the point the system stopped looking is the defect this
// closes. The old path waited for claude's prompt to APPEAR (which happens
// before submission, and stays true after it), pressed Enter, and returned nil.
// Observed rate: one stranded brief in three dispatches.
//
// FAIL-CLOSED: both non-success verdicts — STRANDED and UNKNOWN — exhaust the
// retries and then return an error. An unreadable pane does not "skip the
// check"; it fails the dispatch. The operator can then look at the pane, which
// is cheap, instead of discovering an hour later that a slice never started.
func herdrDeliverBriefConfirmed(ctx context.Context, herdrBin, paneID, brief string, w io.Writer) error {
	if err := herdrPaneSubmitToAgent(ctx, herdrBin, paneID, brief); err != nil {
		return &CodedError{Code: ErrCodeInternalError,
			Msg: "space-agent: deliver brief: " + err.Error(), Err: err}
	}

	var lastVerdict briefSubmissionVerdict
	var lastReason string
	for attempt := 1; attempt <= briefSubmitAttempts; attempt++ {
		before, beforeOK := herdrPaneReadFn(ctx, herdrBin, paneID)
		select {
		case <-ctx.Done():
			return &CodedError{Code: ErrCodeInternalError,
				Msg: "space-agent: confirm brief submitted: " + ctx.Err().Error(), Err: ctx.Err()}
		case <-time.After(briefConfirmSettle):
		}
		after, afterOK := herdrPaneReadFn(ctx, herdrBin, paneID)

		lastVerdict, lastReason = classifyBriefSubmission(before, after, beforeOK, afterOK)
		if lastVerdict == briefSubmissionSubmitted {
			fmt.Fprintf(w, "space-agent: brief submitted (confirmed on attempt %d/%d: %s)\n",
				attempt, briefSubmitAttempts, lastReason)
			return nil
		}
		if attempt == briefSubmitAttempts {
			break
		}
		fmt.Fprintf(w, "space-agent: brief not confirmed submitted (%s: %s); pressing Enter again (attempt %d/%d)\n",
			lastVerdict, lastReason, attempt+1, briefSubmitAttempts)
		if err := herdrPaneSendEnter(ctx, herdrBin, paneID); err != nil {
			return &CodedError{Code: ErrCodeInternalError,
				Msg: "space-agent: re-submit brief: " + err.Error(), Err: err}
		}
	}

	return &CodedError{
		Code: ErrCodeInternalError,
		Msg: fmt.Sprintf("space-agent: brief was NOT confirmed submitted in pane %s after %d Enter presses "+
			"(final verdict %s: %s); the agent is running but its brief may still be sitting unsent in the "+
			"input box — inspect with: herdr pane read %s --source visible",
			paneID, briefSubmitAttempts, lastVerdict, lastReason, paneID),
	}
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
// create to be called to build the sandbox from .nexus/config.yaml. Every other error
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
	fmt.Fprintf(w, "herdr agent: sandbox %q not found; creating from .nexus/config.yaml ...\n", ref)
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
	//    from .nexus/config.yaml (same precedence rules as `sandbox create`). This runs
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

	// 4. (No bypass-permissions consent step: guest claude launches in auto
	//    permission mode via --permission-mode auto; no consent prompt appears.)

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

	// 5b. Resolve which agent this sandbox runs. sb.AgentName was already
	//     validated against cred.ProfileByName at `sandbox create` time; an
	//     empty name (plain sandbox) or a name predating this registry falls
	//     back to claude's descriptor via resolveAgentLaunchDescriptor.
	sb, sbErr := svc.Get(ctx, ref)
	if sbErr != nil {
		return &CodedError{Code: ErrCodeInternalError,
			Msg: "space-agent: re-resolve sandbox for agent dispatch: " + sbErr.Error(), Err: sbErr}
	}
	launchDesc := resolveAgentLaunchDescriptor(sb.AgentName)
	launchCmd := launchDesc.command(autonomous)

	// 6. Launch the agent in the guest shell pane.
	fmt.Fprintf(w, "space-agent: launching %s in pane %s ...\n", launchCmd, paneID)
	if err := herdrPaneRun(ctx, herdrBin, paneID, launchCmd); err != nil {
		return &CodedError{Code: ErrCodeInternalError,
			Msg: "space-agent: launch agent: " + err.Error(), Err: err}
	}

	// 7. Wait for the agent's prompt. See claudeReadyMatch/cursorReadyMatch for
	//    why each agent's token is what it is.
	readyMatch := launchDesc.readyMatch(autonomous)
	fmt.Fprintf(w, "space-agent: waiting for agent prompt (match=%q, timeout=%ds) ...\n",
		readyMatch, claudeReadyTimeoutMS/1000)
	if err := herdrPaneWaitOutput(ctx, herdrBin, paneID, readyMatch, claudeReadyTimeoutMS); err != nil {
		return &CodedError{Code: ErrCodeInternalError,
			Msg: fmt.Sprintf("space-agent: agent did not reach its prompt within %ds: %v",
				claudeReadyTimeoutMS/1000, err), Err: err}
	}

	// 8. Deliver the slice brief AND confirm it was actually submitted.
	//
	//    Step 7's wait is not evidence of delivery. claudeReadyMatch's token is
	//    a permission-mode footer that is present before the brief is pasted and
	//    still present after it strands in the input box — it cannot distinguish
	//    the two states, so a dispatch that stopped looking here reported success
	//    on a brief that was never sent. See herdrDeliverBriefConfirmed.
	fmt.Fprintf(w, "space-agent: delivering brief ...\n")
	if err := herdrDeliverBriefConfirmed(ctx, herdrBin, paneID, brief, w); err != nil {
		return err
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

// herdrRepoHasNexus3Config reports whether the checkout at dir is nexus3-
// onboarded: it carries a .nexus/config.yaml or a .nexus/Containerfile.
func herdrRepoHasNexus3Config(dir string) bool {
	if dir == "" {
		return false
	}
	for _, rel := range []string{config.ConfigRelPath, filepath.Join(".nexus", "Containerfile")} {
		if st, err := os.Stat(filepath.Join(dir, rel)); err == nil && !st.IsDir() {
			return true
		}
	}
	return false
}

// herdrWorktreeAutoBindDecision is the --auto predicate for worktree-sandbox.
//
// It binds when the repo already has a nexus3-bound sibling workspace
// (repoBound) OR when the checkout itself is nexus3-onboarded (hasConfig:
// .nexus/config.yaml / .nexus/Containerfile). The second arm is what makes the FIRST
// worktree of a repo auto-provision; before it, a new user's worktree.created
// hook silently did nothing because no sibling could ever be bound yet.
// Only a repo with neither is skipped. The returned reason is printed either
// way so the provisioning pane never exits without saying why.
func herdrWorktreeAutoBindDecision(repoBound, hasConfig bool) (bind bool, reason string) {
	switch {
	case repoBound:
		return true, "repo already has a nexus3-bound workspace"
	case hasConfig:
		return true, "repo has .nexus/config.yaml or .nexus/Containerfile"
	default:
		return false, "no nexus3-bound workspace in repo and no .nexus/config.yaml or .nexus/Containerfile"
	}
}

// herdrWorktreeListTimeout bounds the `herdr worktree list` probe in step 3.
// A hung herdr daemon must not wedge every new pane on the machine.
const herdrWorktreeListTimeout = 2 * time.Second

// herdrWorktreeCreateTimeout bounds the sandbox create call in step 7.
//
// TBD-2 (nexus3-nonnexus3-repo-sandbox-blockers): this was 90s, documented as
// "generous for typical fast hardware with a warm image cache". That premise
// does not hold: a cold BUILDKIT LAYER CACHE (not merely a cold fingerprint —
// layers are shared across fingerprints, so a fingerprint miss over a warm
// layer cache is fast) measured 120s (a mid-size Next.js monorepo) and 152s (nexus3) through
// the unbounded `nexus3 create --file` path on 2026-08-31, both well over the
// old 90s bound. 240s carries ~60% headroom over the worst measured cold
// build and was chosen over two rejected alternatives:
//
//   - Adaptive/progress-based liveness (distinguish "slow but making
//     progress" from "wedged") was ruled out for this slice: it needs a new
//     channel streaming buildkit solve progress from guest to host over the
//     vsock control plane, which does not exist today. That is real
//     standalone machinery, not a constant tweak — left as follow-up work,
//     not because it is a bad idea.
//   - Removing the bound entirely was ruled out: it is the only thing that
//     protects an operator's pane from a truly wedged daemon (as opposed to
//     a slow one). Enforcement uses exec.CommandContext default behaviour
//     (SIGKILL on expiry) — a clean teardown is not guaranteed on timeout.
//
// A fixed constant still cannot distinguish "legitimately slow" from
// "wedged" — that limitation is inherent to any constant and is accepted for
// this slice.
const herdrWorktreeCreateTimeout = 240 * time.Second

// herdrWorktreeCreateLockTimeout bounds how long the second concurrent caller
// waits for the per-handle create-intent lock. Must exceed the first caller's
// worst-case total create time (herdrWorktreeCreateTimeout = 240s), so the
// first caller always finishes (or is forcibly killed) before the waiter gives up.
const herdrWorktreeCreateLockTimeout = 330 * time.Second

// herdrWorktreeCreateLockPath returns the path to the per-handle create-intent
// lock file.  The lock serialises concurrent auto-create attempts for the same
// sandbox handle (e.g. two panes opening in the same worktree workspace within
// the ~240 s create window — herdrWorktreeCreateTimeout above).
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
//   - "--file", "<dir>"    — build from a .nexus/config.yaml in that directory
//   - "--rootfs", "<path>" — use a raw rootfs (not currently produced by this
//     path, reserved for future use)
//
// Exactly one bootable flag is always present: the caller is responsible for
// resolving imageFlag/imageVal via herdrResolveWorktreeImage before calling
// this function. An empty imageFlag produces an unbootable argv that
// `sandbox create` will reject with exit status 2.
//
// secrets are "--secret ENV@host1,host2" binds derived from the egress.secrets
// section of the checkout's .nexus/config.yaml. allowedRepo, when
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
func herdrWorktreeSandboxCreateArgs(handle, mountSpec, imageFlag, imageVal string, extraMounts, secrets []string, allowedRepo string, pathPolicies domain.EgressPathPolicies, nested bool) []string {
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
	// Go build-cache disks. Unconditional (unlike the docker disk above): every
	// worktree sandbox agent runs `go build`/`go test` against this repo, and Go
	// defaults GOCACHE to $HOME/.cache/go-build and GOPATH/GOMODCACHE under
	// $HOME/go — both under /root, which otherwise sits entirely on the 4 GiB
	// root disk (/dev/vda). Root is NOT governor-visible: diskIndexFromDevice
	// rejects any device letter below 'b', so /dev/vda has no ExtraDisks index
	// and can never be grown by the disk governor no matter how full it gets.
	// Giving these two paths their own kind=disk volumes moves the write
	// pressure onto disks that ARE governor-visible (same ResizableDiskIndices
	// mechanism as the docker disk above), so the axis that was never handed
	// the disk that matters now is.
	//
	// Two disks, not one, because /root/go and /root/.cache are not nested and
	// a single ext4 volume can only be mounted at one guest path. Both stay
	// clear of /root/.claude, /root/.claude.json, and /root/.ssh — the paths
	// the agent's onboarding/credential seeding writes into (seed.go,
	// usermount.go) — so this does not disturb agent config sync.
	//
	// Claude agentcfg overlay disk. The overlayfs upper and work dirs for
	// /root/.claude live at /var/lib/nexus3/agentcfg/{upper,work} — both on
	// this volume. Moving them off root makes the governor-visible: the root
	// disk (/dev/vda) is never enrolled in ResizableDiskIndices, so it could
	// never be grown no matter how full /root/.claude grew. This volume can.
	// Both upper and work share one filesystem (the kernel requirement for
	// overlayfs), satisfying the constraint without touching root ext4.
	// 2 GiB: the upper layer stores only deltas from the RO lower — transcripts,
	// todos, stats — not build artifacts. The real upper layer (agentcfg-upper
	// on root ext4) measured live at 2.0 MB; the remainder of any apparently
	// larger figure is the RO virtiofs lower, host-backed, consuming zero guest
	// disk (D-RAM-12 retracts the earlier 442 MiB figure as a measurement
	// error — it was the merged overlay view, not the writable upper). The
	// operator ratified 2 GiB to give headroom for growth (D-RAM-13).
	args = append(args, "--mount-named", herdrAgentCfgDiskVolumeName(handle)+":/var/lib/nexus3/agentcfg:size=2g")
	// What still cannot grow after this: everything else on root (installed
	// packages, /tmp, /var/lib outside agentcfg, anything outside $HOME/go,
	// $HOME/.cache, and $HOME/.claude) remains on the ungrowable 4 GiB
	// /dev/vda. That is an accepted, explicit gap — see the ticket's Decision
	// section — not a silent reproduction of the bug.
	args = append(args, "--mount-named", herdrGoCacheDiskVolumeName(handle)+":/root/.cache:size=10g")
	args = append(args, "--mount-named", herdrGoPathDiskVolumeName(handle)+":/root/go:size=10g")
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
	if nested {
		args = append(args, "--nested")
	}
	args = append(args, "--agent", herdrPrimaryAgent(), "--egress", "open", handle)
	return args
}

// herdrPrimaryAgent resolves the primary agent name for herdr worktree sandboxes
// (D-TP-09). It reads sandbox.agents from the user-global config; the first
// entry is the primary. Falls back to sandbox.agent (singular), then to
// "claude-code" as the backward-compatible default when neither key is set.
func herdrPrimaryAgent() string {
	cfg, err := config.LoadUserGlobal()
	if err != nil {
		return "claude-code"
	}
	if len(cfg.Sandbox.Agents) > 0 {
		return cfg.Sandbox.Agents[0]
	}
	if cfg.Sandbox.Agent != "" {
		return cfg.Sandbox.Agent
	}
	return "claude-code"
}

// herdrDockerDiskVolumeName derives a VolumeStore-legal name
// ([a-z0-9][a-z0-9._-]*, per D-PD-84) for a worktree sandbox's docker storage
// disk from its handle. The handle is "<repo>/<branch>" with case preserved and
// a "/" separator, neither of which the grammar allows, so this lower-cases,
// maps every out-of-grammar byte (including "/") to a single "-", trims to a
// legal leading char, and appends "-docker".
func herdrDockerDiskVolumeName(handle string) string {
	return herdrHandleSlug(handle) + "-docker"
}

// herdrGoCacheDiskVolumeName derives the per-sandbox volume name for the
// /root/.cache build-cache disk (GOCACHE lives under here), following the same
// D-PD-84 slug rule as herdrDockerDiskVolumeName.
func herdrGoCacheDiskVolumeName(handle string) string {
	return herdrHandleSlug(handle) + "-gocache"
}

// herdrGoPathDiskVolumeName derives the per-sandbox volume name for the
// /root/go disk (GOPATH/GOMODCACHE live under here), following the same
// D-PD-84 slug rule as herdrDockerDiskVolumeName.
func herdrGoPathDiskVolumeName(handle string) string {
	return herdrHandleSlug(handle) + "-gopath"
}

// herdrAgentCfgDiskVolumeName derives the per-sandbox volume name for the
// /var/lib/nexus3/agentcfg disk. The overlayfs upper and work dirs for
// /root/.claude live on this volume so the governor can grow the disk when
// Claude session state grows (root /dev/vda is not governor-visible).
// Follows the same D-PD-84 slug rule as herdrDockerDiskVolumeName.
func herdrAgentCfgDiskVolumeName(handle string) string {
	return herdrHandleSlug(handle) + "-agentcfg"
}

// herdrHandleSlug lower-cases handle and collapses every byte outside
// [a-z0-9._-] (including the "<repo>/<branch>" separator) to a single "-",
// then trims to a VolumeStore-legal leading/trailing character (D-PD-84:
// [a-z0-9][a-z0-9._-]*). Returns "wt" for a degenerate handle that collapses
// to nothing, so callers always get a non-empty stem to append a suffix to.
func herdrHandleSlug(handle string) string {
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
	return slug
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

// herdrWorktreeGroundworkMount returns a mount spec for the main repo's
// .groundwork directory into a linked-worktree sandbox.
//
// .groundwork/ is gitignored and has no tracked files, so a linked worktree
// never contains it. Without this mount, in-guest agents cannot read the motive
// charter, tickets, or open items that drive their work.
//
// The spec is "<mainRepo>/.groundwork:<mainRepo>/.groundwork" — same absolute
// path host and guest, read-write, so in-guest agents can record evidence into
// ticket files. Mirrors the shape of herdrWorktreeGitDirMount.
//
// Returns "" when: (a) the worktreePath is not a linked worktree, (b) the
// .git pointer structure is unexpected, or (c) the .groundwork directory does
// not exist in the main repo root. Never mounts a path that is not present.
func herdrWorktreeGroundworkMount(worktreePath string) string {
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
	if !filepath.IsAbs(target) {
		target = filepath.Join(worktreePath, target)
	}
	target = filepath.Clean(target)
	worktreesDir := filepath.Dir(target)
	gitDir := filepath.Dir(worktreesDir)
	if filepath.Base(worktreesDir) != "worktrees" {
		return "" // unexpected structure; bail
	}
	// gitDir is <mainRepo>/.git; its parent is the main repo root.
	mainRepo := filepath.Dir(gitDir)
	groundwork := filepath.Join(mainRepo, ".groundwork")
	if _, statErr := os.Stat(groundwork); statErr != nil {
		return "" // .groundwork does not exist; nothing to mount
	}
	return groundwork + ":" + groundwork
}

// herdrWorktreePluginMounts returns read-only mount specs for ~/.claude/plugins
// symlinks whose targets live outside ~/.claude (e.g. plugins/nexus3 -> a repo
// checkout). ~/.claude itself is live-mounted into the guest (D-1), so those
// links dangle unless their targets are mounted at the same host path. Warnings
// (dangling links) are passed to warn; hostHome=="" → os.UserHomeDir().
//
// Returns nil when the home dir cannot be resolved or there is nothing to mount.
func herdrWorktreePluginMounts(hostHome string, warn func(string)) []string {
	if hostHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		hostHome = home
	}
	specs, warnings := service.ResolvePluginSymlinkMounts(hostHome)
	if warn != nil {
		for _, msg := range warnings {
			warn(msg)
		}
	}
	return specs
}

// buildWorktreeEgressArgs derives the --secret and --repo CLI args from the
// egress.policy and egress.secrets sections of the checkout's .nexus/config.yaml.
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
	for _, p := range cfg.Egress.Policy {
		if len(p.Paths) > 0 {
			pathPolicies = egressAddHostPolicy(pathPolicies, p.Host,
				domain.EgressHostPolicy{Paths: p.Paths})
		}
	}

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
//     .nexus/config.yaml, return --file <project root: the dir holding .nexus/>
//     so the full project config (image, egress rules, etc.) is applied
//     during the build.
//  2. Otherwise return --image <herdrDefaultImage>.
//
// Never returns an imageFlag/imageVal pair that would produce an unbootable
// sandbox create argv. Returns a non-nil error only when config.Load itself
// fails (e.g., the .nexus/config.yaml file is present but malformed).
func herdrResolveWorktreeImage(checkoutPath string) (imageFlag, imageVal string, err error) {
	_, cfgPath, loadErr := config.Load(checkoutPath)
	if loadErr != nil {
		return "", "", fmt.Errorf("resolve worktree image: load config: %w", loadErr)
	}
	imageFlag, imageVal = herdrResolveWorktreeImageFromConfig(checkoutPath, cfgPath)
	return imageFlag, imageVal, nil
}

// herdrResolveWorktreeImageFromConfig is herdrResolveWorktreeImage with the
// config.Load result already in hand (cfgPath "" when the checkout has no
// .nexus/config.yaml), so a caller that also needs the parsed config loads
// the file once.
func herdrResolveWorktreeImageFromConfig(checkoutPath, cfgPath string) (imageFlag, imageVal string) {
	if cfgPath != "" {
		// .nexus/config.yaml found: use --file <project root> (NOT the .nexus
		// dir) so the build applies the full project config.
		return "--file", config.ProjectDir(cfgPath)
	}
	// No .nexus/config.yaml, but a .nexus/Containerfile (or .nexus/Dockerfile) is itself
	// a complete build definition — the `--file` build engine reads exactly that
	// file from the context dir. So its presence ALONE is enough to build the
	// worktree sandbox from it; requiring a separate .nexus/config.yaml sentinel would be
	// a surprising extra step (the Containerfile is the thing that matters).
	if dir := nexusContainerfileDir(checkoutPath); dir != "" {
		return "--file", dir
	}
	return "--image", herdrDefaultImage
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

// herdrWorktreeSandboxParseArgs strips --auto / --conditional / --nested flags
// from the beginning of args and returns the remaining positional args and mode
// flags.
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
// --nested is the operator opt-in for nested virtualisation (D-N3N-02). An
//
//	operator-supplied flag is safe because it is not branch-controlled.
//	The effective nested value is (--nested flag) OR (sandbox.nested in
//	the checkout's .nexus/config.yaml). See herdrWorktreeSandbox.
//
// Called by the "worktree-sandbox" case in runHerdrPlugin so flag parsing
// happens before the workspace ID is read, preventing the flags from being
// consumed as the workspace ID.
func herdrWorktreeSandboxParseArgs(args []string) (rest []string, conditional bool, auto bool, nested bool) {
	for len(args) > 0 {
		switch args[0] {
		case "--auto":
			auto = true
			args = args[1:]
		case "--conditional":
			conditional = true
			args = args[1:]
		case "--nested":
			nested = true
			args = args[1:]
		default:
			return args, conditional, auto, nested
		}
	}
	return args, conditional, auto, nested
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
	// nestedFlag is the operator opt-in from the --nested CLI flag (D-N3N-02).
	// The effective nested value is nestedFlag OR sandbox.nested from the
	// checkout's .nexus/config.yaml. Either channel alone is sufficient.
	nestedFlag bool,
	createFn func(context.Context, string, string, string, string, []string, []string, string, domain.EgressPathPolicies, bool) error,
	getFn func(context.Context, string) (domain.Sandbox, error),
) error {
	if _, err := herdrSpaceResolve(ctx, storeRoot, workspaceID); err == nil {
		fmt.Fprintf(w, "worktree-sandbox: workspace %s already bound\n", workspaceID)
		return nil
	}

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

	if !info.IsLinkedWorktree {
		fmt.Fprintf(w, "worktree-sandbox: workspace %s is main checkout, skipping\n", workspaceID)
		return nil
	}

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
		bind, reason := herdrWorktreeAutoBindDecision(
			herdrWorktreeSandboxRepoCheck(ctx, storeRoot, info),
			herdrRepoHasNexus3Config(info.Path),
		)
		if !bind {
			fmt.Fprintf(w, "worktree-sandbox: %s at %s; skipping (bind by hand via \"nexus3: sandbox this worktree\", or add .nexus/config.yaml)\n", reason, info.Path)
			return nil
		}
		fmt.Fprintf(w, "worktree-sandbox: auto-binding (%s)\n", reason)
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
		extraMounts = append(extraMounts, gitMount)
	}
	// Mount the main repo's .groundwork dir (gitignored, never tracked) so
	// in-guest agents can read motive charters, tickets, and open items.
	// Read-write so agents can record evidence into their own ticket files.
	if gwMount := herdrWorktreeGroundworkMount(info.Path); gwMount != "" {
		extraMounts = append(extraMounts, gwMount)
	}
	// Host ~/.claude is live-mounted at /root/.claude, but ~/.claude/plugins/*
	// symlinks pointing outside ~/.claude dangle in the guest. Mount their
	// targets read-only at the same host path so the plugin/skills resolve.
	extraMounts = append(extraMounts, herdrWorktreePluginMounts("", func(msg string) {
		fmt.Fprintf(w, "worktree-sandbox: warning: %s\n", msg)
	})...)

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

	// Step 6.5: load the checkout's .nexus/config.yaml once (D-12). config.Load
	// walks from info.Path up to the .git boundary; an absent file is not an
	// error, a malformed one IS — the operator must fix it before the sandbox
	// can be created. The same file drives the bootable image (--file), the
	// egress policy/secrets and the nested opt-in.
	checkoutCfg, cfgPath, loadErr := config.Load(info.Path)
	if loadErr != nil {
		fmt.Fprintf(w, "worktree-sandbox: load checkout config: %v\n", loadErr)
		if !failSafe {
			return fmt.Errorf("worktree-sandbox: load checkout config: %w", loadErr)
		}
		return nil
	}
	imageFlag, imageVal := herdrResolveWorktreeImageFromConfig(info.Path, cfgPath)
	fmt.Fprintf(w, "worktree-sandbox: build source: %s %s\n", imageFlag, imageVal)

	var (
		egressSecrets      []string
		egressAllowedRepo  string
		egressPathPolicies domain.EgressPathPolicies
		nestedCfg          bool
	)
	if cfgPath != "" {
		egressSecrets, egressAllowedRepo, egressPathPolicies, err = buildWorktreeEgressArgs(checkoutCfg)
		if err != nil {
			fmt.Fprintf(w, "worktree-sandbox: build egress args: %v\n", err)
			if !failSafe {
				return fmt.Errorf("worktree-sandbox: build egress args: %w", err)
			}
			return nil
		}
		nestedCfg = checkoutCfg.Sandbox.Nested
		fmt.Fprintf(w, "worktree-sandbox: egress policy from %s: %d secret bind(s), nested=%t\n", cfgPath, len(egressSecrets), nestedCfg)
	} else {
		fmt.Fprintf(w, "worktree-sandbox: %s absent in checkout; no egress policy or nested opt-in\n", config.ConfigRelPath)
	}
	// Step 7: create sandbox. A 240 s context covers image pull, ext4 setup,
	// and VM boot on typical hardware. Explicit mode failures are real errors;
	// auto/conditional mode is fail-safe (workspace stays a host shell).
	// egressPathPolicies is conveyed to the subprocess via --egress-policy-json
	// so the generic policy reaches MITM enforcement (D-PDE-16 worktree gap fix).
	createCtx, createCancel := context.WithTimeout(ctx, herdrWorktreeCreateTimeout)
	defer createCancel()
	// sb is declared here so the reconcile path (step 7 error + getFn success)
	// and the normal path (step 7 success + step 8 getFn) both feed the shared
	// binding-write block below without re-declaring.
	var sb domain.Sandbox
	if createErr := createFn(createCtx, handle, mountSpec, imageFlag, imageVal, extraMounts, egressSecrets, egressAllowedRepo, egressPathPolicies, nestedFlag || nestedCfg); createErr != nil {
		// Create failed. Probe getFn: a prior run may have committed the sandbox
		// store record (e.g. crashed after store.Create but before HerdrSpacePut),
		// leaving an orphaned sandbox with no binding. If getFn confirms the
		// sandbox exists and it is adoptable, reconcile it. If getFn also fails,
		// this is a genuine create failure.
		var reconcileErr error
		sb, reconcileErr = getFn(ctx, handle)
		if reconcileErr != nil {
			// Sandbox does not exist in the store — genuine create failure.
			// MUTATION PROOF: change `if !failSafe { return fmt.Errorf(...) }` to
			// always return nil → TestHerdrWorktreeSandbox_explicitMode_createError_returnsError gets nil → RED.
			fmt.Fprintf(w, "worktree-sandbox: sandbox create: %v\n", createErr)
			if !failSafe {
				return fmt.Errorf("worktree-sandbox: sandbox create: %w", createErr)
			}
			return nil
		}
		// Correction 1: verify the /workspace LiveMount points at this worktree.
		// A handle collision (same repo basename + same branch slug from a different
		// checkout, e.g. a stale record after the worktree was deleted and recreated)
		// must not silently adopt a sandbox whose /workspace points elsewhere.
		var workspaceMount *domain.LiveMount
		for i := range sb.LiveMounts {
			if sb.LiveMounts[i].GuestPath == "/workspace" {
				workspaceMount = &sb.LiveMounts[i]
				break
			}
		}
		if workspaceMount == nil {
			fmt.Fprintf(w, "worktree-sandbox: sandbox %s has no /workspace mount — cannot adopt\n", handle)
			if !failSafe {
				return fmt.Errorf("worktree-sandbox: sandbox %s has no /workspace mount", handle)
			}
			return nil
		}
		if filepath.Clean(workspaceMount.HostPath) != filepath.Clean(info.Path) {
			fmt.Fprintf(w, "worktree-sandbox: sandbox %s /workspace mount is %s (this worktree is %s) — refusing adopt\n", handle, workspaceMount.HostPath, info.Path)
			if !failSafe {
				return fmt.Errorf("worktree-sandbox: sandbox %s /workspace mismatch: record=%s worktree=%s", handle, workspaceMount.HostPath, info.Path)
			}
			return nil
		}
		// Correction 4: refuse to adopt a sandbox that is not in a usable state.
		// A Created-state record left by a killed create subprocess is not safe
		// to adopt — the VM never finished booting. RemovalMarker means the
		// sandbox is being destroyed.
		if sb.RemovalMarker || (sb.State != domain.Running && sb.State != domain.Stopped) {
			fmt.Fprintf(w, "worktree-sandbox: sandbox %s not adoptable: state=%s removal=%v\n", handle, sb.State, sb.RemovalMarker)
			if !failSafe {
				return fmt.Errorf("worktree-sandbox: sandbox %s not adoptable: state=%s removal=%v", handle, sb.State, sb.RemovalMarker)
			}
			return nil
		}
		fmt.Fprintf(w, "worktree-sandbox: sandbox %s already exists (binding absent) — reconciling\n", handle)
	} else {
		// The binding is written BEFORE opening the pane so the idempotency check
		// on the next run sees it even if the process dies during pane open.
		// MUTATION PROOF: keep the error-path return but discard sb on success:
		//   var probe domain.Sandbox; probe, getFnErr = getFn(ctx, handle); _ = probe
		// → sb stays zero → binding.SandboxID == "" → RED
		// (TestHerdrWorktreeSandbox_happyPath_bindingFields: SandboxID = ""; want "<id>").
		var getFnErr error
		sb, getFnErr = getFn(ctx, handle)
		if getFnErr != nil {
			fmt.Fprintf(w, "worktree-sandbox: get sandbox %s: %v\n", handle, getFnErr)
			if !failSafe {
				return fmt.Errorf("worktree-sandbox: get sandbox %s: %w", handle, getFnErr)
			}
			return nil
		}
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

	// Error policy: sandbox+binding already exist and are recoverable on the next run,
	// so pane failure is always printed. Explicit mode also returns it (non-zero exit);
	// auto/conditional mode continues because the binding committed and the workspace is usable.
	if openPane {
		paneID, paneErr := herdrOpenGuestShellPane(ctx, herdrBin, handle, workspaceID, "", false)
		if paneID != "" {
			binding.GuestPaneID = paneID
			_ = HerdrSpacePut(ctx, storeRoot, binding) // best-effort patch
		}
		if paneErr != nil {
			// A pane failure is REPORTED IN EVERY MODE, auto/conditional
			// included. This used to return nil under failSafe, and the
			// consequence was the exact shape this slice exists to close: a live
			// VM, a committed binding, no guest pane, and the only trace of it a
			// line in `herdr plugin log list` —
			//
			//   exit 1  workspace_not_found
			//   worktree-sandbox: open guest pane: space-create: open shell pane: exit status 1
			//
			// — which is nowhere an operator looks. The provisioning now runs
			// inside a pane (plugins/herdr/bin/pane.sh, worktree-sandbox case)
			// that blocks on Enter for a non-zero exit, so returning the error
			// is what keeps the failure ON SCREEN. Returning nil here closes the
			// pane and buries it again.
			//
			// This is safe for the guest-shell dispatcher, the one caller that
			// used to depend on the nil: herdrDefaultShellAutoCreate now re-reads
			// the binding regardless of exit status, because the binding is
			// written BEFORE this step and a missing pane does not invalidate it.
			fmt.Fprintf(w, "worktree-sandbox: open guest pane: %v\n", paneErr)
			fmt.Fprintf(w, "worktree-sandbox: the sandbox %s and its binding are committed and reusable; "+
				"only the pane failed. Retry with: nexus3 herdr space-open-pane %s\n", handle, workspaceID)
			return fmt.Errorf("worktree-sandbox: open guest pane: %w", paneErr)
		}
	}

	if err := herdrWorkspaceRenameFn(ctx, herdrBin, workspaceID, label); err != nil {
		fmt.Fprintf(w, "worktree-sandbox: rename workspace: %v\n", err)
	}

	fmt.Fprintf(w, "worktree-sandbox: bound workspace %s → sandbox %s\n", workspaceID, handle)
	return nil
}

// herdrPluginLocalAgentStartup is the startup hook that herdr >=0.9 runs on
// the laptop when herdr starts. The body lives in internal/clientagent so it
// also builds into cmd/nexus3-client for macOS, where this package (and the
// full nexus3 CLI) does not compile.
func herdrPluginLocalAgentStartup(ctx context.Context) error {
	return clientagent.RunStartup(ctx)
}
