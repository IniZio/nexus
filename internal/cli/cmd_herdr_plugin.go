package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
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
	Register(Command{
		Name:    "__herdr-plugin",
		Summary: "Private shim invoked by the herdr plugin (not a public CLI surface)",
		Run:     runHerdrPlugin,
	})
}

// herdrPluginABIVersion is the integer probed by build.sh to detect skew
// between the installed plugin manifest and the nexus3 binary. Bump this
// whenever __herdr-plugin's subcommand surface changes in an incompatible way.
const herdrPluginABIVersion = "1"

// runHerdrPlugin dispatches __herdr-plugin <subcommand> [args...].
//
// This is a private surface between nexus3 and its own in-repo herdr plugin
// (plugins/herdr/). It is NOT part of the versioned --json machine contract
// and carries no --json guarantees. The double-underscore prefix marks it.
func runHerdrPlugin(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: "__herdr-plugin: subcommand required (abi|context-cwd|workspaces|attach|create|logs|doctor|open-pane|launch|space-create|space-create-from-file|space-open-pane|space-pause|space-resume|space-remove|space-list|shell-cwd)"}
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
		return herdrPluginCreate(ctx, os.Stdin, out.w, svc)

	case "logs":
		fmt.Fprintln(out.w, "log tailing not yet implemented")
		return nil

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
		if len(rest) == 0 {
			return &UsageError{Msg: "__herdr-plugin space-create: sandbox ref required"}
		}
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-create: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-create: resolve store: " + err.Error(), Err: err}
		}
		return herdrPluginSpaceCreate(ctx, rest[0], out.w, svc, storeRoot)

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
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-open-pane: resolve store: " + err.Error(), Err: err}
		}
		return herdrPluginSpaceOpenPane(ctx, rest[0], storeRoot)

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
		b, err := herdrSpaceResolve(ctx, storeRoot, rest[0])
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-pause: binding not found: " + rest[0], Err: err}
		}
		return HerdrSpacePauseByLabel(ctx, svc, storeRoot, b.SpaceLabel)

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
		b, err := herdrSpaceResolve(ctx, storeRoot, rest[0])
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-resume: binding not found: " + rest[0], Err: err}
		}
		return HerdrSpaceResumeByLabel(ctx, svc, storeRoot, b.SpaceLabel)

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
		b, err := herdrSpaceResolve(ctx, storeRoot, rest[0])
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-remove: binding not found: " + rest[0], Err: err}
		}
		herdrBin := os.Getenv("HERDR_BIN_PATH")
		return herdrSpaceRemoveFull(ctx, svc, storeRoot, herdrBin, b)

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

	default:
		return &UsageError{Msg: "__herdr-plugin: unknown subcommand: " + sub}
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

// herdrPluginWorkspaces lists all sandboxes, one per line: "project/name\tstate".
func herdrPluginWorkspaces(ctx context.Context, w io.Writer, svc *service.Service) error {
	sandboxes, err := svc.List(ctx)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin workspaces: " + err.Error(), Err: err}
	}
	for _, sb := range sandboxes {
		fmt.Fprintf(w, "%s\t%s\n", sb.Handle(), sb.State.String())
	}
	return nil
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

	herdrBin := os.Getenv("HERDR_BIN_PATH")
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

