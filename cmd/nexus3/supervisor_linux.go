//go:build linux

package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

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
	result := ""
	for i, p := range *e {
		if i > 0 {
			result += ","
		}
		result += p
	}
	return result
}

func (e *supervisorExtraDisks) Set(v string) error {
	*e = append(*e, v)
	return nil
}

// runSupervisorMain is the entrypoint for `nexus3 __supervisor`. It is
// dispatched from main() before any CLI routing so the supervisor process
// never touches cobra or the CLI command registry.
func runSupervisorMain(args []string) {
	fs := flag.NewFlagSet(supervisor.HiddenSubcommand, flag.ExitOnError)
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
		// -1 (default) means no workspace disk is attached; disk axis is skipped.
		workspaceDiskIndex = fs.Int("workspace-disk-index", -1, "workspace disk ExtraDisks index (-1 = no disk axis)")
		// cmdline: full kernel command line passed to cloud-hypervisor. When empty
		// the driver uses the disk-boot default. Set by SpawnDetached when the
		// caller pre-computed workspace-mount args and auto-resize PID-1 args.
		cmdline = fs.String("cmdline", "", "kernel command line (empty = driver disk-boot default)")
	)
	// extraDisks accumulates repeated --extra-disk flags (one per disk path).
	var extraDisks supervisorExtraDisks
	fs.Var(&extraDisks, "extra-disk", "extra disk image path to re-attach (repeatable, order-preserving)")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
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
		fmt.Fprintf(os.Stderr, "supervisor: %s is required\n", missing)
		os.Exit(1)
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
		ExtraDisks:         []string(extraDisks),
		GovBounds: resize.Bounds{
			MemMinBytes:  *govMemMin,
			MemMaxBytes:  *govMemMax,
			VCPUMin:      int32(*govVCPUMin), //nolint:gosec // range-checked by flag.Int (fits int32)
			VCPUMax:      int32(*govVCPUMax), //nolint:gosec // range-checked by flag.Int (fits int32)
			DiskMaxBytes: *govDiskMax,
		},
		Cmdline:      *cmdline,
		Ephemeral:    *ephemeral,
		ParentPipeFD: *parentPipeFD,
	}
	if err := supervisor.RunDetached(cfg); err != nil {
		slog.Error("supervisor: run failed", "err", err)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
