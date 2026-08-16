package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/builder"
	"github.com/IniZio/nexus3/internal/core/builder/builderimage"
	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/resize"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/core/vmcfg"
	"github.com/IniZio/nexus3/internal/supervisor"
)

func init() {
	Register(Command{
		Name:    "sandbox",
		Summary: "Manage sandboxes (create|list|rm|start|stop|pause|resume)",
		Run:     runSandbox,
	})
}

// ── Sandbox-specific error codes (machine contract) ──────────────────────────
//
// These codes form a stable, versioned API surface. See the full code table in
// output.go. Renaming any constant requires a schemaVersion bump.
//
// Each run* function wraps errors via errSandbox, which calls sandboxCodeFor to
// select the code without string-matching any error message text.
//
// Removed codes (never emitted, safe to drop — no caller could have observed them):
//   - "sandbox_locked" (sandboxErrCodeLocked): mapped store.ErrLocked, which was
//     declared alongside TryExclusive. Both were deleted as speculative API with
//     no callers before the first release.
const (
	// sandboxErrCodeNotFound is returned when a sandbox reference does not
	// match any known sandbox (no-match prefix or missing handle/ID).
	sandboxErrCodeNotFound = "sandbox_not_found"

	// sandboxErrCodeAlreadyExists is returned when creating a sandbox whose
	// project/name handle already exists.
	sandboxErrCodeAlreadyExists = "sandbox_already_exists"

	// sandboxErrCodeAmbiguousRef is returned when an ID prefix matches more
	// than one sandbox. The error message names all candidates.
	sandboxErrCodeAmbiguousRef = "ambiguous_ref"

	// sandboxErrCodeIllegalTransition is returned when a lifecycle trigger is
	// not valid from the sandbox's current state. The error message lists
	// legal triggers.
	sandboxErrCodeIllegalTransition = "illegal_transition"

	// sandboxErrCodeNoSubstrate is returned when a verb requires a hypervisor
	// driver that is not compiled into this build (e.g. start, pause, resume).
	sandboxErrCodeNoSubstrate = "no_substrate"

	// sandboxErrCodeNoGuestImage is returned when start requires a guest kernel
	// or disk image that is not available because the guest image pipeline is not
	// yet implemented. The substrate is present and healthy; only the image is
	// missing.
	sandboxErrCodeNoGuestImage = "no_guest_image"

	// sandboxErrCodeInvalidArgument is returned when a positional argument has
	// an invalid format (e.g. a malformed project/name handle). This is a usage
	// error — root.go will exit with code 2.
	sandboxErrCodeInvalidArgument = "invalid_argument"

	// sandboxErrCodeAgentUnreachable is returned when the VM boots but the
	// guest agent does not become reachable within the configured timeout.
	// The VM is stopped and the record is deleted before this code is returned.
	sandboxErrCodeAgentUnreachable = "agent_unreachable"
)

// ── noopDriver ───────────────────────────────────────────────────────────────

// sandboxNoopDriver is the fallback driver used when substrate selection fails.
// It preserves the existing behaviour for store-only verbs (create, list, rm):
// those do not call the driver at all, so they keep working without a substrate.
// Substrate-requiring verbs (start, stop, pause, resume) get a clear error that
// names the specific check that failed (via the reason field).
//
// Driver decision rationale (locked at architecture review):
//   - A nil driver would panic at the first driver call — unsuitable for a
//     production binary.
//   - An always-error driver for every method would make create/list/rm
//     (which do not require a running VM) fail unnecessarily.
//   - sandboxNoopDriver threads the needle: safe for the store-only verbs,
//     honest for the substrate-requiring verbs.
type sandboxNoopDriver struct {
	// reason is the human-readable explanation from substrate selection. When
	// non-empty it is included in the Start error message so the operator knows
	// exactly which check failed (e.g. "cloud-hypervisor not found in PATH").
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
	// No VM exists; stopping nothing is not an error.
	return nil
}

// sandboxNoopDriver does NOT implement driver.PauseResumer. The type assertion
// in service.Pause/Resume will fail and return a "no substrate configured"
// error, which is correct: pause/resume require a running VM.

// ── service construction ──────────────────────────────────────────────────────

// newSandboxService builds a Service for use by CLI command handlers. The store
// root follows XDG_STATE_HOME (see store.DefaultRoot). SelectSubstrate is
// called to attempt real substrate selection:
//   - On success the real driver (Cloud Hypervisor) is used.
//   - On failure the sandboxNoopDriver is used, carrying the specific failure
//     reason so that substrate-requiring verbs (start, pause, resume) emit an
//     actionable message via the no_substrate error code. Store-only verbs
//     (create, list, rm) do not call the driver at all and keep working.
//
// Tests override via direct function calls with their own service instance;
// they do not go through this constructor.
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
		// Substrate unavailable. Use the noop driver with the specific reason
		// so substrate-requiring verbs name the failed check in their error.
		drv = &sandboxNoopDriver{reason: serr.Msg}
	} else {
		drv = realDrv
	}

	return service.New(st, drv, lifecycle.New()), nil
}

// ── Error code mapping ────────────────────────────────────────────────────────

// sandboxCodeFor maps a core-package error to the appropriate sandbox error
// code using errors.Is / errors.As. It never inspects error message text.
// The switch order matters: ErrAmbiguous is checked before ErrNotFound because
// an ambiguous prefix is also "not nothing" — the more specific type wins.
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

// errSandbox wraps cause in a *CodedError with the appropriate stable error
// code. prefix is prepended to the message (e.g. "sandbox rm"). The original
// error message is preserved in full and the cause participates in the
// errors.Is / errors.As chain via CodedError.Unwrap.
func errSandbox(prefix string, cause error) *CodedError {
	return &CodedError{
		Code: sandboxCodeFor(cause),
		Msg:  prefix + ": " + cause.Error(),
		Err:  cause,
	}
}

// ── JSON data types ───────────────────────────────────────────────────────────

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

// ── dispatcher ───────────────────────────────────────────────────────────────

