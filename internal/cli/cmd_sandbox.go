package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/IniZio/nexus/internal/core/agent"
	"github.com/IniZio/nexus/internal/core/builder"
	"github.com/IniZio/nexus/internal/core/builder/builderimage"
	"github.com/IniZio/nexus/internal/core/config"
	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/core/driver"
	"github.com/IniZio/nexus/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus/internal/core/image"
	"github.com/IniZio/nexus/internal/core/lifecycle"
	"github.com/IniZio/nexus/internal/core/perimeter/cred"
	"github.com/IniZio/nexus/internal/core/resize"
	"github.com/IniZio/nexus/internal/core/service"
	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/core/vmcfg"
	"github.com/IniZio/nexus/internal/core/volumestore"
	"github.com/IniZio/nexus/internal/supervisor"
)

func init() {
	Register(Command{
		Name:    "sandbox",
		Summary: "Manage sandboxes (create|list|rm|start|stop)",
		Run:     runSandbox,
	})
}

const (
	sandboxErrCodeNotFound = "sandbox_not_found"

	sandboxErrCodeAlreadyExists = "sandbox_already_exists"

	sandboxErrCodeAmbiguousRef = "ambiguous_ref"

	sandboxErrCodeIllegalTransition = "illegal_transition"

	sandboxErrCodeNoSubstrate = "no_substrate"

	sandboxErrCodeNoGuestImage = "no_guest_image"

	sandboxErrCodeInvalidArgument = "invalid_argument"

	sandboxErrCodeAgentUnreachable = "agent_unreachable"

	sandboxErrCodeBadCredential = "bad_credential"
)

type sandboxNoopDriver struct {
	reason string
}

func (d *sandboxNoopDriver) Name() string { return "none" }

func (d *sandboxNoopDriver) Observe(_ context.Context, _ domain.SandboxID) (driver.Observation, error) {
	return driver.Observation{State: driver.Absent}, nil
}

func (d *sandboxNoopDriver) Start(_ context.Context, _ driver.StartRequest) (string, error) {
	msg := d.reason
	if msg == "" {
		msg = "no hypervisor driver is available"
	}
	return "", fmt.Errorf("%s: %w", msg, service.ErrNoSubstrate)
}

func (d *sandboxNoopDriver) Stop(_ context.Context, _ domain.SandboxID) error {
	return nil
}

func newSandboxService() (*service.Service, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return nil, fmt.Errorf("sandbox: resolve state directory: %w", err)
	}
	st, err := store.NewFileStore(root)
	if err != nil {
		return nil, fmt.Errorf("sandbox: open state directory: %w", err)
	}

	var drv driver.Driver
	if realDrv, serr := SelectSubstrate(); serr != nil {
		drv = &sandboxNoopDriver{reason: serr.Msg}
	} else {
		drv = realDrv
	}

	svc := service.New(st, drv, lifecycle.New())
	svc.WithVolumes(volumestore.New(filepath.Join(root, "volumes")))
	return svc, nil
}

func sandboxCodeFor(err error) string {
	var ambig *domain.ErrAmbiguous
	var noMatch *domain.ErrNoMatch
	var illegalT *lifecycle.IllegalTransitionError
	switch {
	case errors.As(err, &ambig):
		return sandboxErrCodeAmbiguousRef
	case errors.As(err, &noMatch):
		return sandboxErrCodeNotFound
	case errors.Is(err, store.ErrNotFound):
		return sandboxErrCodeNotFound
	case errors.Is(err, store.ErrAlreadyExists):
		return sandboxErrCodeAlreadyExists
	case errors.As(err, &illegalT):
		return sandboxErrCodeIllegalTransition
	case errors.Is(err, service.ErrNoSubstrate):
		return sandboxErrCodeNoSubstrate
	case errors.Is(err, cloudhypervisor.ErrNoKernelConfigured):
		return sandboxErrCodeNoGuestImage
	case errors.Is(err, service.ErrAgentUnreachable):
		return sandboxErrCodeAgentUnreachable
	default:
		return ErrCodeInternalError
	}
}

func errSandbox(prefix string, cause error) *CodedError {
	return &CodedError{
		Code: sandboxCodeFor(cause),
		Msg:  prefix + ": " + cause.Error(),
		Err:  cause,
	}
}

// sandboxCreateDiskProbe is the seam for the pre-create disk guard; tests
// replace it to drive both branches of sandboxCreateDiskGuard through the
// real runSandboxCreate call site.
var sandboxCreateDiskProbe = func(ctx context.Context, stateDir string, c *image.Cache, agentTag string) (service.DiskUsageReport, error) {
	st, err := store.NewFileStore(stateDir)
	if err != nil {
		return service.DiskUsageReport{}, err
	}
	return service.DiskUsage(ctx, stateDir, c, st, agentTag)
}

// sandboxCreateDiskGuard refuses a booted create when host free space is
// below the builder floor (usage summary + hints on stderr, non-zero exit)
// and warns once when free space is under twice the floor. A failed probe is
// logged and does not block the create.
func sandboxCreateDiskGuard(ctx context.Context, out *Output, stateDir string, c *image.Cache) error {
	rep, err := sandboxCreateDiskProbe(ctx, stateDir, c, currentAgentTag())
	if err != nil {
		slog.Warn("sandbox create: disk usage probe failed; proceeding without disk guard", "err", err)
		return nil
	}
	if ugCfg, ugErr := config.LoadUserGlobal(); ugErr == nil && ugCfg.Image.FreeSpaceFloorGiB > 0 {
		rep.FloorBytes = uint64(ugCfg.Image.FreeSpaceFloorGiB) << 30
		rep.BelowFloor = rep.FreeBytes < rep.FloorBytes
	}
	if rep.BelowFloor {
		fmt.Fprint(out.Stderr(), renderDiskUsage(rep))
		return errSandbox("sandbox create", fmt.Errorf(
			"free space %s on %s is below the %s floor; reclaim space first (see disk usage above, or run: nexus disk usage)",
			humanBytes(int64(rep.FreeBytes)), rep.StateDir, humanBytes(int64(rep.FloorBytes))))
	}
	if rep.FreeBytes < 2*rep.FloorBytes {
		largest := "none"
		var largestBytes int64 = -1
		for _, cat := range rep.Categories {
			if cat.Bytes > largestBytes {
				largest, largestBytes = fmt.Sprintf("%s (%s)", cat.Name, humanBytes(cat.Bytes)), cat.Bytes
			}
		}
		fmt.Fprintf(out.Stderr(), "warning: sandbox create: free space %s is under twice the %s floor; largest category: %s; run `nexus disk usage` for reclaim hints\n",
			humanBytes(int64(rep.FreeBytes)), humanBytes(int64(rep.FloorBytes)), largest)
	}
	return nil
}

type sandboxInfoJSON struct {
	ID           string            `json:"id"`
	Project      string            `json:"project"`
	Name         string            `json:"name"`
	Handle       string            `json:"handle"`
	State        string            `json:"state"`
	Labels       map[string]string `json:"labels,omitempty"`
	RemoveOnExit bool              `json:"remove_on_exit,omitempty"`
	StopReason   string            `json:"stop_reason,omitempty"`
}

func toSandboxInfoJSON(sb domain.Sandbox) sandboxInfoJSON {
	return sandboxInfoJSON{
		ID:           sb.ID.String(),
		Project:      sb.Project,
		Name:         sb.Name,
		Handle:       sb.Handle(),
		State:        sb.State.String(),
		Labels:       sb.Labels,
		RemoveOnExit: sb.RemoveOnExit,
		StopReason:   string(sb.StopReason),
	}
}

type sandboxListDataJSON struct {
	Sandboxes []sandboxInfoJSON `json:"sandboxes"`
}

type sandboxRemovedDataJSON struct {
	ID     string `json:"id"`
	Handle string `json:"handle"`
}

func runSandbox(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: "sandbox: missing subcommand; usage: sandbox <create|list|rm|start|stop>"}
	}

	verb := args[0]
	verbArgs := args[1:]

	switch verb {
	case "start", "stop":
		if _, serr := SelectSubstrate(); serr != nil {
			return &CodedError{
				Code: sandboxErrCodeNoSubstrate,
				Msg:  serr.Msg,
				Err:  serr,
			}
		}
	}

	svc, err := newSandboxService()
	if err != nil {
		return errSandbox("sandbox", err)
	}

	switch verb {
	case "create":
		return runSandboxCreate(ctx, verbArgs, out, svc)
	case "list":
		return runSandboxList(ctx, verbArgs, out, svc)
	case "rm":
		return runSandboxRm(ctx, verbArgs, out, svc)
	case "start":
		return runSandboxStart(ctx, verbArgs, out, svc)
	case "stop":
		return runSandboxStop(ctx, verbArgs, out, svc)
	default:
		return &UsageError{Msg: fmt.Sprintf("sandbox: unknown subcommand %q; valid: create list rm start stop", verb)}
	}
}

type sandboxCreateFlags struct {
	rm               bool
	forceDiskSpace   bool
	imageRef         string
	rootfsPath       string
	filePath         string
	dockerfilePath   string // --dockerfile / -f: explicit Containerfile path override
	memoryMiB        uint32
	vcpus            uint32
	labels           map[string]string
	nestedVirt       bool
	workspacePath    string   // --workspace <host-path>: host git worktree to capture
	captureMaxBytes  int64    // --capture-max <size>: explicit workspace capture cap (0 = auto)
	memoryMaxMiB     uint32   // --memory-max <MiB>: RAM ceiling for hotplug region
	vcpusMax         uint32   // --vcpus-max <n>:    vCPU ceiling for hotplug
	diskMaxGiB       uint32   // --disk-max <GiB>:   disk grow ceiling
	builderMemoryMiB uint32   // --builder-memory <MiB>: builder VM RAM (0 = use default 2048 MiB; min 1024 when set)
	secrets          []string // --secret ENV@host[,host…] (repeatable)
	egressClosed     bool     // --egress closed: disable open egress (D-PD-33)
	egressExplicit   bool
	agentName        string
	extraAgentNames  []string
	allowHosts       []string                  // --allow-host <hostname> (repeatable): add to AllowedHosts when --egress closed
	allowedRepo      string                    // --repo owner/name: scope MITM path allowlist to one GitHub repo (D-PD-36)
	pathPolicies     domain.EgressPathPolicies // --egress-policy-json: JSON-encoded generic path policies (worktree subprocess channel)
	mountNamed       []string                  // --mount-named <vol>:<guest-path>[:ro|kind=dir|size=Xg] (SD2-6-MOUNT)
	mountLive        []string                  // --mount <host-path>:<guest-path>[:ro] (D-PD-53 live virtiofs)
	noShareSettings  bool                      // --no-share-settings: skip curated host agent config overlay (A-MOUNT)
	noUserMounts     bool                      // --no-user-mounts: skip operator tool-dir live mounts (usermount-table-host)
	positionals      []string
}

