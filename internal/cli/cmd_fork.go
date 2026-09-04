package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
	"github.com/IniZio/nexus3/internal/core/service"
	"github.com/IniZio/nexus3/internal/core/store"
	"github.com/IniZio/nexus3/internal/supervisor"
)

func init() {
	Register(Command{
		Name:    "fork",
		Summary: "Fork a sandbox into N running children (--count N, default 1)",
		Run:     runFork,
	})
}

// doSpawnForkChildSupervisor is the per-child supervisor spawn function used
// by the fork path. A package-level variable so tests can replace it with a
// spy without spawning a real supervisor process.
var doSpawnForkChildSupervisor func(ctx context.Context, svc *service.Service, id domain.SandboxID, stateDir string) error = spawnPersistedSupervisorReacquire

// doSpawnForkSupervisors wraps spawnForkChildSupervisors so that tests that
// exercise the flag/routing layer of runForkWith without a real supervisor
// state dir can replace the whole supervisor-spawn block with a no-op.
var doSpawnForkSupervisors = spawnForkChildSupervisors

// doStoreDefaultRoot resolves the nexus3 store root directory. A package-level
// variable so tests can redirect it to a temp dir without touching the real
// state directory.
var doStoreDefaultRoot = store.DefaultRoot

// ── dispatcher / flag parsing ────────────────────────────────────────────────

func runFork(ctx context.Context, args []string, out *Output) error {
	return runForkWith(ctx, args, out)
}

// runForkWith is the testable entry point. Callers may pass a pre-built svc
// to bypass newSandboxService (used by unit tests); if svc is nil the
// production constructor is used.
func runForkWith(ctx context.Context, args []string, out *Output, svcs ...*service.Service) error {
	count := 1
	force := false
	var positionals []string

	i := 0
	for i < len(args) {
		arg := args[i]
		switch {
		case arg == "--count":
			if i+1 >= len(args) {
				return &UsageError{Msg: "fork: --count requires an argument"}
			}
			i++
			n, err := strconv.Atoi(args[i])
			if err != nil || n < 1 {
				return &UsageError{Msg: fmt.Sprintf("fork: --count must be a positive integer, got %q", args[i])}
			}
			count = n
		case strings.HasPrefix(arg, "--count="):
			val := arg[len("--count="):]
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				return &UsageError{Msg: fmt.Sprintf("fork: --count must be a positive integer, got %q", val)}
			}
			count = n
		case arg == "--force":
			// Skip the disk-space preflight. See service.ForkForceDiskSpace:
			// the projection charges the full parent footprint per child, which
			// over-counts on reflink filesystems where the copy is near-free.
			force = true
		case len(arg) > 1 && arg[0] == '-':
			return &UsageError{Msg: fmt.Sprintf("fork: unknown flag %q", arg)}
		default:
			positionals = append(positionals, arg)
		}
		i++
	}

	if len(positionals) != 1 {
		return &UsageError{Msg: "fork: usage: fork <ref> [--count N] [--force]"}
	}
	ref := positionals[0]

	var svc *service.Service
	if len(svcs) > 0 && svcs[0] != nil {
		svc = svcs[0]
	} else {
		var err error
		svc, err = newSandboxService()
		if err != nil {
			return errSandbox("fork", err)
		}
	}

	var forkOpts []service.ForkOption
	if force {
		forkOpts = append(forkOpts, service.ForkForceDiskSpace())
	}
	children, err := svc.Fork(ctx, ref, count, forkOpts...)
	if err != nil {
		return errSandbox("fork", err)
	}

	// Spawn a detached reacquire-mode supervisor for each networked child so
	// that supervisor-upgrade, recover, and the memory governor work
	// identically on fork children as on created ones (D-HSH-27). The
	// reacquire mode adopts the already-running VM without a cold boot — the
	// supervisor does NOT call svc.Start; it re-acquires the network perimeter
	// via the child's NetnsControlSocket. See s46-fork-supervisor-parity AC-5
	// for the full list of create-path steps that are deliberately not replayed.
	//
	// Vsock-only (netless) children have NetnsChildPID=0 and carry no network
	// perimeter; spawnForkChildSupervisors skips them. The len(children) > 0
	// guard is present so storeRoot is only resolved when there is work to do.
	if len(children) > 0 {
		storeRoot, storeErr := doStoreDefaultRoot()
		if storeErr != nil {
			return errSandbox("fork", fmt.Errorf("resolve store root for supervisor: %w", storeErr))
		}
		var parentID domain.SandboxID
		if children[0].Provenance != nil {
			parentID = children[0].Provenance.ParentID
		}
		if spawnErr := doSpawnForkSupervisors(ctx, svc, storeRoot, parentID, children); spawnErr != nil {
			return errSandbox("fork", spawnErr)
		}
	}

	infos := make([]sandboxInfoJSON, len(children))
	for i, ch := range children {
		infos[i] = toSandboxInfoJSON(ch)
	}
	out.EmitSuccess("fork.created", sandboxListDataJSON{Sandboxes: infos},
		fmt.Sprintf("forked %s into %d children", ref, len(children)))
	return nil
}