func runSandbox(ctx context.Context, args []string, out *Output) error {
	if len(args) == 0 {
		return &UsageError{Msg: "sandbox: missing subcommand; usage: sandbox <create|list|rm|start|stop|pause|resume>"}
	}

	verb := args[0]
	verbArgs := args[1:]

	// Substrate-requiring verbs (start, stop, pause, resume) need a real
	// driver. Check availability before calling into the service — service
	// methods resolve the sandbox ID first, so an ErrNotFound would surface
	// before the driver is ever called, hiding the real no_substrate error.
	// By checking upfront, no_substrate is always reported for these verbs
	// regardless of whether the sandbox exists in the store.
	switch verb {
	case "start", "stop", "pause", "resume":
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
	case "pause":
		return runSandboxPause(ctx, verbArgs, out, svc)
	case "resume":
		return runSandboxResume(ctx, verbArgs, out, svc)
	default:
		return &UsageError{Msg: fmt.Sprintf("sandbox: unknown subcommand %q; valid: create list rm start stop pause resume", verb)}
	}
}

// ── create ────────────────────────────────────────────────────────────────────

// runSandboxCreate handles:
//
//	sandbox create <project>/<name> [--rm] [--image <ref>|--rootfs <path>|--file <dir>]
//
// When none of --image, --rootfs, or --file is given the sandbox is recorded
// in Created state only (no VM is started). This preserves the existing
// behaviour for callers that only need a named sandbox record.
//
// When --image or --rootfs is given, CreateAndBoot is called: the ext4 image
// is resolved, a Cloud Hypervisor VM is started, the guest agent is probed for
// reachability, and the sandbox is recorded as Running.
//
// Kernel (boot path only): NEXUS3_KERNEL_PATH env var overrides the default.
// If unset the binary-relative path <binary-dir>/images/kernel/vmlinux-x86_64
// is tried first, then <cwd>/images/kernel/vmlinux-x86_64 (convenient for
// "go run ./cmd/nexus3" from the repo root). Resolution is validated before
// any expensive work (workspace capture, shadow disks, builder VM) begins; a
// missing kernel surfaces a legible error immediately rather than after a
// multi-second capture.
//
// sandboxCreateFlags holds the result of parsing `sandbox create` arguments.
type sandboxCreateFlags struct {
	rm              bool
	imageRef        string
	rootfsPath      string
	filePath        string
	dockerfilePath  string // --dockerfile / -f: explicit Containerfile path override
	memoryMiB       uint32
	vcpus           uint32
	labels          map[string]string
	nestedVirt      bool
	workspacePath   string // --workspace <host-path>: host git worktree to capture
	captureMaxBytes int64  // --capture-max <size>: explicit workspace capture cap (0 = auto)
	// Auto-resize ceiling flags are optional; defaults apply. Auto-resize is
	// unconditional (no opt-out; D-DC-30 revised 2026-08-14).
	memoryMaxMiB uint32   // --memory-max <MiB>: RAM ceiling for hotplug region
	vcpusMax     uint32   // --vcpus-max <n>:    vCPU ceiling for hotplug
	diskMaxGiB   uint32   // --disk-max <GiB>:   disk grow ceiling
	secrets      []string // --secret ENV@host[,host…] (repeatable)
	noBuiltinGH  bool     // --no-builtin-gh: skip host gh auth token bind
	positionals  []string
}

// parseSandboxCreateArgs parses the raw argument slice for `sandbox create`.
// All flags and positional arguments are collected; positional count and
// handle format are validated by the caller.
func parseSandboxCreateArgs(args []string) (sandboxCreateFlags, error) {
	f := sandboxCreateFlags{}
	i := 0
	for i < len(args) {
		arg := args[i]
		switch arg {
		case "--rm":
			f.rm = true
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
		case "--no-builtin-gh":
			f.noBuiltinGH = true
		default:
			if len(arg) > 1 && arg[0] == '-' {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: unknown flag %q", arg)}
			}
			f.positionals = append(f.positionals, arg)
		}
		i++
	}
	// Validate ceiling-vs-boot consistency. vmcfg.Resolve applies default
	// ceilings when these are zero; explicit values must satisfy ceiling > boot
	// so that the driver reserves a non-trivial hotplug region (CHDriver.New
	// enforces VCPUMax > VCPUs and MemoryMaxMiB > MemoryMiB when both are set).
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
	return f, nil
}

// buildCHConfig constructs a cloudhypervisor.Config for the resolved ext4.
// When memMiB or vcpus is zero the respective field is left unset and the
// driver applies its built-in default (512 MiB / 1 vCPU).
// Exposed (unexported) so that cmd_sandbox_create_test.go can assert the
// flag→Config wiring without booting a real VM.
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

// ── workspace mount cmdline helpers ──────────────────────────────────────────

// diskBootCmdlineBase is the kernel command line for disk-boot sandboxes.
// It must stay in sync with diskBootCmdline in
// internal/core/driver/cloudhypervisor/driver.go (unexported constant there).
const diskBootCmdlineBase = "root=/dev/vda rw init=/sbin/nexus3-agent console=ttyS0"

// workspaceMountCmdline builds a kernel command line that wires workspace and
// shadow disk mount specs into the guest agent's argv.
//
// The Linux kernel delivers tokens after "--" in the cmdline directly to PID 1
// as os.Args[1:], so the agent reads them via parseWorkspaceMountArg.
//
// Format per mount: --workspace-mount=<device>:<target>:<fstype>:<readonly>:<workspace>
// The "readonly" field is "true" when m.ReadOnly is true, "false" otherwise.
// The "workspace" field is "true" when m.IsWorkspace is true, "false" otherwise.
// Exactly one mount in a well-formed set carries workspace=true; the agent selects
// the disk-telemetry target by this field (never by position or ReadOnly inference).
//
// Callers must pass a non-empty slice; calling with an empty slice is a no-op
// that is caught by the if-guard in newDriver rather than here to keep the hot
// path (no workspace) zero-allocation.
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
		b += fmt.Sprintf(" --workspace-mount=%s:%s:%s:%s:%s", m.Device, m.Target, m.FSType, ro, ws)
	}
	return b
}

// ── builder-VM helpers ────────────────────────────────────────────────────────