func applyProjectConfig(f *sandboxCreateFlags) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("sandbox create: getwd: %w", err)
	}
	cfg, cfgPath, err := config.Load(cwd)
	if err != nil {
		return fmt.Errorf("sandbox create: %w", err)
	}

	var memPtr *int
	if f.memoryMiB > 0 {
		v := int(f.memoryMiB)
		memPtr = &v
	}
	var vcpusPtr *int
	if f.vcpus > 0 {
		v := int(f.vcpus)
		vcpusPtr = &v
	}
	flags := config.Flags{
		EgressAllow: f.allowHosts, // nil when no --allow-host was given
		Image:       f.imageRef,
		Memory:      memPtr,
		VCPUs:       vcpusPtr,
		Mounts:      f.mountLive, // nil when --mount was not given
	}
	resolved := config.Resolve(flags, cfg, config.Defaults{})

	if f.imageRef == "" && f.filePath == "" && f.rootfsPath == "" {
		f.imageRef = resolved.Image
	}
	if resolved.MemoryMiB != 0 {
		f.memoryMiB = uint32(resolved.MemoryMiB)
	}
	if resolved.VCPUs != 0 {
		f.vcpus = uint32(resolved.VCPUs)
	}

	f.allowHosts = resolved.EgressAllow

	// Populate pathPolicies from .nexus/config.yaml egress.policy when not already
	// set via --egress-policy-json (worktree subprocess channel). The ssh relay
	// (startGitSSHRelay in the supervisor) reads PathPolicies[""] to derive
	// the git SSH allowlist; without this conversion the relay always denies.
	if len(f.pathPolicies) == 0 && len(cfg.Egress.Policy) > 0 {
		pp := make(domain.EgressPathPolicies)
		pp[""] = make(map[string]domain.EgressHostPolicy)
		for _, ep := range cfg.Egress.Policy {
			pp[""][ep.Host] = domain.EgressHostPolicy{Paths: ep.Paths}
		}
		f.pathPolicies = pp
	}

	if f.mountLive == nil && len(resolved.Mounts) > 0 {
		// Mounts resolve against the project root (the dir holding .nexus/),
		// not the .nexus directory itself.
		projectDir := config.ProjectDir(cfgPath)
		resolvedMounts, resolveErr := config.ResolveMounts(resolved.Mounts, projectDir)
		if resolveErr != nil {
			return fmt.Errorf("sandbox create: %s: %w", config.ConfigRelPath, resolveErr)
		}
		f.mountLive = resolvedMounts
	}

	if f.agentName == "" && cfg.Sandbox.Agent != "" {
		if _, ok := cred.ProfileByName(cfg.Sandbox.Agent); !ok {
			return fmt.Errorf("sandbox create: %s: sandbox.agent %q is not a known agent (one of: %s)",
				config.ConfigRelPath, cfg.Sandbox.Agent, strings.Join(cred.ProfileNames(), ", "))
		}
		f.agentName = cfg.Sandbox.Agent
	}

	if f.memoryMaxMiB == 0 && cfg.Sandbox.MemoryMax > 0 {
		f.memoryMaxMiB = uint32(cfg.Sandbox.MemoryMax)
		if f.memoryMaxMiB > 0 && f.memoryMiB > 0 && f.memoryMaxMiB < f.memoryMiB {
			return &UsageError{Msg: fmt.Sprintf(
				"sandbox create: --memory-max %d MiB is less than --memory %d MiB; ceiling must exceed boot size",
				f.memoryMaxMiB, f.memoryMiB)}
		}
	}

	if !f.nestedVirt && cfg.Sandbox.Nested {
		f.nestedVirt = true
	}

	return nil
}

func applyUserGlobalConfig(f *sandboxCreateFlags) error {
	userCfg, err := config.LoadUserGlobal()
	if err != nil {
		slog.Warn("sandbox create: user-global config load failed; skipping user-global defaults", "err", err)
		return nil
	}

	if f.agentName == "" {
		if len(userCfg.Sandbox.Agents) > 0 {
			for _, name := range userCfg.Sandbox.Agents {
				if _, ok := cred.ProfileByName(name); !ok {
					return fmt.Errorf("sandbox create: user-global config sandbox.agents: %q is not a known agent (one of: %s)",
						name, strings.Join(cred.ProfileNames(), ", "))
				}
			}
			f.agentName = userCfg.Sandbox.Agents[0]
			if len(userCfg.Sandbox.Agents) > 1 {
				f.extraAgentNames = append([]string(nil), userCfg.Sandbox.Agents[1:]...)
			}
		} else if userCfg.Sandbox.Agent != "" {
			if _, ok := cred.ProfileByName(userCfg.Sandbox.Agent); !ok {
				slog.Warn("sandbox create: user-global config sandbox.agent is not a known agent; ignoring",
					"agent", userCfg.Sandbox.Agent,
					"known", strings.Join(cred.ProfileNames(), ", "))
			} else {
				f.agentName = userCfg.Sandbox.Agent
			}
		}
	}

	if f.memoryMaxMiB == 0 && userCfg.Sandbox.MemoryMax > 0 {
		f.memoryMaxMiB = uint32(userCfg.Sandbox.MemoryMax)
		if f.memoryMaxMiB > 0 && f.memoryMiB > 0 && f.memoryMaxMiB < f.memoryMiB {
			return &UsageError{Msg: fmt.Sprintf(
				"sandbox create: --memory-max %d MiB is less than --memory %d MiB; ceiling must exceed boot size",
				f.memoryMaxMiB, f.memoryMiB)}
		}
	}

	if f.builderMemoryMiB == 0 && userCfg.Builder.MemoryMiB > 0 {
		f.builderMemoryMiB = uint32(userCfg.Builder.MemoryMiB)
	}

	return nil
}

func parseSandboxCreateArgs(args []string) (sandboxCreateFlags, error) {
	f := sandboxCreateFlags{}
	i := 0
	for i < len(args) {
		arg := args[i]
		switch arg {
		case "--rm":
			f.rm = true
		case "--force":
			f.forceDiskSpace = true
		case "--image":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --image requires an argument"}
			}
			i++
			f.imageRef = args[i]
		case "--rootfs":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --rootfs requires an argument"}
			}
			i++
			f.rootfsPath = args[i]
		case "--file":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --file requires an argument"}
			}
			i++
			f.filePath = args[i]
		case "--memory":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --memory requires an argument"}
			}
			i++
			v, err := strconv.ParseUint(args[i], 10, 32)
			if err != nil {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: --memory %q: invalid MiB value", args[i])}
			}
			f.memoryMiB = uint32(v)
		case "--vcpus":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --vcpus requires an argument"}
			}
			i++
			v, err := strconv.ParseUint(args[i], 10, 32)
			if err != nil {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: --vcpus %q: invalid count", args[i])}
			}
			f.vcpus = uint32(v)
		case "--label":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --label requires an argument"}
			}
			i++
			k, v, ok := strings.Cut(args[i], "=")
			if !ok || k == "" {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: --label %q: must be KEY=VALUE", args[i])}
			}
			if f.labels == nil {
				f.labels = make(map[string]string)
			}
			f.labels[k] = v
		case "--dockerfile", "-f":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --dockerfile requires an argument"}
			}
			i++
			f.dockerfilePath = args[i]
		case "--nested":
			f.nestedVirt = true
		case "--workspace":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --workspace requires an argument"}
			}
			i++
			f.workspacePath = args[i]
		case "--capture-max":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --capture-max requires an argument"}
			}
			i++
			n, err := parseHumanBytes(args[i])
			if err != nil {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: --capture-max %q: %v", args[i], err)}
			}
			f.captureMaxBytes = n
		case "--memory-max":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --memory-max requires an argument"}
			}
			i++
			v, err := strconv.ParseUint(args[i], 10, 32)
			if err != nil {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: --memory-max %q: invalid MiB value", args[i])}
			}
			f.memoryMaxMiB = uint32(v)
		case "--vcpus-max":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --vcpus-max requires an argument"}
			}
			i++
			v, err := strconv.ParseUint(args[i], 10, 32)
			if err != nil {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: --vcpus-max %q: invalid count", args[i])}
			}
			f.vcpusMax = uint32(v)
		case "--disk-max":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --disk-max requires an argument"}
			}
			i++
			v, err := strconv.ParseUint(args[i], 10, 32)
			if err != nil {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: --disk-max %q: invalid GiB value", args[i])}
			}
			f.diskMaxGiB = uint32(v)
		case "--secret":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --secret requires ENV@host[,host…]"}
			}
			i++
			f.secrets = append(f.secrets, args[i])
		case "--no-share-settings":
			f.noShareSettings = true
		case "--no-user-mounts":
			f.noUserMounts = true
		case "--egress":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --egress requires a value (open|closed)"}
			}
			i++
			f.egressExplicit = true
			switch args[i] {
			case "open":
				f.egressClosed = false
			case "closed":
				f.egressClosed = true
			default:
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: --egress %q: want 'open' or 'closed'", args[i])}
			}
		case "--agent":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: --agent requires a name (one of: %s)",
					strings.Join(cred.ProfileNames(), ", "))}
			}
			i++
			if _, ok := cred.ProfileByName(args[i]); !ok {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: --agent %q is not a known agent (one of: %s)",
					args[i], strings.Join(cred.ProfileNames(), ", "))}
			}
			f.agentName = args[i]
		case "--allow-host":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --allow-host requires a hostname"}
			}
			i++
			f.allowHosts = append(f.allowHosts, args[i])
		case "--repo":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --repo requires owner/name"}
			}
			i++
			if !strings.Contains(args[i], "/") {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: --repo %q is not in owner/name format", args[i])}
			}
			f.allowedRepo = args[i]
		case "--egress-policy-json":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --egress-policy-json requires a JSON argument"}
			}
			i++
			var pp domain.EgressPathPolicies
			if err := json.Unmarshal([]byte(args[i]), &pp); err != nil {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: --egress-policy-json: %v", err)}
			}
			for k := range pp {
				if k != "" {
					return f, &UsageError{Msg: fmt.Sprintf(
						"sandbox create: --egress-policy-json: top-level key %q is not enforceable "+
							"(use the wildcard key \"\" — non-empty placeholder keys are only resolved "+
							"after create time and would silently fail to bound the token)",
						k)}
				}
			}
			f.pathPolicies = pp
		case "--mount-named":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --mount-named requires <volume-name>:<guest-path>"}
			}
			i++
			f.mountNamed = append(f.mountNamed, args[i])
		case "--mount":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --mount requires <host-path>:<guest-path>[:ro]"}
			}
			i++
			f.mountLive = append(f.mountLive, args[i])
		case "--builder-memory":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --builder-memory requires an argument"}
			}
			i++
			v, err := strconv.ParseUint(args[i], 10, 32)
			if err != nil {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: --builder-memory %q: invalid MiB value", args[i])}
			}
			if v > 0 && v < 1024 {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: --builder-memory %d MiB is below the minimum of 1024 MiB", v)}
			}
			f.builderMemoryMiB = uint32(v)
		default:
			if len(arg) > 1 && arg[0] == '-' {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: unknown flag %q", arg)}
			}
			f.positionals = append(f.positionals, arg)
		}
		i++
	}
	if f.memoryMaxMiB > 0 && f.memoryMiB > 0 && f.memoryMaxMiB < f.memoryMiB {
		return f, &UsageError{Msg: fmt.Sprintf(
			"sandbox create: --memory-max %d MiB is less than --memory %d MiB; ceiling must exceed boot size",
			f.memoryMaxMiB, f.memoryMiB)}
	}
	if f.vcpusMax > 0 && f.vcpus > 0 && f.vcpusMax < f.vcpus {
		return f, &UsageError{Msg: fmt.Sprintf(
			"sandbox create: --vcpus-max %d is less than --vcpus %d; ceiling must exceed boot count",
			f.vcpusMax, f.vcpus)}
	}
	if f.egressClosed && f.allowedRepo == "" {
		return f, &UsageError{Msg: "sandbox create: --egress closed requires --repo owner/name " +
			"(D-PD-36): GitHub is added to SecretHosts and the full-scope token would " +
			"be unbounded without a per-repo path allowlist"}
	}
	return f, nil
}

