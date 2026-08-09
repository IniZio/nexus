package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/image"
	"github.com/IniZio/nexus3/internal/core/lifecycle"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
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
	ID           string `json:"id"`
	Project      string `json:"project"`
	Name         string `json:"name"`
	Handle       string `json:"handle"`
	State        string `json:"state"`
	RemoveOnExit bool   `json:"remove_on_exit,omitempty"`
	StopReason   string `json:"stop_reason,omitempty"`
}

func toSandboxInfoJSON(sb domain.Sandbox) sandboxInfoJSON {
	return sandboxInfoJSON{
		ID:           sb.ID.String(),
		Project:      sb.Project,
		Name:         sb.Name,
		Handle:       sb.Handle(),
		State:        sb.State.String(),
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
// sandboxCreateFlags holds the result of parsing `sandbox create` arguments.
type sandboxCreateFlags struct {
	rm          bool
	imageRef    string
	rootfsPath  string
	filePath    string
	memoryMiB   uint32
	vcpus       uint32
	motiveID    string
	positionals []string
}

// parseSandboxCreateArgs parses the raw argument slice for `sandbox create`.
// All flags and positional arguments are collected; positional count and
// handle format are validated by the caller.
func parseSandboxCreateArgs(args []string) (sandboxCreateFlags, error) {
	var f sandboxCreateFlags
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
		case "--motive":
			if i+1 >= len(args) {
				return f, &UsageError{Msg: "sandbox create: --motive requires an argument"}
			}
			i++
			f.motiveID = args[i]
		default:
			if len(arg) > 1 && arg[0] == '-' {
				return f, &UsageError{Msg: fmt.Sprintf("sandbox create: unknown flag %q", arg)}
			}
			f.positionals = append(f.positionals, arg)
		}
		i++
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

// The --rm flag may appear before or after the handle. Flag scanning is done
// manually so that docker-style order ("create demo/one --rm --image …") works
// without requiring flags to precede the positional argument.
func runSandboxCreate(ctx context.Context, args []string, out *Output, svc *service.Service) error {
	f, parseErr := parseSandboxCreateArgs(args)
	if parseErr != nil {
		return parseErr
	}

	if len(f.positionals) != 1 {
		return &UsageError{Msg: "sandbox create: usage: sandbox create <project>/<name> [--rm] [--image <ref>|--rootfs <path>] [--memory <MiB>] [--vcpus <n>] [--motive <id>]"}
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
	storeRoot, err := store.DefaultRoot()
	if err != nil {
		return errSandbox("sandbox create", fmt.Errorf("resolve state directory: %w", err))
	}
	cacheRoot := filepath.Join(storeRoot, "images")

	imgCache, err := image.NewCache(cacheRoot)
	if err != nil {
		return errSandbox("sandbox create", fmt.Errorf("open image cache: %w", err))
	}

	// --file: build image via .nexus/Containerfile (best-effort; requires
	// buildkitd). On success the digest is fed into the normal cache path.
	if f.filePath != "" && f.imageRef == "" && f.rootfsPath == "" {
		// Placeholder: builder integration lives in a future slice.
		// For now return a clear actionable error rather than silently falling
		// through to the no-boot path.
		return errSandbox("sandbox create",
			fmt.Errorf("--file: builder integration not yet available; use --image or --rootfs"))
	}

	// Resolve the kernel path: env override → binary-relative default.
	kernelPath := kernelPathFor()

	// newDriver constructs a CHDriver instance for the resolved ext4.
	// Each sandbox gets its own instance because DiskImagePath is static in
	// cloudhypervisor.Config. Socket/log paths use default locations so that
	// svc.Stop (using svc.driver) can find the socket after this call returns.
	newDriver := func(ext4Path string) (driver.Driver, error) {
		cfg := buildCHConfig(kernelPath, ext4Path, f.memoryMiB, f.vcpus)
		// BinaryPath: look for cloud-hypervisor in PATH if not set.
		if p, err := exec.LookPath("cloud-hypervisor"); err == nil {
			cfg.BinaryPath = p
		}
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

	sb, err := service.CreateAndBoot(ctx, svc, imgCache, newDriver, probe,
		project, name,
		service.CreateAndBootOptions{
			MotiveID:     f.motiveID,
			RemoveOnExit: f.rm,
			Image:        spec,
			CacheRoot:    cacheRoot,
			MemoryMiB:    f.memoryMiB,
			VCPUs:        f.vcpus,
		},
	)
	if err != nil {
		return errSandbox("sandbox create", err)
	}

	out.EmitSuccess("sandbox.created", toSandboxInfoJSON(sb),
		fmt.Sprintf("created sandbox %s (%s)", sb.Handle(), sb.ID))
	return nil
}

// kernelPathFor returns the path to the pinned guest kernel.
// Priority: NEXUS3_KERNEL_PATH env > binary-relative default.
func kernelPathFor() string {
	if k := os.Getenv("NEXUS3_KERNEL_PATH"); k != "" {
		return k
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Join(filepath.Dir(exe), "images", "kernel", "vmlinux-x86_64")
}

// ── list ─────────────────────────────────────────────────────────────────────

func runSandboxList(ctx context.Context, args []string, out *Output, svc *service.Service) error {
	if len(args) != 0 {
		return &UsageError{Msg: "sandbox list: usage: sandbox list"}
	}

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

	sb, err := svc.Start(ctx, args[0])
	if err != nil {
		return errSandbox("sandbox start", err)
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

	sb, err := svc.Stop(ctx, args[0])
	if err != nil {
		return errSandbox("sandbox stop", err)
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