// parseHumanBytes parses a human-readable byte count string (e.g. "8GiB",
// "500MB", "1024") into an int64 byte count. Recognised suffixes (case-sensitive):
// TiB, GiB, MiB, KiB (powers of 1024) and TB, GB, MB, KB, B (SI/decimal).
// A bare integer (no suffix) is treated as bytes. Returns an error for
// unrecognised suffixes, non-numeric values, zero, negative values, NaN, Inf,
// or values that overflow int64.
//
// Deliberately rejected (case-sensitive suffix matching, no spaces):
//   - "8gib", "8G", "8 GiB" — wrong case or space before suffix
//   - "1e9"                  — scientific notation not supported
//   - "-1", "-5GiB"          — negative values are meaningless as a byte cap
//   - "0", "0GiB"            — zero is reserved to mean AUTO (free-space-derived)
//   - "NaNGiB", "InfGiB"     — IEEE special values
//   - "99999999TiB"          — overflows int64
func parseHumanBytes(s string) (int64, error) {
	type suffix struct {
		label string
		mult  int64
	}
	// Longest suffixes must come first so "GiB" is matched before "B".
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
	// Plain integer — treat as bytes.
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, &UsageError{Msg: fmt.Sprintf("--capture-max: invalid size %q: expected a positive number with optional suffix like 8GiB or 500MB", s)}
	}
	if n <= 0 {
		return 0, &UsageError{Msg: fmt.Sprintf("--capture-max: size %q must be a positive non-zero value (0 is reserved for AUTO mode)", s)}
	}
	return n, nil
}

// buildTaskTimeout parses NEXUS3_BUILD_TASK_TIMEOUT (a Go duration string) and
// returns the configured duration. It defaults to 20 minutes. This is the OUTER
// hard wall-clock cap for the entire cache-miss build path: EnsureBuilderImage,
// ContextToDisk, BuildInVM (VM boot + buildkitd solve + artifact export). It is
// distinct from NEXUS3_BUILD_SOLVE_TIMEOUT, which caps only the buildkitd solve
// step and cannot catch a stuck VM boot.
func buildTaskTimeout() time.Duration {
	const defaultTimeout = 20 * time.Minute
	s := os.Getenv("NEXUS3_BUILD_TASK_TIMEOUT")
	if s == "" {
		return defaultTimeout
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		slog.Warn("NEXUS3_BUILD_TASK_TIMEOUT: invalid duration, using default",
			"value", s, "default", defaultTimeout)
		return defaultTimeout
	}
	return d
}

// resolveContainerfilePath returns the Containerfile path to use for a --file
// build. Priority:
//  1. explicit — returned as-is (caller validates existence separately)
//  2. <workspaceDir>/.nexus/Containerfile — if present
//  3. <workspaceDir>/.nexus/Dockerfile    — if present
//
// Returns an error when explicit is empty and neither default path exists.
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

// preallocateFile creates (or truncates) the file at path to size bytes as a
// sparse file. Used to pre-size the artifact disk before handing it to the
// builder VM as /dev/vdc.
func preallocateFile(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Truncate(size)
}

// buildWorkspaceSpec constructs a *service.WorkspaceSpec from its constituent
// values. It is the single construction site for WorkspaceSpec on all create
// paths (sandbox create --workspace and orca create) so that removing any field
// assignment here causes tests across both callers to fail — no second site can
// drift silently (see: --memory 8GiB bug post-mortem). The --file path bypasses
// WorkspaceSpec entirely and hands captureMaxBytes directly to
// builder.WorktreeToDisk; that is a legitimately different flow.
// Exposed so that cmd_sandbox_create_test.go can assert the flag→spec handoff
// without booting a VM or requiring a store/factory seam.
func buildWorkspaceSpec(sourcePath, guestPath string, captureMaxBytes int64) *service.WorkspaceSpec {
	return &service.WorkspaceSpec{
		SourcePath:      sourcePath,
		GuestPath:       guestPath,
		CaptureMaxBytes: captureMaxBytes,
	}
}

// workspaceSpecFromFlags is the sole call site for buildWorkspaceSpec on the
// sandbox-create --workspace path. It takes sandboxCreateFlags so that the
// flag→spec handoff is directly testable: a test calling this function with
// sandboxCreateFlags{captureMaxBytes: N} will go RED if f.captureMaxBytes is
// replaced by a literal 0 here — the argument-substitution mutation shape
// caught without a live KVM run (see: --memory 8GiB bug post-mortem).
func workspaceSpecFromFlags(f sandboxCreateFlags, wsAbs, guestPath string) *service.WorkspaceSpec {
	return buildWorkspaceSpec(wsAbs, guestPath, f.captureMaxBytes)
}

// testWorkspaceSpecHook, when non-nil, is called immediately after
// workspaceSpecFromFlags sets bootWorkspace on the --workspace path.  Tests
// set it to inspect the constructed spec without booting a VM.
// The hook must be reset to nil after use (e.g. via defer).
var testWorkspaceSpecHook func(*service.WorkspaceSpec)