func buildCHConfig(kernelPath, ext4Path string, memMiB, vcpus uint32) cloudhypervisor.Config {
	cfg := cloudhypervisor.Config{
		KernelPath:    kernelPath,
		DiskImagePath: ext4Path,
	}
	if memMiB > 0 {
		cfg.MemoryMiB = memMiB
	}
	if vcpus > 0 {
		cfg.VCPUs = vcpus
	}
	return cfg
}

const diskBootCmdlineBase = "root=/dev/vda rw init=/sbin/nexus-agent console=ttyS0"

func sandboxHandleHostname(handle string) string {
	b := make([]byte, 0, len(handle))
	for i := 0; i < len(handle); i++ {
		c := handle[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
			b = append(b, c)
		case c >= 'A' && c <= 'Z':
			b = append(b, c+32) // to lower
		default:
			b = append(b, '-')
		}
	}
	if len(b) > 63 {
		b = b[:63]
	}
	return string(b)
}

func workspaceMountCmdline(mounts []agent.GuestMount) string {
	b := diskBootCmdlineBase + " --"
	for _, m := range mounts {
		ro := "false"
		if m.ReadOnly {
			ro = "true"
		}
		ws := "false"
		if m.IsWorkspace {
			ws = "true"
		}
		rs := "false"
		if m.Resizable {
			rs = "true"
		}
		if m.IsFile {
			b += fmt.Sprintf(" --workspace-mount=%s:%s:%s:%s:%s:%s:%s", m.Device, m.Target, m.FSType, ro, ws, rs, m.FileName)
		} else {
			b += fmt.Sprintf(" --workspace-mount=%s:%s:%s:%s:%s:%s", m.Device, m.Target, m.FSType, ro, ws, rs)
		}
	}
	return b
}

func bootScratchDiskPresent(workspacePath string, liveMounts []domain.LiveMount) bool {
	return workspacePath != "" || service.HasWorkspaceMount(liveMounts)
}

func buildLiveMountDriverSpec(
	f sandboxCreateFlags,
	ar vmcfg.Result,
	kernelPath string,
	bootLiveMounts []domain.LiveMount,
	bootGuestMounts []agent.GuestMount,
	namedDiskMounts []agent.GuestMount,
	project, name string,
) sandboxDriverSpec {
	liveGuestMounts := liveMountsToGuestMounts(bootLiveMounts)
	allGuestMounts := append(append([]agent.GuestMount{}, namedDiskMounts...),
		append(bootGuestMounts, liveGuestMounts...)...)
	return sandboxDriverSpec{
		KernelPath:     kernelPath,
		MemoryMiB:      f.memoryMiB,
		VCPUs:          f.vcpus,
		MemoryMaxMiB:   ar.MemoryMaxMiB,
		VCPUMax:        ar.VCPUMax,
		NestedVirt:     f.nestedVirt,
		PID1Args:       ar.PID1Args,
		SBHandle:       project + "/" + name,
		LiveMounts:     bootLiveMounts,
		GuestMounts:    allGuestMounts,
		HasScratchDisk: bootScratchDiskPresent(f.workspacePath, bootLiveMounts),
	}
}

func guestBootCmdline(mounts []agent.GuestMount, pid1Args, sandboxHandle string, scratchDiskIdx int) string {
	base := diskBootCmdlineBase + " --"
	if len(mounts) > 0 {
		base = workspaceMountCmdline(mounts)
	}
	return base + pid1Args + scratchDiskCmdlineArg(scratchDiskIdx) + " --sandbox-handle=" + sandboxHandleHostname(sandboxHandle)
}

func scratchDiskCmdlineArg(idx int) string {
	if idx < 0 {
		return ""
	}
	dev := fmt.Sprintf("/dev/vd%c", 'b'+rune(idx))
	return fmt.Sprintf(" --scratch-disk=%s", dev)
}

var goArchForBuild = func() string { return runtime.GOARCH }

func resolveAgentPosture(f sandboxCreateFlags) (cred.AgentProfile, []string, bool) {
	if f.agentName == "" {
		return cred.AgentProfile{}, f.allowHosts, !f.egressClosed
	}
	profile, _ := cred.ProfileByName(f.agentName)
	allowHosts := append(service.AgentEgressHosts(profile), f.allowHosts...)
	return profile, allowHosts, f.egressExplicit && !f.egressClosed
}

func credPreflightCheck(profile cred.AgentProfile) error {
	if pf := cred.CheckCred(profile); !pf.OK() {
		return &CodedError{
			Code: sandboxErrCodeBadCredential,
			Msg:  "sandbox create: " + pf.Sentence(),
		}
	}
	return nil
}

func agentDevEgressSecretHostSuffixes(profile cred.AgentProfile, openEgress bool) []string {
	if profile.Name == "" || !openEgress || profile.CredentialedHostSuffix == "" {
		return nil
	}
	return []string{profile.CredentialedHostSuffix}
}

func agentDevEgressSecretHosts(profile cred.AgentProfile, openEgress bool) []string {
	if profile.Name == "" || !openEgress {
		return nil
	}
	return []string{profile.CredentialedHost}
}

func resolveExtraAgentProfiles(names []string) []cred.AgentProfile {
	profiles := make([]cred.AgentProfile, 0, len(names))
	for _, name := range names {
		if p, ok := cred.ProfileByName(name); ok {
			profiles = append(profiles, p)
		}
	}
	return profiles
}

func resolveExtraSecretHosts(primary cred.AgentProfile, extraNames []string, openEgress bool) []string {
	hosts := agentDevEgressSecretHosts(primary, openEgress)
	if !openEgress {
		return hosts
	}
	for _, name := range extraNames {
		if p, ok := cred.ProfileByName(name); ok && p.CredentialedHost != "" {
			hosts = append(hosts, p.CredentialedHost)
		}
	}
	return hosts
}

func resolveExtraSecretHostSuffixes(primary cred.AgentProfile, extraNames []string, openEgress bool) []string {
	suffixes := agentDevEgressSecretHostSuffixes(primary, openEgress)
	if !openEgress {
		return suffixes
	}
	for _, name := range extraNames {
		if p, ok := cred.ProfileByName(name); ok {
			if s := p.CredentialedHostSuffix; s != "" {
				suffixes = append(suffixes, s)
			}
		}
	}
	return suffixes
}