// herdrPluginCreate interactively prompts for project/name and creates a sandbox.
func herdrPluginCreate(ctx context.Context, r io.Reader, w io.Writer, svc *service.Service) error {
	// Preflight: validate the kernel path before prompting. svc.Create is
	// metadata-only (no boot), but the subsequent space-create will need the
	// kernel; failing fast here gives an actionable error before the interactive
	// prompts, rather than a cryptic boot failure on space-create.
	if _, err := resolveKernelPath(); err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin create: " + err.Error(), Err: err}
	}

	scanner := bufio.NewScanner(r)

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

	sb, err := svc.Create(ctx, project, name, service.CreateOptions{})
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin create: " + err.Error(), Err: err}
	}
	fmt.Fprintf(w, "created: %s\n", sb.Handle())
	return nil
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
	fmt.Fprintf(w, "creating sandbox %q from %s ...\n", handle, dockerfile)
	exe, err := os.Executable()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-create-from-file: resolve executable: " + err.Error(), Err: err}
	}
	createCmd := exec.CommandContext(ctx, exe, "sandbox", "create", handle, "--file", contextDir)
	createCmd.Stdout = w
	createCmd.Stderr = w
	if err := createCmd.Run(); err != nil {
		fmt.Fprintf(w, "error: sandbox create failed: %v\n", err)
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-create-from-file: sandbox create: " + err.Error(), Err: err}
	}

	// ── Open herdr space ──────────────────────────────────────────────────────
	fmt.Fprintf(w, "opening herdr space for %s ...\n", handle)
	return herdrPluginSpaceCreate(ctx, handle, w, svc, storeRoot)
}

// herdrPluginDoctor prints host preflight info and ABI version.
func herdrPluginDoctor(w io.Writer) error {
	exe, _ := os.Executable()
	herdrBin := os.Getenv("HERDR_BIN_PATH")
	herdrVer := os.Getenv("HERDR_VERSION")
	if herdrVer == "" {
		herdrVer = "(HERDR_VERSION not set)"
	}
	herdrBinStatus := "(HERDR_BIN_PATH not set)"
	if herdrBin != "" {
		herdrBinStatus = herdrBin
	}
	fmt.Fprintf(w, "nexus3 binary:  %s\n", exe)
	fmt.Fprintf(w, "plugin ABI:     %s\n", herdrPluginABIVersion)
	fmt.Fprintf(w, "herdr version:  %s\n", herdrVer)
	fmt.Fprintf(w, "HERDR_BIN_PATH: %s\n", herdrBinStatus)
	return nil
}