// The --rm flag may appear before or after the handle. Flag scanning is done
// manually so that docker-style order ("create demo/one --rm --image …") works
// without requiring flags to precede the positional argument.
func runSandboxCreate(ctx context.Context, args []string, out *Output, svc *service.Service) error {
	f, parseErr := parseSandboxCreateArgs(args)
	if parseErr != nil {
		return parseErr
	}

	if len(f.positionals) != 1 {
		return &UsageError{Msg: "sandbox create: usage: sandbox create <project>/<name> [--rm] [--image <ref>|--rootfs <path>|--file <context-dir>] [--dockerfile <path>] [--memory <MiB>] [--vcpus <n>] [--label KEY=VALUE] [--nested] [--workspace <host-path>] [--capture-max <size>] [--memory-max <MiB>] [--vcpus-max <n>] [--disk-max <GiB>] [--secret ENV@host[,host…]] [--no-builtin-gh] (auto-resize is unconditional: hotplug hardware is configured at create time; the dynamic governor activates only in the supervisor process)"}
	}

	project, name, err := domain.ParseHandle(f.positionals[0])
	if err != nil {
		return &UsageError{Code: sandboxErrCodeInvalidArgument, Msg: fmt.Sprintf("sandbox create: %v", err)}
	}

	// ── No-boot path: store-only create (backwards-compatible) ───────────────
	if f.imageRef == "" && f.rootfsPath == "" && f.filePath == "" {
		sb, err := svc.Create(ctx, project, name, service.CreateOptions{RemoveOnExit: f.rm})
		if err != nil {
			return errSandbox("sandbox create", err)
		}
		out.EmitSuccess("sandbox.created", toSandboxInfoJSON(sb),
			fmt.Sprintf("created sandbox %s (%s)", sb.Handle(), sb.ID))
		return nil
	}

	// ── Boot path: resolve ext4 → start VM → probe agent ─────────────────────
	//
	// Preflight: validate the kernel path before any expensive work. This must
	// run before shadow disk creation, workspace capture, and builder VM launch
	// so that a misconfigured NEXUS3_KERNEL_PATH (or missing kernel) surfaces
	// immediately with an actionable message rather than after a 28-second capture
	// followed by an opaque CloudHypervisor "Cannot open kernel file" error.
	kernelPath, err := resolveKernelPath()
	if err != nil {
		return errSandbox("sandbox create", err)
	}

	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return errSandbox("sandbox create", fmt.Errorf("resolve state directory: %w", err))
	}
	cacheRoot := filepath.Join(storeRoot, "images")

	imgCache, err := image.NewCache(cacheRoot)
	if err != nil {
		return errSandbox("sandbox create", fmt.Errorf("open image cache: %w", err))
	}

	// --file: build image via a builder VM (self-contained; no external buildkitd
	// required). On success the resulting digest is fed into the normal boot path.
	if f.filePath != "" && f.imageRef == "" && f.rootfsPath == "" {
		// Derive the workspace root from the --file path.
		// Accepted forms:
		//   --file /path/to/project/.nexus/Containerfile  → workspace=/path/to/project
		//   --file /path/to/project                        → workspace=/path/to/project
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

		// Validate the Containerfile exists and save its path for fingerprinting.
		// The resolved path is passed to the builder role as part of the context
		// disk; buildkitd finds it at its canonical location inside the mounted
		// context.
		containerfilePath, err := resolveContainerfilePath(workspaceDir, f.dockerfilePath)
		if err != nil {
			return errSandbox("sandbox create", fmt.Errorf("--file: %w", err))
		}

		// Locate the nexus3-agent binary (same search path as kernelPathFor).
		agentBin, err := exec.LookPath("nexus3-agent")
		if err != nil {
			// Fall back to a binary-relative path.
			agentBin = filepath.Join(filepath.Dir(kernelPath), "nexus3-agent")
		}
		agentBytes, err := os.ReadFile(agentBin)
		if err != nil {
			return errSandbox("sandbox create", fmt.Errorf("--file: read agent binary %q: %w", agentBin, err))
		}

		taskTimeout := buildTaskTimeout()
		buildCtx, buildCancel := context.WithTimeout(ctx, taskTimeout)
		defer buildCancel()

		// ── Rootfs fingerprint cache ──────────────────────────────────────────
		// Compute a stable fingerprint over (Containerfile, FROM ref, agent
		// binary, context directory) and check whether we already have the
		// built image. A hit skips the entire ~25-min builder VM build.
		containerfileBytes, err := os.ReadFile(containerfilePath)
		if err != nil {
			return errSandbox("sandbox create", fmt.Errorf("--file: read Containerfile %q: %w", containerfilePath, err))
		}
		baseImageRef := builder.ExtractFromRef(containerfileBytes)
		fp, err := builder.BuildFingerprint(containerfileBytes, baseImageRef, agentBytes, workspaceDir)
		if err != nil {
			return errSandbox("sandbox create", fmt.Errorf("--file: fingerprint: %w", err))
		}

		// Per-fingerprint exclusive lock: prevents two concurrent `sandbox
		// create --file` calls with identical inputs from both building the
		// same image. The lock is held until f.imageRef is set.
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

			// Ensure the builder rootfs image (moby/buildkit) is available.
			builderRootfs, err := builderimage.EnsureBuilderImage(buildCtx, storeRoot, agentBytes)
			if err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: builder image: %w", err))
			}

			// Prepare an ephemeral directory for the per-build disk images.
			buildWorkDir, err := os.MkdirTemp("", "nexus3-build-*")
			if err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: build workdir: %w", err))
			}
			defer os.RemoveAll(buildWorkDir)

			// vdb — Pack the build context into an ext4 image using the full
			// working-tree capture (D-DC-08). builder.WorktreeToDisk reads
			// directly from disk, capturing committed tracked files, dirty
			// (uncommitted) tracked files, AND untracked files, subject to the
			// project's .dockerignore and nexus3's always-exclude list
			// (.claude, .agents, .groundwork, .pnpm-store). Unlike the former
			// "git archive HEAD" approach, no .git directory or any prior
			// commits are required — plain directories work too.
			//
			// The overlay of .nexus/Containerfile and .dockerignore that was
			// required under git archive (because those files were intentionally
			// untracked) is now redundant: WorktreeToDisk reads the working
			// tree directly and includes those files automatically.
			//
			// OOM risk: git archive avoided host OOM by excluding untracked
			// bulk. WorktreeToDisk re-introduces that risk for workspaces with
			// large untracked trees. Mitigation: a preflight guard rejects
			// captures whose projected ext4 image (source × 2 + 64 MiB headroom)
			// would exceed 80 % of free disk space on the build workdir
			// filesystem, returning an actionable error with the top-5 largest
			// contributor directories. Pass --capture-max to override the guard
			// with an explicit byte cap (e.g. --capture-max 8GiB).
			ctxDiskPath := filepath.Join(buildWorkDir, "ctx.ext4")
			if err := builder.WorktreeToDisk(buildCtx, workspaceDir, ctxDiskPath, f.captureMaxBytes); err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: context disk: %w", err))
			}

			// vdc — Pre-allocate the artifact disk; builder VM overwrites it
			// with the built rootfs ext4.
			const artifactDiskSize = 4 << 30 // 4 GiB sparse file
			artifactDiskPath := filepath.Join(buildWorkDir, "artifact.ext4")
			if err := preallocateFile(artifactDiskPath, artifactDiskSize); err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: artifact disk: %w", err))
			}

			// vdd+ — Attach buildkit (and any future) persistent cache disks.
			cacheDisks, err := builder.SelectCacheDisks(buildCtx, storeRoot, []string{"buildkit"})
			if err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: cache disks: %w", err))
			}

			// Assemble the BuilderVMSpec first so that sizing helpers
			// (VCPUs/MemMiB) can derive the production defaults before the
			// CHDriver config is constructed.
			spec := builder.BuilderVMSpec{
				RootfsDiskPath:   builderRootfs,
				ContextDiskPath:  ctxDiskPath,
				ArtifactDiskPath: artifactDiskPath,
				CacheDisks:       cacheDisks,
			}

			// Sizing is derived from the spec via exported helpers so
			// there is a single source of truth (builder.DefaultBuilderVCPUs /
			// DefaultBuilderMemMiB). vmcfg.Resolve applies the same 4× ceiling
			// defaults as the sandbox path, keeping both paths in sync.
			builderBootMemMiB := uint32(builder.MemMiB(spec))
			builderBootVCPUs := uint32(builder.VCPUs(spec))
			builderAR := vmcfg.Resolve(vmcfg.Config{
				BootMemMiB: builderBootMemMiB,
				BootVCPUs:  builderBootVCPUs,
			})

			// socketDir must be consistent between the CLI-side dialerDrv (for
			// vsock dialing) and the supervisor's own CHDriver (started in the
			// detached subprocess). orcaSocketDir mirrors cloudhypervisor's
			// defaultSocketDir so both sides resolve to the same path.
			builderSocketDir, err := orcaSocketDir()
			if err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: builder socket dir: %w", err))
			}

			// dialerCfg: CHDriver config for vsock dialing only. dialerDrv is
			// never Started from the CLI side — it only provides DialGuest so
			// the execFn can reach the vsock listener the supervisor-owned VM.
			// MemoryMaxMiB and VCPUMax must be set so CHDriver.New validation
			// (ceiling > boot) passes; they are used by the supervisor, not here.
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

			// Extra disk paths for the supervisor (flat []string, not ExtraDisk).
			builderExtraDisks := []string{ctxDiskPath, artifactDiskPath}
			for _, cd := range cacheDisks {
				builderExtraDisks = append(builderExtraDisks, cd.ImagePath)
			}

			// supervisorBuilderDriver routes Start/Stop through SpawnDetached so
			// the builder VM survives CLI exit. DialGuest delegates to dialerDrv.
			bdrv := &supervisorBuilderDriver{
				dialerDrv:  dialerDrv,
				storeRoot:  storeRoot,
				stateBase:  filepath.Join(storeRoot, "builder-supervisors"),
				socketDir:  builderSocketDir,
				kernelPath: kernelPath,
				diskPath:   builderRootfs,
				extraDisks: builderExtraDisks,
				ar:         builderAR,
				bootMemMiB: builderBootMemMiB,
				bootVCPUs:  builderBootVCPUs,
				logPath:    "/tmp/nexus3-builder-supervisor.log",
			}
			execFn := func(ctx context.Context, argv []string, stderr io.Writer) (int32, error) {
				// StartedID is set by bdrv.Start (called inside BuildInVM before
				// execFn runs), so the ID is always available here.
				ac := agent.NewClient(bdrv, bdrv.StartedID())
				// stdout and stderr are merged in the ring without tagging;
				// send both streams to the same writer so consoleFatal output
				// reaches the caller.
				return ac.Exec(ctx, agent.ExecOptions{Argv: argv, Stdout: stderr, Stderr: stderr})
			}

			builderStore, err := store.NewFileStore(storeRoot)
			if err != nil {
				return errSandbox("sandbox create", fmt.Errorf("--file: builder store: %w", err))
			}
			digest, err := builder.BuildInVM(buildCtx, bdrv, spec, imgCache, execFn, builderStore)
			if err != nil {
				// If the outer task deadline fired, return a clear actionable message
				// instead of an opaque wrapped context error. The builder VM's
				// SyncAndStop / panicSafeStop defer has already run at this point, so
				// the CH VMM is stopped and no sandbox record was persisted.
				if buildCtx.Err() != nil {
					return errSandbox("sandbox create", fmt.Errorf(
						"builder task exceeded %v (set NEXUS3_BUILD_TASK_TIMEOUT to change) — aborted",
						taskTimeout))
				}
				return errSandbox("sandbox create", fmt.Errorf("--file: build: %w", err))
			}

			// Cache the produced digest so the next identical create is instant.
			// Non-fatal: the sandbox boots fine even if this write fails.
			if storeErr := builder.StoreBuildCache(storeRoot, fp, digest); storeErr != nil {
				slog.Warn("build-cache: store failed (non-fatal)", "fp", fp[:12], "err", storeErr)
			}

			// Feed the built image into the normal boot path.
			f.imageRef = digest
		}
	}

	// bootGuestMounts is set by the workspace block below and captured by
	// newDriver. It must be declared here (before newDriver) so the closure
	// can reference it; Go closures capture by reference, so newDriver reads
	// the value that is set after CreateAndBoot calls it.
	var bootGuestMounts []agent.GuestMount
	var capturedDiskPath string
	var capturedExtraDisks []string
	var capturedCmdline string
	var capturedCHBin string
	var capturedSocketDir string

	// Resolve auto-resize bounds early via vmcfg so the values are available
	// to both the newDriver closure (for driver config) and the log below.
	// This must happen before newDriver is defined because the closure captures
	// ar by reference at construction time.
	ar := vmcfg.Resolve(vmcfg.Config{
		BootMemMiB: f.memoryMiB,
		BootVCPUs:  f.vcpus,
		MemMaxMiB:  f.memoryMaxMiB,
		VCPUsMax:   f.vcpusMax,
		DiskMaxGiB: f.diskMaxGiB,
	})
	// govBounds is an alias so callers that previously referenced govBounds
	// fields continue to work without a larger rename.
	govBounds := ar.Bounds
	// effectiveMemMaxMiB is the resolved ceiling in MiB.
	// AR-CLI-AC2: sandbox create runs no supervisor (D-DC-12 scope boundary).
	// The hotplug hardware region is configured here; the dynamic governor
	// loop activates only in the detached supervisor process.
	effectiveMemMaxMiB := ar.MemoryMaxMiB
	slog.Info("auto-resize: hotplug hardware configured; governor activates in supervisor",
		"mem_max_mib", effectiveMemMaxMiB,
		"vcpus_max", govBounds.VCPUMax,
		"disk_max_gib", govBounds.DiskMaxBytes/(1024*1024*1024),
	)

	// newDriver constructs a CHDriver instance for the resolved ext4.
	// Each sandbox gets its own instance because DiskImagePath is static in
	// cloudhypervisor.Config. Socket/log paths use default locations so that
	// svc.Stop (using svc.driver) can find the socket after this call returns.
	newDriver := func(ext4Path string, extraDisks []service.ExtraDisk) (driver.Driver, error) {
		cfg := buildCHConfig(kernelPath, ext4Path, f.memoryMiB, f.vcpus)
		cfg.NestedVirt = f.nestedVirt
		cfg.MemoryMaxMiB = ar.MemoryMaxMiB
		cfg.VCPUMax = ar.VCPUMax
		capturedExtraDisks = nil
		for _, ed := range extraDisks {
			cfg.ExtraDisks = append(cfg.ExtraDisks, cloudhypervisor.ExtraDisk{Path: ed.Path})
			capturedExtraDisks = append(capturedExtraDisks, ed.Path)
		}
		if len(bootGuestMounts) > 0 {
			cfg.Cmdline = workspaceMountCmdline(bootGuestMounts) + ar.PID1Args
		} else {
			cfg.Cmdline = diskBootCmdlineBase + " --" + ar.PID1Args
		}
		if p, err := exec.LookPath("cloud-hypervisor"); err == nil {
			cfg.BinaryPath = p
		}
		if socketDir, serr := orcaSocketDir(); serr == nil {
			cfg.SocketDir = socketDir
			capturedSocketDir = socketDir
		}
		capturedDiskPath = ext4Path
		capturedCmdline = cfg.Cmdline
		capturedCHBin = cfg.BinaryPath
		return cloudhypervisor.New(cfg)
	}

	// probe verifies reachability via GuestDialer (vsock connect).
	// It polls with a 300 ms back-off until the guest agent's vsock listener
	// accepts the connection or the context (ReachabilityTimeout) expires.
	// A single-shot attempt is not enough: CH's vsock multiplexer returns EOF
	// while the virtio-vsock device is still being negotiated by the guest
	// (typically < 1 s after vm.boot returns), so a retry loop is required.
	probe := func(ctx context.Context, drv driver.Driver, id domain.SandboxID) error {
		gd, ok := drv.(driver.GuestDialer)
		if !ok {
			// Driver doesn't implement DialGuest; skip reachability check.
			return nil
		}
		for {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			conn, err := gd.DialGuest(dialCtx, id, driver.AgentControlPort)
			cancel()
			if err == nil {
				_ = conn.Close()
				return nil
			}
			time.Sleep(300 * time.Millisecond)
		}
	}

	spec := service.ImageSpec{
		Ref:        f.imageRef,
		RootfsPath: f.rootfsPath,
	}

	// ── Workspace + shadow disks (D-DC-10) ────────────────────────────────────
	//
	// When --workspace is provided:
	//   1. Shadow disks (DefaultShadowDirs) are created as sparse ext4 images in
	//      <storeRoot>/disks and prepended to ExtraDisks[0..N-1].
	//   2. The workspace disk is captured by service.CreateAndBoot (using the
	//      shadow-aware capturer that excludes shadow dir names from .dockerignore)
	//      and appended as ExtraDisks[N] — always AFTER shadow disks.
	//
	// Device-letter derivation (pinned by unit test):
	//   ExtraDisks[i]   → /dev/vd{b+i}   (shadow disk i, 0-indexed)
	//   ExtraDisks[N]   → /dev/vd{b+N}   (workspace, appended by service)
	//
	// bootGuestMounts is populated below and consumed by newDriver (above).
	// newDriver wires the mounts into the kernel cmdline via workspaceMountCmdline
	// so that the guest agent (PID 1) calls agent.MountWorkspace before any vsock
	// workload can touch the workspace path.
	var (
		bootExtraDisks     []service.ExtraDisk
		bootWorkspace      *service.WorkspaceSpec
		bootCapturer       func(context.Context, string, string, int64) error
		bootBaseRef        string   // host HEAD SHA when the workspace carries .git (GIT-SEED)
		shadowDiskCleanups []string // host paths to remove on CreateAndBoot failure
	)
	if f.workspacePath != "" {
		// Validate workspace path.
		if _, statErr := os.Stat(f.workspacePath); statErr != nil {
			return errSandbox("sandbox create", fmt.Errorf("--workspace: %w", statErr))
		}
		wsAbs, absErr := filepath.Abs(f.workspacePath)
		if absErr != nil {
			return errSandbox("sandbox create", fmt.Errorf("--workspace: abs path: %w", absErr))
		}

		// Guest path: /workspace/<basename>.  Convention keeps all workspace
		// mounts under /workspace (matching WorkspaceMount.GuestPath contract).
		guestPath := "/workspace/" + filepath.Base(wsAbs)

		// Durable disk directory — same root as the workspace disk written by
		// service.CreateAndBoot (defaultDiskDir mirrors <storeRoot>/disks).
		diskDir := filepath.Join(storeRoot, "disks")
		if mkErr := os.MkdirAll(diskDir, 0o700); mkErr != nil {
			return errSandbox("sandbox create", fmt.Errorf("--workspace: disk dir: %w", mkErr))
		}

		// Build shadow disk specs for DefaultShadowDirs.
		shadowSpecs := buildShadowDiskSpecs(DefaultShadowDirs, diskDir, guestPath, f.positionals[0])

		// Create sparse ext4 shadow disks.  Each disk is preallocated and
		// formatted with mke2fs before the VM boots so the guest can mount them
		// immediately.  Failures here abort sandbox creation; already-created
		// images are cleaned up via the defer below.
		for _, spec := range shadowSpecs {
			if cErr := createShadowDisk(ctx, spec); cErr != nil {
				// Clean up any images already written.
				for _, p := range shadowDiskCleanups {
					_ = os.Remove(p)
				}
				return errSandbox("sandbox create", fmt.Errorf("--workspace: %w", cErr))
			}
			shadowDiskCleanups = append(shadowDiskCleanups, spec.HostPath)
		}

		bootExtraDisks = shadowExtraDisks(shadowSpecs)
		bootWorkspace = workspaceSpecFromFlags(f, wsAbs, guestPath)
		if h := testWorkspaceSpecHook; h != nil {
			h(bootWorkspace)
		}

		// GIT-SEED (D-PD-29): this is the HUMAN create path — capture the
		// full repository (working tree + .git) so the guest can commit and
		// push. The .dockerignore `.git` exclusion is image-build hygiene and
		// is negated here for this path only; other create paths (agent,
		// herdr/mcp/orca) use the service default capturer (nil →
		// builder.WorktreeToDisk), which always honours .dockerignore.
		bootCapturer = makeHumanWorkspaceCapturer(DefaultShadowDirs)

		// Fail fast on a git workspace without a configured host identity
		// (ID-1): the guest would mint commits with no author. Refuse BEFORE
		// the expensive capture rather than seeding a synthetic bot identity.
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

		// Compute GuestMount specs for all disks. bootGuestMounts is read by
		// the newDriver closure (above) to build the kernel cmdline fragment
		// that delivers the specs to the guest agent (PID 1) as os.Args.
		allMounts := append(shadowGuestMounts(shadowSpecs, 0),
			WorkspaceGuestMount(guestPath, len(shadowSpecs)))
		bootGuestMounts = allMounts
		slog.Info("workspace shadow disks prepared",
			"workspace_host", wsAbs,
			"workspace_guest", guestPath,
			"num_shadow_disks", len(shadowSpecs),
			"workspace_device", WorkspaceGuestMount(guestPath, len(shadowSpecs)).Device,
			"mounts", fmt.Sprintf("%v", allMounts),
		)
	}
	secrets, err := resolveCreateSecrets(ctx, f)
	if err != nil {
		return errSandbox("sandbox create", err)
	}

	sb, err := service.CreateAndBoot(ctx, svc, imgCache, newDriver, probe,
		project, name,
		service.CreateAndBootOptions{
			Labels:            f.labels,
			RemoveOnExit:      f.rm,
			Image:             spec,
			CacheRoot:         cacheRoot,
			MemoryMiB:         f.memoryMiB,
			VCPUs:             f.vcpus,
			NestedVirt:        f.nestedVirt,
			ExtraDisks:        bootExtraDisks,
			Workspace:         bootWorkspace,
			WorkspaceCapturer: bootCapturer,
			BaseRef:           bootBaseRef, // GIT-SEED: host HEAD at capture time (D-PD-19/D-PD-29)
			Secrets:           secrets,
		},
	)
	if err != nil {
		// Clean up shadow disk images that were created before CreateAndBoot
		// failed; the workspace disk is cleaned up by service.CreateAndBoot itself.
		for _, p := range shadowDiskCleanups {
			_ = os.Remove(p)
		}
		return errSandbox("sandbox create", err)
	}

	if handoffErr := handoffHumanSupervisor(ctx, svc, sb, storeRoot, kernelPath, govBounds, f.memoryMiB, f.vcpus,
		capturedDiskPath, capturedExtraDisks, capturedCmdline, capturedCHBin, capturedSocketDir, bootWorkspace != nil, len(bootExtraDisks),
		workspaceGuestPathFor(bootWorkspace)); handoffErr != nil {
		slog.Warn("sandbox create: supervisor handoff failed; broker will not survive CLI exit",
			"sandbox", sb.ID, "err", handoffErr)
	}

	out.EmitSuccess("sandbox.created", toSandboxInfoJSON(sb),
		fmt.Sprintf("created sandbox %s (%s)", sb.Handle(), sb.ID))
	return nil
}