func parseHumanBytes(s string) (int64, error) {
	type suffix struct {
		label string
		mult  int64
	}
	suffixes := []suffix{
		{"TiB", 1 << 40},
		{"GiB", 1 << 30},
		{"MiB", 1 << 20},
		{"KiB", 1 << 10},
		{"TB", 1_000_000_000_000},
		{"GB", 1_000_000_000},
		{"MB", 1_000_000},
		{"KB", 1_000},
		{"B", 1},
	}
	for _, su := range suffixes {
		if strings.HasSuffix(s, su.label) {
			num := s[:len(s)-len(su.label)]
			f, err := strconv.ParseFloat(num, 64)
			if err != nil {
				return 0, &UsageError{Msg: fmt.Sprintf("--capture-max: invalid size value %q: %v", num, err)}
			}
			if math.IsNaN(f) || math.IsInf(f, 0) {
				return 0, &UsageError{Msg: fmt.Sprintf("--capture-max: invalid size %q: value must be a finite positive number", s)}
			}
			product := f * float64(su.mult)
			if product > math.MaxInt64 {
				return 0, &UsageError{Msg: fmt.Sprintf("--capture-max: size %q overflows int64", s)}
			}
			n := int64(product)
			if n <= 0 {
				return 0, &UsageError{Msg: fmt.Sprintf("--capture-max: size %q must be a positive non-zero value (0 is reserved for AUTO mode)", s)}
			}
			return n, nil
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, &UsageError{Msg: fmt.Sprintf("--capture-max: invalid size %q: expected a positive number with optional suffix like 8GiB or 500MB", s)}
	}
	if n <= 0 {
		return 0, &UsageError{Msg: fmt.Sprintf("--capture-max: size %q must be a positive non-zero value (0 is reserved for AUTO mode)", s)}
	}
	return n, nil
}

func buildTaskTimeout() time.Duration {
	const defaultTimeout = 20 * time.Minute
	s := os.Getenv("NEXUS_BUILD_TASK_TIMEOUT")
	if s == "" {
		return defaultTimeout
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		slog.Warn("NEXUS_BUILD_TASK_TIMEOUT: invalid duration, using default",
			"value", s, "default", defaultTimeout)
		return defaultTimeout
	}
	return d
}

func resolveContainerfilePath(workspaceDir, explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	for _, rel := range []string{".nexus/Containerfile", ".nexus/Dockerfile"} {
		p := filepath.Join(workspaceDir, rel)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("no Containerfile found in %s: tried .nexus/Containerfile and .nexus/Dockerfile", workspaceDir)
}

func preallocateFile(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(size)
}

func buildWorkspaceSpec(sourcePath, guestPath string, captureMaxBytes int64) *service.WorkspaceSpec {
	return &service.WorkspaceSpec{
		SourcePath:      sourcePath,
		GuestPath:       guestPath,
		CaptureMaxBytes: captureMaxBytes,
	}
}

func workspaceSpecFromFlags(f sandboxCreateFlags, wsAbs, guestPath string) *service.WorkspaceSpec {
	return buildWorkspaceSpec(wsAbs, guestPath, f.captureMaxBytes)
}

var testWorkspaceSpecHook func(*service.WorkspaceSpec)

func runSandboxCreate(ctx context.Context, args []string, out *Output, svc *service.Service) error {
	f, parseErr := parseSandboxCreateArgs(args)
	if parseErr != nil {
		return parseErr
	}

	if cfgErr := applyProjectConfig(&f); cfgErr != nil {
		return cfgErr
	}

	if cfgErr := applyUserGlobalConfig(&f); cfgErr != nil {
		return cfgErr
	}

	if len(f.positionals) != 1 {
		return &UsageError{Msg: "sandbox create: usage: sandbox create <project>/<name> [--rm] [--image <ref>|--rootfs <path>|--file <context-dir>] [--dockerfile <path>] [--memory <MiB>] [--vcpus <n>] [--label KEY=VALUE] [--nested] [--mount <host>:<guest>[:ro]] [--mount-named <volume>:<guest>[:ro]] [--workspace <host-path>] [--capture-max <size>] [--builder-memory <MiB>] [--memory-max <MiB>] [--vcpus-max <n>] [--disk-max <GiB>] [--secret ENV@host[,host…]] [--egress <mode>] [--allow-host <host>] [--repo <owner>/<name>] [--no-share-settings] [--no-user-mounts] [--agent <name>] [--force] (auto-resize is unconditional: hotplug hardware is configured at create time; the dynamic governor activates only in the supervisor process)"}
	}

	project, name, err := domain.ParseHandle(f.positionals[0])
	if err != nil {
		return &UsageError{Code: sandboxErrCodeInvalidArgument, Msg: fmt.Sprintf("sandbox create: %v", err)}
	}

	if f.imageRef == "" && f.rootfsPath == "" && f.filePath == "" {
		if len(f.mountLive) > 0 {
			return &UsageError{Msg: "sandbox create: --mount requires a bootable sandbox; use --image, --rootfs, or --file to boot"}
		}
		if len(f.mountNamed) > 0 {
			return &UsageError{Msg: "sandbox create: --mount-named requires a bootable sandbox; use --image, --rootfs, or --file to boot"}
		}
		noBootProfile, _, _ := resolveAgentPosture(f)
		if err := credPreflightCheck(noBootProfile); err != nil {
			return err
		}
		if !f.noShareSettings && len(noBootProfile.MountAllowlist) > 0 {
			slog.Warn("sandbox create: agent settings / MCP sharing requires a booted sandbox; skipped for store-only record (use --image, --rootfs, or --file to enable)")
		}
		sb, err := svc.Create(ctx, project, name, service.CreateOptions{
			RemoveOnExit: f.rm,
			AgentName:    noBootProfile.Name,
		})
		if err != nil {
			return errSandbox("sandbox create", err)
		}
		out.EmitSuccess("sandbox.created", toSandboxInfoJSON(sb),
			fmt.Sprintf("created sandbox %s (%s)", sb.Handle(), sb.ID))
		return nil
	}

	kernelPath, err := resolveKernelPath()
	if err != nil {
		return errSandbox("sandbox create", err)
	}

	var agentBytes []byte
	var agentBytesLoadErr error
	{
		ab, lookErr := exec.LookPath("nexus-agent")
		if lookErr != nil {
			ab = filepath.Join(filepath.Dir(kernelPath), "nexus-agent")
		}
		agentBytes, agentBytesLoadErr = os.ReadFile(ab)
		if agentBytesLoadErr != nil {
			agentBytesLoadErr = fmt.Errorf("read agent binary %q: %w", ab, agentBytesLoadErr)
		}
	}

	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return errSandbox("sandbox create", fmt.Errorf("resolve state directory: %w", err))
	}
	cacheRoot := filepath.Join(storeRoot, "images")

	var namedMounts []service.NamedVolumeMount
	var namedVS *volumestore.VolumeStore
	if len(f.mountNamed) > 0 {
		namedVS = volumestore.New(filepath.Join(storeRoot, "volumes"))
		svc.WithVolumes(namedVS)
		for _, spec := range f.mountNamed {
			m, mErr := parseMountNamed(spec)
			if mErr != nil {
				return mErr
			}
			namedMounts = append(namedMounts, m)
		}
	}

	// resolveAgentPosture is also called at the egress-wiring block below; this
	// early call is needed only for the agentcfg-volume auto-provision guard.
	agentProfile, _, _ := resolveAgentPosture(f)
	if f.agentName != "" {
		hasAgentCfgDisk := false
		for _, m := range namedMounts {
			if m.GuestPath == "/var/lib/nexus/agentcfg" {
				hasAgentCfgDisk = true
				break
			}
		}
		if !hasAgentCfgDisk {
			autoVolName := sandboxAgentCfgVolumeName(project, name)
			autoMount, autoErr := parseMountNamed(autoVolName + ":/var/lib/nexus/agentcfg:size=2g")
			if autoErr != nil {
				return errSandbox("sandbox create", fmt.Errorf("auto-provision agentcfg volume: %w", autoErr))
			}
			if namedVS == nil {
				namedVS = volumestore.New(filepath.Join(storeRoot, "volumes"))
				svc.WithVolumes(namedVS)
			}
			namedMounts = append(namedMounts, autoMount)
		}
	}

	namedDiskMounts := namedDiskGuestMounts(namedMounts)

	imgCache, err := image.NewCache(cacheRoot)
	if err != nil {
		return errSandbox("sandbox create", fmt.Errorf("open image cache: %w", err))
	}

	// Disk guard runs before any temp dir, capture, or build: a full host used
	// to surface only at CreateAndBoot's free-space check, after a 28-40s capture.
	if guardErr := sandboxCreateDiskGuard(ctx, out, storeRoot, imgCache); guardErr != nil {
		return guardErr
	}

	if f.filePath != "" && f.imageRef == "" && f.rootfsPath == "" {
		var workspaceDir string
		fi, statErr := os.Stat(f.filePath)
		if statErr != nil {
			return errSandbox("sandbox create", fmt.Errorf("--file: stat %q: %w", f.filePath, statErr))
		}
		if fi.IsDir() {
			workspaceDir = f.filePath
		} else {
			parent := filepath.Dir(f.filePath)
			if filepath.Base(parent) == ".nexus" {
				workspaceDir = filepath.Dir(parent)
			} else {
				workspaceDir = parent
			}
		}

		containerfilePath, err := resolveContainerfilePath(workspaceDir, f.dockerfilePath)
		if err != nil {
			return errSandbox("sandbox create", fmt.Errorf("--file: %w", err))
		}

		if len(agentBytes) == 0 {
			return errSandbox("sandbox create", fmt.Errorf("--file: %w", agentBytesLoadErr))
		}

		taskTimeout := buildTaskTimeout()
		buildCtx, buildCancel := context.WithTimeout(ctx, taskTimeout)
		defer buildCancel()

		containerfileBytes, err := os.ReadFile(containerfilePath)
		if err != nil {
			return errSandbox("sandbox create", fmt.Errorf("--file: read Containerfile %q: %w", containerfilePath, err))
		}
		baseImageRef := builder.ExtractFromRef(containerfileBytes)
		buildProfile, _, _ := resolveAgentPosture(f)
		buildTargetArch := builder.GoArchToVendorArch(goArchForBuild())
		resolvedRecipe, err := cred.ResolveFloatingVersions(buildCtx, buildProfile.Recipe())
		if err != nil {
			return errSandbox("sandbox create", fmt.Errorf("--file: resolve tool recipe versions: %w", err))
		}
		fp, err := builder.BuildFingerprint(containerfileBytes, baseImageRef, agentBytes, workspaceDir, resolvedRecipe, buildTargetArch)
		if err != nil {
			return errSandbox("sandbox create", fmt.Errorf("--file: fingerprint: %w", err))
		}

		buildCacheBase := filepath.Join(storeRoot, "build-cache")
		if err := os.MkdirAll(buildCacheBase, 0700); err != nil {
			return errSandbox("sandbox create", fmt.Errorf("--file: build-cache dir: %w", err))
		}
		fpLock, err := store.OpenLock(filepath.Join(buildCacheBase, fp+".lock"))
		if err != nil {
			return errSandbox("sandbox create", fmt.Errorf("--file: build-cache lock: %w", err))
		}
		defer fpLock.Close()
		if err := fpLock.Exclusive(buildCtx); err != nil {
			return errSandbox("sandbox create", fmt.Errorf("--file: build-cache lock acquire: %w", err))
		}

		if cachedDigest, hit, lookupErr := builder.LookupBuildCache(buildCtx, storeRoot, fp, imgCache); hit {
			slog.Info("build-cache: hit — skipping builder VM", "fp", fp[:12])
			f.imageRef = cachedDigest
		} else {
			if lookupErr != nil {
				slog.Warn("build-cache: lookup error, proceeding with build",
					"fp", fp[:12], "err", lookupErr)
			} else {
				slog.Info("build-cache: miss — starting builder VM", "fp", fp[:12])
			}

			{
				gcFloorBytes := uint64(service.DefaultGCFreeSpaceFloorGiB) * (1 << 30)
				if ugCfg, ugErr := config.LoadUserGlobal(); ugErr == nil && ugCfg.Image.FreeSpaceFloorGiB > 0 {
					gcFloorBytes = uint64(ugCfg.Image.FreeSpaceFloorGiB) * (1 << 30)
				}
				if gcStore, gcStoreErr := store.NewFileStore(storeRoot); gcStoreErr == nil {
					if preErr := service.BuildPreflight(buildCtx, storeRoot, gcFloorBytes, imgCache, gcStore); preErr != nil {
						return errSandbox("sandbox create", preErr)
					}
				}
			}

			// Admit before any side effects: EnsureBuilderImage, the worktree copy
			// and SelectCacheDisks (which fences the cache disk dirty) all come after.
			builderBootMemMiB := uint32(builder.MemMiB(builder.BuilderVMSpec{MemoryMiB: uint16(f.builderMemoryMiB)}))
			if err := builder.AdmitBuilderBootHere(builderBootMemMiB); err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: %w", err))
			}

			builderRootfsTemplate, err := builderimage.EnsureBuilderImage(buildCtx, storeRoot, agentBytes)
			if err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: builder image: %w", err))
			}

			buildWorkDir, err := os.MkdirTemp("", "nexus-build-*")
			if err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: build workdir: %w", err))
			}
			defer os.RemoveAll(buildWorkDir)

			builderRootfs, err := builder.PrivateRootfs(buildCtx, builderRootfsTemplate, buildWorkDir)
			if err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: %w", err))
			}

			ctxDiskPath := filepath.Join(buildWorkDir, "ctx.ext4")
			if err := builder.WorktreeToDisk(buildCtx, workspaceDir, ctxDiskPath, f.captureMaxBytes); err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: context disk: %w", err))
			}

			const artifactDiskSize = 4 << 30 // 4 GiB sparse file
			artifactDiskPath := filepath.Join(buildWorkDir, "artifact.ext4")
			if err := preallocateFile(artifactDiskPath, artifactDiskSize); err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: artifact disk: %w", err))
			}

			cacheDisks, cacheDiskLeases, err := builder.SelectCacheDisks(buildCtx, storeRoot, []string{"buildkit"})
			if err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: cache disks: %w", err))
			}
			defer builder.ReleaseCacheDiskLeases(cacheDiskLeases)

			spec := builder.BuilderVMSpec{
				RootfsDiskPath:   builderRootfs,
				ContextDiskPath:  ctxDiskPath,
				ArtifactDiskPath: artifactDiskPath,
				CacheDisks:       cacheDisks,
				MemoryMiB:        uint16(f.builderMemoryMiB),
				ToolRecipe:       resolvedRecipe,
				TargetArch:       buildTargetArch,
			}

			builderBootVCPUs := uint32(builder.VCPUs(spec))
			builderAR := vmcfg.Resolve(vmcfg.Config{
				BootMemMiB: builderBootMemMiB,
				BootVCPUs:  builderBootVCPUs,
				MemMaxMiB:  builder.MemMaxMiB(spec),
			})

			builderSocketDir, err := orcaSocketDir()
			if err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: builder socket dir: %w", err))
			}

			dialerCfg := buildCHConfig(kernelPath, builderRootfs,
				builderBootMemMiB, builderBootVCPUs)
			dialerCfg.SocketDir = builderSocketDir
			dialerCfg.MemoryMaxMiB = builderAR.MemoryMaxMiB
			dialerCfg.VCPUMax = builderAR.VCPUMax
			if p, err := exec.LookPath("cloud-hypervisor"); err == nil {
				dialerCfg.BinaryPath = p
			}
			dialerDrv, err := cloudhypervisor.New(dialerCfg)
			if err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: builder dialer driver: %w", err))
			}

			builderExtraDisks := []string{ctxDiskPath, artifactDiskPath}
			for _, cd := range cacheDisks {
				builderExtraDisks = append(builderExtraDisks, cd.ImagePath)
			}

			cacheDiskMountPaths := make([]string, len(cacheDisks))
			for i, cd := range cacheDisks {
				cacheDiskMountPaths[i] = cd.MountPath
			}

			bdrv := &supervisorBuilderDriver{
				dialerDrv:           dialerDrv,
				storeRoot:           storeRoot,
				stateBase:           filepath.Join(storeRoot, "builder-supervisors"),
				socketDir:           builderSocketDir,
				kernelPath:          kernelPath,
				diskPath:            builderRootfs,
				extraDisks:          builderExtraDisks,
				ar:                  builderAR,
				bootMemMiB:          builderBootMemMiB,
				bootVCPUs:           builderBootVCPUs,
				logPath:             "",
				cacheDiskMountPaths: cacheDiskMountPaths,
				cacheDiskLeases:     cacheDiskLeases,
			}
			execFn := func(ctx context.Context, argv []string, stderr io.Writer) (int32, error) {
				ac := agent.NewClient(bdrv, bdrv.StartedID())
				return ac.Exec(ctx, agent.ExecOptions{Argv: argv, Stdout: stderr, Stderr: stderr})
			}

			builderStore, err := store.NewFileStore(storeRoot)
			if err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: builder store: %w", err))
			}
			digest, err := builder.BuildInVM(buildCtx, bdrv, spec, imgCache, execFn, builderStore)
			if err != nil {
				if buildCtx.Err() != nil {
					return errSandbox("sandbox create", fmt.Errorf(
						"builder task exceeded %v (set NEXUS_BUILD_TASK_TIMEOUT to change) — aborted",
						taskTimeout))
				}
				return errSandbox("sandbox create", fmt.Errorf("--file: build: %w", err))
			}

			if storeErr := builder.StoreBuildCache(storeRoot, fp, digest); storeErr != nil {
				slog.Warn("build-cache: store failed (non-fatal)", "fp", fp[:12], "err", storeErr)
			}

			f.imageRef = digest

			{
				keepNewest := 0
				if ugCfg, ugErr := config.LoadUserGlobal(); ugErr == nil {
					keepNewest = ugCfg.Image.KeepNewestBuilderImages
				}
				if newDigest, parseErr := domain.ParseDigest(f.imageRef); parseErr == nil {
					if pruned, gcErr := service.AutoPruneAfterBuild(buildCtx, imgCache, builderStore, keepNewest, newDigest); gcErr != nil {
						slog.Warn("image gc: post-build prune failed (non-fatal)", "err", gcErr)
					} else if pruned > 0 {
						slog.Info("image gc: pruned orphan builder images", "count", pruned)
					}
				}
			}
		}
	}

	var bootGuestMounts []agent.GuestMount
	var bootLiveMounts []domain.LiveMount // D-PD-53: captured by newDriver closure
	var caps sandboxDriverCaptures

	ar := vmcfg.Resolve(vmcfg.Config{
		BootMemMiB: f.memoryMiB,
		BootVCPUs:  f.vcpus,
		MemMaxMiB:  f.memoryMaxMiB,
		Nested:     f.nestedVirt,
		VCPUsMax:   f.vcpusMax,
		DiskMaxGiB: f.diskMaxGiB,
	})
	govBounds := ar.Bounds
	effectiveMemMaxMiB := ar.MemoryMaxMiB
	slog.Info("auto-resize: hotplug hardware configured; governor activates in supervisor",
		"mem_max_mib", effectiveMemMaxMiB,
		"vcpus_max", govBounds.VCPUMax,
		"disk_max_gib", govBounds.DiskMaxBytes/(1024*1024*1024),
	)

	newDriver := func(ext4Path string, extraDisks []service.ExtraDisk) (driver.Driver, error) {
		spec := buildLiveMountDriverSpec(f, ar, kernelPath, bootLiveMounts, bootGuestMounts, namedDiskMounts, project, name)
		return buildSandboxDriverFactory(spec, &caps)(ext4Path, extraDisks)
	}

	probe := vsockProbe

	spec := service.ImageSpec{
		Ref:        f.imageRef,
		RootfsPath: f.rootfsPath,
	}

	var (
		bootExtraDisks     []service.ExtraDisk
		bootWorkspace      *service.WorkspaceSpec
		bootCapturer       func(context.Context, string, string, int64) error
		bootBaseRef        string   // host HEAD SHA when the workspace carries .git (GIT-SEED)
		shadowDiskCleanups []string // host paths to remove on CreateAndBoot failure
		shadowLease        *service.ShadowIntentLease
	)
	defer func() { shadowLease.Release() }()
	if f.workspacePath != "" {
		if _, statErr := os.Stat(f.workspacePath); statErr != nil {
			return errSandbox("sandbox create", fmt.Errorf("--workspace: %w", statErr))
		}
		wsAbs, absErr := filepath.Abs(f.workspacePath)
		if absErr != nil {
			return errSandbox("sandbox create", fmt.Errorf("--workspace: abs path: %w", absErr))
		}

		guestPath := "/workspace/" + filepath.Base(wsAbs)

		diskDir := filepath.Join(storeRoot, "disks")
		if mkErr := os.MkdirAll(diskDir, 0o700); mkErr != nil {
			return errSandbox("sandbox create", fmt.Errorf("--workspace: disk dir: %w", mkErr))
		}

		shadowSpecs := buildShadowDiskSpecs(DefaultShadowDirs, diskDir, guestPath, f.positionals[0])

		var siErr error
		shadowLease, shadowDiskCleanups, siErr = prepareShadowDisks(ctx, diskDir, f.positionals[0], shadowSpecs)
		if siErr != nil {
			return errSandbox("sandbox create", fmt.Errorf("--workspace: %w", siErr))
		}

		bootExtraDisks = shadowExtraDisks(shadowSpecs)
		bootWorkspace = workspaceSpecFromFlags(f, wsAbs, guestPath)
		if h := testWorkspaceSpecHook; h != nil {
			h(bootWorkspace)
		}

		bootCapturer = makeHumanWorkspaceCapturer(DefaultShadowDirs)

		if _, statErr := os.Stat(filepath.Join(wsAbs, ".git")); statErr == nil {
			if _, _, idErr := service.HostGitIdentity(); idErr != nil {
				return errSandbox("sandbox create", fmt.Errorf(
					"--workspace %s is a git repository but the host has no git identity configured; "+
						"run `git config --global user.name <name>` and `git config --global user.email <email>` first: %w",
					wsAbs, idErr))
			}
			if head, headErr := service.HostHeadSHA(wsAbs); headErr == nil {
				bootBaseRef = head
			} else {
				slog.Warn("sandbox create: cannot resolve host HEAD for BaseRef", "workspace", wsAbs, "err", headErr)
			}
		}

		allMounts := append(shadowGuestMounts(shadowSpecs, len(namedDiskMounts)),
			WorkspaceGuestMount(guestPath, len(namedDiskMounts)+len(shadowSpecs)))
		bootGuestMounts = allMounts
		slog.Info("workspace shadow disks prepared",
			"workspace_host", wsAbs,
			"workspace_guest", guestPath,
			"num_shadow_disks", len(shadowSpecs),
			"workspace_device", WorkspaceGuestMount(guestPath, len(shadowSpecs)).Device,
			"mounts", fmt.Sprintf("%v", allMounts),
		)
	}
	for _, spec := range f.mountLive {
		lm, lmErr := parseMountLive(spec)
		if lmErr != nil {
			return lmErr
		}
		bootLiveMounts = append(bootLiveMounts, lm)
	}

	secrets, err := resolveCreateSecrets(ctx, f)
	if err != nil {
		return errSandbox("sandbox create", err)
	}

	agentProfile, allowHosts, openEgress := resolveAgentPosture(f)
	for _, name := range f.extraAgentNames {
		if p, ok := cred.ProfileByName(name); ok {
			allowHosts = append(allowHosts, service.AgentEgressHosts(p)...)
		}
	}
	if err := credPreflightCheck(agentProfile); err != nil {
		return err
	}

	mcpSourceDir := ""
	if f.workspacePath != "" {
		if abs, absErr := filepath.Abs(f.workspacePath); absErr == nil {
			mcpSourceDir = abs
		}
	} else if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		mcpSourceDir = cwd
	}
	sharedMCP, mcpErr := buildGuestMCPServers(f, agentProfile, mcpSourceDir)
	if mcpErr != nil {
		return errSandbox("sandbox create", mcpErr)
	}
	for _, b := range sharedMCP.HTTPBinds {
		secrets = service.MergeSecrets(secrets, b)
		allowHosts = append(allowHosts, b.Hosts...)
	}

	if f.egressClosed {
		secrets = service.MergeSecrets(secrets, service.SecretBind{
			Env:   service.BuiltinGitHubEnv,
			Hosts: append([]string(nil), service.GitHubSecretHosts...),
		})
	}

	var preMintedID domain.SandboxID // zero unless A-MOUNT staging pre-mints
	var agentCfgStageDir string      // non-empty when staging succeeded; tracks cleanup
	if !f.noShareSettings && len(agentProfile.MountAllowlist) > 0 {
		id := domain.NewSandboxID()
		stageDir := filepath.Join(storeRoot, "disks", id.String()+"-agentcfg-lower")
		if stageErr := stageAgentCuratedConfig(agentProfile, stageDir); stageErr != nil {
			_ = os.RemoveAll(stageDir)
			slog.Warn("sandbox create: failed to stage agent config; running without shared settings", "err", stageErr)
		} else {
			preMintedID = id
			agentCfgStageDir = stageDir
			bootLiveMounts = append(bootLiveMounts, domain.LiveMount{
				HostPath:  stageDir,
				GuestPath: "/run/nexus/agentcfg-lower",
				ReadOnly:  true,
			})
			slog.Info("sandbox create: agent config staged for overlay", "staging", stageDir)
			if len(sharedMCP.Servers) > 0 {
				if mcpJSON, marshalErr := json.Marshal(sharedMCP.Servers); marshalErr == nil {
					mcpPath := filepath.Join(stageDir, "mcp-servers.json")
					if writeErr := os.WriteFile(mcpPath, mcpJSON, 0o600); writeErr != nil {
						slog.Warn("sandbox create: failed to write mcp-servers.json; MCP definitions will not be injected",
							"err", writeErr)
					}
				}
			}
			if !f.noUserMounts {
				if hostHome, homeErr := os.UserHomeDir(); homeErr == nil {
					userGlobalCfg, ugErr := config.LoadUserGlobal()
					if ugErr != nil {
						slog.Warn("sandbox create: failed to load user global config; user mounts disabled", "err", ugErr)
					}
					manifest := service.BuildUserMountManifest(hostHome, []string(userGlobalCfg.Sandbox.Mounts))
					manifest.ExtraPathDirs = service.ResolveHookRuntimePathDirs(runtime.GOOS, exec.LookPath, []string{
						filepath.Join(hostHome, ".local", "bin"),
						filepath.Join(hostHome, ".local", "share", "mise", "installs"),
					})
					if len(agentProfile.ToolRecipe.Packages) > 0 {
						for _, w := range service.CheckRecipeShadows([]string(userGlobalCfg.Sandbox.Mounts), agentProfile.ToolRecipe) {
							slog.Warn("sandbox create: " + w)
						}
					}
					for _, m := range manifest.Mounts {
						bootLiveMounts = append(bootLiveMounts, domain.LiveMount{
							HostPath:  m.HostPath,
							GuestPath: m.StagingGuestPath,
							ReadOnly:  true,
							IsFile:    m.IsFile,
						})
					}
					if len(manifest.Mounts) > 0 || len(manifest.ExtraPathDirs) > 0 {
						if writeErr := service.WriteUserMountManifest(stageDir, manifest); writeErr != nil {
							slog.Warn("sandbox create: failed to write usermounts.json; operator tool dirs will not be visible in guest",
								"err", writeErr)
						}
					}
					for _, pat := range agentProfile.MountAllowlist {
						if pat == "plugins/**" {
							ccSpecs, ccWarns := service.ResolveClaudeCodeBindMounts(hostHome, "")
							for _, msg := range ccWarns {
								slog.Warn("sandbox create: " + msg)
							}
							for _, spec := range ccSpecs {
								parts := strings.SplitN(spec, ":", 3)
								if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
									continue
								}
								bootLiveMounts = append(bootLiveMounts, domain.LiveMount{
									HostPath:  parts[0],
									GuestPath: parts[1],
									ReadOnly:  len(parts) == 3 && parts[2] == "ro",
								})
							}
							break
						}
					}
				} else {
					slog.Warn("sandbox create: os.UserHomeDir failed; skipping user-mount table", "err", homeErr)
				}
			}
		}
	}

	sb, err := service.CreateAndBoot(ctx, svc, imgCache, newDriver, probe,
		project, name,
		service.CreateAndBootOptions{
			PreMintedID:             preMintedID, // A-MOUNT: zero unless curated config was staged at an ID-keyed path
			Labels:                  f.labels,
			RemoveOnExit:            f.rm,
			ForceDiskSpace:          f.forceDiskSpace,
			Image:                   spec,
			CacheRoot:               cacheRoot,
			MemoryMiB:               f.memoryMiB,
			VCPUs:                   f.vcpus,
			NestedVirt:              f.nestedVirt,
			ExtraDisks:              bootExtraDisks,
			Workspace:               bootWorkspace,
			WorkspaceCapturer:       bootCapturer,
			BaseRef:                 bootBaseRef, // GIT-SEED: host HEAD at capture time (D-PD-19/D-PD-29)
			Secrets:                 secrets,
			AllowedHosts:            allowHosts, // --allow-host, plus the agent's own hosts when --agent is set
			OpenEgress:              openEgress,
			ExtraSecretHosts:        resolveExtraSecretHosts(agentProfile, f.extraAgentNames, openEgress),
			ExtraSecretHostSuffixes: resolveExtraSecretHostSuffixes(agentProfile, f.extraAgentNames, openEgress),
			ExtraAgentProfiles:      resolveExtraAgentProfiles(f.extraAgentNames), // D-TP-09: full profiles so supervisor can seed credentials
			AgentProfile:            agentProfile,                                 // zero value when --agent was not passed
			AllowedRepo:             f.allowedRepo,                                // D-PD-36: set by --repo; empty for open-egress sandboxes
			PathPolicies:            f.pathPolicies,                               // conveyed via --egress-policy-json on the worktree subprocess path
			Volumes:                 namedVS,                                      // SD2-6-MOUNT: nil when --mount-named not used
			NamedVolumeMounts:       namedMounts,
			LiveMounts:              bootLiveMounts, // D-PD-53: populated from --mount flags
			AgentBytes:              agentBytes,
		},
	)
	if err != nil {
		for _, p := range shadowDiskCleanups {
			_ = os.Remove(p)
		}
		if agentCfgStageDir != "" {
			_ = os.RemoveAll(agentCfgStageDir)
		}
		return errSandbox("sandbox create", err)
	}

	var mcpOAuthRefreshConfigs []service.MCPOAuthRefreshConfig
	if agentProfile.MCPConfigFormat == cred.MCPConfigFormatClaudeJSON {
		_, oauthRefreshCfgs, oauthErr := service.BuildMCPOAuthBinds("")
		if oauthErr != nil {
			slog.Warn("sandbox create: BuildMCPOAuthBinds failed; MCP OAuth token refresh will not be active", "err", oauthErr)
		} else {
			mcpOAuthRefreshConfigs = oauthRefreshCfgs
		}
	}

	if handoffErr := handoffHumanSupervisor(ctx, svc, sb, storeRoot, kernelPath, govBounds, f.memoryMiB, f.vcpus,
		caps.DiskPath, caps.ExtraDisks, caps.Cmdline, caps.CHBin, caps.SocketDir, bootWorkspace != nil, len(bootExtraDisks),
		len(namedDiskMounts),
		workspaceGuestPathFor(bootWorkspace), bootLiveMounts, caps.VirtiofsdPath,
		f.nestedVirt,
		mcpOAuthRefreshConfigs, agentProfile); handoffErr != nil {
		slog.Warn("sandbox create: supervisor handoff failed; broker will not survive CLI exit",
			"sandbox", sb.ID, "err", handoffErr)
	}

	out.EmitSuccess("sandbox.created", toSandboxInfoJSON(sb),
		fmt.Sprintf("created sandbox %s (%s)", sb.Handle(), sb.ID))
	return nil
}

