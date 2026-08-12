package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
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
		return &UsageError{Msg: "__herdr-plugin: subcommand required (abi|context-cwd|workspaces|attach|create|logs|doctor|open-pane|launch|space-create|space-create-from-file|space-open-pane|space-pause|space-resume|space-remove|space-list)"}
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
		if len(rest) < 2 {
			return &UsageError{Msg: "__herdr-plugin launch: usage: launch <image-ref> <command> [args...] (command must be an absolute path, e.g. /usr/local/bin/claude)"}
		}
		return herdrPluginLaunch(ctx, rest[0], rest[1:], out)

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
			return &UsageError{Msg: "__herdr-plugin space-pause: space label required"}
		}
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-pause: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-pause: resolve store: " + err.Error(), Err: err}
		}
		return HerdrSpacePauseByLabel(ctx, svc, storeRoot, rest[0])

	case "space-resume":
		if len(rest) == 0 {
			return &UsageError{Msg: "__herdr-plugin space-resume: space label required"}
		}
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-resume: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-resume: resolve store: " + err.Error(), Err: err}
		}
		return HerdrSpaceResumeByLabel(ctx, svc, storeRoot, rest[0])

	case "space-remove":
		if len(rest) == 0 {
			return &UsageError{Msg: "__herdr-plugin space-remove: space label required"}
		}
		svc, err := newSandboxService()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-remove: " + err.Error(), Err: err}
		}
		storeRoot, err := store.DefaultRoot()
		if err != nil {
			return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin space-remove: resolve store: " + err.Error(), Err: err}
		}
		return HerdrSpaceRemoveByLabel(ctx, svc, storeRoot, rest[0])

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

// herdrPluginLaunch boots a sandbox from imageRef, runs argv in-guest via the
// agent exec path (vsock gRPC control + data plane), streams stdout to out,
// then removes the sandbox. Boot and exec happen in one process; the boot
// driver's vsock socket path (SocketDir+ID) is shared with svc.driver, so
// svc.Exec can dial it after CreateAndBoot returns.
//
// Auth-header probe finding (D-P4-02 empirical status): UNKNOWN.
// CreateAndBootOptions.Broker is nil, so startSupervisor is never called and
// proxy.go's OnRequest swap hook is not on the data path at all. Code
// inspection confirms proxy.go OnRequest DOES swap Authorization: Bearer
// placeholders on allowlisted hosts when opts.Broker is non-nil. Empirical
// YES/NO deferred to S-EGRESS where a Broker will be wired.
func herdrPluginLaunch(ctx context.Context, imageRef string, argv []string, out *Output) error {
	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin launch: resolve store: " + err.Error(), Err: err}
	}
	cacheRoot := filepath.Join(storeRoot, "images")

	imgCache, err := image.NewCache(cacheRoot)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin launch: open image cache: " + err.Error(), Err: err}
	}

	svc, err := newSandboxService()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "__herdr-plugin launch: " + err.Error(), Err: err}
	}

	kernelPath := kernelPathFor()

	// newDriver mirrors cmd_sandbox.go's factory: socket/log paths use default
	// locations so that svc.driver (from SelectSubstrate, same SocketDir) can
	// reach the vsock file for svc.Exec after CreateAndBoot returns.
	newDriver := service.DriverFactory(func(ext4Path string) (driver.Driver, error) {
		cfg := buildCHConfig(kernelPath, ext4Path, 0, 0)
		if p, err := exec.LookPath("cloud-hypervisor"); err == nil {
			cfg.BinaryPath = p
		}
		return cloudhypervisor.New(cfg)
	})

	// probe polls vsock until the guest agent's listener accepts. Mirrors the
	// exact probe in cmd_sandbox.go (runSandboxCreate).
	probe := func(pCtx context.Context, drv driver.Driver, id domain.SandboxID) error {
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

	// Unique name per invocation to prevent ErrAlreadyExists on retry or concurrent runs.
	name := fmt.Sprintf("run-%x", time.Now().UnixNano())

	sb, err := service.CreateAndBoot(ctx, svc, imgCache, newDriver, probe,
		"herdr", name,
		service.CreateAndBootOptions{
			Image:               service.ImageSpec{Ref: imageRef},
			CacheRoot:           cacheRoot,
			ReachabilityTimeout: 60 * time.Second,
		},
	)
	if err != nil {
		return &CodedError{Code: sandboxCodeFor(err), Msg: "__herdr-plugin launch: boot: " + err.Error(), Err: err}
	}

	// Remove internally stops the VM — no separate Stop call needed.
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := svc.Remove(rmCtx, sb.ID.String()); err != nil {
			fmt.Fprintf(os.Stderr, "__herdr-plugin launch: cleanup: remove %s: %v\n", sb.ID, err)
		}
	}()

	// Exec via svc.Exec (the surface-layer path per agent_ops.go:28). svc.driver
	// shares defaultSocketDir() with the boot driver, so it can dial the vsock.
	// Env semantics: the guest agent appends req.Env to os.Environ() — it is a
	// merge, not a replacement. argv[0] must be an absolute path because
	// exec.Command resolves it via exec.LookPath in the agent binary's own
	// process environment (before cmd.Env applies). PATH is still injected for
	// the child's own subprocess lookups (claude shells out to bash, git, etc.).
	// HOME is injected for credential path resolution (claude's OAuth files).
	exitCode, err := svc.Exec(ctx, sb.ID.String(), agent.ExecOptions{
		Argv: argv,
		Env: map[string]string{
			"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
			"HOME": "/root",
		},
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