// resolveCreateSecrets parses --secret flags and, unless --no-builtin-gh,
// appends the host `gh auth token` as GH_TOKEN@github.com,api.github.com.
// Explicit GH_TOKEN / GITHUB_TOKEN binds win over the builtin.
func resolveCreateSecrets(ctx context.Context, f sandboxCreateFlags) ([]service.SecretBind, error) {
	var binds []service.SecretBind
	for _, spec := range f.secrets {
		b, err := service.ParseSecretSpec(spec)
		if err != nil {
			return nil, &UsageError{Msg: "sandbox create: " + err.Error()}
		}
		binds = append(binds, b)
	}
	if f.noBuiltinGH {
		return binds, nil
	}
	builtin, ok, err := service.BuiltinGitHubSecret(ctx)
	if err != nil {
		return nil, err
	}
	if !ok {
		return binds, nil
	}
	return service.MergeSecrets(binds, builtin), nil
}

// handoffHumanSupervisor stops the in-process boot VM and re-owns it in a
// detached supervisor that holds the credential broker (D-PD-26).
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
	workspaceGuestPath string,
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
	cfg := supervisor.Config{
		SandboxRef:         sb.ID.String(),
		StoreRoot:          storeRoot,
		StateDir:           stateDir,
		CHBin:              chBin,
		SocketDir:          socketDir,
		KernelPath:         kernelPath,
		DiskPath:           diskPath,
		ExtraDisks:         extraDisks,
		MemoryMiB:          memoryMiB,
		BootVCPUs:          bootVCPUs,
		HasWorkspaceDisk:   hasWorkspace,
		WorkspaceDiskIndex: workspaceDiskIndex,
		WorkspaceGuestPath: workspaceGuestPath, // GIT-SEED: git identity seed target
		GovBounds:          govBounds,
		Cmdline:            cmdline,
	}
	if err := supervisor.WriteSpawnSpec(stateDir, cfg); err != nil {
		return err
	}
	if _, err := svc.Stop(ctx, sb.ID.String()); err != nil {
		return fmt.Errorf("stop before supervisor handoff: %w", err)
	}
	return spawnPersistedSupervisor(ctx, svc, sb.ID, stateDir)
}

