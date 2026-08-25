//go:build linux

package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/resize"
	"github.com/IniZio/nexus3/internal/supervisor"
)

// supervisorExtraDisks is a flag.Value implementation that accumulates
// repeated --extra-disk <path> flags into a []string slice.
type supervisorExtraDisks []string

func (e *supervisorExtraDisks) String() string {
	if e == nil {
		return ""
	}
	return strings.Join(*e, ",")
}

func (e *supervisorExtraDisks) Set(v string) error {
	*e = append(*e, v)
	return nil
}

// supervisorResizableDiskIndices is a flag.Value implementation that accumulates
// repeated --resizable-disk-index <int> flags into a []int slice.
type supervisorResizableDiskIndices []int

func (r *supervisorResizableDiskIndices) String() string {
	if r == nil {
		return ""
	}
	parts := make([]string, len(*r))
	for i, idx := range *r {
		parts[i] = strconv.Itoa(idx)
	}
	return strings.Join(parts, ",")
}

func (r *supervisorResizableDiskIndices) Set(v string) error {
	idx, err := strconv.Atoi(v)
	if err != nil {
		return fmt.Errorf("--resizable-disk-index: %w", err)
	}
	*r = append(*r, idx)
	return nil
}

// supervisorLiveMounts is a flag.Value implementation that accumulates
// repeated --mount <host>:<guest>[:ro] flags into []domain.LiveMount.
// Encoding and decoding both go through supervisor.EncodeLiveMount /
// supervisor.ParseLiveMountSpec so the two sides cannot drift.
type supervisorLiveMounts []domain.LiveMount

func (m *supervisorLiveMounts) String() string {
	if m == nil {
		return ""
	}
	specs := make([]string, 0, len(*m))
	for _, lm := range *m {
		specs = append(specs, supervisor.EncodeLiveMount(lm))
	}
	return strings.Join(specs, ",")
}

func (m *supervisorLiveMounts) Set(v string) error {
	lm, err := supervisor.ParseLiveMountSpec(v)
	if err != nil {
		return err
	}
	*m = append(*m, lm)
	return nil
}