// herdrPluginOpenPane calls herdr to open a pane for the given workspace.
func herdrPluginOpenPane(ws string, extraArgs []string) error {
	herdrBin := os.Getenv("HERDR_BIN_PATH")
	if herdrBin == "" {
		fmt.Fprintln(os.Stderr, "__herdr-plugin open-pane: HERDR_BIN_PATH not set")
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin open-pane: HERDR_BIN_PATH not set"}
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
func herdrPluginSpaceCreate(ctx context.Context, ref string, w io.Writer, svc *service.Service, storeRoot string) error {
	herdrBin := os.Getenv("HERDR_BIN_PATH")
	if herdrBin == "" {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-create: HERDR_BIN_PATH not set"}
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

	// Idempotency: reuse existing binding if one exists for this handle.
	if existing, err := HerdrSpaceGetByLabel(ctx, storeRoot, label); err == nil {
		fmt.Fprintf(w, "reusing space: label=%s workspace_id=%s\n", existing.SpaceLabel, existing.HerdrWorkspaceID)
		return herdrOpenGuestShellPane(herdrBin, ref, existing.HerdrWorkspaceID)
	}

	// Create herdr workspace and capture its ID.
	workspaceID, err := herdrWorkspaceCreate(herdrBin, label)
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
	return herdrOpenGuestShellPane(herdrBin, ref, workspaceID)
}

// herdrWorkspaceCreate runs "herdr workspace create --label <label>" and returns the workspace ID.
// herdr prints a JSON envelope; we parse result.workspace.workspace_id from it.
func herdrWorkspaceCreate(herdrBin, label string) (string, error) {
	var buf strings.Builder
	cmd := exec.Command(herdrBin, "workspace", "create", "--label", label, "--no-focus")
	cmd.Stdout = &buf
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	// Parse {"id":...,"result":{"workspace":{"workspace_id":"wF",...},...}}
	var envelope struct {
		Result struct {
			Workspace struct {
				WorkspaceID string `json:"workspace_id"`
			} `json:"workspace"`
		} `json:"result"`
	}
	raw := strings.TrimSpace(buf.String())
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		// Fall back to treating the raw output as a plain ID (plain-text mode).
		id := raw
		if id == "" {
			return "", fmt.Errorf("herdr workspace create: empty output")
		}
		return id, nil
	}
	id := envelope.Result.Workspace.WorkspaceID
	if id == "" {
		return "", fmt.Errorf("herdr workspace create: workspace_id not found in response: %s", raw)
	}
	return id, nil
}

// herdrOpenGuestShellPane opens a guest-shell pane in the named workspace.
func herdrOpenGuestShellPane(herdrBin, ref, workspaceID string) error {
	cmd := exec.Command(herdrBin, "plugin", "pane", "open",
		"--plugin", "nexus3",
		"--entrypoint", "shell",
		"--workspace", workspaceID,
		"--env", "NEXUS3_WORKSPACE="+ref,
		"--focus",
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-create: open shell pane: " + err.Error(), Err: err}
	}
	return nil
}

// herdrPluginSpaceOpenPane resolves a space by ref, label, or workspace_id and opens another guest-shell pane.
func herdrPluginSpaceOpenPane(ctx context.Context, refOrLabel string, storeRoot string) error {
	herdrBin := os.Getenv("HERDR_BIN_PATH")
	if herdrBin == "" {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-open-pane: HERDR_BIN_PATH not set"}
	}

	b, err := herdrSpaceResolve(ctx, storeRoot, refOrLabel)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "space-open-pane: no space binding for " + refOrLabel}
	}

	return herdrOpenGuestShellPane(herdrBin, b.SandboxHandle, b.HerdrWorkspaceID)
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

// herdrSpaceRemoveFull is the complete teardown sequence for a herdr space:
//  1. Remove the sandbox (tolerating store.ErrNotFound — already gone).
//  2. Close the herdr workspace (tolerating workspace_not_found — already closed).
//  3. Delete the binding.
func herdrSpaceRemoveFull(ctx context.Context, svc HerdrSpaceSandboxService, storeRoot, herdrBin string, b HerdrSpaceBinding) error {
	if err := svc.Remove(ctx, b.SandboxHandle); err != nil {
		// Tolerate not-found: sandbox already gone via another route (reaper,
		// manual remove). Delete the binding anyway so no orphan mapping remains.
		// store.ErrNotFound is wrapped by service.resolve as "resolve %q: %w",
		// so errors.Is traverses the chain; the string fallback catches fakes.
		if !errors.Is(err, store.ErrNotFound) && !strings.Contains(err.Error(), "not found") {
			return fmt.Errorf("herdr-space: remove sandbox %q: %w", b.SandboxHandle, err)
		}
	}
	if err := herdrWorkspaceClose(ctx, herdrBin, b.HerdrWorkspaceID); err != nil {
		// Non-fatal: log and continue so the binding is always cleaned up.
		fmt.Fprintf(os.Stderr, "herdr-space: close workspace %q: %v (continuing)\n", b.HerdrWorkspaceID, err)
	}
	if err := HerdrSpaceDelete(ctx, storeRoot, b.SpaceLabel); err != nil && !errors.Is(err, ErrHerdrSpaceNotFound) {
		return fmt.Errorf("herdr-space: delete binding %q: %w", b.SpaceLabel, err)
	}
	return nil
}

// herdrWorkspaceClose runs `herdr workspace close <workspaceID>`.
// A "workspace_not_found" response is treated as success (already closed).
// herdrBin or workspaceID being empty is a no-op.
func herdrWorkspaceClose(ctx context.Context, herdrBin, workspaceID string) error {
	if herdrBin == "" || workspaceID == "" {
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
		fmt.Fprintf(w, "label=%s\tworkspace_id=%s\thandle=%s\tsandbox_id=%s\n",
			b.SpaceLabel, b.HerdrWorkspaceID, b.SandboxHandle, b.SandboxID)
	}
	return nil
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
