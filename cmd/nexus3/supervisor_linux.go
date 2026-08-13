//go:build linux

package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/IniZio/nexus3/internal/supervisor"
)

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
	)
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
	}
	if err := supervisor.RunDetached(cfg); err != nil {
		slog.Error("supervisor: run failed", "err", err)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