// workspaceGuestPathFor returns the workspace's in-guest mount point, or ""
// when the sandbox has no workspace. Consumed by the supervisor handoff so
// the detached supervisor can seed the git identity (GIT-SEED).
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

func stopDetachedSupervisor(ctx context.Context, svc *service.Service, sb domain.Sandbox) {
	if sb.SupervisorSock == "" {
		return
	}
	if err := supervisor.StopSupervisor(ctx, sb.SupervisorSock); err != nil {
		slog.Warn("sandbox: StopSupervisor", "sock", sb.SupervisorSock, "err", err)
	}
	_ = svc.ClearSupervisor(ctx, sb.ID)
}

// kernelPathFor is retained for callers outside runSandboxCreate that only need
// a best-effort path (no preflight validation). New callers should prefer
// resolveKernelPath which validates existence and returns a legible error.
func kernelPathFor() string {
	p, _ := resolveKernelPath()
	if p != "" {
		return p
	}
	// Fallback: return the binary-relative candidate even if it does not exist,
	// so callers that only need the path for printing/logging still get something.
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "images", "kernel", "vmlinux-x86_64")
}

// ── list ─────────────────────────────────────────────────────────────────────

// sandboxListWideRowJSON is one row in the wide label-filtered list output.
// Used only when --label and --wide are both present.
type sandboxListWideRowJSON struct {
	ID             string `json:"id"`
	Handle         string `json:"handle"`
	State          string `json:"state"`
	UptimeSeconds  int64  `json:"uptime_seconds"`
	AllocatedBytes int64  `json:"allocated_bytes"`
	Error          string `json:"error,omitempty"`
}