// buildGuestMCPServers resolves the user mounts the guest will see (the same
// operator config the staging block below turns into usermounts.json, plus the
// live ~/.claude mount when the profile wires one) and builds the guest MCP
// config against them, so stdio commands under a mounted host dir are
// rewritten to their guest path and host-only absolute commands are dropped.
func buildGuestMCPServers(f sandboxCreateFlags, agentProfile cred.AgentProfile, mcpSourceDir string) (service.SharedMCPServers, error) {
	var mounts []service.ResolvedUserMount
	if !f.noShareSettings {
		if hostHome, homeErr := os.UserHomeDir(); homeErr == nil {
			if !f.noUserMounts {
				if userGlobalCfg, ugErr := config.LoadUserGlobal(); ugErr == nil {
					mounts = append(mounts, service.BuildUserMountManifest(hostHome, []string(userGlobalCfg.Sandbox.Mounts)).Mounts...)
				}
			}
		}
	}
	return service.BuildSharedMCPServers(agentProfile, mcpSourceDir, mounts...)
}

func resolveCreateSecrets(ctx context.Context, f sandboxCreateFlags) ([]service.SecretBind, error) {
	var binds []service.SecretBind
	for _, spec := range f.secrets {
		b, err := service.ParseSecretSpec(spec)
		if err != nil {
			return nil, &UsageError{Msg: "sandbox create: " + err.Error()}
		}
		binds = append(binds, b)
	}
	for _, b := range binds {
		if !service.SecretTouchesGitHub(b) {
			continue
		}
		for _, h := range b.Hosts {
			if !domain.IsGitHubHost(strings.ToLower(h)) {
				continue
			}
			if githubHostBoundCLI(h, f.allowedRepo, f.pathPolicies) {
				continue
			}
			return nil, &UsageError{Msg: "sandbox create: GitHub credential would be " +
				"unbounded (D-PD-36): pass --repo owner/name to scope the per-repo " +
				"path allowlist"}
		}
	}
	return binds, nil
}