// runSupervisorMain is the entrypoint for `nexus3 __supervisor`. It is
// dispatched from main() before any CLI routing so the supervisor process
// never touches cobra or the CLI command registry.
func runSupervisorMain(args []string) {
	cfg, err := parseSupervisorFlags(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := supervisor.RunDetached(cfg); err != nil {
		slog.Error("supervisor: run failed", "err", err)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// parseSupervisorFlags turns `nexus3 __supervisor` argv into a
// supervisor.Config. Extracted from runSupervisorMain so the flag→struct
// glue is unit-testable: a field silently dropped from the struct literal
// (as happened with ExtraDisks on 2026-08-16 — the supervisor then attached
// only the rootfs and the guest panicked mounting its workspace) is caught
// by a round-trip test instead of a live boot.
func parseSupervisorFlags(args []string) (supervisor.Config, error) {
	fs := flag.NewFlagSet(supervisor.HiddenSubcommand, flag.ContinueOnError)
	var (
		sandboxRef = fs.String("sandbox-ref", "", "sandbox ID hex or <project>/<name> handle (required)")
		storeRoot  = fs.String("store-root", "", "FileStore root directory (required)")
		stateDir   = fs.String("state-dir", "", "directory for supervisor.pid and supervisor.sock (required)")
		chBin      = fs.String("ch-bin", "", "cloud-hypervisor binary path (required)")
		socketDir  = fs.String("socket-dir", "", "per-sandbox CH API socket directory (required)")
		kernel     = fs.String("kernel", "", "guest kernel path (required)")
		disk       = fs.String("disk", "", "per-sandbox ext4 disk image path (required)")
		credsFile  = fs.String("creds-file", "", "creds.json path for real-token seeding (optional)")
		memoryMiB  = fs.Uint("memory", 0, "guest RAM in MiB (default 512)")
		// GovBounds fields: all zero by default (passive-mode governor, D-DC-13).
		// Forwarded from the SpawnConfig.Cmdline built by cmd_sandbox.go at create
		// time. Auto-resize is unconditional; the cmdline always carries
		// --mem-ceiling=<bytes> (appended by autoResizePID1Args) as its PID-1 arg.
		govMemMin  = fs.Int64("gov-mem-min", 0, "governor min RAM bytes (0 = passive)")
		govMemMax  = fs.Int64("gov-mem-max", 0, "governor max RAM bytes (0 = passive)")
		govVCPUMin = fs.Int("gov-vcpu-min", 0, "governor min vCPU count (0 = passive)")
		govVCPUMax = fs.Int("gov-vcpu-max", 0, "governor max vCPU count (0 = passive)")
		govDiskMax = fs.Int64("gov-disk-max", 0, "governor max disk bytes (0 = passive)")
		// bootVCPUs: seeds SandboxResizer.CurrentVCPUs() before the first resize.
		// 0 means the supervisor applies the driver default (1 vCPU).
		bootVCPUs = fs.Uint("boot-vcpus", 0, "vCPU count at VM boot (0 = driver default = 1)")
		// ephemeral: one-shot/builder mode — exit on POST /supervisor/stop
		// (the build-complete signal) rather than waiting indefinitely for SIGTERM.
		ephemeral = fs.Bool("ephemeral", false, "one-shot mode: terminate on /supervisor/stop completion signal")
		// parentPipeFD: file descriptor holding the read end of the parent-watchdog
		// pipe created by SpawnDetached. When the spawning CLI exits (including via
		// SIGKILL) the write end closes and the supervisor reads EOF, triggering
		// graceful shutdown. 0 means no watchdog pipe (non-ephemeral supervisors).
		parentPipeFD = fs.Int("parent-pipe-fd", 0, "parent-watchdog pipe read fd (0 = none; ephemeral only)")
		// workspaceDiskIndex: 0-based ExtraDisks index of the workspace disk.
		workspaceDiskIndex = fs.Int("workspace-disk-index", -1, "workspace disk ExtraDisks index (-1 = no disk axis)")
		// workspaceGuestPath: in-guest mount point of the workspace disk. When
		// non-empty the supervisor seeds the operator's git identity into the
		// guest after the human-secret seed loop (GIT-SEED, D-PD-29).
		workspaceGuestPath = fs.String("workspace-guest-path", "", "in-guest workspace mount point (empty = no workspace)")
		// cmdline: full kernel command line passed to cloud-hypervisor. When empty
		// the driver uses the disk-boot default. Set by SpawnDetached when the
		// caller pre-computed workspace-mount args and auto-resize PID-1 args.
		cmdline = fs.String("cmdline", "", "kernel command line (empty = driver disk-boot default)")
		// virtiofsd: absolute path to the virtiofsd 1.x binary. Required
		// whenever --mount is passed; the driver refuses to boot with live
		// mounts and an empty VirtiofsdPath.
		virtiofsd = fs.String("virtiofsd", "", "virtiofsd binary path (required with --mount)")
	)
	// liveMounts accumulates repeated --mount flags (one per virtiofs share).
	// These must be re-attached on every supervisor boot: the guest cmdline
	// carries a --workspace-mount=<tag>:...:virtiofs entry per share, and a
	// guest that mounts a tag with no backing device blocks at boot forever.
	var liveMounts supervisorLiveMounts
	fs.Var(&liveMounts, "mount", "live virtiofs share <host-path>:<guest-path>[:ro] (repeatable, order-preserving)")
	// extraDisks accumulates repeated --extra-disk flags (one per disk path).
	var extraDisks supervisorExtraDisks
	fs.Var(&extraDisks, "extra-disk", "extra disk image path to re-attach (repeatable, order-preserving)")
	// resizableDiskIndices accumulates repeated --resizable-disk-index flags.
	// Each value is a 0-based ExtraDisks index whose ext4 disk the disk governor
	// should monitor and grow. For builder VMs this is [2] (the buildkit cache
	// disk at ExtraDisks[2]=vdd); for normal sandboxes it is derived from the
	// workspace disk index and forwarded via HasWorkspaceDisk/WorkspaceDiskIndex.
	var resizableDiskIndices supervisorResizableDiskIndices
	fs.Var(&resizableDiskIndices, "resizable-disk-index", "0-based ExtraDisks index for disk governor (repeatable)")
	if err := fs.Parse(args); err != nil {
		return supervisor.Config{}, err
	}

	missing := ""
	switch {
	case *sandboxRef == "":
		missing = "--sandbox-ref"
	case *storeRoot == "":
		missing = "--store-root"
	case *stateDir == "":
		missing = "--state-dir"
	case *chBin == "":
		missing = "--ch-bin"
	case *socketDir == "":
		missing = "--socket-dir"
	case *kernel == "":
		missing = "--kernel"
	case *disk == "":
		missing = "--disk"
	}
	if missing != "" {
		return supervisor.Config{}, fmt.Errorf("supervisor: %s is required", missing)
	}

	cfg := supervisor.Config{
		SandboxRef: *sandboxRef,
		StoreRoot:  *storeRoot,
		StateDir:   *stateDir,
		CHBin:      *chBin,
		SocketDir:  *socketDir,
		KernelPath: *kernel,
		DiskPath:   *disk,
		CredsFile:  *credsFile,
		MemoryMiB:  uint32(*memoryMiB),
		BootVCPUs:  uint32(*bootVCPUs), //nolint:gosec // range-checked by flag.Uint; vCPUs fit uint32
		// HasWorkspaceDisk / WorkspaceDiskIndex: the workspace disk index is
		// meaningful only when the flag was explicitly passed (>= 0). The -1
		// default means no workspace disk is attached; the disk axis is skipped.
		HasWorkspaceDisk:   *workspaceDiskIndex >= 0,
		WorkspaceDiskIndex: *workspaceDiskIndex,
		WorkspaceGuestPath: *workspaceGuestPath,
		ExtraDisks:           []string(extraDisks),
		ResizableDiskIndices: []int(resizableDiskIndices),
		GovBounds: resize.Bounds{
			MemMinBytes:  *govMemMin,
			MemMaxBytes:  *govMemMax,
			VCPUMin:      int32(*govVCPUMin), //nolint:gosec // range-checked by flag.Int (fits int32)
			VCPUMax:      int32(*govVCPUMax), //nolint:gosec // range-checked by flag.Int (fits int32)
			DiskMaxBytes: *govDiskMax,
		},
		Cmdline:       *cmdline,
		LiveMounts:    []domain.LiveMount(liveMounts),
		VirtiofsdPath: *virtiofsd,
		Ephemeral:     *ephemeral,
		ParentPipeFD:  *parentPipeFD,
	}
	return cfg, nil
}