// sandboxListWideDataJSON is the machine-contract payload for sandbox list --wide.
type sandboxListWideDataJSON struct {
	// LabelKey and LabelValue record the filter that produced this view.
	LabelKey        string                   `json:"label_key"`
	LabelValue      string                   `json:"label_value"`
	Sandboxes       []sandboxListWideRowJSON `json:"sandboxes"`
	TotalAllocBytes int64                    `json:"total_alloc_bytes"`
	LeakedResources int                      `json:"leaked_resources"`
}

// parseLabel splits a KEY=VALUE label string. Returns an error on bad format.
func parseLabel(cmd, label string) (key, value string, err error) {
	k, v, ok := strings.Cut(label, "=")
	if !ok || k == "" || v == "" {
		return "", "", &UsageError{Msg: fmt.Sprintf("%s --label: requires KEY=VALUE format; got %q", cmd, label)}
	}
	return k, v, nil
}

// runSandboxList implements `sandbox list [--label KEY=VALUE] [--wide]`.
//
// Without flags: lists all sandboxes (existing behaviour).
// --label KEY=VALUE: filters by label (AND-matched when repeated; any key accepted).
// --wide (requires --label): shows per-sandbox disk allocation, uptime, and
//
//	host-wide leaked-resource count using the same three-way ResourceIndex
//	classification as `nexus3 reap` (owned/orphan/live). Allocated bytes use
//	stat(2).Blocks*512 — never apparent size — to avoid the sparse-disk trap.
//	Degradation: an unreachable sandbox appears as a row with its error; other
//	rows are unaffected. Zero matches → empty output, exit 0.
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

	// Unfiltered list: existing behaviour unchanged.
	if labelFlag == "" {
		all, err := svc.List(ctx)
		if err != nil {
			return errSandbox("sandbox list", err)
		}
		infos := make([]sandboxInfoJSON, 0, len(all))
		for _, sb := range all {
			infos = append(infos, toSandboxInfoJSON(sb))
		}
		out.EmitSuccess("sandbox.list", sandboxListDataJSON{Sandboxes: infos},
			fmt.Sprintf("%d sandbox(es)", len(infos)))
		return nil
	}

	// Label-filtered list.
	lKey, lVal, lErr := parseLabel("sandbox list", labelFlag)
	if lErr != nil {
		return lErr
	}

	if wideFlag {
		return runSandboxListWide(ctx, lKey, lVal, out, svc)
	}

	// Narrow label-filtered list: AND-match on the single specified label.
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