func githubHostBoundCLI(h, allowedRepo string, pp domain.EgressPathPolicies) bool {
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

func handoffHumanSupervisor(
	ctx context.Context,
	svc *service.Service,
	sb domain.Sandbox,
	storeRoot, kernelPath string,
	govBounds resize.Bounds,
	memoryMiB, bootVCPUs uint32,
	diskPath string,
	extraDisks []string,
	cmdline, chBin, socketDir string,
	hasWorkspace bool,
	workspaceDiskIndex int,
	numNamedDisks int,
	workspaceGuestPath string,
	liveMounts []domain.LiveMount,
	virtiofsdPath string,
	nestedVirt bool,
	mcpOAuthRefreshConfigs []service.MCPOAuthRefreshConfig,
	agentProfile cred.AgentProfile,
) error {
	if diskPath == "" {
		return fmt.Errorf("no disk path captured")
	}
	if chBin == "" {
		chBin, _ = exec.LookPath("cloud-hypervisor")
	}
	if socketDir == "" {
		var err error
		socketDir, err = orcaSocketDir()
		if err != nil {
			return err
		}
	}
	stateDir := supervisor.DefaultStateDir(storeRoot, sb.ID)
	cfg := buildHumanSupervisorConfig(
		sb.ID.String(), storeRoot, stateDir,
		kernelPath, govBounds, memoryMiB, bootVCPUs,
		diskPath, extraDisks, cmdline, chBin, socketDir,
		hasWorkspace, workspaceDiskIndex, numNamedDisks, workspaceGuestPath,
		hasWorkspace,                       // hasScratchDisk: workspace sandboxes always get scratch
		numNamedDisks+workspaceDiskIndex+1, // scratchDiskIndex: after workspace disk
		liveMounts, virtiofsdPath,
		nestedVirt,
		mcpOAuthRefreshConfigs,
		agentProfile,
	)
	if err := supervisor.WriteSpawnSpec(stateDir, cfg); err != nil {
		return err
	}
	if _, err := svc.Stop(ctx, sb.ID.String()); err != nil {
		return fmt.Errorf("stop before supervisor handoff: %w", err)
	}
	return spawnPersistedSupervisor(ctx, svc, sb.ID, stateDir)
}

func wireLiveMountsToConfig(cfg *cloudhypervisor.Config, mounts []domain.LiveMount) (virtiofsdPath string, err error) {
	cfg.LiveMounts = mounts
	if len(mounts) == 0 {
		return "", nil
	}
	vp, verr := resolveVirtiofsdPath()
	if verr != nil {
		return "", fmt.Errorf("--mount requires virtiofsd: %w", verr)
	}
	cfg.VirtiofsdPath = vp
	return vp, nil
}

func buildHumanSupervisorConfig(
	sandboxRef, storeRoot, stateDir string,
	kernelPath string,
	govBounds resize.Bounds,
	memoryMiB, bootVCPUs uint32,
	diskPath string,
	extraDisks []string,
	cmdline, chBin, socketDir string,
	hasWorkspace bool,
	workspaceDiskIndex int,
	numNamedDisks int, // number of kind=disk named volumes prepended to ExtraDisks[0..n-1]
	workspaceGuestPath string, // GIT-SEED: git identity seed target
	hasScratchDisk bool,
	scratchDiskIndex int,
	liveMounts []domain.LiveMount,
	virtiofsdPath string,
	nestedVirt bool,
	mcpOAuthRefreshConfigs []service.MCPOAuthRefreshConfig,
	agentProfile cred.AgentProfile,
) supervisor.Config {
	resizableDiskIndices := []int{resize.RootDiskIndex}
	for i := range numNamedDisks {
		resizableDiskIndices = append(resizableDiskIndices, i)
	}
	if hasWorkspace {
		resizableDiskIndices = append(resizableDiskIndices, numNamedDisks+workspaceDiskIndex)
	}

	return supervisor.Config{
		SandboxRef:             sandboxRef,
		StoreRoot:              storeRoot,
		StateDir:               stateDir,
		CHBin:                  chBin,
		SocketDir:              socketDir,
		KernelPath:             kernelPath,
		DiskPath:               diskPath,
		ExtraDisks:             extraDisks,
		MemoryMiB:              memoryMiB,
		BootVCPUs:              bootVCPUs,
		HasWorkspaceDisk:       hasWorkspace,
		WorkspaceDiskIndex:     numNamedDisks + workspaceDiskIndex,
		HasScratchDisk:         hasScratchDisk,
		ScratchDiskIndex:       scratchDiskIndex,
		ResizableDiskIndices:   resizableDiskIndices,
		WorkspaceGuestPath:     workspaceGuestPath,
		GovBounds:              govBounds,
		Cmdline:                cmdline,
		LiveMounts:             liveMounts,
		VirtiofsdPath:          virtiofsdPath,
		CredsFile:              service.DedicatedCredStorePathForProfile(agentProfile),
		NestedVirt:             nestedVirt,
		MCPOAuthRefreshConfigs: mcpOAuthRefreshConfigs,
	}
}

func workspaceGuestPathFor(ws *service.WorkspaceSpec) string {
	if ws == nil {
		return ""
	}
	return ws.GuestPath
}

func spawnPersistedSupervisor(ctx context.Context, svc *service.Service, id domain.SandboxID, stateDir string) error {
	cfg, err := supervisor.ReadSpawnSpec(stateDir)
	if err != nil {
		return err
	}
	pid, _, err := supervisor.SpawnDetached(supervisor.SpawnConfig{
		Config:       cfg,
		ReadyTimeout: 5 * time.Minute,
	})
	if err != nil {
		return err
	}
	sock := supervisor.SockPath(stateDir)
	if err := svc.SetSupervisor(ctx, id, pid, sock); err != nil {
		return fmt.Errorf("persist supervisor pid: %w", err)
	}
	slog.Info("sandbox: supervisor ready", "sandbox", id, "pid", pid, "sock", sock)
	return nil
}

func spawnPersistedSupervisorReacquire(ctx context.Context, svc *service.Service, id domain.SandboxID, stateDir string) error {
	cfg, err := supervisor.ReadSpawnSpec(stateDir)
	if err != nil {
		return err
	}
	pid, err := supervisor.SpawnReacquireDetached(supervisor.SpawnConfig{
		Config:       cfg,
		ReadyTimeout: 5 * time.Minute,
	})
	if err != nil {
		return err
	}
	sock := supervisor.SockPath(stateDir)
	if err := svc.SetSupervisor(ctx, id, pid, sock); err != nil {
		return fmt.Errorf("persist fork supervisor pid: %w", err)
	}
	slog.Info("sandbox: fork supervisor ready", "sandbox", id, "pid", pid, "sock", sock)
	return nil
}

func ensureDetachedSupervisor(ctx context.Context, svc *service.Service, sb domain.Sandbox) error {
	if sb.SupervisorPID > 0 {
		alive, _ := supervisor.CheckAndReconcile(sb.SupervisorPID, sb.SupervisorSock)
		if alive {
			return nil
		}
		_ = svc.ClearSupervisor(ctx, sb.ID)
	}
	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return err
	}
	stateDir := supervisor.DefaultStateDir(storeRoot, sb.ID)
	if _, err := os.Stat(supervisor.SpecPath(stateDir)); err != nil {
		return fmt.Errorf("no spawn spec for %s: %w", sb.ID, err)
	}
	return spawnPersistedSupervisor(ctx, svc, sb.ID, stateDir)
}