// spawnForkChildSupervisors writes a spawn.json and starts a reacquire-mode
// supervisor for each fork child. It reads the parent's spawn.json to derive
// the config template (kernel, memory, governor bounds, etc.) that every child
// inherits; only the identity fields (SandboxRef, DiskPath, ExtraDisks,
// StateDir) are replaced with each child's own values.
//
// Create-path steps deliberately NOT replayed (D-HSH-27 / s46-fork-supervisor-parity AC-5):
//   - svc.Stop: fork children are already Running from the snapshot restore;
//     stopping them destroys the in-memory state fork is meant to preserve.
//   - svc.Start / cold boot: doSpawnForkChildSupervisor runs RunReacquire,
//     not RunDetached, so the supervisor never calls svc.Start.
//   - disk initialisation: ForkFrom already copied the parent disks; no format
//     or resize step is needed.
//   - guest boot sequence: the VM was restored from a memory snapshot, not
//     re-booted, so no systemd units re-run and no boot-tasks execute.
func spawnForkChildSupervisors(
	ctx context.Context,
	svc *service.Service,
	storeRoot string,
	parentID domain.SandboxID,
	children []domain.Sandbox,
) error {
	// Only spawn supervisors for children that have a live netns runtime.
	// Vsock-only (netless) children do not have a network perimeter to
	// re-acquire, so reacquirePreflight would refuse them. Skipping them is
	// correct: they carry no supervisor on the create path either when the
	// driver registers no netns runtime.
	//
	// This check is the canonical location for the "needs a supervisor"
	// precondition: a future caller of spawnForkChildSupervisors inherits it
	// here and cannot accidentally skip it by not replicating a call-site
	// guard.
	var netted []domain.Sandbox
	for _, ch := range children {
		if ch.NetnsChildPID > 0 {
			netted = append(netted, ch)
		}
	}
	if len(netted) == 0 {
		return nil
	}

	parentStateDir := supervisor.DefaultStateDir(storeRoot, parentID)
	parentCfg, err := supervisor.ReadSpawnSpec(parentStateDir)
	if err != nil {
		return fmt.Errorf("fork supervisor: read parent spawn spec for %s: %w", parentID, err)
	}

	// Attempt every netted child and collect failures. Children are already
	// Running in the store after svc.Fork; rolling back a running VM is more
	// destructive than reporting honestly which ones lack a supervisor.
	var errs []error
	for _, child := range netted {
		if spawnErr := writeAndSpawnForkChild(ctx, svc, storeRoot, child, parentCfg); spawnErr != nil {
			errs = append(errs, fmt.Errorf("child %s: %w", child.ID, spawnErr))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("fork supervisor: %d of %d networked children failed to acquire a supervisor: %w",
			len(errs), len(netted), errors.Join(errs...))
	}
	return nil
}

// writeAndSpawnForkChild writes spawn.json for one fork child, then starts its
// supervisor. Disk paths are derived from the parent config using the same
// naming convention ForkFrom (cloudhypervisor/fork.go) uses:
//   - root disk: <diskDir>/<childID>.raw
//   - extra disks: cloudhypervisor.ChildExtraDiskPath(childID, parentPath)
func writeAndSpawnForkChild(
	ctx context.Context,
	svc *service.Service,
	storeRoot string,
	child domain.Sandbox,
	parentCfg supervisor.Config,
) error {
	childStateDir := supervisor.DefaultStateDir(storeRoot, child.ID)

	// Derive the child's disk paths from the parent's disk paths.
	// A parent with no disk (initramfs-only) has empty DiskPath; preserve that.
	var childDiskPath string
	if parentCfg.DiskPath != "" {
		childDiskPath = filepath.Join(filepath.Dir(parentCfg.DiskPath), child.ID.String()+".raw")
	}
	childExtraDisks := make([]string, len(parentCfg.ExtraDisks))
	for i, p := range parentCfg.ExtraDisks {
		childExtraDisks[i] = cloudhypervisor.ChildExtraDiskPath(child.ID, p)
	}

	// Build child config: copy parent template, replace only identity fields.
	childCfg := parentCfg
	childCfg.SandboxRef = child.ID.String()
	childCfg.DiskPath = childDiskPath
	childCfg.ExtraDisks = childExtraDisks
	childCfg.StateDir = childStateDir

	if err := supervisor.WriteSpawnSpec(childStateDir, childCfg); err != nil {
		return fmt.Errorf("fork supervisor: write spawn spec for child %s: %w", child.ID, err)
	}
	slog.Info("sandbox: fork child spawn spec written", "child", child.ID, "stateDir", childStateDir)
	return doSpawnForkChildSupervisor(ctx, svc, child.ID, childStateDir)
}