// runSandboxListWide renders the wide (--wide) view for
// `sandbox list --label KEY=VALUE --wide`. It calls service.LabelStatus which
// uses the same ResourceIndex three-way classification (owned/orphan/live) as
// `nexus3 reap`, ensuring that this view and `nexus3 reap` never disagree
// about leaked count.
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

// renderSandboxListWide formats the wide list as a tabwriter table. Returns the
// table as a string without a trailing newline (EmitSuccess appends its own).
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

// ── rm ────────────────────────────────────────────────────────────────────────

func runSandboxRm(ctx context.Context, args []string, out *Output, svc *service.Service) error {
	if len(args) != 1 {
		return &UsageError{Msg: "sandbox rm: usage: sandbox rm <id|prefix|project/name>"}
	}
	ref := args[0]

	// Resolve before remove so we can include the handle in the success message.
	// If resolution fails, return immediately without touching the store.
	all, err := svc.List(ctx)
	if err != nil {
		return errSandbox("sandbox rm", err)
	}
	var target *domain.Sandbox
	for i := range all {
		if all[i].ID.String() == ref || all[i].Handle() == ref {
			sb := all[i]
			target = &sb
			break
		}
	}
	if target != nil {
		stopDetachedSupervisor(ctx, svc, *target)
	}
	// We prefer the service.Remove resolution (prefix/handle/id) rather than
	// duplicating it; target is just for the success message.
	if err := svc.Remove(ctx, ref); err != nil {
		return errSandbox("sandbox rm", err)
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

// ── start ─────────────────────────────────────────────────────────────────────

func runSandboxStart(ctx context.Context, args []string, out *Output, svc *service.Service) error {
	if len(args) != 1 {
		return &UsageError{Msg: "sandbox start: usage: sandbox start <id|prefix|project/name>"}
	}
	sb, err := svc.ResolveRef(ctx, args[0])
	if err != nil {
		return errSandbox("sandbox start", err)
	}
	if err := ensureDetachedSupervisor(ctx, svc, sb); err != nil {
		// No spawn spec (store-only or pre-D-PD-26 sandbox): fall back to in-process Start.
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

// ── stop ──────────────────────────────────────────────────────────────────────

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

// ── pause ─────────────────────────────────────────────────────────────────────

func runSandboxPause(ctx context.Context, args []string, out *Output, svc *service.Service) error {
	if len(args) != 1 {
		return &UsageError{Msg: "sandbox pause: usage: sandbox pause <id|prefix|project/name>"}
	}

	sb, err := svc.Pause(ctx, args[0])
	if err != nil {
		return errSandbox("sandbox pause", err)
	}

	out.EmitSuccess("sandbox.paused", toSandboxInfoJSON(sb),
		fmt.Sprintf("paused sandbox %s (%s)", sb.Handle(), sb.ID))
	return nil
}

// ── resume ────────────────────────────────────────────────────────────────────

func runSandboxResume(ctx context.Context, args []string, out *Output, svc *service.Service) error {
	if len(args) != 1 {
		return &UsageError{Msg: "sandbox resume: usage: sandbox resume <id|prefix|project/name>"}
	}

	sb, err := svc.Resume(ctx, args[0])
	if err != nil {
		return errSandbox("sandbox resume", err)
	}

	out.EmitSuccess("sandbox.resumed", toSandboxInfoJSON(sb),
		fmt.Sprintf("resumed sandbox %s (%s)", sb.Handle(), sb.ID))
	return nil
}