const supervisorExitTimeout = 15 * time.Second

func stopDetachedSupervisor(ctx context.Context, svc *service.Service, sb domain.Sandbox) {
	if sb.SupervisorSock == "" {
		return
	}
	if err := supervisor.StopSupervisor(ctx, sb.SupervisorSock); err != nil {
		slog.Warn("sandbox: StopSupervisor", "sock", sb.SupervisorSock, "err", err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, supervisorExitTimeout)
	defer cancel()
	if err := supervisorWaitForExit(waitCtx, filepath.Dir(sb.SupervisorSock)); err != nil {
		slog.Warn("sandbox: supervisor did not exit within timeout; state may lag",
			"sock", sb.SupervisorSock, "timeout", supervisorExitTimeout, "err", err)
	}
	_ = svc.ClearSupervisor(ctx, sb.ID)
}

var supervisorWaitForExit = supervisor.WaitForExit

func kernelPathFor() string {
	p, _ := resolveKernelPath()
	if p != "" {
		return p
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "images", "kernel", "vmlinux-x86_64")
}

type sandboxListWideRowJSON struct {
	ID             string `json:"id"`
	Handle         string `json:"handle"`
	State          string `json:"state"`
	UptimeSeconds  int64  `json:"uptime_seconds"`
	AllocatedBytes int64  `json:"allocated_bytes"`
	Error          string `json:"error,omitempty"`
}

type sandboxListWideDataJSON struct {
	LabelKey        string                   `json:"label_key"`
	LabelValue      string                   `json:"label_value"`
	Sandboxes       []sandboxListWideRowJSON `json:"sandboxes"`
	TotalAllocBytes int64                    `json:"total_alloc_bytes"`
	LeakedResources int                      `json:"leaked_resources"`
}

func parseLabel(cmd, label string) (key, value string, err error) {
	k, v, ok := strings.Cut(label, "=")
	if !ok || k == "" || v == "" {
		return "", "", &UsageError{Msg: fmt.Sprintf("%s --label: requires KEY=VALUE format; got %q", cmd, label)}
	}
	return k, v, nil
}

var sandboxListHeaders = []string{"HANDLE", "STATE", "AGENT", "MOUNTS", "ID"}

func runSandboxList(ctx context.Context, args []string, out *Output, svc *service.Service) error {
	var labelFlag string
	var wideFlag bool
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--label":
			if i+1 >= len(args) {
				return &UsageError{Msg: "sandbox list: --label requires an argument"}
			}
			i++
			labelFlag = args[i]
		case args[i] == "--wide":
			wideFlag = true
		default:
			return &UsageError{Msg: fmt.Sprintf("sandbox list: unknown flag %q; usage: sandbox list [--label KEY=VALUE] [--wide]", args[i])}
		}
	}

	if wideFlag && labelFlag == "" {
		return &UsageError{Msg: "sandbox list: --wide requires --label"}
	}

	if labelFlag == "" {
		all, err := svc.List(ctx)
		if err != nil {
			return errSandbox("sandbox list", err)
		}
		infos := make([]sandboxInfoJSON, 0, len(all))
		for _, sb := range all {
			infos = append(infos, toSandboxInfoJSON(sb))
		}
		if !out.IsJSON() && len(all) > 0 {
			rows := make([][]string, 0, len(all))
			for _, sb := range all {
				rows = append(rows, []string{
					sb.Handle(),
					sb.State.String(),
					herdrWorkspaceAgent(sb),
					herdrWorkspaceMounts(sb),
					sb.ID.String(),
				})
			}
			fmt.Fprint(out.w, renderTable(sandboxListHeaders, rows))
		}
		out.EmitSuccess("sandbox.list", sandboxListDataJSON{Sandboxes: infos},
			fmt.Sprintf("%d sandbox(es)", len(infos)))
		return nil
	}

	lKey, lVal, lErr := parseLabel("sandbox list", labelFlag)
	if lErr != nil {
		return lErr
	}

	if wideFlag {
		return runSandboxListWide(ctx, lKey, lVal, out, svc)
	}

	sandboxes, err := svc.GetByLabels(ctx, map[string]string{lKey: lVal})
	if err != nil {
		return errSandbox("sandbox list", err)
	}
	infos := make([]sandboxInfoJSON, 0, len(sandboxes))
	for _, sb := range sandboxes {
		infos = append(infos, toSandboxInfoJSON(sb))
	}
	out.EmitSuccess("sandbox.list", sandboxListDataJSON{Sandboxes: infos},
		fmt.Sprintf("%d sandbox(es) with %s=%s", len(infos), lKey, lVal))
	return nil
}

func runSandboxListWide(ctx context.Context, labelKey, labelValue string, out *Output, svc *service.Service) error {
	report, err := svc.LabelStatus(ctx, labelKey, labelValue)
	if err != nil {
		return errSandbox("sandbox list --wide", err)
	}

	rows := make([]sandboxListWideRowJSON, 0, len(report.Rows))
	for _, row := range report.Rows {
		r := sandboxListWideRowJSON{
			ID:             row.Sandbox.ID.String(),
			Handle:         row.Sandbox.Handle(),
			State:          row.Sandbox.State.String(),
			UptimeSeconds:  row.UptimeSeconds,
			AllocatedBytes: row.AllocatedBytes,
		}
		if row.Err != nil {
			r.Error = row.Err.Error()
		}
		rows = append(rows, r)
	}

	data := sandboxListWideDataJSON{
		LabelKey:        labelKey,
		LabelValue:      labelValue,
		Sandboxes:       rows,
		TotalAllocBytes: report.TotalAllocBytes,
		LeakedResources: report.LeakedCount,
	}

	msg := renderSandboxListWide(labelKey, labelValue, report)
	out.EmitSuccess("sandbox.list.wide", data, msg)
	return nil
}

func renderSandboxListWide(labelKey, labelValue string, report *service.LabelStatusReport) string {
	var buf bytes.Buffer
	tw := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)

	fmt.Fprintf(tw, "ID\tSTATE\tUPTIME\tDISK\tERROR\n")
	for _, row := range report.Rows {
		errStr := ""
		if row.Err != nil {
			errStr = row.Err.Error()
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			row.Sandbox.ID.String(),
			row.Sandbox.State.String(),
			service.FormatUptime(row.UptimeSeconds),
			service.FormatBytes(row.AllocatedBytes),
			errStr,
		)
	}
	tw.Flush()

	fmt.Fprintf(&buf, "\n%s=%s | total disk: %s | leaked resources: %d",
		labelKey, labelValue,
		service.FormatBytes(report.TotalAllocBytes),
		report.LeakedCount,
	)
	return strings.TrimRight(buf.String(), "\n")
}

func runSandboxRm(ctx context.Context, args []string, out *Output, svc *service.Service) error {
	root, _ := store.DefaultRoot() // best-effort: herdr cascade proceeds even on error
	herdrBin, herdrBinErr := resolveHerdrBin()
	var closer func(ctx context.Context, workspaceID string) error
	if herdrBinErr != nil {
		slog.Warn("sandbox rm: herdr not found; binding retained for space-prune recovery", "reason", herdrBinErr.Error())
		closer = func(_ context.Context, _ string) error { return herdrBinErr }
	} else {
		closer = func(ctx context.Context, workspaceID string) error {
			return herdrWorkspaceClose(ctx, herdrBin, workspaceID)
		}
	}
	return runSandboxRmFull(ctx, args, out, svc, root, closer)
}

func runSandboxRmFull(ctx context.Context, args []string, out *Output, svc *service.Service, storeRoot string, closeWorkspace func(context.Context, string) error) error {
	if len(args) != 1 {
		return &UsageError{Msg: "sandbox rm: usage: sandbox rm <id|prefix|project/name>"}
	}
	ref := args[0]

	var target *domain.Sandbox
	if sb, resolveErr := svc.Get(ctx, ref); resolveErr == nil {
		target = &sb
	} else {
		var ambig *domain.ErrAmbiguous
		if errors.As(resolveErr, &ambig) {
			return errSandbox("sandbox rm", resolveErr)
		}
	}
	if target != nil {
		stopDetachedSupervisor(ctx, svc, *target)
	}
	if err := svc.Remove(ctx, ref); err != nil {
		return errSandbox("sandbox rm", err)
	}

	if target != nil && storeRoot != "" {
		deps := txnDeps{
			workspaceClose: func(ctx context.Context, wsID string) error {
				return closeWorkspace(ctx, wsID)
			},
			bindingDelete: func(ctx context.Context, label string) error {
				return HerdrSpaceDelete(ctx, storeRoot, label)
			},
		}
		_ = herdrSpaceTeardown(ctx, storeRoot, target.Handle(), deps, teardownOpts{
			expectedSandboxID:     target.ID.String(),
			sandboxAlreadyRemoved: true,
			failOpen:              true,
		})
	}

	id := ref
	handle := ref
	if target != nil {
		id = target.ID.String()
		handle = target.Handle()
	}

	out.EmitSuccess("sandbox.removed", sandboxRemovedDataJSON{ID: id, Handle: handle},
		fmt.Sprintf("removed sandbox %s", handle))
	return nil
}

func runSandboxStart(ctx context.Context, args []string, out *Output, svc *service.Service) error {
	if len(args) != 1 {
		return &UsageError{Msg: "sandbox start: usage: sandbox start <id|prefix|project/name>"}
	}
	sb, err := svc.ResolveRef(ctx, args[0])
	if err != nil {
		return errSandbox("sandbox start", err)
	}
	if err := ensureDetachedSupervisor(ctx, svc, sb); err != nil {
		slog.Info("sandbox start: no detached supervisor; in-process start", "sandbox", sb.ID, "err", err)
		started, startErr := svc.Start(ctx, args[0])
		if startErr != nil {
			return errSandbox("sandbox start", startErr)
		}
		sb = started
	} else {
		fresh, getErr := svc.GetSandboxByID(ctx, sb.ID)
		if getErr == nil {
			sb = fresh
		}
	}
	out.EmitSuccess("sandbox.started", toSandboxInfoJSON(sb),
		fmt.Sprintf("started sandbox %s (%s)", sb.Handle(), sb.ID))
	return nil
}

func runSandboxStop(ctx context.Context, args []string, out *Output, svc *service.Service) error {
	if len(args) != 1 {
		return &UsageError{Msg: "sandbox stop: usage: sandbox stop <id|prefix|project/name>"}
	}
	sb, err := svc.ResolveRef(ctx, args[0])
	if err != nil {
		return errSandbox("sandbox stop", err)
	}
	if sb.SupervisorSock != "" {
		stopDetachedSupervisor(ctx, svc, sb)
		fresh, getErr := svc.GetSandboxByID(ctx, sb.ID)
		if getErr == nil {
			sb = fresh
		}
		if sb.State != domain.Stopped {
			return &CodedError{
				Code: ErrCodeInternalError,
				Msg: fmt.Sprintf(
					"sandbox stop: supervisor for %s did not finish within %s; sandbox is still %s — re-run `nexus ps` in a moment, or `nexus reap` if it stays this way",
					sb.Handle(), supervisorExitTimeout, sb.State),
			}
		}
	} else {
		stopped, stopErr := svc.Stop(ctx, args[0])
		if stopErr != nil {
			return errSandbox("sandbox stop", stopErr)
		}
		sb = stopped
	}
	out.EmitSuccess("sandbox.stopped", toSandboxInfoJSON(sb),
		fmt.Sprintf("stopped sandbox %s (%s)", sb.Handle(), sb.ID))
	return nil
}

func namedDiskGuestMounts(mounts []service.NamedVolumeMount) []agent.GuestMount {
	var out []agent.GuestMount
	for _, m := range mounts {
		if m.Kind != volumestore.KindDisk {
			continue
		}
		out = append(out, agent.GuestMount{
			Device:    shadowDevicePath(len(out)), // ExtraDisks[len(out)] → /dev/vd{b+len(out)}
			Target:    m.GuestPath,
			FSType:    "ext4",
			ReadOnly:  m.ReadOnly,
			Resizable: true, // named kind=disk volumes are governor-managed (independent of IsWorkspace)
		})
	}
	return out
}

func sandboxAgentCfgVolumeName(project, name string) string {
	return herdrHandleSlug(project+"/"+name) + "-agentcfg"
}

func stageAgentCuratedConfig(profile cred.AgentProfile, stageDir string) error {
	agentConfigDir, err := service.AgentSettingsDir(profile)
	if err != nil {
		return err
	}
	return service.AssembleCuratedConfig(profile, agentConfigDir, stageDir)
}

func parseMountNamed(spec string) (service.NamedVolumeMount, error) {
	parts := strings.SplitN(spec, ":", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return service.NamedVolumeMount{}, &UsageError{
			Msg: fmt.Sprintf("sandbox create: --mount-named %q: want <volume-name>:<guest-path>[:ro|kind=dir|size=Xg]", spec),
		}
	}
	name := parts[0]
	guestPath := parts[1]

	if hasGitComponent(guestPath) {
		return service.NamedVolumeMount{}, &UsageError{
			Msg: fmt.Sprintf("sandbox create: --mount-named %q: guest path %q must not contain a .git component (design line 63)", spec, guestPath),
		}
	}

	m := service.NamedVolumeMount{
		Name:      name,
		GuestPath: guestPath,
		Kind:      volumestore.KindDisk, // default
	}

	if len(parts) == 3 {
		opts := strings.Split(parts[2], ",")
		for _, opt := range opts {
			switch {
			case opt == "ro":
				m.ReadOnly = true
			case opt == "kind=dir":
				m.Kind = volumestore.KindDir
			case strings.HasPrefix(opt, "size="):
				sizeStr := strings.TrimPrefix(opt, "size=")
				sz, sErr := parseVolumeSize(sizeStr)
				if sErr != nil {
					return service.NamedVolumeMount{}, &UsageError{
						Msg: fmt.Sprintf("sandbox create: --mount-named %q: invalid size %q: %v", spec, sizeStr, sErr),
					}
				}
				m.SizeBytes = sz
			default:
				return service.NamedVolumeMount{}, &UsageError{
					Msg: fmt.Sprintf("sandbox create: --mount-named %q: unknown option %q", spec, opt),
				}
			}
		}
	}
	return m, nil
}

func parseMountLive(spec string) (domain.LiveMount, error) {
	parts := strings.SplitN(spec, ":", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return domain.LiveMount{}, &UsageError{
			Msg: fmt.Sprintf("sandbox create: --mount %q: want <host-path>:<guest-path>[:ro]", spec),
		}
	}
	hostPath := parts[0]
	guestPath := parts[1]

	info, err := os.Stat(hostPath)
	if err != nil {
		return domain.LiveMount{}, &UsageError{
			Msg: fmt.Sprintf("sandbox create: --mount %q: host path %q: %v", spec, hostPath, err),
		}
	}
	if !info.IsDir() {
		return domain.LiveMount{}, &UsageError{
			Msg: fmt.Sprintf("sandbox create: --mount %q: host path %q is not a directory (virtiofs shares directories only)", spec, hostPath),
		}
	}
	abs, err := filepath.Abs(hostPath)
	if err != nil {
		return domain.LiveMount{}, &UsageError{
			Msg: fmt.Sprintf("sandbox create: --mount %q: resolve host path: %v", spec, err),
		}
	}

	lm := domain.LiveMount{
		HostPath:  abs,
		GuestPath: guestPath,
	}
	if len(parts) == 3 {
		switch parts[2] {
		case "ro":
			lm.ReadOnly = true
		default:
			return domain.LiveMount{}, &UsageError{
				Msg: fmt.Sprintf("sandbox create: --mount %q: unknown option %q; want <host-path>:<guest-path>[:ro]", spec, parts[2]),
			}
		}
	}
	return lm, nil
}

func liveMountsToGuestMounts(mounts []domain.LiveMount) []agent.GuestMount {
	out := make([]agent.GuestMount, len(mounts))
	for i, m := range mounts {
		out[i] = agent.GuestMount{
			Device:      cloudhypervisor.VirtiofsTag(i),
			Target:      m.GuestPath,
			FSType:      "virtiofs",
			ReadOnly:    m.ReadOnly,
			IsWorkspace: false,
			IsFile:      m.IsFile,
			FileName:    filepath.Base(m.HostPath),
		}
		if !m.IsFile {
			out[i].FileName = ""
		}
	}
	return out
}

func hasGitComponent(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".git" {
			return true
		}
	}
	return false
}

func parseVolumeSize(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("empty size string")
	}
	suffix := strings.ToLower(string(s[len(s)-1]))
	numStr := s[:len(s)-1]
	switch suffix {
	case "g":
		v, err := strconv.ParseInt(numStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid GiB value %q", s)
		}
		return v << 30, nil
	case "m":
		v, err := strconv.ParseInt(numStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid MiB value %q", s)
		}
		return v << 20, nil
	case "k":
		v, err := strconv.ParseInt(numStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid KiB value %q", s)
		}
		return v << 10, nil
	default:
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid byte count %q", s)
		}
		return v, nil
	}
}
